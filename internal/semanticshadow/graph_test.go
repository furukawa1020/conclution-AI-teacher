package semanticshadow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func validSignals() Signals {
	return Signals{
		AssistanceTarget: "respondent", RespondentStage: "restructure",
		CoachPhase: "awaiting_restatement", CoachAction: "restate",
		AnswerProof:           "question_bound_input_answer_first",
		AnswerTransitionProof: "question_bound_input_clause_later_to_first",
		GuestAFirstOutcome:    "changed_to_answer_first",
	}
}

func TestBuildIsDeterministicAndContentFree(t *testing.T) {
	digest, err := TurnDigest(make([]byte, 32), "server-request-1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := Build(digest, validSignals())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(digest, validSignals())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := CanonicalJSON(first)
	b, _ := CanonicalJSON(second)
	if string(a) != string(b) {
		t.Fatal("graph is not deterministic")
	}
	if !strings.Contains(string(a), `"relation":"restatement"`) {
		t.Fatalf("graph = %s", a)
	}
	for _, forbidden := range []string{"server-request-1", "caption", "audio", "uid", "token", "reasoning", "content"} {
		if strings.Contains(strings.ToLower(string(a)), forbidden) {
			t.Fatalf("graph leaked %q: %s", forbidden, a)
		}
	}
	if len(first.Nodes) != 4 || len(first.Edges) != 3 {
		t.Fatalf("unexpected graph: %+v", first)
	}
}

func TestBuildFiniteRelationsAndUnknownFailClosed(t *testing.T) {
	digest := strings.Repeat("a", 64)
	tests := []struct {
		name   string
		mutate func(*Signals)
		want   string
	}{
		{"direct", func(s *Signals) { s.AnswerTransitionProof = "none"; s.GuestAFirstOutcome = "stayed_answer_first" }, "direct"},
		{"unresolved", func(s *Signals) {
			s.AnswerProof = "none"
			s.AnswerTransitionProof = "none"
			s.GuestAFirstOutcome = "no_verified_change"
		}, "unresolved"},
		{"conflict", func(s *Signals) { s.GuestAFirstOutcome = "stayed_answer_first" }, "conflict"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := validSignals()
			tc.mutate(&s)
			graph, err := Build(digest, s)
			if err != nil {
				t.Fatal(err)
			}
			if got := graph.Edges[2].Relation; got != tc.want {
				t.Fatalf("relation = %q", got)
			}
		})
	}
	s := validSignals()
	s.AnswerProof = "model_says_yes"
	if _, err := Build(digest, s); err == nil {
		t.Fatal("unknown proof was accepted")
	}
}

type blockingExporter struct {
	once    sync.Once
	started chan struct{}
}

func TestBuildAcceptsProductionCoachVocabulary(t *testing.T) {
	tests := []Signals{
		{AssistanceTarget: "assistant", RespondentStage: "none", CoachPhase: "none", CoachAction: "none", AnswerProof: "none", AnswerTransitionProof: "none", GuestAFirstOutcome: "no_verified_change"},
		{AssistanceTarget: "respondent", RespondentStage: "awaiting_answer", CoachPhase: "awaiting_answer", CoachAction: "elicit", AnswerProof: "none", AnswerTransitionProof: "none", GuestAFirstOutcome: "no_verified_change"},
		{AssistanceTarget: "respondent", RespondentStage: "restructure", CoachPhase: "awaiting_restatement", CoachAction: "restate", AnswerProof: "none", AnswerTransitionProof: "none", GuestAFirstOutcome: "no_verified_change"},
		{AssistanceTarget: "respondent", RespondentStage: "restructure", CoachPhase: "expanding", CoachAction: "expand", AnswerProof: "question_bound_input_answer_first", AnswerTransitionProof: "none", GuestAFirstOutcome: "stayed_answer_first"},
		{AssistanceTarget: "respondent", RespondentStage: "restructure", CoachPhase: "complete", CoachAction: "complete", AnswerProof: "question_bound_input_answer_first", AnswerTransitionProof: "none", GuestAFirstOutcome: "stayed_answer_first"},
		{AssistanceTarget: "respondent", RespondentStage: "awaiting_answer", CoachPhase: "blocked", CoachAction: "retry", AnswerProof: "none", AnswerTransitionProof: "none", GuestAFirstOutcome: "no_verified_change"},
		{AssistanceTarget: "respondent", RespondentStage: "restructure", CoachPhase: "complete", CoachAction: "release", AnswerProof: "question_bound_input_answer_first", AnswerTransitionProof: "none", GuestAFirstOutcome: "stayed_answer_first"},
	}
	for index, signals := range tests {
		if _, err := Build(strings.Repeat("1", 64), signals); err != nil {
			t.Fatalf("production state %d rejected: %+v: %v", index, signals, err)
		}
	}
}

type resultExporter struct {
	err error
}

func (e resultExporter) Export(context.Context, Graph) error { return e.err }

func waitForSnapshot(t *testing.T, d *Dispatcher, ready func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := d.Snapshot()
		if ready(got) {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("dispatcher snapshot did not converge: %+v", d.Snapshot())
	return Snapshot{}
}

func (e *blockingExporter) Export(ctx context.Context, _ Graph) error {
	e.once.Do(func() { close(e.started) })
	<-ctx.Done()
	return ctx.Err()
}

func TestObserveNeverWaitsForExporter(t *testing.T) {
	e := &blockingExporter{started: make(chan struct{})}
	d, err := NewDispatcher(e)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	started := time.Now()
	if !d.Observe(strings.Repeat("b", 64), validSignals()) {
		t.Fatal("first observation dropped")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Millisecond {
		t.Fatalf("Observe blocked for %s", elapsed)
	}
	select {
	case <-e.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
}

func TestDispatcherCountsFailuresWithoutContent(t *testing.T) {
	d, err := NewDispatcher(resultExporter{err: errors.New("receiver returned 500 with secret answer")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !d.Observe(strings.Repeat("d", 64), validSignals()) {
		t.Fatal("observation dropped")
	}
	got := waitForSnapshot(t, d, func(stats Snapshot) bool { return stats.ExportFailed == 1 })
	if got.Accepted != 1 || got.Exported != 0 || got.ExportTimedOut != 0 {
		t.Fatalf("snapshot = %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "answer", strings.Repeat("d", 64)} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("snapshot leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestDispatcherTimeoutIsBoundedAndCounted(t *testing.T) {
	e := &blockingExporter{started: make(chan struct{})}
	d, err := NewDispatcher(e)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !d.Observe(strings.Repeat("e", 64), validSignals()) {
		t.Fatal("observation dropped")
	}
	got := waitForSnapshot(t, d, func(stats Snapshot) bool { return stats.ExportTimedOut == 1 })
	if got.ExportFailed != 1 || got.Exported != 0 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestDispatcherDropsSaturatedQueueWithoutWaiting(t *testing.T) {
	e := &blockingExporter{started: make(chan struct{})}
	d, err := NewDispatcher(e)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !d.Observe(strings.Repeat("f", 64), validSignals()) {
		t.Fatal("first observation dropped")
	}
	select {
	case <-e.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	for index := 0; index < defaultQueueCapacity; index++ {
		if !d.Observe(strings.Repeat("a", 63)+string(rune('0'+index%10)), validSignals()) {
			t.Fatalf("queue rejected item %d before capacity", index)
		}
	}
	started := time.Now()
	if d.Observe(strings.Repeat("9", 64), validSignals()) {
		t.Fatal("saturated queue accepted another observation")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Millisecond {
		t.Fatalf("saturated Observe blocked for %s", elapsed)
	}
	got := d.Snapshot()
	if got.Accepted != defaultQueueCapacity+1 || got.DroppedFull != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	d.Close()
	if d.Observe(strings.Repeat("8", 64), validSignals()) {
		t.Fatal("closed dispatcher accepted observation")
	}
	if got := d.Snapshot(); got.DroppedClosed != 1 {
		t.Fatalf("closed snapshot = %+v", got)
	}
}

func TestHTTPExporterRejects429And500WithoutReadingMessages(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("sensitive upstream message"))
			}))
			defer server.Close()
			exporter, err := NewHTTPExporter(server.Client(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			graph, _ := Build(strings.Repeat("7", 64), validSignals())
			err = exporter.Export(context.Background(), graph)
			if err == nil || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("export error = %v", err)
			}
		})
	}
}

func TestHTTPExporterExactContract(t *testing.T) {
	var received map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/shadow/graphs" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	exporter, err := NewHTTPExporter(server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	graph, _ := Build(strings.Repeat("c", 64), validSignals())
	if err := exporter.Export(context.Background(), graph); err != nil {
		t.Fatal(err)
	}
	if received["schemaVersion"] != float64(1) {
		t.Fatalf("payload = %+v", received)
	}
	if _, ok := received["caption"]; ok {
		t.Fatal("caption escaped")
	}
}
