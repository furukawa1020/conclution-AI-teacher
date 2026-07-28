package guard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	DefaultPerMinute = 5
	DefaultPerDay    = 40
	MinPerMinute     = 1
	MaxPerMinute     = 20
	MinPerDay        = 1
	MaxPerDay        = 200
)

var ErrRateLimitExceeded = errors.New("evaluation rate limit exceeded")

type Limits struct {
	PerMinute int
	PerDay    int
}

func (l Limits) Validate() error {
	if l.PerMinute < MinPerMinute || l.PerMinute > MaxPerMinute {
		return fmt.Errorf("per-minute limit must be between %d and %d", MinPerMinute, MaxPerMinute)
	}
	if l.PerDay < MinPerDay || l.PerDay > MaxPerDay {
		return fmt.Errorf("daily limit must be between %d and %d", MinPerDay, MaxPerDay)
	}
	return nil
}

type Limiter interface {
	Consume(ctx context.Context, uid string, now time.Time) error
}

type counterState struct {
	MinuteStart time.Time `firestore:"minuteStart"`
	MinuteCount int64     `firestore:"minuteCount"`
	DayStart    time.Time `firestore:"dayStart"`
	DayCount    int64     `firestore:"dayCount"`
	Schema      int       `firestore:"schemaVersion"`
}

func (s *counterState) advance(now time.Time, limits Limits) error {
	minuteStart, dayStart := utcWindows(now)
	if s.Schema != 0 && s.Schema != 1 {
		return errors.New("unsupported rate-limit schema")
	}
	if s.MinuteCount < 0 || s.DayCount < 0 {
		return errors.New("invalid negative rate-limit counter")
	}
	if !s.MinuteStart.Equal(minuteStart) {
		s.MinuteStart = minuteStart
		s.MinuteCount = 0
	}
	if !s.DayStart.Equal(dayStart) {
		s.DayStart = dayStart
		s.DayCount = 0
	}
	s.Schema = 1
	if s.MinuteCount >= int64(limits.PerMinute) || s.DayCount >= int64(limits.PerDay) {
		return ErrRateLimitExceeded
	}
	s.MinuteCount++
	s.DayCount++
	return nil
}

func utcWindows(now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	return now.Truncate(time.Minute), time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func userDocumentID(uid string) (string, error) {
	if strings.TrimSpace(uid) == "" {
		return "", errors.New("uid is required")
	}
	digest := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(digest[:]), nil
}

type FirestoreLimiter struct {
	client *firestore.Client
	limits Limits
}

func NewFirestoreLimiter(client *firestore.Client, limits Limits) (*FirestoreLimiter, error) {
	if client == nil {
		return nil, errors.New("firestore client is required")
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &FirestoreLimiter{client: client, limits: limits}, nil
}

func (l *FirestoreLimiter) Consume(ctx context.Context, uid string, now time.Time) error {
	documentID, err := userDocumentID(uid)
	if err != nil {
		return err
	}
	ref := l.client.Collection("evaluationRateLimits").Doc(documentID)
	err = l.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		state := counterState{}
		snapshot, getErr := tx.Get(ref)
		switch {
		case getErr == nil:
			if decodeErr := snapshot.DataTo(&state); decodeErr != nil {
				return fmt.Errorf("decode rate-limit counter: %w", decodeErr)
			}
		case status.Code(getErr) != codes.NotFound:
			return fmt.Errorf("read rate-limit counter: %w", getErr)
		}

		if advanceErr := state.advance(now, l.limits); advanceErr != nil {
			return advanceErr
		}
		return tx.Set(ref, map[string]any{
			"minuteStart":   state.MinuteStart,
			"minuteCount":   state.MinuteCount,
			"dayStart":      state.DayStart,
			"dayCount":      state.DayCount,
			"schemaVersion": 1,
			"updatedAt":     firestore.ServerTimestamp,
		}, firestore.MergeAll)
	})
	if errors.Is(err, ErrRateLimitExceeded) {
		return ErrRateLimitExceeded
	}
	if err != nil {
		return fmt.Errorf("consume evaluation quota: %w", err)
	}
	return nil
}

type MemoryLimiter struct {
	mu       sync.Mutex
	limits   Limits
	counters map[string]counterState
}

func NewMemoryLimiter(limits Limits) (*MemoryLimiter, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &MemoryLimiter{
		limits:   limits,
		counters: make(map[string]counterState),
	}, nil
}

func (l *MemoryLimiter) Consume(ctx context.Context, uid string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	documentID, err := userDocumentID(uid)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.counters[documentID]
	if err := state.advance(now, l.limits); err != nil {
		return err
	}
	l.counters[documentID] = state
	return nil
}
