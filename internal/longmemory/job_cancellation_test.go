package longmemory

import (
	"context"
	"testing"
	"time"
)

type cancelBoundaryStore struct {
	Store
	statusCalls, puts int
	onStatus          func()
}

func (s *cancelBoundaryStore) Status(context.Context, string) (Consent, error) {
	s.statusCalls++
	s.onStatus()
	return Consent{Enabled: true, Generation: 1}, nil
}
func (s *cancelBoundaryStore) Put(context.Context, string, int64, Record, time.Time) error {
	s.puts++
	return nil
}

type cancelBoundarySource struct {
	calls       int
	onCandidate func()
}

func (s *cancelBoundarySource) LongTermMemoryCandidate(string, string) (Payload, bool, error) {
	s.calls++
	s.onCandidate()
	return Payload{Topics: []string{"quiet conversation"}}, true, nil
}

func TestMemoryJobStopsAtCanceledStageBoundary(t *testing.T) {
	for _, phase := range []string{"before", "status", "candidate", "normal"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &cancelBoundaryStore{Store: NewMemoryStore(), onStatus: func() {
				if phase == "status" {
					cancel()
				}
			}}
			source := &cancelBoundarySource{onCandidate: func() {
				if phase == "candidate" {
					cancel()
				}
			}}
			d := &Dispatcher{manager: dispatcherManager(t, store), source: source, now: time.Now, ttl: time.Minute, timeout: time.Second}
			token := []byte("opaque-state")
			if phase == "before" {
				cancel()
			}
			d.process(ctx, memoryJob{uid: "user", stateToken: token, createdAt: time.Now()})
			wantStatus, wantCandidates, wantPuts := 1, 1, 0
			if phase == "before" {
				wantStatus, wantCandidates = 0, 0
			}
			if phase == "status" {
				wantCandidates = 0
			}
			if phase == "normal" {
				wantPuts = 1
			}
			if store.statusCalls != wantStatus || source.calls != wantCandidates || store.puts != wantPuts {
				t.Fatalf("status=%d candidates=%d puts=%d", store.statusCalls, source.calls, store.puts)
			}
			for _, b := range token {
				if b != 0 {
					t.Fatal("state token retained")
				}
			}
		})
	}
}
