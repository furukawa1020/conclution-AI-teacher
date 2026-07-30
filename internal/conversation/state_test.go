package conversation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
)

func TestStateCodecRoundTripUIDBindingExpiryAndTamper(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	codec, err := NewStateCodec(key)
	if err != nil {
		t.Fatalf("NewStateCodec: %v", err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	codec.now = func() time.Time { return now }
	codec.random = bytes.NewReader(bytes.Repeat([]byte{0x24}, 64))

	state := conversationState{
		Turn: 1,
		Graph: ThoughtStateGraph{
			Claims:         []string{"速度より検証可能性を優先する"},
			Grounds:        []string{"審査では再現性が必要"},
			Assumptions:    []string{},
			OpenLoops:      []string{"実測値を確認する"},
			Contradictions: []string{},
			Decisions:      []string{"小さく検証する"},
		},
		ConversationSummary: "応募案の検証方法を整理中",
		DocumentSummary:     "論文は小規模な比較実験を報告",
		SelfCorrectionGrace: true,
		LastIntervention: ArbiterDecision{
			Benefit: 0.8, InterruptionCost: 0.2, Urgency: 0.1,
			Confidence: 0.9, Score: 0.62, Act: "reflect",
		},
	}
	token, err := codec.seal("uid-a", state)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(token, state.ConversationSummary) ||
		strings.Contains(token, state.DocumentSummary) {
		t.Fatal("encrypted token exposes plaintext")
	}

	opened, err := codec.open("uid-a", token)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened.Turn != 1 ||
		opened.ConversationSummary != state.ConversationSummary ||
		opened.Graph.Claims[0] != state.Graph.Claims[0] ||
		!validSessionID(opened.SessionID) {
		t.Fatalf("unexpected round trip state: %#v", opened)
	}
	if strings.Contains(token, opened.SessionID) {
		t.Fatal("encrypted token exposes the session identifier")
	}
	nextToken, err := codec.seal("uid-a", opened)
	if err != nil {
		t.Fatalf("reseal: %v", err)
	}
	reopened, err := codec.open("uid-a", nextToken)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.SessionID != opened.SessionID {
		t.Fatalf(
			"session id rotated within one encrypted conversation: %q != %q",
			reopened.SessionID,
			opened.SessionID,
		)
	}
	if _, err := codec.open("uid-b", token); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("wrong UID: got %v", err)
	}

	tampered := []byte(token)
	tampered[len(tampered)/2] ^= 1
	if _, err := codec.open("uid-a", string(tampered)); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("bit flip: got %v", err)
	}

	codec.now = func() time.Time { return now.Add(stateTokenTTL) }
	if _, err := codec.open("uid-a", token); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("expired token: got %v", err)
	}
}

func TestStateCodecRequiresAES256Key(t *testing.T) {
	for _, size := range []int{0, 16, 24, 31, 33} {
		if _, err := NewStateCodec(make([]byte, size)); err == nil {
			t.Errorf("key size %d unexpectedly accepted", size)
		}
	}
	if _, err := NewStateCodec(make([]byte, 32)); err != nil {
		t.Fatalf("32-byte key rejected: %v", err)
	}
}

func TestConversationSessionIDRemainsStableAcrossTurns(t *testing.T) {
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(
		context.Background(),
		"uid-stable-session",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "こんにちは",
		},
	)
	if err != nil {
		t.Fatalf("first Process: %v", err)
	}
	firstState, err := agent.codec.open(
		"uid-stable-session",
		first.StateToken,
	)
	if err != nil {
		t.Fatalf("open first state: %v", err)
	}

	second, err := agent.Process(
		context.Background(),
		"uid-stable-session",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "こんにちは",
			StateToken:    first.StateToken,
		},
	)
	if err != nil {
		t.Fatalf("second Process: %v", err)
	}
	secondState, err := agent.codec.open(
		"uid-stable-session",
		second.StateToken,
	)
	if err != nil {
		t.Fatalf("open second state: %v", err)
	}
	if !validSessionID(firstState.SessionID) ||
		secondState.SessionID != firstState.SessionID ||
		firstState.Turn != 1 ||
		secondState.Turn != 2 ||
		len(fake.calls) != 0 {
		t.Fatalf(
			"session binding rotated or reached model: first=%#v second=%#v calls=%d",
			firstState,
			secondState,
			len(fake.calls),
		)
	}
}

func TestLegacyStateWithoutSessionIDIsUpgradedInMemory(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	codec, err := NewStateCodec(key)
	if err != nil {
		t.Fatalf("NewStateCodec: %v", err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	codec.now = func() time.Time { return now }
	legacy := conversationState{
		Version:   SchemaVersion,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(stateTokenTTL).Unix(),
		Turn:      3,
		Graph: ThoughtStateGraph{
			Goals:          []string{},
			Claims:         []string{},
			Grounds:        []string{},
			Assumptions:    []string{},
			Constraints:    []string{},
			OpenLoops:      []string{},
			Contradictions: []string{},
			Decisions:      []string{},
		},
		PendingAnswer: PendingAnswerFrame{
			RequiredSlots: []answercontract.RequiredSlot{},
		},
		LastIntervention: ArbiterDecision{Act: "silent"},
	}
	plaintext, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy state: %v", err)
	}
	nonce := bytes.Repeat([]byte{0x27}, codec.aead.NonceSize())
	sealed := codec.aead.Seal(
		nil,
		nonce,
		plaintext,
		makeAAD("uid-legacy-session"),
	)
	raw := append(append([]byte(nil), nonce...), sealed...)
	token := stateTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)

	opened, err := codec.open("uid-legacy-session", token)
	if err != nil {
		t.Fatalf("open legacy state: %v", err)
	}
	if opened.SessionID != "" {
		t.Fatalf("legacy state unexpectedly had session id: %q", opened.SessionID)
	}
	upgraded, err := codec.ensureSessionID(opened)
	if err != nil {
		t.Fatalf("ensureSessionID: %v", err)
	}
	if !validSessionID(upgraded.SessionID) ||
		upgraded.Turn != legacy.Turn {
		t.Fatalf("legacy state upgrade changed semantics: %#v", upgraded)
	}
}
