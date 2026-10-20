package guesttrial

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func verificationFixture(t *testing.T) (*Service, Owner, Snapshot) {
	t.Helper()
	store, err := NewMemoryStore(8)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	owner := Owner{UID: "guest", AppID: "app"}
	first, err := service.Begin(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	return service, owner, first
}

func TestVerificationCommitsOnlyOwnedSnapshot(t *testing.T) {
	for _, verdict := range []Verdict{VerdictAnswerFirst, VerdictUnverified} {
		for _, assisted := range []bool{false, true} {
			service, owner, first := verificationFixture(t)
			if assisted {
				var err error
				first, err = service.Update(context.Background(), owner, first.ID, 1, "help")
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			verify := func(_ context.Context, actual Snapshot) (Verdict, error) {
				calls++
				if actual != first {
					t.Fatal("verifier received a different question or assistance scope")
				}
				return verdict, nil
			}
			for _, other := range []Owner{{UID: "other", AppID: owner.AppID}, {UID: owner.UID, AppID: "other"}} {
				if _, err := service.Evaluate(context.Background(), other, first.ID, first.Generation, verify); !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
			}
			wrongGeneration := int64(2)
			if assisted {
				wrongGeneration = 1
			}
			if _, err := service.Evaluate(context.Background(), owner, first.ID, wrongGeneration, verify); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatal("unauthorized or stale work reached verifier")
			}
			got, err := service.Evaluate(context.Background(), owner, first.ID, first.Generation, verify)
			if err != nil || got.Assisted != assisted || got.Generation != first.Generation+1 ||
				got.ExpiresAt != first.ExpiresAt || (got.Status == AnswerFirst) != (verdict == VerdictAnswerFirst) {
				t.Fatal(got, err)
			}
			if _, err := service.Evaluate(context.Background(), owner, first.ID, got.Generation, verify); !errors.Is(err, ErrConflict) {
				t.Fatal("terminal retry", err)
			}
			if calls != 1 {
				t.Fatal("terminal retry repeated verifier")
			}
		}
	}
}

func TestVerificationCannotPublishAfterHelpStopExpiryOrCancellation(t *testing.T) {
	for _, action := range []string{"help", "stop", "expire", "cancel", "provider_error", "invalid_verdict"} {
		t.Run(action, func(t *testing.T) {
			service, owner, first := verificationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			providerError := errors.New("verification unavailable")
			got, err := service.Evaluate(ctx, owner, first.ID, 1, func(_ context.Context, _ Snapshot) (Verdict, error) {
				switch action {
				case "help", "stop":
					if _, err := service.Update(ctx, owner, first.ID, 1, action); err != nil {
						t.Fatal(err)
					}
				case "expire":
					service.now = func() time.Time { return first.ExpiresAt }
				case "cancel":
					cancel()
				case "provider_error":
					return 0, providerError
				case "invalid_verdict":
					return 0, nil
				}
				return VerdictAnswerFirst, nil
			})
			want := map[string]error{
				"help": ErrConflict, "stop": ErrConflict, "expire": ErrExpired,
				"cancel": context.Canceled, "provider_error": providerError, "invalid_verdict": ErrInvalid,
			}[action]
			if !errors.Is(err, want) || got != (Snapshot{}) {
				t.Fatal("uncommitted result escaped", got, err)
			}
			service.now = time.Now
			current, err := service.Read(context.Background(), owner, first.ID)
			if err != nil || current.Status == AnswerFirst || current.Status == Unverified {
				t.Fatal("failure became a judgement", current, err)
			}
		})
	}
}

func TestConcurrentVerificationPublishesOneResult(t *testing.T) {
	service, owner, first := verificationFixture(t)
	var entered sync.WaitGroup
	entered.Add(2)
	release := make(chan struct{})
	var published atomic.Int32
	var finished sync.WaitGroup
	finished.Add(2)
	for range 2 {
		go func() {
			defer finished.Done()
			got, err := service.Evaluate(context.Background(), owner, first.ID, 1, func(context.Context, Snapshot) (Verdict, error) {
				entered.Done()
				<-release
				return VerdictAnswerFirst, nil
			})
			if err == nil {
				published.Add(1)
			} else if !errors.Is(err, ErrConflict) || got != (Snapshot{}) {
				t.Error(got, err)
			}
		}()
	}
	entered.Wait()
	close(release)
	finished.Wait()
	if published.Load() != 1 {
		t.Fatal("duplicate publication", published.Load())
	}
}
