package conversation

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestCompatibilitySealDropsExtendedJSONAndFutureScopeAsUnit(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = false
	const uid = "uid-compatibility-writer"

	future := coachState(
		answercontract.OperatorState,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	)
	future.Support = &conversationSupport{
		FadingStage:      1,
		QuestionCooldown: 1,
	}
	future.PendingAnswer.QuestionContinuityTag =
		agent.coachQuestionContinuityTag("日本の首都")
	future.PendingAnswer.ContinuityTag =
		agent.coachContinuityTag("state\x00東京")
	future.PendingAnswer.ExpansionOptIn = true

	// Compatibility readers must accept a token emitted by the future writer.
	futureToken, err := agent.codec.seal(uid, future)
	if err != nil {
		t.Fatalf("seal future token: %v", err)
	}
	openedFuture, err := agent.codec.open(uid, futureToken)
	if err != nil {
		t.Fatalf("open future token: %v", err)
	}
	if openedFuture.Support == nil ||
		openedFuture.PendingAnswer.QuestionContinuityTag == "" ||
		openedFuture.PendingAnswer.ContinuityTag == "" ||
		!openedFuture.PendingAnswer.ExpansionOptIn {
		t.Fatal("future fields were not decoded")
	}

	compatToken, err := agent.sealState(uid, openedFuture)
	if err != nil {
		t.Fatalf("compatibility seal: %v", err)
	}
	compatState, err := agent.codec.open(uid, compatToken)
	if err != nil {
		t.Fatalf("open compatibility token: %v", err)
	}
	if compatState.Support != nil {
		t.Fatal("compatibility writer renewed Support")
	}
	if compatState.PendingAnswer.Active {
		t.Fatal("future capability proofs were stripped without clearing their scope")
	}

	encoded := decryptedStateJSON(t, agent.codec, uid, compatToken)
	if bytes.Contains(encoded, []byte(`"support"`)) ||
		bytes.Contains(encoded, []byte(`"question_continuity_tag"`)) ||
		bytes.Contains(encoded, []byte(`"continuity_tag"`)) ||
		bytes.Contains(encoded, []byte(`"expansion_opt_in"`)) {
		t.Fatalf("compatibility JSON contains an extended field: %s", encoded)
	}
}

func TestExtendedSealPreservesAdditiveFields(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true
	const uid = "uid-extended-writer"

	state := coachState(
		answercontract.OperatorState,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	)
	state.Support = &conversationSupport{QuestionCooldown: 1}
	state.PendingAnswer.QuestionContinuityTag =
		agent.coachQuestionContinuityTag("日本の首都")
	state.PendingAnswer.ContinuityTag =
		agent.coachContinuityTag("state\x00東京")
	state.PendingAnswer.ExpansionOptIn = true

	token, err := agent.sealState(uid, state)
	if err != nil {
		t.Fatalf("extended seal: %v", err)
	}
	encoded := decryptedStateJSON(t, agent.codec, uid, token)
	for _, field := range []string{
		`"support"`,
		`"question_continuity_tag"`,
		`"continuity_tag"`,
		`"expansion_opt_in"`,
	} {
		if !bytes.Contains(encoded, []byte(field)) {
			t.Fatalf("extended JSON omitted %s: %s", field, encoded)
		}
	}
}

func TestPromptPendingAnswerOmitsEveryServerProofAndExpansionCapability(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	frame := coachState(
		answercontract.OperatorState,
		respondent.CoachPhaseAwaitingRestatement,
		1,
	).PendingAnswer
	frame.RestatementTag = "legacy-server-proof"
	frame.QuestionContinuityTag =
		agent.coachQuestionContinuityTag("日本の首都")
	frame.ContinuityTag = agent.coachContinuityTag("state\x00東京")
	frame.ExpansionOptIn = true

	encoded, err := json.Marshal(pendingAnswerForPrompt(frame))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"restatement_tag",
		"question_continuity_tag",
		"continuity_tag",
		"expansion_opt_in",
		frame.RestatementTag,
		frame.QuestionContinuityTag,
		frame.ContinuityTag,
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("server proof reached prompt JSON: %s", encoded)
		}
	}
}

func TestStoredQuestionAllowsBarePersonallyOwnedA(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	frame := coachState(
		answercontract.OperatorState,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	).PendingAnswer
	frame.QuestionContinuityTag =
		agent.coachQuestionContinuityTag("日本の首都")

	plan := coachBareAnswerPlan("日本の首都", "東京です", "東京")
	bound, verificationPlan := agent.bindFirstCoachAnswerForTurn(
		frame,
		plan,
		"東京です",
	)
	if bound.ContinuityTag == "" {
		t.Fatal("bare A was not bound to the authenticated pending question")
	}
	if verificationPlan.AnswerContract.QuestionFrame.Subject != "日本の首都" {
		t.Fatalf(
			"verification subject = %q",
			verificationPlan.AnswerContract.QuestionFrame.Subject,
		)
	}
	continuous, selfContained := agent.coachAttemptContinuity(
		bound,
		verificationPlan,
		"東京です",
	)
	if !continuous || !selfContained {
		t.Fatal("bare A failed deterministic continuity after binding")
	}
}

func TestNaturalReportedQuestionBindsBareAnswerAcrossTurns(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true

	awaiting := validModelPlan()
	awaiting.AssistanceTarget = "respondent"
	awaiting.RespondentStage = "awaiting_answer"
	awaiting.AnswerContract.QuestionFrame.Operator =
		answercontract.OperatorPurpose
	awaiting.AnswerContract.QuestionFrame.Subject = "導入目的"
	awaiting.AnswerContract.QuestionFrame.RequiredSlots =
		[]answercontract.RequiredSlot{answercontract.SlotPurpose}

	frame := agent.pendingAnswerFromPlan(
		awaiting,
		"上司に何のために入れるのかと聞かれました",
	)
	if !frame.Active || frame.QuestionContinuityTag == "" {
		t.Fatalf("natural reported question was not committed: %#v", frame)
	}

	attempt := validModelPlan()
	attempt.AssistanceTarget = "respondent"
	attempt.RespondentStage = "restructure"
	attempt.AnswerAttempt = "品質向上です"
	attempt.RespondentEvidence = []modelSlotEvidence{{
		Slot: answercontract.SlotPurpose,
		Span: "品質向上",
	}}
	attempt.AnswerContract.QuestionFrame.Operator =
		answercontract.OperatorPurpose
	attempt.AnswerContract.QuestionFrame.Subject = "導入目的"
	attempt.AnswerContract.QuestionFrame.RequiredSlots =
		[]answercontract.RequiredSlot{answercontract.SlotPurpose}

	bound, _ := agent.bindFirstCoachAnswerForTurn(
		frame,
		attempt,
		"品質向上です",
	)
	if bound.ContinuityTag == "" {
		t.Fatal("bare natural answer was not bound to the stored question")
	}
}

func TestBareABindingRejectsNewQuestionQuoteProxyAndCorrection(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	base := coachState(
		answercontract.OperatorState,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	).PendingAnswer
	base.QuestionContinuityTag =
		agent.coachQuestionContinuityTag("日本の首都")

	tests := []struct {
		name      string
		utterance string
		span      string
	}{
		{name: "new question", utterance: "東京です。大阪の首都は何ですか？", span: "東京"},
		{name: "quoted", utterance: "前の文は「東京です」", span: "東京"},
		{name: "proxy", utterance: "ChatGPTの答えは東京です", span: "東京"},
		{name: "correction", utterance: "東京です。いや大阪です", span: "東京"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := coachBareAnswerPlan(
				"日本の首都",
				test.utterance,
				test.span,
			)
			bound, _ := agent.bindFirstCoachAnswerForTurn(
				base,
				plan,
				test.utterance,
			)
			if bound.ContinuityTag != "" {
				t.Fatal("unsafe answer acquired a continuity capability")
			}
		})
	}
}

func TestContinuityTagsRequireCanonicalSixteenByteBase64(t *testing.T) {
	frame := coachState(
		answercontract.OperatorState,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	).PendingAnswer
	valid := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 16))
	frame.QuestionContinuityTag = valid
	frame.ContinuityTag = valid
	frame.ExpansionOptIn = true
	if _, err := normalizePendingAnswer(frame); err != nil {
		t.Fatalf("valid additive fields rejected: %v", err)
	}

	for _, invalid := range []string{
		"not-base64!",
		base64.RawURLEncoding.EncodeToString([]byte{1}),
		valid + "=",
	} {
		invalidFrame := frame
		invalidFrame.ContinuityTag = invalid
		if _, err := normalizePendingAnswer(invalidFrame); err == nil {
			t.Fatalf("invalid continuity tag accepted: %q", invalid)
		}
	}
}

func coachBareAnswerPlan(subject, utterance, target string) modelPlan {
	plan := validModelPlan()
	plan.AssistanceTarget = "respondent"
	plan.RespondentStage = "restructure"
	plan.AnswerAttempt = utterance
	plan.RespondentEvidence = []modelSlotEvidence{{
		Slot: answercontract.SlotState,
		Span: target,
	}}
	plan.RespondentProtected = []string{}
	plan.AnswerContract.QuestionFrame.Operator = answercontract.OperatorState
	plan.AnswerContract.QuestionFrame.Subject = subject
	plan.AnswerContract.QuestionFrame.RequiredSlots =
		[]answercontract.RequiredSlot{answercontract.SlotState}
	return plan
}

func decryptedStateJSON(
	t *testing.T,
	codec *StateCodec,
	uid string,
	token string,
) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(
		strings.TrimPrefix(token, stateTokenPrefix),
	)
	if err != nil {
		t.Fatal(err)
	}
	nonceSize := codec.aead.NonceSize()
	plaintext, err := codec.aead.Open(
		nil,
		raw[:nonceSize],
		raw[nonceSize:],
		makeAAD(uid),
	)
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}
