package guard

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryLimiterEnforcesMinuteAndDailyWindows(t *testing.T) {
	t.Parallel()

	limiter, err := NewMemoryLimiter(Limits{PerMinute: 2, PerDay: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := time.Date(2026, time.July, 29, 10, 15, 30, 0, time.FixedZone("JST", 9*60*60))

	if err := limiter.Consume(ctx, "private-user-id", start); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := limiter.Consume(ctx, "private-user-id", start.Add(10*time.Second)); err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if err := limiter.Consume(ctx, "private-user-id", start.Add(20*time.Second)); !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("minute overflow = %v; want ErrRateLimitExceeded", err)
	}

	if err := limiter.Consume(ctx, "private-user-id", start.Add(time.Minute)); err != nil {
		t.Fatalf("new minute consume: %v", err)
	}
	if err := limiter.Consume(ctx, "private-user-id", start.Add(2*time.Minute)); !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("daily overflow = %v; want ErrRateLimitExceeded", err)
	}

	nextUTCDate := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)
	if err := limiter.Consume(ctx, "private-user-id", nextUTCDate); err != nil {
		t.Fatalf("new UTC day consume: %v", err)
	}
}

func TestMemoryLimiterIsAtomicUnderConcurrency(t *testing.T) {
	t.Parallel()

	const allowed = 5
	limiter, err := NewMemoryLimiter(Limits{PerMinute: allowed, PerDay: 40})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC)
	var successes atomic.Int32
	var unexpected atomic.Int32
	var wait sync.WaitGroup

	for range 50 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := limiter.Consume(context.Background(), "same-user", now)
			switch {
			case err == nil:
				successes.Add(1)
			case !errors.Is(err, ErrRateLimitExceeded):
				unexpected.Add(1)
			}
		}()
	}
	wait.Wait()

	if got := successes.Load(); got != allowed {
		t.Fatalf("successful consumes = %d; want %d", got, allowed)
	}
	if got := unexpected.Load(); got != 0 {
		t.Fatalf("unexpected errors = %d; want 0", got)
	}
}

func TestMemoryLimiterStoresOnlyUIDDigest(t *testing.T) {
	t.Parallel()

	const uid = "sensitive-firebase-uid"
	limiter, err := NewMemoryLimiter(Limits{PerMinute: 5, PerDay: 40})
	if err != nil {
		t.Fatal(err)
	}
	if err := limiter.Consume(context.Background(), uid, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, rawUIDStored := limiter.counters[uid]; rawUIDStored {
		t.Fatal("raw UID was used as an in-memory counter key")
	}
	documentID, err := userDocumentID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(documentID) != 64 {
		t.Fatalf("digest length = %d; want 64", len(documentID))
	}
	if _, digestStored := limiter.counters[documentID]; !digestStored {
		t.Fatal("hashed UID counter key is missing")
	}
}

func TestLimitsRejectUnsafeValues(t *testing.T) {
	t.Parallel()

	for _, limits := range []Limits{
		{PerMinute: 0, PerDay: 40},
		{PerMinute: MaxPerMinute + 1, PerDay: 40},
		{PerMinute: 5, PerDay: 0},
		{PerMinute: 5, PerDay: MaxPerDay + 1},
	} {
		if err := limits.Validate(); err == nil {
			t.Fatalf("Validate(%+v) succeeded; want error", limits)
		}
	}
}
