package guard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const voiceLiveLeaseSchema = 1

var ErrVoiceLiveLeaseHeld = errors.New("voice live lease is already held")

// VoiceLiveLease is an ownership-token protected lease. Release is safe to
// call after expiry: it cannot delete a lease acquired later by another
// connection.
type VoiceLiveLease interface {
	Release(ctx context.Context) error
}

// VoiceLiveLeaseManager serializes live voice connections per Firebase UID.
// Implementations must not persist the raw UID.
type VoiceLiveLeaseManager interface {
	Acquire(
		ctx context.Context,
		uid string,
		now time.Time,
		ttl time.Duration,
	) (VoiceLiveLease, error)
}

type voiceLiveLeaseState struct {
	Holder    string    `firestore:"holder"`
	ExpiresAt time.Time `firestore:"expiresAt"`
	Schema    int       `firestore:"schemaVersion"`
}

func (state voiceLiveLeaseState) validate() error {
	if state.Schema != voiceLiveLeaseSchema {
		return errors.New("unsupported voice live lease schema")
	}
	if len(state.Holder) != 64 {
		return errors.New("invalid voice live lease holder")
	}
	if _, err := hex.DecodeString(state.Holder); err != nil {
		return errors.New("invalid voice live lease holder")
	}
	if state.ExpiresAt.IsZero() {
		return errors.New("invalid voice live lease expiry")
	}
	return nil
}

// validateVoiceLiveLeaseForAcquire decides whether an existing Firestore
// document may be replaced. Expiry is intentionally checked before the legacy
// schema fields: an unambiguously expired lease cannot still own the slot, but
// an active lease must have the complete current schema before it can safely be
// treated as held.
func validateVoiceLiveLeaseForAcquire(data map[string]any, now time.Time) error {
	expiresValue, ok := data["expiresAt"]
	if !ok {
		return errors.New("voice live lease expiry is missing")
	}
	expired, expiresAt, err := voiceLiveLeaseExpiry(expiresValue, now)
	if err != nil {
		return err
	}
	if expired {
		return nil
	}

	holder, ok := data["holder"].(string)
	if !ok {
		return errors.New("invalid voice live lease holder")
	}
	schema, ok := data["schemaVersion"].(int64)
	if !ok || schema != voiceLiveLeaseSchema {
		return errors.New("unsupported voice live lease schema")
	}
	persisted := voiceLiveLeaseState{
		Holder:    holder,
		ExpiresAt: expiresAt,
		Schema:    int(schema),
	}
	if err := persisted.validate(); err != nil {
		return err
	}
	return ErrVoiceLiveLeaseHeld
}

func voiceLiveLeaseExpiry(
	expiresValue any,
	now time.Time,
) (bool, time.Time, error) {
	expiresAt, ok := expiresValue.(time.Time)
	if !ok || expiresAt.IsZero() {
		return false, time.Time{}, errors.New("invalid voice live lease expiry")
	}
	return !expiresAt.After(now.UTC()), expiresAt, nil
}

func newVoiceLiveLeaseState(now time.Time, ttl time.Duration) (voiceLiveLeaseState, error) {
	if ttl <= 0 {
		return voiceLiveLeaseState{}, errors.New("voice live lease TTL must be positive")
	}
	holderBytes := make([]byte, 32)
	if _, err := rand.Read(holderBytes); err != nil {
		return voiceLiveLeaseState{}, fmt.Errorf("create voice live lease holder: %w", err)
	}
	return voiceLiveLeaseState{
		Holder:    hex.EncodeToString(holderBytes),
		ExpiresAt: now.UTC().Add(ttl),
		Schema:    voiceLiveLeaseSchema,
	}, nil
}

type FirestoreVoiceLiveLeaseManager struct {
	client *firestore.Client
}

func NewFirestoreVoiceLiveLeaseManager(
	client *firestore.Client,
) (*FirestoreVoiceLiveLeaseManager, error) {
	if client == nil {
		return nil, errors.New("firestore client is required")
	}
	return &FirestoreVoiceLiveLeaseManager{client: client}, nil
}

func (manager *FirestoreVoiceLiveLeaseManager) Acquire(
	ctx context.Context,
	uid string,
	now time.Time,
	ttl time.Duration,
) (VoiceLiveLease, error) {
	documentID, err := userDocumentID(uid)
	if err != nil {
		return nil, err
	}
	requested, err := newVoiceLiveLeaseState(now, ttl)
	if err != nil {
		return nil, err
	}
	ref := manager.client.Collection("voiceLiveLeases").Doc(documentID)
	err = manager.client.RunTransaction(ctx, func(
		ctx context.Context,
		tx *firestore.Transaction,
	) error {
		snapshot, getErr := tx.Get(ref)
		switch {
		case getErr == nil:
			if validateErr := validateVoiceLiveLeaseForAcquire(
				snapshot.Data(),
				now,
			); validateErr != nil {
				return validateErr
			}
		case status.Code(getErr) != codes.NotFound:
			return fmt.Errorf("read voice live lease: %w", getErr)
		}

		return tx.Set(ref, map[string]any{
			"holder":        requested.Holder,
			"expiresAt":     requested.ExpiresAt,
			"schemaVersion": voiceLiveLeaseSchema,
			"updatedAt":     firestore.ServerTimestamp,
		})
	})
	if errors.Is(err, ErrVoiceLiveLeaseHeld) {
		return nil, ErrVoiceLiveLeaseHeld
	}
	if err != nil {
		return nil, fmt.Errorf("acquire voice live lease: %w", err)
	}
	return &firestoreVoiceLiveLease{
		client: manager.client,
		ref:    ref,
		holder: requested.Holder,
	}, nil
}

type firestoreVoiceLiveLease struct {
	client *firestore.Client
	ref    *firestore.DocumentRef
	holder string
}

func (lease *firestoreVoiceLiveLease) Release(ctx context.Context) error {
	err := lease.client.RunTransaction(ctx, func(
		ctx context.Context,
		tx *firestore.Transaction,
	) error {
		snapshot, getErr := tx.Get(lease.ref)
		if status.Code(getErr) == codes.NotFound {
			return nil
		}
		if getErr != nil {
			return fmt.Errorf("read voice live lease for release: %w", getErr)
		}
		persisted := voiceLiveLeaseState{}
		if err := snapshot.DataTo(&persisted); err != nil {
			return fmt.Errorf("decode voice live lease for release: %w", err)
		}
		if err := persisted.validate(); err != nil {
			return err
		}
		if persisted.Holder != lease.holder {
			return nil
		}
		return tx.Delete(lease.ref)
	})
	if err != nil {
		return fmt.Errorf("release voice live lease: %w", err)
	}
	return nil
}

type MemoryVoiceLiveLeaseManager struct {
	mu     sync.Mutex
	leases map[string]voiceLiveLeaseState
}

// NewMemoryVoiceLiveLeaseManager is for insecure local development and unit
// tests only. Production uses Firestore so the invariant spans Cloud Run
// instances.
func NewMemoryVoiceLiveLeaseManager() *MemoryVoiceLiveLeaseManager {
	return &MemoryVoiceLiveLeaseManager{
		leases: make(map[string]voiceLiveLeaseState),
	}
}

func (manager *MemoryVoiceLiveLeaseManager) Acquire(
	ctx context.Context,
	uid string,
	now time.Time,
	ttl time.Duration,
) (VoiceLiveLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	documentID, err := userDocumentID(uid)
	if err != nil {
		return nil, err
	}
	requested, err := newVoiceLiveLeaseState(now, ttl)
	if err != nil {
		return nil, err
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if persisted, ok := manager.leases[documentID]; ok {
		expired, _, expiryErr := voiceLiveLeaseExpiry(persisted.ExpiresAt, now)
		if expiryErr != nil {
			return nil, expiryErr
		}
		if expired {
			manager.leases[documentID] = requested
			return &memoryVoiceLiveLease{
				manager:    manager,
				documentID: documentID,
				holder:     requested.Holder,
			}, nil
		}
		if err := persisted.validate(); err != nil {
			return nil, err
		}
		return nil, ErrVoiceLiveLeaseHeld
	}
	manager.leases[documentID] = requested
	return &memoryVoiceLiveLease{
		manager:    manager,
		documentID: documentID,
		holder:     requested.Holder,
	}, nil
}

type memoryVoiceLiveLease struct {
	manager    *MemoryVoiceLiveLeaseManager
	documentID string
	holder     string
}

func (lease *memoryVoiceLiveLease) Release(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lease.manager.mu.Lock()
	defer lease.manager.mu.Unlock()
	persisted, ok := lease.manager.leases[lease.documentID]
	if ok && persisted.Holder == lease.holder {
		delete(lease.manager.leases, lease.documentID)
	}
	return nil
}
