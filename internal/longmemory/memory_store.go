package longmemory

import (
	"context"
	"sync"
	"time"
)

type memoryEntry struct {
	consent Consent
	record  *Record
}

type MemoryStore struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make(map[string]memoryEntry)}
}

func (s *MemoryStore) Status(ctx context.Context, key string) (Consent, error) {
	if err := ctx.Err(); err != nil {
		return Consent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return Consent{Enabled: false, Generation: 0}, nil
	}
	return entry.consent, nil
}

func (s *MemoryStore) Enable(ctx context.Context, key string, now time.Time) (Consent, error) {
	if err := ctx.Err(); err != nil || key == "" || now.IsZero() {
		return Consent{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[key]
	if !entry.consent.Enabled {
		entry.consent.Generation++
		entry.consent.Enabled = true
		entry.record = nil
		s.entries[key] = entry
	}
	return entry.consent, nil
}

func (s *MemoryStore) DisableAndDelete(ctx context.Context, key string, now time.Time) error {
	if err := ctx.Err(); err != nil || key == "" || now.IsZero() {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[key]
	entry.consent.Generation++
	entry.consent.Enabled = false
	entry.record = nil
	s.entries[key] = entry
	return nil
}

func (s *MemoryStore) Put(ctx context.Context, key string, generation int64, record Record, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[key]
	if !entry.consent.Enabled {
		return ErrDisabled
	}
	if entry.consent.Generation != generation {
		return ErrStale
	}
	if validateRecord(record, generation, now) != nil {
		return ErrInvalid
	}
	copy := cloneRecord(record)
	entry.record = &copy
	s.entries[key] = entry
	return nil
}

func (s *MemoryStore) Get(ctx context.Context, key string, generation int64, now time.Time) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[key]
	if !entry.consent.Enabled {
		return Record{}, ErrDisabled
	}
	if entry.consent.Generation != generation {
		return Record{}, ErrStale
	}
	if entry.record == nil || !entry.record.ExpiresAt.After(now.UTC()) {
		return Record{}, ErrNotFound
	}
	if validateRecord(*entry.record, generation, now) != nil {
		return Record{}, ErrInvalid
	}
	return cloneRecord(*entry.record), nil
}

func validateRecord(record Record, generation int64, now time.Time) error {
	if record.SchemaVersion != SchemaVersion || record.Generation != generation || generation < 1 || len(record.Ciphertext) == 0 || len(record.Ciphertext) > 4096 || len(record.Nonce) != 12 || record.ExpiresAt.IsZero() || !record.ExpiresAt.After(now.UTC()) || record.ExpiresAt.After(now.UTC().Add(DefaultTTL+time.Minute)) {
		return ErrInvalid
	}
	return nil
}

func cloneRecord(record Record) Record {
	record.Ciphertext = append([]byte(nil), record.Ciphertext...)
	record.Nonce = append([]byte(nil), record.Nonce...)
	return record
}
