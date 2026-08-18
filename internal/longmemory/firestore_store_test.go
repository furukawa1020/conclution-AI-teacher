package longmemory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
)

func TestFirestoreStoreOptOutPreventsStaleMemoryResurrection(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not configured")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "kotae-long-memory-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewFirestoreStore(client)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(bytes.Repeat([]byte{0x61}, 32), store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	manager.now = func() time.Time { return now }
	uid := "emulator-" + strings.ReplaceAll(t.Name(), "/", "-")
	consent, err := manager.Enable(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(ctx, uid, consent.Generation, Payload{Topics: []string{"暗号化memory"}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DisableAndDelete(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(ctx, uid, consent.Generation, Payload{Topics: []string{"復活してはいけない"}}); !errors.Is(err, ErrDisabled) && !errors.Is(err, ErrStale) {
		t.Fatalf("stale save err=%v", err)
	}
	status, err := manager.Status(ctx, uid)
	if err != nil || status.Enabled || status.Generation <= consent.Generation {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	key, _ := manager.principalKey(uid)
	if _, err := client.Collection(recordsCollection).Doc(key).Get(ctx); err == nil {
		t.Fatal("opt-out left a memory record")
	}
}
