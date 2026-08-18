package longmemory

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"
)

const (
	SessionContextTTL      = 15 * time.Minute
	sessionContextPrefix   = "kms1."
	sessionContextIDBytes  = 16
	maxSessionContextBytes = 4096
	maxSessionPlaintext    = 3072
)

type SessionContextIssuer interface {
	ConsumeSessionContext(context.Context, string, string, string) (string, int64, error)
}

type sessionContextEnvelope struct {
	Version     int     `json:"v"`
	UIDDigest   string  `json:"u"`
	AppIDDigest string  `json:"a"`
	Generation  int64   `json:"g"`
	IssuedAt    int64   `json:"iat"`
	ExpiresAt   int64   `json:"exp"`
	SessionID   string  `json:"sid"`
	Memory      Payload `json:"m"`
}

func (m *Manager) ConsumeSessionContext(ctx context.Context, uid, appID, capability string) (string, int64, error) {
	if m == nil || m.sessionAEAD == nil {
		return "", 0, ErrInvalid
	}
	now := m.now().UTC()
	contextEnvelope, err := m.decryptContextEnvelope(uid, appID, capability, now)
	if err != nil {
		return "", 0, err
	}
	uidDigest, err := m.principalKey(uid)
	if err != nil {
		return "", 0, err
	}
	issuedAt := now.Truncate(time.Second)
	random := make([]byte, sessionContextIDBytes+m.sessionAEAD.NonceSize())
	if _, err := io.ReadFull(m.rand, random); err != nil {
		clear(random)
		return "", 0, ErrInvalid
	}
	defer clear(random)
	envelope := sessionContextEnvelope{
		Version: SchemaVersion, UIDDigest: uidDigest, AppIDDigest: contextEnvelope.AppIDDigest,
		Generation: contextEnvelope.Generation, IssuedAt: issuedAt.Unix(),
		ExpiresAt: issuedAt.Add(SessionContextTTL).Unix(),
		SessionID: base64.RawURLEncoding.EncodeToString(random[:sessionContextIDBytes]),
		Memory:    contextEnvelope.Memory,
	}
	plaintext, err := json.Marshal(envelope)
	if err != nil || len(plaintext) > maxSessionPlaintext {
		clear(plaintext)
		return "", 0, ErrInvalid
	}
	defer clear(plaintext)
	encodedSize := len(sessionContextPrefix) + base64.RawURLEncoding.EncodedLen(m.sessionAEAD.NonceSize()+len(plaintext)+m.sessionAEAD.Overhead())
	if encodedSize > maxSessionContextBytes {
		return "", 0, ErrInvalid
	}
	useDigest := m.capabilityUseKey(contextEnvelope.CapabilityID, uidDigest, contextEnvelope.AppIDDigest)
	if err := m.store.ConsumeCapability(ctx, uidDigest, contextEnvelope.Generation, useDigest, time.Unix(contextEnvelope.ExpiresAt, 0).UTC(), now); err != nil {
		return "", 0, err
	}
	nonce := random[sessionContextIDBytes:]
	sealed := m.sessionAEAD.Seal(nil, nonce, plaintext, sessionContextAAD(uidDigest, contextEnvelope.AppIDDigest))
	raw := make([]byte, 0, len(nonce)+len(sealed))
	raw = append(raw, nonce...)
	raw = append(raw, sealed...)
	token := sessionContextPrefix + base64.RawURLEncoding.EncodeToString(raw)
	clear(raw)
	clear(sealed)
	return token, int64(SessionContextTTL / time.Second), nil
}

func (m *Manager) OpenSessionContext(uid, appID, token string) (Payload, int64, error) {
	if m == nil || m.sessionAEAD == nil || !validAppID(appID) ||
		!strings.HasPrefix(token, sessionContextPrefix) || len(token) > maxSessionContextBytes {
		return Payload{}, 0, ErrInvalid
	}
	uidDigest, err := m.principalKey(uid)
	if err != nil {
		return Payload{}, 0, err
	}
	appDigest := m.appIDKey(appID)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, sessionContextPrefix))
	if err != nil {
		clear(raw)
		return Payload{}, 0, ErrInvalid
	}
	defer clear(raw)
	nonceSize := m.sessionAEAD.NonceSize()
	if len(raw) < nonceSize+m.sessionAEAD.Overhead()+2 {
		return Payload{}, 0, ErrInvalid
	}
	plaintext, err := m.sessionAEAD.Open(nil, raw[:nonceSize], raw[nonceSize:], sessionContextAAD(uidDigest, appDigest))
	if err != nil || len(plaintext) > maxSessionPlaintext {
		clear(plaintext)
		return Payload{}, 0, ErrInvalid
	}
	defer clear(plaintext)
	var envelope sessionContextEnvelope
	if json.Unmarshal(plaintext, &envelope) != nil || validateSessionContextEnvelope(envelope, uidDigest, appDigest, m.now().UTC()) != nil {
		return Payload{}, 0, ErrInvalid
	}
	return envelope.Memory, envelope.Generation, nil
}

func validateSessionContextEnvelope(envelope sessionContextEnvelope, uidDigest, appDigest string, now time.Time) error {
	id, err := base64.RawURLEncoding.DecodeString(envelope.SessionID)
	defer clear(id)
	if err != nil || len(id) != sessionContextIDBytes || envelope.Version != SchemaVersion ||
		!hmac.Equal([]byte(envelope.UIDDigest), []byte(uidDigest)) ||
		!hmac.Equal([]byte(envelope.AppIDDigest), []byte(appDigest)) ||
		envelope.Generation < 1 || envelope.IssuedAt < 1 ||
		envelope.ExpiresAt-envelope.IssuedAt != int64(SessionContextTTL/time.Second) ||
		now.Unix() < envelope.IssuedAt-30 || now.Unix() >= envelope.ExpiresAt ||
		validatePayload(envelope.Memory) != nil {
		return ErrInvalid
	}
	return nil
}

func sessionContextAAD(uidDigest, appDigest string) []byte {
	return []byte("kotae-long-memory-session-context-v1\x00" + uidDigest + "\x00" + appDigest)
}
