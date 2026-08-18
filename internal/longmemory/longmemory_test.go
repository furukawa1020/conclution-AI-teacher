package longmemory

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"
)

func testManager(t *testing.T) (*Manager, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	manager, err := New(bytes.Repeat([]byte{0x51}, 32), store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
	manager.now = func() time.Time { return now }
	manager.rand = bytes.NewReader(bytes.Repeat([]byte{0x27}, 256))
	return manager, store
}

func TestManagerEncryptsFiniteMemoryAndBindsCallerGeneration(t *testing.T) {
	manager, store := testManager(t)
	ctx := context.Background()
	consent, err := manager.Enable(ctx, "uid-a")
	if err != nil {
		t.Fatal(err)
	}
	payload := Payload{Topics: []string{"小声でも安心して話す"}, Preferences: []string{"短く返してほしい"}, OpenLoops: []string{"次は会話の続きを試す"}}
	if err := manager.Save(ctx, "uid-a", consent.Generation, payload); err != nil {
		t.Fatal(err)
	}

	key, _ := manager.principalKey("uid-a")
	record := store.entries[key].record
	if record == nil {
		t.Fatal("encrypted record was not stored")
	}
	for _, secret := range []string{payload.Topics[0], payload.Preferences[0], payload.OpenLoops[0], "uid-a"} {
		if bytes.Contains(record.Ciphertext, []byte(secret)) {
			t.Fatalf("ciphertext exposed %q", secret)
		}
	}
	loaded, err := manager.Load(ctx, "uid-a", consent.Generation)
	if err != nil || loaded.Topics[0] != payload.Topics[0] {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if _, err := manager.Load(ctx, "uid-b", consent.Generation); !errors.Is(err, ErrDisabled) {
		t.Fatalf("foreign caller err=%v", err)
	}
	if _, err := manager.Load(ctx, "uid-a", consent.Generation+1); !errors.Is(err, ErrStale) {
		t.Fatalf("future generation err=%v", err)
	}
}

func TestDisableTombstonePreventsConcurrentOrStaleResurrection(t *testing.T) {
	manager, _ := testManager(t)
	ctx := context.Background()
	consent, err := manager.Enable(ctx, "uid-a")
	if err != nil {
		t.Fatal(err)
	}
	payload := Payload{Topics: []string{"会話の続き"}}
	if err := manager.DisableAndDelete(ctx, "uid-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(ctx, "uid-a", consent.Generation, payload); !errors.Is(err, ErrDisabled) && !errors.Is(err, ErrStale) {
		t.Fatalf("stale save err=%v", err)
	}
	status, err := manager.Status(ctx, "uid-a")
	if err != nil || status.Enabled || status.Generation <= consent.Generation {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err := manager.Load(ctx, "uid-a", consent.Generation); !errors.Is(err, ErrDisabled) {
		t.Fatalf("deleted load err=%v", err)
	}
}

func TestConcurrentSaveCannotCrossOptOutGeneration(t *testing.T) {
	manager, _ := testManager(t)
	manager.rand = rand.Reader
	ctx := context.Background()
	consent, _ := manager.Enable(ctx, "uid-a")
	payload := Payload{Topics: []string{"有限memory"}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _ = manager.Save(ctx, "uid-a", consent.Generation, payload) }()
	}
	close(start)
	if err := manager.DisableAndDelete(ctx, "uid-a"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	status, _ := manager.Status(ctx, "uid-a")
	if status.Enabled {
		t.Fatal("concurrent save resurrected opt-out")
	}
	if _, err := manager.Load(ctx, "uid-a", status.Generation); !errors.Is(err, ErrDisabled) {
		t.Fatalf("load err=%v", err)
	}
}

func TestPayloadRejectsPIIAndUnboundedFields(t *testing.T) {
	manager, _ := testManager(t)
	consent, _ := manager.Enable(context.Background(), "uid-a")
	for _, payload := range []Payload{
		{Topics: []string{"person@example.com"}},
		{Topics: []string{string(bytes.Repeat([]byte("a"), maxItemRunes+1))}},
		{Topics: []string{"a", "b", "c", "d", "e"}},
		{},
	} {
		if err := manager.Save(context.Background(), "uid-a", consent.Generation, payload); !errors.Is(err, ErrInvalid) {
			t.Fatalf("payload=%+v err=%v", payload, err)
		}
	}
}
