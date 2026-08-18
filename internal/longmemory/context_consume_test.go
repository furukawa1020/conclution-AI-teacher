package longmemory

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestContextCapabilityConcurrentConsumeHasExactlyOneWinner(t *testing.T) {
	manager, want := enabledMemoryContext(t)
	token, available, err := manager.BeginContext(context.Background(), "uid-a", "app-a")
	if err != nil || !available {
		t.Fatal(err)
	}
	const attempts = 100
	var successes atomic.Int32
	var replays atomic.Int32
	var unexpected atomic.Int32
	var wait sync.WaitGroup
	start := make(chan struct{})
	for range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			payload, generation, err := manager.ConsumeContext(context.Background(), "uid-a", "app-a", token)
			switch {
			case err == nil && generation == 1 && len(payload.Topics) == 1 && payload.Topics[0] == want.Topics[0]:
				successes.Add(1)
			case errors.Is(err, ErrReplay):
				replays.Add(1)
			default:
				unexpected.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if successes.Load() != 1 || replays.Load() != attempts-1 || unexpected.Load() != 0 {
		t.Fatalf("success=%d replay=%d unexpected=%d", successes.Load(), replays.Load(), unexpected.Load())
	}
}

func TestRejectedContextConsumeDoesNotSpendCapability(t *testing.T) {
	manager, _ := enabledMemoryContext(t)
	token, available, err := manager.BeginContext(context.Background(), "uid-a", "app-a")
	if err != nil || !available {
		t.Fatal(err)
	}
	for _, principal := range []struct{ uid, app string }{{"uid-b", "app-a"}, {"uid-a", "app-b"}} {
		if _, _, err := manager.ConsumeContext(context.Background(), principal.uid, principal.app, token); !errors.Is(err, ErrInvalid) {
			t.Fatalf("foreign principal %+v err=%v", principal, err)
		}
	}
	if _, _, err := manager.ConsumeContext(context.Background(), "uid-a", "app-a", token); err != nil {
		t.Fatalf("valid consume after rejected attempts: %v", err)
	}
}

func TestOptOutAndGenerationChangeRejectWithoutCreatingUse(t *testing.T) {
	manager, _ := enabledMemoryContext(t)
	token, available, err := manager.BeginContext(context.Background(), "uid-a", "app-a")
	if err != nil || !available {
		t.Fatal(err)
	}
	if err := manager.DisableAndDelete(context.Background(), "uid-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.ConsumeContext(context.Background(), "uid-a", "app-a", token); !errors.Is(err, ErrDisabled) {
		t.Fatalf("opt-out err=%v", err)
	}
	if _, err := manager.Enable(context.Background(), "uid-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.ConsumeContext(context.Background(), "uid-a", "app-a", token); !errors.Is(err, ErrStale) {
		t.Fatalf("new generation err=%v", err)
	}
}
