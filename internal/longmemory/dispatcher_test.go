package longmemory

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type dispatcherSource struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	panicAt int32
}

func (s *dispatcherSource) LongTermMemoryCandidate(string, string) (Payload, bool, error) {
	call := s.calls.Add(1)
	if s.started != nil && call == 1 {
		close(s.started)
	}
	if s.release != nil && call == 1 {
		<-s.release
	}
	if call == s.panicAt {
		panic("private source failure")
	}
	return Payload{Topics: []string{"quiet conversation"}}, true, nil
}

type countingStore struct {
	Store
	puts atomic.Int32
}

func (s *countingStore) Put(ctx context.Context, key string, generation int64, record Record, now time.Time) error {
	s.puts.Add(1)
	return s.Store.Put(ctx, key, generation, record, now)
}

func dispatcherManager(t *testing.T, store Store) *Manager {
	t.Helper()
	manager, err := New(bytes.Repeat([]byte{0x41}, 32), store)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestDispatcherEnqueueNeverWaitsForCandidateOrStorage(t *testing.T) {
	manager := dispatcherManager(t, NewMemoryStore())
	if _, err := manager.Enable(context.Background(), "user"); err != nil {
		t.Fatal(err)
	}
	source := &dispatcherSource{started: make(chan struct{}), release: make(chan struct{})}
	dispatcher, err := NewDispatcher(manager, source, DispatcherOptions{QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	if !dispatcher.Enqueue("user", "opaque-state") {
		t.Fatal("first job was rejected")
	}
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("enqueue joined asynchronous work: %s", elapsed)
	}
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if !dispatcher.Enqueue("user", "queued-state") {
		t.Fatal("bounded queue did not accept its capacity")
	}
	if dispatcher.Enqueue("user", "must-drop") {
		t.Fatal("full queue blocked or accepted excess work")
	}
	close(source.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherDisabledConsentGeneratesNoPayloadAndWritesNothing(t *testing.T) {
	store := &countingStore{Store: NewMemoryStore()}
	manager := dispatcherManager(t, store)
	source := &dispatcherSource{}
	outcomes := make(chan Outcome, 1)
	dispatcher, err := NewDispatcher(manager, source, DispatcherOptions{Observer: func(outcome Outcome, _ time.Duration) { outcomes <- outcome }})
	if err != nil {
		t.Fatal(err)
	}
	if !dispatcher.Enqueue("disabled-user", "opaque-state") {
		t.Fatal("job was rejected")
	}
	select {
	case outcome := <-outcomes:
		if outcome != OutcomeDisabled {
			t.Fatalf("outcome=%q", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("outcome timed out")
	}
	if source.calls.Load() != 0 || store.puts.Load() != 0 {
		t.Fatalf("disabled work crossed boundary: candidates=%d puts=%d", source.calls.Load(), store.puts.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherExpiresQueuedStateBeforeConsentOrCandidateWork(t *testing.T) {
	manager := dispatcherManager(t, NewMemoryStore())
	source := &dispatcherSource{}
	fixedNow := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	var outcome Outcome
	dispatcher := &Dispatcher{
		manager: manager,
		source:  source,
		now:     func() time.Time { return fixedNow },
		ttl:     time.Second,
		timeout: time.Second,
		observe: func(value Outcome, _ time.Duration) { outcome = value },
	}
	token := []byte("expired-opaque-state")
	dispatcher.process(context.Background(), memoryJob{
		uid:        "user",
		stateToken: token,
		createdAt:  fixedNow.Add(-time.Second - time.Nanosecond),
	})
	if outcome != OutcomeExpired || source.calls.Load() != 0 {
		t.Fatalf("expired outcome=%q source calls=%d", outcome, source.calls.Load())
	}
	for index, value := range token {
		if value != 0 {
			t.Fatalf("expired token byte %d was not zeroized", index)
		}
	}
}

func TestDispatcherRecoversPerJobWithoutLeakingPanicOrLosingWorker(t *testing.T) {
	manager := dispatcherManager(t, NewMemoryStore())
	if _, err := manager.Enable(context.Background(), "user"); err != nil {
		t.Fatal(err)
	}
	source := &dispatcherSource{panicAt: 1}
	outcomes := make(chan Outcome, 2)
	dispatcher, err := NewDispatcher(manager, source, DispatcherOptions{Observer: func(outcome Outcome, _ time.Duration) { outcomes <- outcome }})
	if err != nil {
		t.Fatal(err)
	}
	if !dispatcher.Enqueue("user", "first") {
		t.Fatal("first job was rejected")
	}
	if outcome := <-outcomes; outcome != OutcomeInvalid {
		t.Fatalf("panic outcome=%q", outcome)
	}
	if !dispatcher.Enqueue("user", "second") {
		t.Fatal("second job was rejected")
	}
	select {
	case outcome := <-outcomes:
		if outcome != OutcomeSaved {
			t.Fatalf("second outcome=%q", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("worker was lost after source panic")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
