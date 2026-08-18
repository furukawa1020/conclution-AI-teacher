package longmemory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	capability, available, err := manager.BeginContext(ctx, uid, "firebase-app-id")
	if err != nil || !available || capability == "" {
		t.Fatalf("available=%v capability=%q err=%v", available, capability, err)
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
	if _, _, err := manager.OpenContext(ctx, uid, "firebase-app-id", capability); !errors.Is(err, ErrDisabled) {
		t.Fatalf("stale capability err=%v", err)
	}
	key, _ := manager.principalKey(uid)
	if _, err := client.Collection(recordsCollection).Doc(key).Get(ctx); err == nil {
		t.Fatal("opt-out left a memory record")
	}
}

func TestFirestoreCapabilityConsumeIsAtomic(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not configured")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "kotae-long-memory-consume-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store, _ := NewFirestoreStore(client)
	manager, _ := New(bytes.Repeat([]byte{0x72}, 32), store)
	uid := "consume-" + strings.ReplaceAll(t.Name(), "/", "-")
	consent, err := manager.Enable(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(ctx, uid, consent.Generation, Payload{Topics: []string{"one use"}}); err != nil {
		t.Fatal(err)
	}
	token, available, err := manager.BeginContext(ctx, uid, "firebase-app-id")
	if err != nil || !available {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var replays atomic.Int32
	var wait sync.WaitGroup
	start := make(chan struct{})
	for range 100 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, _, err := manager.ConsumeContext(ctx, uid, "firebase-app-id", token)
			if err == nil {
				successes.Add(1)
			} else if errors.Is(err, ErrReplay) {
				replays.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if successes.Load() != 1 || replays.Load() != 99 {
		t.Fatalf("success=%d replay=%d", successes.Load(), replays.Load())
	}
}
