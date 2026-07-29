package conversation

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
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
		opened.Graph.Claims[0] != state.Graph.Claims[0] {
		t.Fatalf("unexpected round trip state: %#v", opened)
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
