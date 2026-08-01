package guard

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateVoiceLiveLeaseForAcquire(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	validHolder := strings.Repeat("a", 64)
	tests := []struct {
		name     string
		data     map[string]any
		wantHeld bool
		wantErr  bool
	}{
		{
			name: "expired old schema is replaceable",
			data: map[string]any{
				"expiresAt":     now.Add(-time.Second),
				"holder":        validHolder,
				"schemaVersion": int64(0),
			},
		},
		{
			name: "expired invalid holder is replaceable",
			data: map[string]any{
				"expiresAt":     now,
				"holder":        "invalid",
				"schemaVersion": int64(voiceLiveLeaseSchema),
			},
		},
		{
			name: "active old schema is rejected",
			data: map[string]any{
				"expiresAt":     now.Add(time.Second),
				"holder":        validHolder,
				"schemaVersion": int64(0),
			},
			wantErr: true,
		},
		{
			name: "active invalid holder is rejected",
			data: map[string]any{
				"expiresAt":     now.Add(time.Second),
				"holder":        "invalid",
				"schemaVersion": int64(voiceLiveLeaseSchema),
			},
			wantErr: true,
		},
		{
			name:    "missing expiry is rejected",
			data:    map[string]any{},
			wantErr: true,
		},
		{
			name: "wrong expiry type is rejected",
			data: map[string]any{
				"expiresAt": "2026-08-01T00:00:00Z",
			},
			wantErr: true,
		},
		{
			name: "zero expiry is rejected",
			data: map[string]any{
				"expiresAt": time.Time{},
			},
			wantErr: true,
		},
		{
			name: "active valid lease is held",
			data: map[string]any{
				"expiresAt":     now.Add(time.Second),
				"holder":        validHolder,
				"schemaVersion": int64(voiceLiveLeaseSchema),
			},
			wantHeld: true,
			wantErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateVoiceLiveLeaseForAcquire(test.data, now)
			if got := err != nil; got != test.wantErr {
				t.Fatalf("error = %v; want error %t", err, test.wantErr)
			}
			if got := errors.Is(err, ErrVoiceLiveLeaseHeld); got != test.wantHeld {
				t.Fatalf("held = %t; want %t (error: %v)", got, test.wantHeld, err)
			}
		})
	}
}

func TestMemoryVoiceLiveLeaseAllowsOnlyOneConcurrentConnectionPerUID(
	t *testing.T,
) {
	t.Parallel()
	manager := NewMemoryVoiceLiveLeaseManager()
	now := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	const contenders = 40
	var acquired atomic.Int32
	var held atomic.Int32
	var unexpected atomic.Int32
	var wait sync.WaitGroup

	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := manager.Acquire(
				context.Background(),
				"same-firebase-uid",
				now,
				7*time.Minute,
			)
			switch {
			case err == nil:
				acquired.Add(1)
			case errors.Is(err, ErrVoiceLiveLeaseHeld):
				held.Add(1)
			default:
				unexpected.Add(1)
			}
		}()
	}
	wait.Wait()

	if got := acquired.Load(); got != 1 {
		t.Fatalf("acquired = %d; want 1", got)
	}
	if got := held.Load(); got != contenders-1 {
		t.Fatalf("held = %d; want %d", got, contenders-1)
	}
	if got := unexpected.Load(); got != 0 {
		t.Fatalf("unexpected errors = %d; want 0", got)
	}
}

func TestMemoryVoiceLiveLeaseReleaseMakesUIDImmediatelyAvailable(t *testing.T) {
	t.Parallel()
	manager := NewMemoryVoiceLiveLeaseManager()
	now := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	lease, err := manager.Acquire(
		context.Background(),
		"firebase-uid",
		now,
		7*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(
		context.Background(),
		"firebase-uid",
		now.Add(time.Second),
		7*time.Minute,
	); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestMemoryVoiceLiveLeaseExpiresAndOldReleaseCannotDeleteNewOwner(
	t *testing.T,
) {
	t.Parallel()
	manager := NewMemoryVoiceLiveLeaseManager()
	now := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	oldLease, err := manager.Acquire(
		context.Background(),
		"firebase-uid",
		now,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	newLease, err := manager.Acquire(
		context.Background(),
		"firebase-uid",
		now.Add(time.Minute),
		time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire at expiry: %v", err)
	}
	if err := oldLease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(
		context.Background(),
		"firebase-uid",
		now.Add(time.Minute+time.Second),
		time.Minute,
	); !errors.Is(err, ErrVoiceLiveLeaseHeld) {
		t.Fatalf("acquire after stale release = %v; want ErrVoiceLiveLeaseHeld", err)
	}
	if err := newLease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryVoiceLiveLeaseStoresOnlyHashedUID(t *testing.T) {
	t.Parallel()
	const uid = "sensitive-firebase-uid"
	manager := NewMemoryVoiceLiveLeaseManager()
	if _, err := manager.Acquire(
		context.Background(),
		uid,
		time.Now(),
		time.Minute,
	); err != nil {
		t.Fatal(err)
	}
	if _, rawUIDStored := manager.leases[uid]; rawUIDStored {
		t.Fatal("raw UID was used as a live lease key")
	}
	documentID, err := userDocumentID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, digestStored := manager.leases[documentID]; !digestStored {
		t.Fatal("hashed UID live lease is missing")
	}
}

func TestVoiceLiveLeaseRejectsInvalidInputsFailClosed(t *testing.T) {
	t.Parallel()
	if _, err := NewFirestoreVoiceLiveLeaseManager(nil); err == nil {
		t.Fatal("nil Firestore client was accepted")
	}
	manager := NewMemoryVoiceLiveLeaseManager()
	for _, test := range []struct {
		uid string
		ttl time.Duration
	}{
		{uid: "", ttl: time.Minute},
		{uid: "firebase-uid", ttl: 0},
		{uid: "firebase-uid", ttl: -time.Second},
	} {
		if _, err := manager.Acquire(
			context.Background(),
			test.uid,
			time.Now(),
			test.ttl,
		); err == nil {
			t.Fatalf("Acquire(%q, %s) succeeded", test.uid, test.ttl)
		}
	}
}
