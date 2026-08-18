package longmemory

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	settingsCollection = "conversation_memory_settings_v1"
	recordsCollection  = "conversation_memories_v1"
	usesCollection     = "conversation_memory_capability_uses_v1"
)

type FirestoreStore struct{ client *firestore.Client }

type settingDocument struct {
	SchemaVersion int       `firestore:"schemaVersion"`
	Enabled       bool      `firestore:"enabled"`
	Generation    int64     `firestore:"generation"`
	UpdatedAt     time.Time `firestore:"updatedAt"`
}

type recordDocument struct {
	SchemaVersion int       `firestore:"schemaVersion"`
	Generation    int64     `firestore:"generation"`
	Ciphertext    []byte    `firestore:"ciphertext"`
	Nonce         []byte    `firestore:"nonce"`
	ExpiresAt     time.Time `firestore:"expiresAt"`
	UpdatedAt     time.Time `firestore:"updatedAt"`
}

type capabilityUseDocument struct {
	SchemaVersion int       `firestore:"schemaVersion"`
	Generation    int64     `firestore:"generation"`
	ExpiresAt     time.Time `firestore:"expiresAt"`
	ConsumedAt    time.Time `firestore:"consumedAt"`
}

func (s *FirestoreStore) ConsumeCapability(ctx context.Context, key string, generation int64, useDigest string, expiresAt, now time.Time) error {
	if key == "" || generation < 1 || !validUseDigest(useDigest) || !expiresAt.After(now.UTC()) || expiresAt.After(now.UTC().Add(ContextCapabilityTTL)) {
		return ErrInvalid
	}
	settings, _ := s.refs(key)
	use := s.client.Collection(usesCollection).Doc(useDigest)
	return s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		settingSnapshot, err := tx.Get(settings)
		if status.Code(err) == codes.NotFound {
			return ErrDisabled
		}
		if err != nil {
			return err
		}
		var setting settingDocument
		if settingSnapshot.DataTo(&setting) != nil || validateSetting(setting) != nil {
			return ErrInvalid
		}
		if !setting.Enabled {
			return ErrDisabled
		}
		if setting.Generation != generation {
			return ErrStale
		}
		_, err = tx.Get(use)
		if err == nil {
			return ErrReplay
		}
		if status.Code(err) != codes.NotFound {
			return err
		}
		return tx.Create(use, capabilityUseDocument{SchemaVersion: SchemaVersion, Generation: generation, ExpiresAt: expiresAt.UTC(), ConsumedAt: now.UTC()})
	})
}

func NewFirestoreStore(client *firestore.Client) (*FirestoreStore, error) {
	if client == nil {
		return nil, ErrInvalid
	}
	return &FirestoreStore{client: client}, nil
}

func (s *FirestoreStore) refs(key string) (*firestore.DocumentRef, *firestore.DocumentRef) {
	return s.client.Collection(settingsCollection).Doc(key), s.client.Collection(recordsCollection).Doc(key)
}

func (s *FirestoreStore) Status(ctx context.Context, key string) (Consent, error) {
	settings, _ := s.refs(key)
	snapshot, err := settings.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Consent{}, nil
	}
	if err != nil {
		return Consent{}, err
	}
	var document settingDocument
	if snapshot.DataTo(&document) != nil || validateSetting(document) != nil {
		return Consent{}, ErrInvalid
	}
	return Consent{Enabled: document.Enabled, Generation: document.Generation}, nil
}

func (s *FirestoreStore) Enable(ctx context.Context, key string, now time.Time) (result Consent, err error) {
	settings, records := s.refs(key)
	err = s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		document := settingDocument{SchemaVersion: SchemaVersion}
		snapshot, getErr := tx.Get(settings)
		if getErr == nil {
			if snapshot.DataTo(&document) != nil || validateSetting(document) != nil {
				return ErrInvalid
			}
		} else if status.Code(getErr) != codes.NotFound {
			return getErr
		}
		if !document.Enabled {
			document.Generation++
			document.Enabled = true
			document.UpdatedAt = now.UTC()
			if err := tx.Set(settings, document); err != nil {
				return err
			}
			if err := tx.Delete(records); err != nil {
				return err
			}
		}
		result = Consent{Enabled: document.Enabled, Generation: document.Generation}
		return nil
	})
	return result, err
}

func (s *FirestoreStore) DisableAndDelete(ctx context.Context, key string, now time.Time) error {
	settings, records := s.refs(key)
	return s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		document := settingDocument{SchemaVersion: SchemaVersion}
		snapshot, getErr := tx.Get(settings)
		if getErr == nil {
			if snapshot.DataTo(&document) != nil || validateSetting(document) != nil {
				return ErrInvalid
			}
		} else if status.Code(getErr) != codes.NotFound {
			return getErr
		}
		document.Generation++
		document.Enabled = false
		document.UpdatedAt = now.UTC()
		if err := tx.Set(settings, document); err != nil {
			return err
		}
		return tx.Delete(records)
	})
}

func (s *FirestoreStore) Put(ctx context.Context, key string, generation int64, record Record, now time.Time) error {
	if validateRecord(record, generation, now) != nil {
		return ErrInvalid
	}
	settings, records := s.refs(key)
	return s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(settings)
		if status.Code(err) == codes.NotFound {
			return ErrDisabled
		}
		if err != nil {
			return err
		}
		var document settingDocument
		if snapshot.DataTo(&document) != nil || validateSetting(document) != nil {
			return ErrInvalid
		}
		if !document.Enabled {
			return ErrDisabled
		}
		if document.Generation != generation {
			return ErrStale
		}
		return tx.Set(records, recordDocument{SchemaVersion: record.SchemaVersion, Generation: record.Generation, Ciphertext: append([]byte(nil), record.Ciphertext...), Nonce: append([]byte(nil), record.Nonce...), ExpiresAt: record.ExpiresAt.UTC(), UpdatedAt: now.UTC()})
	})
}

func (s *FirestoreStore) Get(ctx context.Context, key string, generation int64, now time.Time) (result Record, err error) {
	consent, result, err := s.GetCurrent(ctx, key, now)
	if err != nil {
		return Record{}, err
	}
	if consent.Generation != generation {
		return Record{}, ErrStale
	}
	return result, nil
}

func (s *FirestoreStore) GetCurrent(ctx context.Context, key string, now time.Time) (consent Consent, result Record, err error) {
	settings, records := s.refs(key)
	err = s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		settingSnapshot, getErr := tx.Get(settings)
		if status.Code(getErr) == codes.NotFound {
			return ErrDisabled
		}
		if getErr != nil {
			return getErr
		}
		var setting settingDocument
		if settingSnapshot.DataTo(&setting) != nil || validateSetting(setting) != nil {
			return ErrInvalid
		}
		if !setting.Enabled {
			return ErrDisabled
		}
		consent = Consent{Enabled: true, Generation: setting.Generation}
		recordSnapshot, getErr := tx.Get(records)
		if status.Code(getErr) == codes.NotFound {
			return ErrNotFound
		}
		if getErr != nil {
			return getErr
		}
		var document recordDocument
		if recordSnapshot.DataTo(&document) != nil {
			return ErrInvalid
		}
		result = Record{SchemaVersion: document.SchemaVersion, Generation: document.Generation, Ciphertext: append([]byte(nil), document.Ciphertext...), Nonce: append([]byte(nil), document.Nonce...), ExpiresAt: document.ExpiresAt}
		if validateRecord(result, setting.Generation, now) != nil {
			return ErrInvalid
		}
		return nil
	})
	return consent, result, err
}

func validateSetting(document settingDocument) error {
	if document.SchemaVersion != SchemaVersion || document.Generation < 1 || document.UpdatedAt.IsZero() {
		return ErrInvalid
	}
	return nil
}
