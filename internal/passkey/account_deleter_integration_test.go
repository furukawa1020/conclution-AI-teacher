package passkey_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/longmemory"
	"github.com/furukawa1020/conclution-ai-teacher/internal/passkey"
)

type acceptingPasskeyDeletionStore struct {
	passkey.Store
}

func (acceptingPasskeyDeletionStore) DeleteAccountData(context.Context, string, time.Time) error {
	return nil
}

type successfulFirebaseDeletion struct {
	uid string
}

func (d *successfulFirebaseDeletion) DeleteAccount(_ context.Context, uid string) error {
	d.uid = uid
	return nil
}

func (*successfulFirebaseDeletion) MintCustomToken(context.Context, string, map[string]any) (string, error) {
	return "unused-token", nil
}

func TestCompleteAccountDeletionDisablesAndRemovesLongTermMemory(t *testing.T) {
	ctx := context.Background()
	memory, err := longmemory.New(bytes.Repeat([]byte{0x47}, 32), longmemory.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	consent, err := memory.Enable(ctx, "uid-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.Save(ctx, "uid-a", consent.Generation, longmemory.Payload{
		Topics: []string{"must be deleted"},
	}); err != nil {
		t.Fatal(err)
	}
	firebase := &successfulFirebaseDeletion{}
	service, err := passkey.New(passkey.Config{
		RPID:               "kotae-ai.web.app",
		Origin:             "https://kotae-ai.web.app",
		Store:              acceptingPasskeyDeletionStore{},
		TokenMinter:        firebase,
		AccountDataCleaner: memory,
		AccountDeleter:     firebase,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAccount(ctx, "uid-a"); err != nil {
		t.Fatal(err)
	}
	if firebase.uid != "uid-a" {
		t.Fatalf("Firebase deletion uid = %q", firebase.uid)
	}
	status, err := memory.Status(ctx, "uid-a")
	if err != nil || status.Enabled || status.Generation <= consent.Generation {
		t.Fatalf("memory status=%+v err=%v", status, err)
	}
	if _, err := memory.Load(ctx, "uid-a", status.Generation); !errors.Is(err, longmemory.ErrDisabled) {
		t.Fatalf("deleted memory load err=%v", err)
	}
}
