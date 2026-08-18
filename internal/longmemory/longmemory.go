// Package longmemory owns the opt-in, encrypted persistence boundary for
// cross-session semantic memory. It is deliberately disconnected from the
// voice hot path; later integrations may call Save only after a turn ends.
package longmemory

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
)

const (
	SchemaVersion = 1
	DefaultTTL    = 30 * 24 * time.Hour
	maxItems      = 4
	maxItemRunes  = 96
	maxPlaintext  = 2048
)

var (
	ErrInvalid  = errors.New("long memory state is invalid")
	ErrDisabled = errors.New("long memory is disabled")
	ErrStale    = errors.New("long memory generation is stale")
	ErrNotFound = errors.New("long memory was not found")
	ErrReplay   = errors.New("long memory capability was already consumed")
)

type Consent struct {
	Enabled    bool
	Generation int64
}

type Payload struct {
	Topics      []string `json:"topics"`
	Preferences []string `json:"preferences"`
	OpenLoops   []string `json:"openLoops"`
}

type Record struct {
	SchemaVersion int
	Generation    int64
	Ciphertext    []byte
	Nonce         []byte
	ExpiresAt     time.Time
}

type Store interface {
	Status(context.Context, string) (Consent, error)
	Enable(context.Context, string, time.Time) (Consent, error)
	DisableAndDelete(context.Context, string, time.Time) error
	Put(context.Context, string, int64, Record, time.Time) error
	Get(context.Context, string, int64, time.Time) (Record, error)
	GetCurrent(context.Context, string, time.Time) (Consent, Record, error)
	ConsumeCapability(context.Context, string, int64, string, time.Time, time.Time) error
}

type Control interface {
	Status(context.Context, string) (Consent, error)
	Enable(context.Context, string) (Consent, error)
	DisableAndDelete(context.Context, string) error
}

type Manager struct {
	store       Store
	aead        cipher.AEAD
	contextAEAD cipher.AEAD
	sessionAEAD cipher.AEAD
	key         []byte
	now         func() time.Time
	rand        io.Reader
}

func New(key []byte, store Store) (*Manager, error) {
	if len(key) != 32 || store == nil {
		return nil, ErrInvalid
	}
	encryptionKey := deriveKey(key, "kotae-long-memory-aead-v1")
	defer clear(encryptionKey)
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, ErrInvalid
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalid
	}
	contextEncryptionKey := deriveKey(key, "kotae-long-memory-context-capability-aead-v1")
	defer clear(contextEncryptionKey)
	contextBlock, err := aes.NewCipher(contextEncryptionKey)
	if err != nil {
		return nil, ErrInvalid
	}
	contextAEAD, err := cipher.NewGCM(contextBlock)
	if err != nil {
		return nil, ErrInvalid
	}
	sessionEncryptionKey := deriveKey(key, "kotae-long-memory-session-context-aead-v1")
	defer clear(sessionEncryptionKey)
	sessionBlock, err := aes.NewCipher(sessionEncryptionKey)
	if err != nil {
		return nil, ErrInvalid
	}
	sessionAEAD, err := cipher.NewGCM(sessionBlock)
	if err != nil {
		return nil, ErrInvalid
	}
	return &Manager{store: store, aead: aead, contextAEAD: contextAEAD, sessionAEAD: sessionAEAD, key: append([]byte(nil), key...), now: time.Now, rand: rand.Reader}, nil
}

func deriveKey(root []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, root)
	_, _ = mac.Write([]byte(purpose))
	return mac.Sum(nil)
}

func (m *Manager) Status(ctx context.Context, uid string) (Consent, error) {
	key, err := m.principalKey(uid)
	if err != nil {
		return Consent{}, err
	}
	return m.store.Status(ctx, key)
}

func (m *Manager) Enable(ctx context.Context, uid string) (Consent, error) {
	key, err := m.principalKey(uid)
	if err != nil {
		return Consent{}, err
	}
	return m.store.Enable(ctx, key, m.now().UTC())
}

func (m *Manager) DisableAndDelete(ctx context.Context, uid string) error {
	key, err := m.principalKey(uid)
	if err != nil {
		return err
	}
	return m.store.DisableAndDelete(ctx, key, m.now().UTC())
}

func (m *Manager) Save(ctx context.Context, uid string, generation int64, payload Payload) error {
	if generation < 1 || validatePayload(payload) != nil {
		return ErrInvalid
	}
	key, err := m.principalKey(uid)
	if err != nil {
		return err
	}
	plaintext, err := json.Marshal(payload)
	if err != nil || len(plaintext) > maxPlaintext {
		clear(plaintext)
		return ErrInvalid
	}
	defer clear(plaintext)
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := io.ReadFull(m.rand, nonce); err != nil {
		clear(nonce)
		return ErrInvalid
	}
	ciphertext := m.aead.Seal(nil, nonce, plaintext, aad(key, generation))
	record := Record{SchemaVersion: SchemaVersion, Generation: generation, Ciphertext: ciphertext, Nonce: nonce, ExpiresAt: m.now().UTC().Add(DefaultTTL)}
	err = m.store.Put(ctx, key, generation, record, m.now().UTC())
	clear(ciphertext)
	clear(nonce)
	return err
}

func (m *Manager) Load(ctx context.Context, uid string, generation int64) (Payload, error) {
	if generation < 1 {
		return Payload{}, ErrInvalid
	}
	key, err := m.principalKey(uid)
	if err != nil {
		return Payload{}, err
	}
	record, err := m.store.Get(ctx, key, generation, m.now().UTC())
	if err != nil {
		return Payload{}, err
	}
	if record.SchemaVersion != SchemaVersion || record.Generation != generation || len(record.Nonce) != m.aead.NonceSize() || len(record.Ciphertext) == 0 {
		return Payload{}, ErrInvalid
	}
	plaintext, err := m.aead.Open(nil, record.Nonce, record.Ciphertext, aad(key, generation))
	if err != nil || len(plaintext) > maxPlaintext {
		clear(plaintext)
		return Payload{}, ErrInvalid
	}
	defer clear(plaintext)
	var payload Payload
	decoderErr := json.Unmarshal(plaintext, &payload)
	if decoderErr != nil || validatePayload(payload) != nil {
		return Payload{}, ErrInvalid
	}
	return payload, nil
}

func (m *Manager) principalKey(uid string) (string, error) {
	uid = strings.TrimSpace(uid)
	if uid == "" || len(uid) > 256 || !utf8.ValidString(uid) {
		return "", ErrInvalid
	}
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write([]byte("kotae-long-memory-principal-v1\x00"))
	_, _ = mac.Write([]byte(uid))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func aad(key string, generation int64) []byte {
	result := make([]byte, 0, 64+len(key))
	result = append(result, "kotae-long-memory-envelope-v1\x00"...)
	result = append(result, key...)
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(generation))
	return append(result, encoded[:]...)
}

func validatePayload(payload Payload) error {
	if len(payload.Topics)+len(payload.Preferences)+len(payload.OpenLoops) == 0 {
		return ErrInvalid
	}
	for _, group := range [][]string{payload.Topics, payload.Preferences, payload.OpenLoops} {
		if len(group) > maxItems {
			return ErrInvalid
		}
		for _, value := range group {
			value = strings.TrimSpace(value)
			if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxItemRunes || privacyguard.HasHighConfidenceFinding(value) {
				return ErrInvalid
			}
		}
	}
	return nil
}
