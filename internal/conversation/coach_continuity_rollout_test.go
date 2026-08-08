package conversation

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
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

func TestAnswerProofReaderFirstSealOmitsOnlyNewInstanceField(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true
	agent.answerProofWrites = false
	const uid = "uid-answer-proof-reader-first"
	state := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	)
	state.PendingAnswer.QuestionContinuityTag =
		agent.coachQuestionContinuityTag("導入目的")
	state.PendingAnswer.QuestionInstanceTag =
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 16))

	token, err := agent.sealState(uid, state)
	if err != nil {
		t.Fatalf("reader-first seal: %v", err)
	}
	plaintext := decryptedStateJSON(t, agent.codec, uid, token)
	if bytes.Contains(plaintext, []byte("question_instance_tag")) {
		t.Fatalf("reader-first revision emitted the new field: %s", plaintext)
	}
	decoded, err := agent.codec.open(uid, token)
	if err != nil {
		t.Fatalf("reader-first token did not decode: %v", err)
	}
	if !decoded.PendingAnswer.Active ||
		decoded.PendingAnswer.QuestionContinuityTag == "" {
		t.Fatalf("reader-first rollout dropped ordinary coaching state: %#v", decoded.PendingAnswer)
	}
}

func TestVerifierProgressReaderFirstSealAndWriterRollout(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true
	agent.verifierProgressWrites = false
	const uid = "uid-verifier-progress-reader-first"
	state := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	)
	stored := respondent.StoreVerifierProgress(
		respondent.DefaultVerifierProgressPosterior(),
	)
	state.PendingAnswer.VerifierProgress = &stored

	readerToken, err := agent.sealState(uid, state)
	if err != nil {
		t.Fatalf("reader-first seal: %v", err)
	}
	readerJSON := decryptedStateJSON(t, agent.codec, uid, readerToken)
	if bytes.Contains(readerJSON, []byte("verifier_progress")) {
		t.Fatalf("reader-first revision emitted posterior: %s", readerJSON)
	}

	agent.verifierProgressWrites = true
	writerToken, err := agent.sealState(uid, state)
	if err != nil {
		t.Fatalf("writer seal: %v", err)
	}
	writerJSON := decryptedStateJSON(t, agent.codec, uid, writerToken)
	if !bytes.Contains(writerJSON, []byte("verifier_progress")) {
		t.Fatalf("writer omitted posterior: %s", writerJSON)
	}
	decoded, err := agent.codec.open(uid, writerToken)
	if err != nil {
		t.Fatalf("writer token did not decode: %v", err)
	}
	if decoded.PendingAnswer.VerifierProgress == nil ||
		!decoded.PendingAnswer.VerifierProgress.Valid() {
		t.Fatalf("posterior did not round trip: %#v", decoded.PendingAnswer)
	}
}

func TestPendingAnswerRejectsMalformedVerifierProgress(t *testing.T) {
	frame := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	).PendingAnswer
	invalid := respondent.StoreVerifierProgress(
		respondent.DefaultVerifierProgressPosterior(),
	)
	invalid.Mass[0]++
	frame.VerifierProgress = &invalid
	if _, err := normalizePendingAnswer(frame); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("malformed verifier progress error = %v", err)
	}
}

func TestVerifierProgressResetsAcrossSemanticControlTransitions(t *testing.T) {
	frame := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	).PendingAnswer
	stale := respondent.StoreVerifierProgress(respondent.VerifierProgressPosterior{
		CommittedFirst: 1,
	})
	frame.VerifierProgress = &stale

	same := pendingAnswerWithControl(
		frame,
		respondent.CoachPhaseAwaitingAnswer,
		1,
	)
	if same.VerifierProgress == nil {
		t.Fatal("attempt-only transition discarded the current-scope posterior")
	}

	for name, next := range map[string]PendingAnswerFrame{
		"phase": pendingAnswerWithControl(
			frame,
			respondent.CoachPhaseAwaitingRestatement,
			1,
		),
		"expansion": pendingAnswerWithControl(
			frame,
			respondent.CoachPhaseExpanding,
			0,
		),
		"complete or release": emptyPendingAnswer(),
	} {
		t.Run(name, func(t *testing.T) {
			if next.VerifierProgress != nil {
				t.Fatalf("semantic transition retained stale progress: %#v", next)
			}
			reset := verifierProgressForControlTransition(
				frame,
				next,
				respondent.VerifierProgressPosterior{CommittedFirst: 1},
			)
			if reset != respondent.DefaultVerifierProgressPosterior() {
				t.Fatalf("semantic transition did not reset prior: %#v", reset)
			}
		})
	}

	differentOperator := frame
	differentOperator.Operator = answercontract.OperatorCause
	differentOperator.RequiredSlots = []answercontract.RequiredSlot{
		answercontract.SlotCause,
	}
	if sameVerifierProgressScope(frame, differentOperator) {
		t.Fatal("different operator reused the previous posterior")
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
	progress := respondent.StoreVerifierProgress(
		respondent.VerifierProgressPosterior{CommittedFirst: 1},
	)
	frame.VerifierProgress = &progress

	encoded, err := json.Marshal(pendingAnswerForPrompt(frame))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"restatement_tag",
		"question_continuity_tag",
		"continuity_tag",
		"expansion_opt_in",
		"verifier_progress",
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
		"QUFBQUFBQUFBQUFBQUFBQQ",
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

func TestProofSpanRejectsWholeTurnEvidenceWithoutExceptions(t *testing.T) {
	tests := []struct {
		name     string
		operator answercontract.Operator
		slot     answercontract.RequiredSlot
		subject  string
		answer   string
	}{
		{
			name:     "fronted label hides background",
			operator: answercontract.OperatorPurpose,
			slot:     answercontract.SlotPurpose,
			subject:  "導入目的",
			answer:   "目的は背景を説明すると長いのですが評価基準をそろえることです",
		},
		{
			name:     "fronted label hides AI proxy",
			operator: answercontract.OperatorState,
			slot:     answercontract.SlotState,
			subject:  "日本の首都",
			answer:   "答えはChatGPTに作ってもらった東京です",
		},
		{
			name:     "fronted label hides correction",
			operator: answercontract.OperatorState,
			slot:     answercontract.SlotState,
			subject:  "日本の首都",
			answer:   "答えは東京じゃなく大阪です",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validModelPlan()
			plan.AssistanceTarget = "respondent"
			plan.RespondentStage = "restructure"
			plan.AnswerAttempt = test.answer
			plan.RespondentEvidence = []modelSlotEvidence{{
				Slot: test.slot,
				Span: test.answer,
			}}
			plan.AnswerContract.QuestionFrame.Operator = test.operator
			plan.AnswerContract.QuestionFrame.Subject = test.subject
			plan.AnswerContract.QuestionFrame.RequiredSlots =
				[]answercontract.RequiredSlot{test.slot}
			if coachProofSpanBound(plan, test.answer) {
				t.Fatal("whole-turn evidence passed the proof-only span gate")
			}
		})
	}

	const answer = "目的は評価基準をそろえることです。判断のばらつきを減らします"
	plan := validModelPlan()
	plan.AssistanceTarget = "respondent"
	plan.RespondentStage = "restructure"
	plan.AnswerAttempt = answer
	plan.RespondentEvidence = []modelSlotEvidence{{
		Slot: answercontract.SlotPurpose,
		Span: "目的は評価基準をそろえることです",
	}}
	plan.AnswerContract.QuestionFrame.Operator = answercontract.OperatorPurpose
	plan.AnswerContract.QuestionFrame.Subject = "導入目的"
	plan.AnswerContract.QuestionFrame.RequiredSlots =
		[]answercontract.RequiredSlot{answercontract.SlotPurpose}
	if !coachProofSpanBound(plan, answer) {
		t.Fatal("bounded A-first evidence was rejected")
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
