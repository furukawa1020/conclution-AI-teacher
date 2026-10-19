package guesttrial

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is bounded and intended for tests/local development only.
// Separate processes must use a shared transactional store.
type MemoryStore struct {
	mu       sync.Mutex
	capacity int
	records  map[string]Record
}

func NewMemoryStore(capacity int) (*MemoryStore, error) {
	if capacity < 1 || capacity > 10000 {
		return nil, ErrInvalid
	}
	return &MemoryStore{capacity: capacity, records: make(map[string]Record)}, nil
}

func (s *MemoryStore) Create(ctx context.Context, key string, record Record) error {
	if !validKey(key) || record.validate() != nil || record.Generation != 1 || record.Assisted || record.Status != Pending {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := s.records[key]; exists {
		return ErrConflict
	}
	for id, r := range s.records {
		if !r.ExpiresAt.After(record.CreatedAt) {
			delete(s.records, id)
		}
	}
	if len(s.records) >= s.capacity {
		return ErrCapacity
	}
	s.records[key] = record
	return nil
}

func (s *MemoryStore) Read(ctx context.Context, key string, now time.Time) (Record, error) {
	if !validKey(key) {
		return Record{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	r, exists := s.records[key]
	if !exists {
		return Record{}, ErrNotFound
	}
	return current(r, now)
}

func (s *MemoryStore) Apply(ctx context.Context, key string, generation int64, event Event, now time.Time) (Record, error) {
	if !validKey(key) {
		return Record{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	r, exists := s.records[key]
	if !exists {
		return Record{}, ErrNotFound
	}
	next, err := transition(r, generation, event, now)
	if err != nil {
		return Record{}, err
	}
	s.records[key] = next
	return next, nil
}
