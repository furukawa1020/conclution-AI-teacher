package guesttrial

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestControlOwnerIsolationAndLostResponseRecovery(t *testing.T) {
	store, _ := NewMemoryStore(8)
	s, _ := NewService(store, bytes.Repeat([]byte{1}, 32))
	now := initial().CreatedAt
	s.now = func() time.Time { return now }
	owner := Owner{UID: "guest-user", AppID: "app-a"}
	first, err := s.Begin(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if first.Assisted || first.Generation != 1 || first.Status != Pending || first.Question != QuestionText {
		t.Fatal(first)
	}
	for _, other := range []Owner{{UID: "other", AppID: owner.AppID}, {UID: owner.UID, AppID: "other"}} {
		if _, err := s.Read(context.Background(), other, first.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("owner isolation", err)
		}
	}
	helped, err := s.Update(context.Background(), owner, first.ID, 1, "help")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(context.Background(), owner, first.ID, 1, "stop"); !errors.Is(err, ErrConflict) {
		t.Fatal("old generation", err)
	}
	recovered, err := s.Read(context.Background(), owner, first.ID)
	if err != nil || recovered != helped {
		t.Fatal("recover committed state", recovered, err)
	}
	if _, err := s.Update(context.Background(), owner, first.ID, 2, "complete"); !errors.Is(err, ErrInvalid) {
		t.Fatal("client completion", err)
	}
	stopped, err := s.Update(context.Background(), owner, first.ID, 2, "stop")
	if err != nil || !stopped.Assisted || stopped.Status != Stopped || stopped.ExpiresAt != first.ExpiresAt {
		t.Fatal(stopped, err)
	}
	for key := range store.records {
		if strings.Contains(key, owner.UID) || key == first.ID {
			t.Fatal("raw owner or public trial id used as database key")
		}
	}
	now = first.ExpiresAt
	if _, err := s.Read(context.Background(), owner, first.ID); !errors.Is(err, ErrExpired) {
		t.Fatal("expiry", err)
	}
}

func TestControlCannotStartAfterCancellationOrRandomFailure(t *testing.T) {
	store, _ := NewMemoryStore(1)
	s, _ := NewService(store, bytes.Repeat([]byte{1}, 32))
	owner := Owner{UID: "u", AppID: "a"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Begin(ctx, owner); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.random = bytes.NewReader(nil)
	if _, err := s.Begin(context.Background(), owner); err == nil {
		t.Fatal("random failure ignored")
	}
	if len(store.records) != 0 {
		t.Fatal("failed begin wrote a record")
	}
	if _, err := s.Begin(context.Background(), Owner{UID: "u\x00a", AppID: "b"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
