package longmemory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type contextCountingStore struct {
	Store
	statusCalls     int
	getCalls        int
	getCurrentCalls int
}

func (s *contextCountingStore) Status(ctx context.Context, key string) (Consent, error) {
	s.statusCalls++
	return s.Store.Status(ctx, key)
}

func (s *contextCountingStore) Get(ctx context.Context, key string, generation int64, now time.Time) (Record, error) {
	s.getCalls++
	return s.Store.Get(ctx, key, generation, now)
}

func (s *contextCountingStore) GetCurrent(ctx context.Context, key string, now time.Time) (Consent, Record, error) {
	s.getCurrentCalls++
	return s.Store.GetCurrent(ctx, key, now)
}

func enabledMemoryContext(t *testing.T) (*Manager, Payload) {
	t.Helper()
	manager, _ := testManager(t)
	consent, err := manager.Enable(context.Background(), "uid-a")
	if err != nil {
		t.Fatal(err)
	}
	payload := Payload{
		Topics:      []string{"quiet conversation"},
		Preferences: []string{"short questions"},
		OpenLoops:   []string{"continue next time"},
	}
	if err := manager.Save(context.Background(), "uid-a", consent.Generation, payload); err != nil {
		t.Fatal(err)
	}
	return manager, payload
}

func TestContextCapabilityIsOpaquePrincipalBoundAndGenerationChecked(t *testing.T) {
	manager, want := enabledMemoryContext(t)
	token, available, err := manager.BeginContext(context.Background(), "uid-a", "app-a")
	if err != nil || !available || !strings.HasPrefix(token, contextCapabilityPrefix) {
		t.Fatalf("available=%v token=%q err=%v", available, token, err)
	}
	for _, forbidden := range []string{"uid-a", "app-a", want.Topics[0], want.Preferences[0], want.OpenLoops[0]} {
		if strings.Contains(token, forbidden) {
			t.Fatalf("capability exposed %q", forbidden)
		}
	}
	got, generation, err := manager.OpenContext(context.Background(), "uid-a", "app-a", token)
	if err != nil || generation != 1 || got.Topics[0] != want.Topics[0] {
		t.Fatalf("memory=%+v generation=%d err=%v", got, generation, err)
	}
	for _, principal := range []struct{ uid, app string }{{"uid-b", "app-a"}, {"uid-a", "app-b"}} {
		if _, _, err := manager.OpenContext(context.Background(), principal.uid, principal.app, token); !errors.Is(err, ErrInvalid) {
			t.Fatalf("foreign principal %+v err=%v", principal, err)
		}
	}
	raw, decodeErr := decodeContextRaw(token)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	raw[len(raw)-1] ^= 0x01
	tampered := contextCapabilityPrefix + base64.RawURLEncoding.EncodeToString(raw)
	clear(raw)
	if _, _, err := manager.OpenContext(context.Background(), "uid-a", "app-a", tampered); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered err=%v", err)
	}
	if err := manager.DisableAndDelete(context.Background(), "uid-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.OpenContext(context.Background(), "uid-a", "app-a", token); !errors.Is(err, ErrDisabled) {
		t.Fatalf("opt-out capability err=%v", err)
	}
	consent, err := manager.Enable(context.Background(), "uid-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(context.Background(), "uid-a", consent.Generation, Payload{Topics: []string{"new generation"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.OpenContext(context.Background(), "uid-a", "app-a", token); !errors.Is(err, ErrStale) {
		t.Fatalf("previous-generation capability err=%v", err)
	}
}

func TestContextCapabilityExpiresAtExactFiniteBoundary(t *testing.T) {
	manager, _ := enabledMemoryContext(t)
	issuedAt := manager.now()
	token, available, err := manager.BeginContext(context.Background(), "uid-a", "app-a")
	if err != nil || !available {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return issuedAt.Add(ContextCapabilityTTL - time.Second) }
	if _, _, err := manager.OpenContext(context.Background(), "uid-a", "app-a", token); err != nil {
		t.Fatalf("capability expired early: %v", err)
	}
	manager.now = func() time.Time { return issuedAt.Add(ContextCapabilityTTL) }
	if _, _, err := manager.OpenContext(context.Background(), "uid-a", "app-a", token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expiry boundary err=%v", err)
	}
}

func TestContextBeginDoesNotCreateCapabilityWithoutOptInMemory(t *testing.T) {
	manager, _ := testManager(t)
	for _, uid := range []string{"disabled-a", "disabled-b"} {
		token, available, err := manager.BeginContext(context.Background(), uid, "app-a")
		if err != nil || available || token != "" {
			t.Fatalf("uid=%q available=%v token=%q err=%v", uid, available, token, err)
		}
	}
	if _, err := manager.Enable(context.Background(), "enabled-without-memory"); err != nil {
		t.Fatal(err)
	}
	token, available, err := manager.BeginContext(context.Background(), "enabled-without-memory", "app-a")
	if err != nil || available || token != "" {
		t.Fatalf("no memory available=%v token=%q err=%v", available, token, err)
	}
}

func TestContextBeginUsesOneAtomicCurrentSnapshotAndFailsClosedOnEntropy(t *testing.T) {
	base := NewMemoryStore()
	store := &contextCountingStore{Store: base}
	manager, err := New(bytes.Repeat([]byte{0x31}, 32), store)
	if err != nil {
		t.Fatal(err)
	}
	fixedNow := time.Date(2026, 8, 18, 5, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return fixedNow }
	consent, err := manager.Enable(context.Background(), "uid-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(context.Background(), "uid-a", consent.Generation, Payload{Topics: []string{"safe topic"}}); err != nil {
		t.Fatal(err)
	}
	store.statusCalls, store.getCalls, store.getCurrentCalls = 0, 0, 0
	manager.rand = bytes.NewReader(bytes.Repeat([]byte{0x11}, 64))
	if _, available, err := manager.BeginContext(context.Background(), "uid-a", "app-a"); err != nil || !available {
		t.Fatalf("available=%v err=%v", available, err)
	}
	if store.getCurrentCalls != 1 || store.statusCalls != 0 || store.getCalls != 0 {
		t.Fatalf("status=%d get=%d current=%d", store.statusCalls, store.getCalls, store.getCurrentCalls)
	}
	manager.rand = io.LimitReader(bytes.NewReader([]byte{0x01}), 1)
	if token, available, err := manager.BeginContext(context.Background(), "uid-a", "app-a"); !errors.Is(err, ErrInvalid) || available || token != "" {
		t.Fatalf("entropy failure available=%v token=%q err=%v", available, token, err)
	}
}

func TestContextCapabilityUsesDistinctAEADAnd128BitIdentifier(t *testing.T) {
	manager, _ := enabledMemoryContext(t)
	manager.rand = bytes.NewReader(bytes.Repeat([]byte{0x42}, 64))
	token, available, err := manager.BeginContext(context.Background(), "uid-a", "app-a")
	if err != nil || !available {
		t.Fatal(err)
	}
	raw, err := decodeContextRaw(token)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	if _, err := manager.aead.Open(nil, raw[:manager.aead.NonceSize()], raw[manager.aead.NonceSize():], contextAADMust(manager, "uid-a", "app-a")); err == nil {
		t.Fatal("persistence AEAD opened context capability")
	}
	plaintext, err := manager.contextAEAD.Open(nil, raw[:manager.contextAEAD.NonceSize()], raw[manager.contextAEAD.NonceSize():], contextAADMust(manager, "uid-a", "app-a"))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plaintext)
	var envelope contextEnvelope
	if json.Unmarshal(plaintext, &envelope) != nil {
		t.Fatal("context envelope could not be decoded")
	}
	id, err := base64.RawURLEncoding.DecodeString(envelope.CapabilityID)
	defer clear(id)
	if err != nil || len(id) != contextCapabilityIDBytes {
		t.Fatalf("capability id bytes=%d err=%v", len(id), err)
	}
	payload, _, err := manager.OpenContext(context.Background(), "uid-a", "app-a", token)
	if err != nil || len(payload.Topics) != 1 {
		t.Fatal(err)
	}
}

func decodeContextRaw(token string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, contextCapabilityPrefix))
}

func contextAADMust(manager *Manager, uid, appID string) []byte {
	key, _ := manager.principalKey(uid)
	return contextAAD(key, manager.appIDKey(appID))
}
