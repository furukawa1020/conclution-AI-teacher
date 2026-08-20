package passkey

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type orderedAccountCleaner struct {
	steps *[]string
	err   error
}

func (c orderedAccountCleaner) DisableAndDelete(_ context.Context, uid string) error {
	*c.steps = append(*c.steps, "memory:"+uid)
	return c.err
}

type orderedDeletionStore struct {
	Store
	steps *[]string
}

func (s orderedDeletionStore) DeleteAccountData(ctx context.Context, uid string, now time.Time) error {
	*s.steps = append(*s.steps, "passkeys:"+uid)
	return nil
}

type orderedFirebaseDeleter struct {
	steps *[]string
}

func (d orderedFirebaseDeleter) DeleteAccount(_ context.Context, uid string) error {
	*d.steps = append(*d.steps, "firebase:"+uid)
	return nil
}

func orderedDeletionService(t *testing.T, cleaner AccountDataCleaner, steps *[]string) *Service {
	t.Helper()
	service, err := New(Config{
		RPID:               "kotae-ai.web.app",
		Origin:             "https://kotae-ai.web.app",
		Store:              orderedDeletionStore{Store: NewMemoryStore(), steps: steps},
		TokenMinter:        &recordingMinter{},
		AccountDataCleaner: cleaner,
		AccountDeleter:     orderedFirebaseDeleter{steps: steps},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestAccountDeletionTombstonesMemoryBeforePasskeysAndFirebase(t *testing.T) {
	steps := []string{}
	service := orderedDeletionService(t, orderedAccountCleaner{steps: &steps}, &steps)
	if err := service.DeleteAccount(context.Background(), "uid-a"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"memory:uid-a", "passkeys:uid-a", "firebase:uid-a"}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("deletion order = %v, want %v", steps, want)
	}
}

func TestAccountDeletionStopsBeforePasskeysAndFirebaseWhenTombstoneFails(t *testing.T) {
	steps := []string{}
	service := orderedDeletionService(t, orderedAccountCleaner{
		steps: &steps,
		err:   errors.New("memory unavailable"),
	}, &steps)
	if err := service.DeleteAccount(context.Background(), "uid-a"); !errors.Is(err, ErrAccountDeletion) {
		t.Fatalf("delete error = %v", err)
	}
	if want := []string{"memory:uid-a"}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("deletion steps = %v, want %v", steps, want)
	}
}

func TestAccountDeletionFailsClosedWithoutMemoryCleaner(t *testing.T) {
	steps := []string{}
	service := orderedDeletionService(t, nil, &steps)
	if err := service.DeleteAccount(context.Background(), "uid-a"); !errors.Is(err, ErrAccountDeletion) {
		t.Fatalf("delete error = %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("delete mutated state without cleaner: %v", steps)
	}
}
