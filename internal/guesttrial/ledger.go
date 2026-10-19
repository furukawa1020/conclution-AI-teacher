// Package guesttrial tracks content-free, bounded guest trial control state.
// It does not evaluate answers or claim improvements in speaking ability.
package guesttrial

import (
	"context"
	"encoding/hex"
	"errors"
	"time"
)

const TTL = 15 * time.Minute
const QuestionID = "rest_activity_v1"
const QuestionText = "少し休めるとしたら、何をしたいですか？"

var (
	ErrInvalid  = errors.New("guest trial: invalid input")
	ErrConflict = errors.New("guest trial: stale or terminal state")
	ErrExpired  = errors.New("guest trial: expired")
	ErrNotFound = errors.New("guest trial: not found")
	ErrCapacity = errors.New("guest trial: capacity exceeded")
)

type Status string

const (
	Pending     Status = "pending"
	Stopped     Status = "stopped"
	AnswerFirst Status = "answer_first"
	Unverified  Status = "unverified"
)

type Event uint8

const (
	Help Event = iota + 1
	Stop
	// Only a server-side, committed, question-bound verifier may emit these.
	CompleteVerified
	CompleteUnverified
)

// Record deliberately has no user, transcript, answer or model text field.
type Record struct {
	Version    int       `firestore:"version"`
	Question   string    `firestore:"question"`
	Generation int64     `firestore:"generation"`
	Assisted   bool      `firestore:"assisted"`
	Status     Status    `firestore:"status"`
	CreatedAt  time.Time `firestore:"createdAt"`
	ExpiresAt  time.Time `firestore:"expiresAt"`
}

type Store interface {
	Create(context.Context, string, Record) error
	Read(context.Context, string, time.Time) (Record, error)
	Apply(context.Context, string, int64, Event, time.Time) (Record, error)
}

func validKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	raw, err := hex.DecodeString(key)
	return err == nil && hex.EncodeToString(raw) == key
}

func (r Record) validate() error {
	if r.Version != 1 || r.Question != QuestionID || r.Generation < 1 || r.Generation > 4 ||
		r.CreatedAt.IsZero() || r.ExpiresAt.Sub(r.CreatedAt) != TTL {
		return ErrInvalid
	}
	switch r.Status {
	case Pending, Stopped, AnswerFirst, Unverified:
	default:
		return ErrInvalid
	}
	expected := int64(1)
	if r.Assisted {
		expected++
	}
	if r.Status != Pending {
		expected++
	}
	if r.Generation != expected {
		return ErrInvalid
	}
	return nil
}

func current(r Record, now time.Time) (Record, error) {
	if err := r.validate(); err != nil {
		return Record{}, err
	}
	if now.IsZero() || now.Before(r.CreatedAt) {
		return Record{}, ErrInvalid
	}
	if !now.Before(r.ExpiresAt) {
		return Record{}, ErrExpired
	}
	return r, nil
}

// transition is pure so transaction retries cannot repeat external effects.
// Assistance can be requested once; the original deadline never slides.
func transition(r Record, generation int64, event Event, now time.Time) (Record, error) {
	r, err := current(r, now)
	if err != nil {
		return Record{}, err
	}
	if generation != r.Generation || r.Status != Pending {
		return Record{}, ErrConflict
	}
	switch event {
	case Help:
		if r.Assisted {
			return Record{}, ErrConflict
		}
		r.Assisted = true
	case Stop:
		r.Status = Stopped
	case CompleteVerified:
		r.Status = AnswerFirst
	case CompleteUnverified:
		r.Status = Unverified
	default:
		return Record{}, ErrInvalid
	}
	r.Generation++
	if err := r.validate(); err != nil {
		return Record{}, err
	}
	return r, nil
}
