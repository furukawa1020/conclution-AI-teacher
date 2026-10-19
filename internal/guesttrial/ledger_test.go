package guesttrial

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func initial() Record {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	return Record{Version: 1, Question: QuestionID, Generation: 1, Status: Pending, CreatedAt: now, ExpiresAt: now.Add(TTL)}
}

func TestLedgerMonotonicAssistanceAndTerminal(t *testing.T) {
	r := initial()
	a, err := transition(r, 1, Help, r.CreatedAt)
	if err != nil || !a.Assisted || a.Generation != 2 || a.ExpiresAt != r.ExpiresAt {
		t.Fatal(a, err)
	}
	if _, err := transition(a, 1, CompleteVerified, r.CreatedAt); !errors.Is(err, ErrConflict) {
		t.Fatal("replay", err)
	}
	if _, err := transition(a, 2, Help, r.CreatedAt); !errors.Is(err, ErrConflict) {
		t.Fatal("repeat help", err)
	}
	for _, event := range []Event{Stop, CompleteVerified, CompleteUnverified} {
		end, err := transition(a, 2, event, r.CreatedAt)
		if err != nil || !end.Assisted || end.Generation != 3 {
			t.Fatal(end, err)
		}
		if _, err := transition(end, 3, Help, r.CreatedAt); !errors.Is(err, ErrConflict) {
			t.Fatal("reopened", err)
		}
	}
	if _, err := transition(r, 1, Help, r.ExpiresAt); !errors.Is(err, ErrExpired) {
		t.Fatal("deadline", err)
	}
	if _, err := transition(r, 1, Event(99), r.CreatedAt); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestMemoryLedgerConcurrentReplayAndCapacity(t *testing.T) {
	s, _ := NewMemoryStore(1)
	r, key := initial(), strings.Repeat("a", 64)
	if err := s.Create(context.Background(), key, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(context.Background(), key, r); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := s.Create(context.Background(), strings.Repeat("b", 64), r); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Apply(context.Background(), key, 1, Help, r.CreatedAt)
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("concurrent writes", wins.Load())
	}
	got, err := s.Read(context.Background(), key, r.CreatedAt)
	if err != nil || !got.Assisted || got.Generation != 2 {
		t.Fatal(got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Apply(ctx, key, 2, Stop, r.CreatedAt); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	got, _ = s.Read(context.Background(), key, r.CreatedAt)
	if got.Status != Pending {
		t.Fatal("cancel mutated state")
	}
}
