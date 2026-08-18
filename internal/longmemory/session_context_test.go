package longmemory

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionContextIsOpaquePrincipalBoundAndDatabaseFree(t *testing.T) {
	base := NewMemoryStore()
	store := &contextCountingStore{Store: base}
	manager, err := New(bytes.Repeat([]byte{0x51}, 32), store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	consent, _ := manager.Enable(context.Background(), "uid-a")
	want := Payload{Topics: []string{"quiet topic"}, Preferences: []string{"short reply"}}
	if err := manager.Save(context.Background(), "uid-a", consent.Generation, want); err != nil {
		t.Fatal(err)
	}
	capability, available, err := manager.BeginContext(context.Background(), "uid-a", "app-a")
	if err != nil || !available {
		t.Fatal(err)
	}
	session, expires, err := manager.ConsumeSessionContext(context.Background(), "uid-a", "app-a", capability)
	if err != nil || expires != 900 || !strings.HasPrefix(session, sessionContextPrefix) {
		t.Fatalf("expires=%d session=%q err=%v", expires, session, err)
	}
	for _, forbidden := range []string{"uid-a", "app-a", "quiet topic", "short reply"} {
		if strings.Contains(session, forbidden) {
			t.Fatalf("session exposed %q", forbidden)
		}
	}
	store.statusCalls, store.getCalls, store.getCurrentCalls = 0, 0, 0
	for turn := range 1000 {
		got, generation, err := manager.OpenSessionContext("uid-a", "app-a", session)
		if err != nil || generation != consent.Generation || got.Topics[0] != want.Topics[0] {
			t.Fatalf("turn=%d payload=%+v generation=%d err=%v", turn, got, generation, err)
		}
	}
	if store.statusCalls != 0 || store.getCalls != 0 || store.getCurrentCalls != 0 {
		t.Fatalf("session validation reached store: status=%d get=%d current=%d", store.statusCalls, store.getCalls, store.getCurrentCalls)
	}
	for _, principal := range []struct{ uid, app string }{{"uid-b", "app-a"}, {"uid-a", "app-b"}} {
		if _, _, err := manager.OpenSessionContext(principal.uid, principal.app, session); !errors.Is(err, ErrInvalid) {
			t.Fatalf("foreign principal %+v err=%v", principal, err)
		}
	}
}

func TestSessionContextUsesThirdAEADAndRejectsTamper(t *testing.T) {
	manager, _ := enabledMemoryContext(t)
	capability, _, _ := manager.BeginContext(context.Background(), "uid-a", "app-a")
	session, _, err := manager.ConsumeSessionContext(context.Background(), "uid-a", "app-a", capability)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(session, sessionContextPrefix))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	uidDigest, _ := manager.principalKey("uid-a")
	appDigest := manager.appIDKey("app-a")
	for name, candidate := range map[string]cipher.AEAD{"persistence": manager.aead, "capability": manager.contextAEAD} {
		if _, err := candidate.Open(nil, raw[:candidate.NonceSize()], raw[candidate.NonceSize():], sessionContextAAD(uidDigest, appDigest)); err == nil {
			t.Fatalf("%s AEAD opened session context", name)
		}
	}
	plaintext, err := manager.sessionAEAD.Open(nil, raw[:manager.sessionAEAD.NonceSize()], raw[manager.sessionAEAD.NonceSize():], sessionContextAAD(uidDigest, appDigest))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plaintext)
	var envelope sessionContextEnvelope
	if json.Unmarshal(plaintext, &envelope) != nil {
		t.Fatal("session envelope decode failed")
	}
	envelope.Version++
	unknownPlaintext, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(unknownPlaintext)
	nonce := bytes.Repeat([]byte{0x7a}, manager.sessionAEAD.NonceSize())
	unknownRaw := append(append([]byte(nil), nonce...), manager.sessionAEAD.Seal(nil, nonce, unknownPlaintext, sessionContextAAD(uidDigest, appDigest))...)
	unknown := sessionContextPrefix + base64.RawURLEncoding.EncodeToString(unknownRaw)
	clear(unknownRaw)
	if _, _, err := manager.OpenSessionContext("uid-a", "app-a", unknown); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown schema err=%v", err)
	}
	raw[len(raw)-1] ^= 1
	tampered := sessionContextPrefix + base64.RawURLEncoding.EncodeToString(raw)
	if _, _, err := manager.OpenSessionContext("uid-a", "app-a", tampered); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered err=%v", err)
	}
}

func TestOptOutBeforeSessionConsumeIssuesNothing(t *testing.T) {
	manager, _ := enabledMemoryContext(t)
	capability, _, _ := manager.BeginContext(context.Background(), "uid-a", "app-a")
	if err := manager.DisableAndDelete(context.Background(), "uid-a"); err != nil {
		t.Fatal(err)
	}
	if token, expires, err := manager.ConsumeSessionContext(context.Background(), "uid-a", "app-a", capability); !errors.Is(err, ErrDisabled) || token != "" || expires != 0 {
		t.Fatalf("token=%q expires=%d err=%v", token, expires, err)
	}
}

func TestSessionContextConcurrentConsumeIssuesExactlyOnce(t *testing.T) {
	manager, _ := enabledMemoryContext(t)
	capability, _, _ := manager.BeginContext(context.Background(), "uid-a", "app-a")
	manager.rand = rand.Reader
	var issued atomic.Int32
	var replay atomic.Int32
	var unexpected atomic.Int32
	var wait sync.WaitGroup
	start := make(chan struct{})
	for range 100 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			token, expires, err := manager.ConsumeSessionContext(context.Background(), "uid-a", "app-a", capability)
			switch {
			case err == nil && token != "" && expires == 900:
				issued.Add(1)
			case errors.Is(err, ErrReplay) && token == "" && expires == 0:
				replay.Add(1)
			default:
				unexpected.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if issued.Load() != 1 || replay.Load() != 99 || unexpected.Load() != 0 {
		t.Fatalf("issued=%d replay=%d unexpected=%d", issued.Load(), replay.Load(), unexpected.Load())
	}
}

func TestSessionPreparationFailureDoesNotConsumeCapability(t *testing.T) {
	manager, _ := enabledMemoryContext(t)
	capability, _, _ := manager.BeginContext(context.Background(), "uid-a", "app-a")
	manager.rand = io.LimitReader(bytes.NewReader([]byte{1}), 1)
	if token, expires, err := manager.ConsumeSessionContext(context.Background(), "uid-a", "app-a", capability); !errors.Is(err, ErrInvalid) || token != "" || expires != 0 {
		t.Fatalf("token=%q expires=%d err=%v", token, expires, err)
	}
	manager.rand = bytes.NewReader(bytes.Repeat([]byte{2}, 64))
	if token, expires, err := manager.ConsumeSessionContext(context.Background(), "uid-a", "app-a", capability); err != nil || token == "" || expires != 900 {
		t.Fatalf("retry token=%q expires=%d err=%v", token, expires, err)
	}
}

func TestSessionContextExactExpiryAndFutureBoundary(t *testing.T) {
	manager, _ := enabledMemoryContext(t)
	issuedAt := manager.now().UTC().Truncate(time.Second)
	capability, _, _ := manager.BeginContext(context.Background(), "uid-a", "app-a")
	session, _, err := manager.ConsumeSessionContext(context.Background(), "uid-a", "app-a", capability)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return issuedAt.Add(SessionContextTTL - time.Second) }
	if _, _, err := manager.OpenSessionContext("uid-a", "app-a", session); err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return issuedAt.Add(SessionContextTTL) }
	if _, _, err := manager.OpenSessionContext("uid-a", "app-a", session); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expiry err=%v", err)
	}
	manager.now = func() time.Time { return issuedAt.Add(-31 * time.Second) }
	if _, _, err := manager.OpenSessionContext("uid-a", "app-a", session); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future err=%v", err)
	}
}
