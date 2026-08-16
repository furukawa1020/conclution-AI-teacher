package semanticshadow

import (
	"context"
	"encoding/json"
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
		CoachPhase: "complete", CoachAction: "complete",
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
