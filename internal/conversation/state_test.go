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
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestPendingAnswerRestatementTagIsBoundedAndPhaseScoped(t *testing.T) {
	validTag := base64.RawURLEncoding.EncodeToString(
		bytes.Repeat([]byte{0x64}, coachRestatementTagBytes),
	)
	base := PendingAnswerFrame{
		Active:            true,
		Operator:          answercontract.OperatorPurpose,
		Subject:           pendingSubjectForOperator(answercontract.OperatorPurpose),
		RequiredSlots:     []answercontract.RequiredSlot{answercontract.SlotPurpose},
		ExpansionOperator: answercontract.OperatorCause,
		Phase:             respondent.CoachPhaseAwaitingRestatement,
		Attempts:          1,
		RestatementTag:    validTag,
	}
	if _, err := normalizePendingAnswer(base); err != nil {
		t.Fatalf("valid restatement tag rejected: %v", err)
	}
	for name, mutate := range map[string]func(*PendingAnswerFrame){
		"malformed": func(frame *PendingAnswerFrame) {
			frame.RestatementTag = "not-base64!"
		},
		"wrong length": func(frame *PendingAnswerFrame) {
			frame.RestatementTag = base64.RawURLEncoding.EncodeToString([]byte{1})
		},
		"wrong phase": func(frame *PendingAnswerFrame) {
			frame.Phase = respondent.CoachPhaseAwaitingAnswer
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if _, err := normalizePendingAnswer(candidate); !errors.Is(err, ErrInvalidStateToken) {
				t.Fatalf("invalid tag state error = %v", err)
			}
		})
	}
}

func TestCoachRestatementTagBindsSession(t *testing.T) {
	key := deriveCoachRestatementKey(bytes.Repeat([]byte{0x32}, 32))
	defer wipe(key)
	sessionA := base64.RawURLEncoding.EncodeToString(
		bytes.Repeat([]byte{0x41}, sessionIDBytes),
	)
	sessionB := base64.RawURLEncoding.EncodeToString(
		bytes.Repeat([]byte{0x42}, sessionIDBytes),
	)
	const fingerprint = "purpose\x00purpose\x00目的は評価基準をそろえることです"

	tag, ok := coachRestatementTag(key, sessionA, fingerprint)
	if !ok || !validCoachRestatementTag(tag) {
		t.Fatalf("valid tag = %q, ok = %v", tag, ok)
	}
	repeated, ok := coachRestatementTag(key, sessionA, fingerprint)
	if !ok || repeated != tag {
		t.Fatalf("same scope was not deterministic: %q != %q", repeated, tag)
	}
	otherSession, ok := coachRestatementTag(key, sessionB, fingerprint)
	if !ok || otherSession == tag {
		t.Fatal("tag was not bound to the encrypted session")
	}
	if _, ok := coachRestatementTag(key[:8], sessionA, fingerprint); ok {
		t.Fatal("short HMAC key was accepted")
	}
}

func TestRestatementCorrectionGuardDoesNotMatchOrdinaryWords(t *testing.T) {
	target := "目的は相談相手への思いやりを広げることです"
	clauses := []string{
		target,
		"間違う人を減らすのではなく支えます",
	}
	// Contrast in a supporting clause is conservatively rejected, but ordinary
	// substrings such as 思いやり and 間違う must not match いや / 違う alone.
	if restatementHasCorrectionSignal([]string{target, "思いやりを大切にします"}, target) {
		t.Fatal("ordinary 思いやり was treated as a correction")
	}
	if restatementHasCorrectionSignal([]string{target, "間違う人を責めません"}, target) {
		t.Fatal("ordinary 間違う was treated as a correction")
	}
	if !restatementHasCorrectionSignal(clauses, target) {
		t.Fatal("explicit contrast in a non-target clause was not rejected")
	}
}

func TestCoachRestatementFingerprintNormalizesWidthAndSpace(t *testing.T) {
	frame := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingRestatement,
		1,
	).PendingAnswer
	leftPlan := modelPlan{
		RespondentStage: "restructure",
		RespondentEvidence: []modelSlotEvidence{{
			Slot: answercontract.SlotPurpose,
			Span: "目的はＡ案　です",
		}},
	}
	rightPlan := modelPlan{
		RespondentStage: "restructure",
		RespondentEvidence: []modelSlotEvidence{{
			Slot: answercontract.SlotPurpose,
			Span: "目的はA案 です",
		}},
	}
	left, ok := coachRestatementFingerprint(
		leftPlan,
		frame,
		"背景です。目的はＡ案　です。",
	)
	if !ok {
		t.Fatal("full-width target clause was not fingerprinted")
	}
	right, ok := coachRestatementFingerprint(
		rightPlan,
		frame,
		"目的はA案 です。別の説明です。",
	)
	if !ok || right != left {
		t.Fatalf("normalized target clause changed: %q != %q", right, left)
	}
}

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
		Support: &conversationSupport{
			FadingStage:          1,
			VerifiedFirstAnswers: 1,
			QuestionCooldown:     2,
		},
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
		opened.Support == nil ||
		*opened.Support != *state.Support ||
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
	if _, err := codec.open("uid-a", token); !errors.Is(err, ErrInvalidStateToken) ||
		!errors.Is(err, ErrExpiredStateToken) {
		t.Fatalf("expired token: got %v", err)
	}
}

func TestStateCodecDoesNotClassifyUnauthenticatedTokensAsExpired(t *testing.T) {
	t.Parallel()

	codec, err := NewStateCodec(bytes.Repeat([]byte{0x52}, 32))
	if err != nil {
		t.Fatalf("NewStateCodec: %v", err)
	}
	for name, token := range map[string]string{
		"malformed": stateTokenPrefix + "not-base64!",
		"truncated": stateTokenPrefix + base64.RawURLEncoding.EncodeToString(
			bytes.Repeat([]byte{0x24}, 8),
		),
	} {
		t.Run(name, func(t *testing.T) {
			_, openErr := codec.open("uid-a", token)
			if !errors.Is(openErr, ErrInvalidStateToken) {
				t.Fatalf("error = %v; want invalid state", openErr)
			}
			if errors.Is(openErr, ErrExpiredStateToken) {
				t.Fatalf("unauthenticated token classified as expired: %v", openErr)
			}
		})
	}
}

func TestStateCodecRejectsPrePrivacyEpochToken(t *testing.T) {
	const uid = "uid-pre-privacy-epoch"
	key := bytes.Repeat([]byte{0x53}, 32)
	codec, err := NewStateCodec(key)
	if err != nil {
		t.Fatalf("NewStateCodec: %v", err)
	}
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	codec.now = func() time.Time { return now }
	legacy := conversationState{
		Version:   SchemaVersion,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(stateTokenTTL).Unix(),
		Turn:      1,
		Graph: ThoughtStateGraph{
			Goals:          []string{},
			Claims:         []string{"pre-boundary content"},
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
	nonce := bytes.Repeat([]byte{0x29}, codec.aead.NonceSize())
	legacyAAD := append([]byte("kotae-conversation-state-v1\x00"), []byte(uid)...)
	sealed := codec.aead.Seal(nil, nonce, plaintext, legacyAAD)
	raw := append(append([]byte(nil), nonce...), sealed...)
	legacyToken := "v1." + base64.RawURLEncoding.EncodeToString(raw)

	if _, err := codec.open(uid, legacyToken); !errors.Is(err, ErrInvalidStateToken) ||
		errors.Is(err, ErrExpiredStateToken) {
		t.Fatalf("pre-privacy epoch token error = %v", err)
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
