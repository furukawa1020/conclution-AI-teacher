package conversation

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	stateTokenPrefix = "v1."
	stateTokenTTL    = 15 * time.Minute
	sessionIDBytes   = 16
	maxStateTurns    = 10_000
	maxUIDBytes      = 256
)

var stateAADPrefix = []byte("kotae-conversation-state-v1\x00")

type StateCodec struct {
	aead   cipher.AEAD
	now    func() time.Time
	random io.Reader
}

type conversationState struct {
	Version             int                  `json:"v"`
	IssuedAt            int64                `json:"iat"`
	ExpiresAt           int64                `json:"exp"`
	SessionID           string               `json:"sid,omitempty"`
	Turn                int                  `json:"turn"`
	Graph               ThoughtStateGraph    `json:"thought_state_graph"`
	ConversationSummary string               `json:"conversation_summary,omitempty"`
	DocumentSummary     string               `json:"document_summary,omitempty"`
	PendingAnswer       PendingAnswerFrame   `json:"pending_answer"`
	Support             *conversationSupport `json:"support,omitempty"`
	SelfCorrectionGrace bool                 `json:"self_correction_grace"`
	LastIntervention    ArbiterDecision      `json:"last_intervention"`
}

func NewStateCodec(key []byte) (*StateCodec, error) {
	if len(key) != 32 {
		return nil, errors.New("conversation: state key must contain exactly 32 bytes")
	}
	keyCopy := append([]byte(nil), key...)
	block, err := aes.NewCipher(keyCopy)
	wipe(keyCopy)
	if err != nil {
		return nil, errors.New("conversation: create state cipher")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("conversation: create state AEAD")
	}
	return &StateCodec{aead: aead, now: time.Now, random: rand.Reader}, nil
}

func (codec *StateCodec) seal(uid string, state conversationState) (string, error) {
	if !validUID(uid) || codec == nil || codec.aead == nil || state.Turn < 1 || state.Turn > maxStateTurns {
		return "", ErrInvalidStateToken
	}
	normalized, err := normalizeConversationState(state)
	if err != nil {
		return "", ErrInvalidStateToken
	}
	if normalized.SessionID == "" {
		normalized.SessionID, err = codec.newSessionID()
		if err != nil {
			return "", ErrInvalidStateToken
		}
	}

	now := codec.now().UTC().Truncate(time.Second)
	normalized.Version = SchemaVersion
	normalized.IssuedAt = now.Unix()
	normalized.ExpiresAt = now.Add(stateTokenTTL).Unix()
	plaintext, err := json.Marshal(normalized)
	if err != nil {
		return "", ErrInvalidStateToken
	}
	defer wipe(plaintext)

	nonce := make([]byte, codec.aead.NonceSize())
	if _, err := io.ReadFull(codec.random, nonce); err != nil {
		return "", ErrInvalidStateToken
	}
	aad := makeAAD(uid)
	sealed := codec.aead.Seal(nil, nonce, plaintext, aad)
	raw := make([]byte, 0, len(nonce)+len(sealed))
	raw = append(raw, nonce...)
	raw = append(raw, sealed...)
	token := stateTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	wipe(raw)
	wipe(sealed)
	if len(token) > MaxStateTokenBytes {
		return "", ErrInvalidStateToken
	}
	return token, nil
}

func (codec *StateCodec) open(uid, token string) (conversationState, error) {
	if !validUID(uid) || codec == nil || codec.aead == nil ||
		len(token) > MaxStateTokenBytes || !strings.HasPrefix(token, stateTokenPrefix) {
		return conversationState{}, ErrInvalidStateToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, stateTokenPrefix))
	if err != nil {
		return conversationState{}, ErrInvalidStateToken
	}
	defer wipe(raw)
	nonceSize := codec.aead.NonceSize()
	if len(raw) < nonceSize+codec.aead.Overhead()+2 {
		return conversationState{}, ErrInvalidStateToken
	}

	aad := makeAAD(uid)
	plaintext, err := codec.aead.Open(nil, raw[:nonceSize], raw[nonceSize:], aad)
	if err != nil {
		return conversationState{}, ErrInvalidStateToken
	}
	defer wipe(plaintext)

	var state conversationState
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return conversationState{}, ErrInvalidStateToken
	}
	if err := requireJSONEOF(decoder); err != nil {
		return conversationState{}, ErrInvalidStateToken
	}

	now := codec.now().UTC().Unix()
	if state.Version != SchemaVersion ||
		state.ExpiresAt-state.IssuedAt != int64(stateTokenTTL/time.Second) ||
		state.IssuedAt > now+60 ||
		state.Turn < 1 ||
		state.Turn > maxStateTurns {
		return conversationState{}, ErrInvalidStateToken
	}
	state, err = normalizeConversationState(state)
	if err != nil {
		return conversationState{}, ErrInvalidStateToken
	}
	if now >= state.ExpiresAt {
		return conversationState{}, errors.Join(
			ErrInvalidStateToken,
			ErrExpiredStateToken,
		)
	}
	return state, nil
}

func (codec *StateCodec) ensureSessionID(
	state conversationState,
) (conversationState, error) {
	if codec == nil {
		return conversationState{}, ErrInvalidStateToken
	}
	if state.SessionID != "" {
		if !validSessionID(state.SessionID) {
			return conversationState{}, ErrInvalidStateToken
		}
		return state, nil
	}
	sessionID, err := codec.newSessionID()
	if err != nil {
		return conversationState{}, ErrInvalidStateToken
	}
	state.SessionID = sessionID
	return state, nil
}

func (codec *StateCodec) newSessionID() (string, error) {
	if codec == nil || codec.random == nil {
		return "", ErrInvalidStateToken
	}
	raw := make([]byte, sessionIDBytes)
	if _, err := io.ReadFull(codec.random, raw); err != nil {
		return "", ErrInvalidStateToken
	}
	defer wipe(raw)
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func normalizeConversationState(state conversationState) (conversationState, error) {
	if state.SessionID != "" && !validSessionID(state.SessionID) {
		return conversationState{}, ErrInvalidStateToken
	}
	graph, err := normalizeGraph(state.Graph)
	if err != nil {
		return conversationState{}, err
	}
	state.Graph = graph
	if !utf8.ValidString(state.ConversationSummary) ||
		!utf8.ValidString(state.DocumentSummary) {
		return conversationState{}, ErrInvalidStateToken
	}
	state.ConversationSummary = collapseSpace(state.ConversationSummary)
	state.DocumentSummary = collapseSpace(state.DocumentSummary)
	if utf8.RuneCountInString(state.ConversationSummary) > maxConversationSummaryRunes ||
		utf8.RuneCountInString(state.DocumentSummary) > maxDocumentSummaryRunes {
		return conversationState{}, ErrInvalidStateToken
	}
	state.PendingAnswer, err = normalizePendingAnswer(state.PendingAnswer)
	if err != nil {
		return conversationState{}, ErrInvalidStateToken
	}
	state.Support, err = normalizeConversationSupport(state.Support)
	if err != nil {
		return conversationState{}, ErrInvalidStateToken
	}
	if state.Support != nil &&
		state.Support.CompanionOnly &&
		state.PendingAnswer.Active {
		return conversationState{}, ErrInvalidStateToken
	}
	if err := validateArbiter(state.LastIntervention); err != nil {
		return conversationState{}, ErrInvalidStateToken
	}
	if mathInvalid(state.LastIntervention.Score) {
		return conversationState{}, ErrInvalidStateToken
	}
	return state, nil
}

func validSessionID(value string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return false
	}
	valid := len(raw) == sessionIDBytes &&
		base64.RawURLEncoding.EncodeToString(raw) == value
	wipe(raw)
	return valid
}

func validUID(uid string) bool {
	return uid != "" &&
		len(uid) <= maxUIDBytes &&
		utf8.ValidString(uid) &&
		uid == strings.TrimSpace(uid)
}

func makeAAD(uid string) []byte {
	aad := make([]byte, 0, len(stateAADPrefix)+len(uid))
	aad = append(aad, stateAADPrefix...)
	aad = append(aad, uid...)
	return aad
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func mathInvalid(value float64) bool {
	return value != value || value > 2 || value < -1
}
