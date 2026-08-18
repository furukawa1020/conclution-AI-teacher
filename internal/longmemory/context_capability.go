package longmemory

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ContextCapabilityTTL      = 15 * time.Minute
	contextCapabilityPrefix   = "kmc1."
	contextCapabilityIDBytes  = 16
	maxContextCapabilityBytes = 4096
	maxContextPlaintext       = 3072
)

type ContextIssuer interface {
	BeginContext(context.Context, string, string) (string, bool, error)
}

type contextEnvelope struct {
	Version      int     `json:"v"`
	UIDDigest    string  `json:"u"`
	AppIDDigest  string  `json:"a"`
	Generation   int64   `json:"g"`
	IssuedAt     int64   `json:"iat"`
	ExpiresAt    int64   `json:"exp"`
	CapabilityID string  `json:"jti"`
	Memory       Payload `json:"m"`
}

func (m *Manager) BeginContext(ctx context.Context, uid, appID string) (string, bool, error) {
	if m == nil || m.contextAEAD == nil || !validAppID(appID) {
		return "", false, ErrInvalid
	}
	uidDigest, err := m.principalKey(uid)
	if err != nil {
		return "", false, err
	}
	now := m.now().UTC()
	consent, record, err := m.store.GetCurrent(ctx, uidDigest, now)
	if errors.Is(err, ErrDisabled) || errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil || !consent.Enabled || consent.Generation < 1 {
		return "", false, ErrInvalid
	}
	memory, err := m.openRecord(uidDigest, consent.Generation, record, now)
	if err != nil {
		return "", false, err
	}
	appDigest := m.appIDKey(appID)
	issuedAt := now.Truncate(time.Second)
	random := make([]byte, contextCapabilityIDBytes+m.contextAEAD.NonceSize())
	if _, err := io.ReadFull(m.rand, random); err != nil {
		clear(random)
		return "", false, ErrInvalid
	}
	defer clear(random)
	envelope := contextEnvelope{
		Version: SchemaVersion, UIDDigest: uidDigest, AppIDDigest: appDigest,
		Generation: consent.Generation, IssuedAt: issuedAt.Unix(),
		ExpiresAt:    issuedAt.Add(ContextCapabilityTTL).Unix(),
		CapabilityID: base64.RawURLEncoding.EncodeToString(random[:contextCapabilityIDBytes]),
		Memory:       memory,
	}
	plaintext, err := json.Marshal(envelope)
	if err != nil || len(plaintext) > maxContextPlaintext {
		clear(plaintext)
		return "", false, ErrInvalid
	}
	defer clear(plaintext)
	nonce := random[contextCapabilityIDBytes:]
	sealed := m.contextAEAD.Seal(nil, nonce, plaintext, contextAAD(uidDigest, appDigest))
	raw := make([]byte, 0, len(nonce)+len(sealed))
	raw = append(raw, nonce...)
	raw = append(raw, sealed...)
	token := contextCapabilityPrefix + base64.RawURLEncoding.EncodeToString(raw)
	clear(raw)
	clear(sealed)
	if len(token) > maxContextCapabilityBytes {
		return "", false, ErrInvalid
	}
	return token, true, nil
}

func (m *Manager) OpenContext(ctx context.Context, uid, appID, token string) (Payload, int64, error) {
	envelope, err := m.decryptContextEnvelope(uid, appID, token, m.now().UTC())
	if err != nil {
		return Payload{}, 0, err
	}
	consent, err := m.Status(ctx, uid)
	if err != nil {
		return Payload{}, 0, err
	}
	if !consent.Enabled {
		return Payload{}, 0, ErrDisabled
	}
	if consent.Generation != envelope.Generation {
		return Payload{}, 0, ErrStale
	}
	return envelope.Memory, envelope.Generation, nil
}

func (m *Manager) ConsumeContext(ctx context.Context, uid, appID, token string) (Payload, int64, error) {
	now := m.now().UTC()
	envelope, err := m.decryptContextEnvelope(uid, appID, token, now)
	if err != nil {
		return Payload{}, 0, err
	}
	uidDigest, err := m.principalKey(uid)
	if err != nil {
		return Payload{}, 0, err
	}
	useDigest := m.capabilityUseKey(envelope.CapabilityID, uidDigest, envelope.AppIDDigest)
	if err := m.store.ConsumeCapability(ctx, uidDigest, envelope.Generation, useDigest, time.Unix(envelope.ExpiresAt, 0).UTC(), now); err != nil {
		return Payload{}, 0, err
	}
	return envelope.Memory, envelope.Generation, nil
}

func (m *Manager) decryptContextEnvelope(uid, appID, token string, now time.Time) (contextEnvelope, error) {
	if m == nil || m.contextAEAD == nil || !validAppID(appID) ||
		!strings.HasPrefix(token, contextCapabilityPrefix) || len(token) > maxContextCapabilityBytes {
		return contextEnvelope{}, ErrInvalid
	}
	uidDigest, err := m.principalKey(uid)
	if err != nil {
		return contextEnvelope{}, err
	}
	appDigest := m.appIDKey(appID)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, contextCapabilityPrefix))
	if err != nil {
		clear(raw)
		return contextEnvelope{}, ErrInvalid
	}
	defer clear(raw)
	nonceSize := m.contextAEAD.NonceSize()
	if len(raw) < nonceSize+m.contextAEAD.Overhead()+2 {
		return contextEnvelope{}, ErrInvalid
	}
	plaintext, err := m.contextAEAD.Open(nil, raw[:nonceSize], raw[nonceSize:], contextAAD(uidDigest, appDigest))
	if err != nil || len(plaintext) > maxContextPlaintext {
		clear(plaintext)
		return contextEnvelope{}, ErrInvalid
	}
	defer clear(plaintext)
	var envelope contextEnvelope
	if json.Unmarshal(plaintext, &envelope) != nil ||
		validateContextEnvelope(envelope, uidDigest, appDigest, now) != nil {
		return contextEnvelope{}, ErrInvalid
	}
	return envelope, nil
}

func (m *Manager) openRecord(key string, generation int64, record Record, now time.Time) (Payload, error) {
	if validateRecord(record, generation, now) != nil || len(record.Nonce) != m.aead.NonceSize() {
		return Payload{}, ErrInvalid
	}
	plaintext, err := m.aead.Open(nil, record.Nonce, record.Ciphertext, aad(key, generation))
	if err != nil || len(plaintext) > maxPlaintext {
		clear(plaintext)
		return Payload{}, ErrInvalid
	}
	defer clear(plaintext)
	var payload Payload
	if json.Unmarshal(plaintext, &payload) != nil || validatePayload(payload) != nil {
		return Payload{}, ErrInvalid
	}
	return payload, nil
}

func validateContextEnvelope(envelope contextEnvelope, uidDigest, appDigest string, now time.Time) error {
	id, err := base64.RawURLEncoding.DecodeString(envelope.CapabilityID)
	defer clear(id)
	if err != nil || len(id) != contextCapabilityIDBytes || envelope.Version != SchemaVersion ||
		!hmac.Equal([]byte(envelope.UIDDigest), []byte(uidDigest)) ||
		!hmac.Equal([]byte(envelope.AppIDDigest), []byte(appDigest)) ||
		envelope.Generation < 1 || envelope.IssuedAt < 1 ||
		envelope.ExpiresAt-envelope.IssuedAt != int64(ContextCapabilityTTL/time.Second) ||
		now.Unix() < envelope.IssuedAt-30 || now.Unix() >= envelope.ExpiresAt ||
		validatePayload(envelope.Memory) != nil {
		return ErrInvalid
	}
	return nil
}

func (m *Manager) appIDKey(appID string) string {
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write([]byte("kotae-long-memory-app-id-v1\x00"))
	_, _ = mac.Write([]byte(strings.TrimSpace(appID)))
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *Manager) capabilityUseKey(capabilityID, uidDigest, appDigest string) string {
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write([]byte("kotae-long-memory-capability-use-v1\x00"))
	_, _ = mac.Write([]byte(capabilityID + "\x00" + uidDigest + "\x00" + appDigest))
	return hex.EncodeToString(mac.Sum(nil))
}

func validUseDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	defer clear(decoded)
	return err == nil && len(decoded) == sha256.Size && len(value) == sha256.Size*2
}

func contextAAD(uidDigest, appDigest string) []byte {
	return []byte("kotae-long-memory-context-capability-v1\x00" + uidDigest + "\x00" + appDigest)
}

func validAppID(appID string) bool {
	appID = strings.TrimSpace(appID)
	return appID != "" && len(appID) <= 512 && utf8.ValidString(appID)
}
