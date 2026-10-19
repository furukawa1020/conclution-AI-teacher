package guesttrial

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

type Owner struct{ UID, AppID string }

type Snapshot struct {
	Version    int       `json:"version"`
	ID         string    `json:"id"`
	QuestionID string    `json:"questionId"`
	Question   string    `json:"question"`
	Generation int64     `json:"generation"`
	Assisted   bool      `json:"assisted"`
	Status     Status    `json:"status"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type Control interface {
	Begin(context.Context, Owner) (Snapshot, error)
	Read(context.Context, Owner, string) (Snapshot, error)
	Update(context.Context, Owner, string, int64, string) (Snapshot, error)
}

type Service struct {
	store  Store
	key    []byte
	now    func() time.Time
	random io.Reader
}

func NewService(store Store, key []byte) (*Service, error) {
	if store == nil || len(key) != 32 {
		return nil, ErrInvalid
	}
	return &Service{store: store, key: append([]byte(nil), key...), now: time.Now, random: rand.Reader}, nil
}

func validOwner(o Owner) bool {
	for _, v := range []string{o.UID, o.AppID} {
		if v == "" || len(v) > 256 || !utf8.ValidString(v) || strings.TrimSpace(v) != v || strings.ContainsRune(v, 0) {
			return false
		}
	}
	return true
}

func (s *Service) documentKey(owner Owner, id string) (string, error) {
	if !validOwner(owner) || !validKey(id) {
		return "", ErrInvalid
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte("kotae-guest-trial-v1\x00" + owner.UID + "\x00" + owner.AppID + "\x00" + id))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func snapshot(id string, r Record) Snapshot {
	return Snapshot{Version: 1, ID: id, QuestionID: r.Question, Question: QuestionText, Generation: r.Generation, Assisted: r.Assisted, Status: r.Status, ExpiresAt: r.ExpiresAt}
}

func (s *Service) Begin(ctx context.Context, owner Owner) (Snapshot, error) {
	if !validOwner(owner) {
		return Snapshot{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return Snapshot{}, err
	}
	id := hex.EncodeToString(raw)
	key, err := s.documentKey(owner, id)
	if err != nil {
		return Snapshot{}, err
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	r := Record{Version: 1, Question: QuestionID, Generation: 1, Status: Pending, CreatedAt: now, ExpiresAt: now.Add(TTL)}
	if err := s.store.Create(ctx, key, r); err != nil {
		return Snapshot{}, err
	}
	return snapshot(id, r), nil
}

func (s *Service) Read(ctx context.Context, owner Owner, id string) (Snapshot, error) {
	key, err := s.documentKey(owner, id)
	if err != nil {
		return Snapshot{}, err
	}
	r, err := s.store.Read(ctx, key, s.now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot(id, r), nil
}

func (s *Service) Update(ctx context.Context, owner Owner, id string, generation int64, action string) (Snapshot, error) {
	key, err := s.documentKey(owner, id)
	if err != nil {
		return Snapshot{}, err
	}
	var event Event
	switch action {
	case "help":
		event = Help
	case "stop":
		event = Stop
	default:
		return Snapshot{}, ErrInvalid
	}
	if generation < 1 || generation > 2 {
		return Snapshot{}, ErrInvalid
	}
	r, err := s.store.Apply(ctx, key, generation, event, s.now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot(id, r), nil
}
