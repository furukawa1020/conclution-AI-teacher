package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestAgentAssistantFollowUpRemainsOrdinaryConversation(t *testing.T) {
	const (
		uid           = "uid-natural-assistant-follow-up"
		followUp      = "理由はあとで聞きます。まず、目的は何ですか？"
		answer        = "目的は評価基準をそろえることです"
		ordinaryReply = "評価基準をそろえたいんですね。今はどこまで決まっていますか？"
	)
	clarify := validModelPlan()
	clarify.InterventionPolicy = "clarify"
	clarify.Intervention.Act = "clarify"
	clarify.SpokenReply = followUp
	clarify.AnswerContract = validCriticContract(followUp)
	ordinary := validModelPlan()
	ordinary.InterventionPolicy = "clarify"
	ordinary.Intervention.Act = "clarify"
	ordinary.SpokenReply = ordinaryReply
	ordinary.AnswerContract = validCriticContract(ordinaryReply)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, clarify)},
		{body: encodeContract(t, validCriticContract(followUp))},
		{body: encodePlan(t, ordinary)},
		{body: encodeContract(t, validCriticContract(ordinaryReply))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "まだ目的を決めきれていません",
		Ambient:       true,
		Foreground:    true,
	})
	if err != nil {
		t.Fatalf("assistant follow-up: %v", err)
	}
	if first.AssistanceTarget != "assistant" ||
		first.CoachPhase != "none" ||
		first.CoachAction != "none" ||
		first.SpokenReply != followUp {
		t.Fatalf("assistant follow-up metadata: %#v", first)
	}
	afterQuestion, err := agent.codec.open(uid, first.StateToken)
	if err != nil {
		t.Fatalf("open state after assistant question: %v", err)
	}
	if afterQuestion.PendingAnswer.Active {
		t.Fatalf("assistant-authored question became a graded scope: %#v", afterQuestion.PendingAnswer)
	}
	stateJSON, err := json.Marshal(afterQuestion)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if bytes.Contains(stateJSON, []byte(followUp)) ||
		bytes.Contains(stateJSON, []byte("評価基準の導入目的")) {
		t.Fatalf("assistant question prose entered state: %s", stateJSON)
	}

	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     answer,
		StateToken:    first.StateToken,
		Ambient:       true,
		Foreground:    true,
	})
	if err != nil {
		t.Fatalf("natural foreground answer: %v", err)
	}
	if second.AssistanceTarget != "assistant" ||
		second.RespondentStage != "none" ||
		second.CoachPhase != "none" ||
		second.CoachAction != "none" ||
		second.SpokenReply != ordinaryReply {
		t.Fatalf("ordinary follow-up answer entered coaching: %#v", second)
	}
	if len(fake.calls) != 4 ||
		!strings.Contains(fake.calls[2].prompt, `"support_style":"listen"`) {
		t.Fatalf("ordinary answer turn did not suppress stacked optional questions: %#v", fake.calls)
	}
	completed, err := agent.codec.open(uid, second.StateToken)
	if err != nil {
		t.Fatalf("open ordinary continuation state: %v", err)
	}
	if completed.PendingAnswer.Active {
		t.Fatalf("ordinary continuation created a coach frame: %#v", completed.PendingAnswer)
	}
	if completed.Support == nil || completed.Support.QuestionCooldown != 1 {
		t.Fatalf("ordinary follow-up did not pause repeated questions: %#v", completed.Support)
	}
}

func TestAgentExplicitRespondentCoachRunsBoundedAnswerFirstSequence(t *testing.T) {
	const (
		uid          = "uid-explicit-coach-sequence"
		questionText = "上司に、導入目的は何かと聞かれました"
		lateAnswer   = "判断のばらつきを減らすためです。目的は評価基準をそろえることです"
		coreAnswer   = "目的は評価基準をそろえることです。判断のばらつきを減らします"
		proxyDraft   = "AIが本人の代わりに作った回答です。"
		naturalReply = "なるほど、そう考えているんですね。"
	)
	awaiting := respondentAwaitingPlan()
	late := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		lateAnswer,
		"目的は評価基準をそろえることです",
		proxyDraft,
	)
	core := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		coreAnswer,
		"目的は評価基準をそろえることです",
		proxyDraft,
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, awaiting)},
		{body: encodePlan(t, late)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			lateAnswer,
			"目的は評価基準をそろえることです",
			answercontract.PositionLater,
		))},
		{body: encodePlan(t, core)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			coreAnswer,
			"目的は評価基準をそろえることです",
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     questionText + "。どう答えればいいですか",
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}
	assertCoachMetadata(t, first, "awaiting_answer", "elicit")
	firstState := openCoachState(t, agent, uid, first.StateToken)
	if !firstState.PendingAnswer.Active ||
		firstState.PendingAnswer.Attempts != 0 ||
		firstState.PendingAnswer.Operator != answercontract.OperatorPurpose ||
		firstState.PendingAnswer.Subject != "質問が求める目的" ||
		len(firstState.PendingAnswer.RequiredSlots) != 1 ||
		firstState.PendingAnswer.RequiredSlots[0] != answercontract.SlotPurpose ||
		firstState.PendingAnswer.QuestionContinuityTag !=
			agent.coachQuestionContinuityTag("導入目的") ||
		firstState.PendingAnswer.ContinuityTag != "" {
		t.Fatalf("initial coach frame: %#v", firstState.PendingAnswer)
	}
	stateJSON, err := json.Marshal(firstState)
	if err != nil {
		t.Fatalf("marshal coach state: %v", err)
	}
	for _, forbidden := range []string{questionText, "導入目的"} {
		if bytes.Contains(stateJSON, []byte(forbidden)) {
			t.Fatalf("question content entered state: %s", stateJSON)
		}
	}

	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     lateAnswer,
		StateToken:    first.StateToken,
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("A later: %v", err)
	}
	assertCoachMetadata(t, second, "awaiting_restatement", "restate")
	if strings.Contains(second.SpokenReply, proxyDraft) ||
		strings.Contains(second.SpokenReply, lateAnswer) {
		t.Fatalf("proxy answer leaked into restatement guidance: %q", second.SpokenReply)
	}
	bound := openCoachState(t, agent, uid, second.StateToken)
	if !validCoachRestatementTag(bound.PendingAnswer.RestatementTag) {
		t.Fatalf("restatement target was not bound: %#v", bound.PendingAnswer)
	}
	boundJSON, err := json.Marshal(bound)
	if err != nil {
		t.Fatalf("marshal bound state: %v", err)
	}
	for _, forbidden := range []string{
		lateAnswer,
		"目的は評価基準をそろえることです",
	} {
		if bytes.Contains(boundJSON, []byte(forbidden)) {
			t.Fatalf("answer evidence entered encrypted state plaintext: %s", boundJSON)
		}
	}

	third, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     coreAnswer,
		StateToken:    second.StateToken,
	})
	if err != nil {
		t.Fatalf("A first: %v", err)
	}
	assertCoachMetadata(t, third, "complete", "complete")
	if strings.Contains(third.SpokenReply, proxyDraft) ||
		strings.Contains(third.SpokenReply, coreAnswer) ||
		third.SpokenReply != naturalReply ||
		strings.HasSuffix(third.SpokenReply, "？") {
		t.Fatalf("core completion opened an unremembered question: %#v", third)
	}
	following := openCoachState(t, agent, uid, third.StateToken)
	if following.PendingAnswer.Active {
		t.Fatalf("optional assistant follow-up became a hidden graded scope: %#v", following.PendingAnswer)
	}
	for _, call := range fake.calls[3:] {
		if strings.Contains(call.prompt, bound.PendingAnswer.RestatementTag) {
			t.Fatal("server-only restatement verifier entered a model prompt")
		}
	}
}

func TestAgentBoundExternalQuestionCannotCompleteOnUnrelatedNextTurn(t *testing.T) {
	const (
		uid          = "uid-bound-question-unrelated-turn"
		questionText = "上司に、導入目的は何かと聞かれました"
		unrelated    = "話題を変えて、今日はゲームの音楽がよかったです"
	)
	ordinary := validModelPlan()
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, respondentAwaitingPlan())},
		{body: encodePlan(t, ordinary)},
		{body: encodeContract(t, validCriticContract(ordinary.SpokenReply))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     questionText + "。どう答えればいいですか",
	})
	if err != nil {
		t.Fatalf("bind external question: %v", err)
	}
	bound := openCoachState(t, agent, uid, first.StateToken)
	if bound.PendingAnswer.QuestionContinuityTag !=
		agent.coachQuestionContinuityTag("導入目的") {
		t.Fatalf("external question was not bound: %#v", bound.PendingAnswer)
	}

	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     unrelated,
		StateToken:    first.StateToken,
	})
	if err != nil {
		t.Fatalf("unrelated next turn: %v", err)
	}
	if second.CoachPhase == "complete" || second.CoachAction == "complete" {
		t.Fatalf("unrelated turn completed the bound answer: %#v", second)
	}
	following := openCoachState(t, agent, uid, second.StateToken)
	if following.PendingAnswer.Active {
		t.Fatalf(
			"explicit topic change retained the old answer scope: result=%#v pending=%#v calls=%d",
			second,
			following.PendingAnswer,
			len(fake.calls),
		)
	}
}

func TestAgentExplicitFirstAnswerUpdatesBoundedFadingMetadata(t *testing.T) {
	const (
		uid          = "uid-explicit-coach-first-answer"
		questionText = "上司に、導入目的は何かと聞かれました"
		coreAnswer   = "目的は評価基準をそろえることです。判断のばらつきを減らします"
		proxyDraft   = "AIが本人の代わりに作った回答です。"
	)
	core := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		coreAnswer,
		"目的は評価基準をそろえることです",
		proxyDraft,
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, respondentAwaitingPlan())},
		{body: encodePlan(t, core)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			coreAnswer,
			"目的は評価基準をそろえることです",
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     questionText + "。答え方を一問だけ手伝って",
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}
	assertCoachMetadata(t, first, "awaiting_answer", "elicit")
	firstState := openCoachState(t, agent, uid, first.StateToken)
	if first.AnswerProof != AnswerProofNone ||
		firstState.PendingAnswer.QuestionInstanceTag == "" {
		t.Fatalf("question instance was not bound without premature proof: %#v %#v", first, firstState.PendingAnswer)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     coreAnswer,
		StateToken:    first.StateToken,
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("A first: %v", err)
	}
	assertCoachMetadata(t, result, "complete", "complete")
	if result.AnswerProof != AnswerProofQuestionBoundInputAnswerFirst {
		t.Fatalf("verified answer proof = %q", result.AnswerProof)
	}
	if result.SpokenReply != "" ||
		strings.Contains(result.SpokenReply, proxyDraft) ||
		strings.Contains(result.SpokenReply, coreAnswer) ||
		strings.HasSuffix(result.SpokenReply, "？") {
		t.Fatalf("owned answer did not yield the floor: %#v", result)
	}
	following := openCoachState(t, agent, uid, result.StateToken)
	if following.PendingAnswer.Active {
		t.Fatalf("successful explicit coaching retained a follow-up: %#v", following.PendingAnswer)
	}
	if following.Support == nil ||
		following.Support.VerifiedFirstAnswers != 1 ||
		following.Support.QuestionCooldown != questionCooldownAfterAnswer {
		t.Fatalf("verified explicit answer did not update bounded fading metadata: %#v", following.Support)
	}
}

func TestAnswerProofRejectsWholeTurnTargetEvenWhenBothModelsSayFirst(t *testing.T) {
	const (
		uid      = "uid-proof-whole-turn-target"
		question = "上司に導入目的は何かと聞かれました。答え方を一問だけ手伝って"
		answer   = "目的は背景を説明すると長いのですが評価基準をそろえることです"
	)
	plan := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		answer,
		answer,
		"この下書きは読み上げない。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, respondentAwaitingPlan())},
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			answer,
			answer,
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)
	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     question,
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("bind question: %v", err)
	}
	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     answer,
		StateToken:    first.StateToken,
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("whole-turn answer: %v", err)
	}
	if second.AnswerProof != AnswerProofNone ||
		second.AnswerProofCandidate != AnswerProofNone {
		t.Fatalf("whole-turn target minted proof: %#v", second)
	}
}

func TestAnswerProofRejectsQuestionOperatorMismatch(t *testing.T) {
	const (
		uid      = "uid-proof-question-operator-mismatch"
		question = "上司に導入時期はいつかと聞かれました。答え方を一問だけ手伝って"
		answer   = "目的は品質向上です。判断のばらつきを減らします"
	)
	awaiting := respondentAwaitingPlan()
	awaiting.AnswerContract.QuestionFrame.Subject = "導入時期"
	plan := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入時期",
		answer,
		"目的は品質向上です",
		"この下書きは読み上げない。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, awaiting)},
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			answer,
			"目的は品質向上です",
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)
	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     question,
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("bind mismatched question: %v", err)
	}
	frame := openCoachState(t, agent, uid, first.StateToken).PendingAnswer
	if frame.QuestionInstanceTag != "" {
		t.Fatalf("operator mismatch minted a question capability: %#v", frame)
	}
	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     answer,
		StateToken:    first.StateToken,
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("mismatched answer: %v", err)
	}
	if second.AnswerProof != AnswerProofNone ||
		second.AnswerProofCandidate != AnswerProofNone {
		t.Fatalf("operator mismatch minted proof: %#v", second)
	}
}

func TestAgentRestatementCannotReplaceBoundAnswer(t *testing.T) {
	const (
		uid               = "uid-restatement-answer-binding"
		lateAnswer        = "背景を説明すると長いです。目的は評価基準をそろえることです"
		selectedEvidence  = "目的"
		replacementAnswer = "目的は運動習慣を作ることです"
		releaseReply      = "大丈夫です。言い直さなくても、今のままで話を続けられます。"
	)
	late := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		lateAnswer,
		selectedEvidence,
		"モデルの下書きは使いません。",
	)
	replacement := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"別の目的",
		replacementAnswer,
		selectedEvidence,
		"別の答えへ置き換えます。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, late)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			lateAnswer,
			selectedEvidence,
			answercontract.PositionLater,
		))},
		{body: encodePlan(t, replacement)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			replacementAnswer,
			selectedEvidence,
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)
	initial := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	)
	token, err := agent.codec.seal(uid, initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     lateAnswer,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("bind late answer: %v", err)
	}
	assertCoachMetadata(t, first, "awaiting_restatement", "restate")
	bound := openCoachState(t, agent, uid, first.StateToken)
	tag := bound.PendingAnswer.RestatementTag
	if !validCoachRestatementTag(tag) {
		t.Fatalf("invalid restatement tag: %#v", bound.PendingAnswer)
	}
	// The compatibility revision does not issue tags, but it must still verify
	// tagged tokens if traffic rolls back after activation.
	agent.coachRestatementBinding = false

	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     replacementAnswer,
		StateToken:    first.StateToken,
	})
	if err != nil {
		t.Fatalf("reject replacement answer: %v", err)
	}
	assertCoachMetadata(t, second, "blocked", "release")
	if second.SpokenReply != releaseReply {
		t.Fatalf("replacement answer was not released naturally: %q", second.SpokenReply)
	}
	if openCoachState(t, agent, uid, second.StateToken).PendingAnswer.Active {
		t.Fatal("replacement answer retained the coaching scope")
	}
	for _, call := range fake.calls[2:] {
		if strings.Contains(call.prompt, tag) {
			t.Fatal("server-only restatement verifier entered a model prompt")
		}
	}
}

func TestAgentUrgentSafetyPreservesRestatementBinding(t *testing.T) {
	const (
		uid               = "uid-restatement-safety-binding"
		lateAnswer        = "背景を説明すると長いです。目的は評価基準をそろえることです"
		originalEvidence  = "目的は評価基準をそろえることです"
		safetyUtterance   = "いま火事で危険です。すぐに避難します"
		safetyEvidence    = "すぐに避難します"
		safetyReply       = "今は安全を優先して、すぐに避難し緊急窓口へ連絡してください。"
		replacementAnswer = "目的は運動習慣を作ることです"
	)

	for _, assistanceTarget := range []string{"respondent", "assistant"} {
		t.Run(assistanceTarget, func(t *testing.T) {
			late := coachAttemptPlan(
				answercontract.OperatorPurpose,
				answercontract.SlotPurpose,
				"導入目的",
				lateAnswer,
				originalEvidence,
				"モデルの下書きは使いません。",
			)
			safety := validModelPlan()
			safetyCritic := validCriticContract(safetyReply)
			if assistanceTarget == "respondent" {
				safety = coachAttemptPlan(
					answercontract.OperatorPurpose,
					answercontract.SlotPurpose,
					"導入目的",
					safetyUtterance,
					safetyEvidence,
					safetyReply,
				)
				safetyCritic = coachCriticContract(
					answercontract.OperatorPurpose,
					answercontract.SlotPurpose,
					safetyUtterance,
					safetyEvidence,
					answercontract.PositionFirst,
				)
			}
			safety.InterventionPolicy = "safety"
			safety.SpokenReply = safetyReply
			safety.Intervention = modelArbiter{
				Benefit: 0, InterruptionCost: 1, Urgency: 0.95,
				Confidence: 1, Act: "reflect",
			}
			replacement := coachAttemptPlan(
				answercontract.OperatorPurpose,
				answercontract.SlotPurpose,
				"導入目的",
				replacementAnswer,
				"目的",
				"別の答えへ置き換えます。",
			)
			fake := &fakeGenerator{generations: []fakeGeneration{
				{body: encodePlan(t, late)},
				{body: encodeContract(t, coachCriticContract(
					answercontract.OperatorPurpose,
					answercontract.SlotPurpose,
					lateAnswer,
					originalEvidence,
					answercontract.PositionLater,
				))},
				{body: encodePlan(t, safety)},
				{body: encodeContract(t, safetyCritic)},
				{body: encodePlan(t, replacement)},
				{body: encodeContract(t, coachCriticContract(
					answercontract.OperatorPurpose,
					answercontract.SlotPurpose,
					replacementAnswer,
					"目的",
					answercontract.PositionFirst,
				))},
			}}
			agent := newTestAgent(t, fake)
			token, err := agent.codec.seal(
				uid,
				coachState(
					answercontract.OperatorPurpose,
					respondent.CoachPhaseAwaitingAnswer,
					0,
				),
			)
			if err != nil {
				t.Fatalf("seal initial state: %v", err)
			}

			first, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     lateAnswer,
				StateToken:    token,
			})
			if err != nil {
				t.Fatalf("bind late answer: %v", err)
			}
			bound := openCoachState(t, agent, uid, first.StateToken)
			if !validCoachRestatementTag(bound.PendingAnswer.RestatementTag) {
				t.Fatalf("invalid tag before safety turn: %#v", bound.PendingAnswer)
			}

			safetyResult, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     safetyUtterance,
				StateToken:    first.StateToken,
			})
			if err != nil {
				t.Fatalf("urgent safety: %v", err)
			}
			if safetyResult.InterventionPolicy != "safety" ||
				safetyResult.SpokenReply == "" ||
				safetyResult.CoachAction == "complete" {
				t.Fatalf("urgent safety response was suppressed: %#v", safetyResult)
			}
			afterSafety := openCoachState(t, agent, uid, safetyResult.StateToken)
			if !reflect.DeepEqual(afterSafety.PendingAnswer, bound.PendingAnswer) {
				t.Fatalf(
					"safety turn changed the bound frame: before=%#v after=%#v",
					bound.PendingAnswer,
					afterSafety.PendingAnswer,
				)
			}

			result, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     replacementAnswer,
				StateToken:    safetyResult.StateToken,
			})
			if err != nil {
				t.Fatalf("reject replacement after safety: %v", err)
			}
			assertCoachMetadata(t, result, "blocked", "release")
			if openCoachState(t, agent, uid, result.StateToken).PendingAnswer.Active {
				t.Fatal("replacement answer retained the coaching scope after safety")
			}
			if len(fake.calls) != 6 {
				t.Fatalf("model calls = %d, want three isolated planner/critic pairs", len(fake.calls))
			}
			for _, call := range fake.calls[2:] {
				if strings.Contains(call.prompt, bound.PendingAnswer.RestatementTag) {
					t.Fatal("server-only restatement verifier entered a model prompt after safety")
				}
			}
		})
	}
}

func TestAgentRestatementRejectsQuotedRetractedAnswer(t *testing.T) {
	const (
		uid              = "uid-restatement-quoted-retraction"
		lateAnswer       = "背景です。目的は評価基準をそろえることです"
		originalEvidence = "目的は評価基準をそろえることです"
		attackUtterance  = "前の「目的は評価基準をそろえることです」は違います。本当は目的は運動習慣を作ることです"
	)
	late := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		lateAnswer,
		originalEvidence,
		"モデルの下書きは使いません。",
	)
	selectedOldAnswer := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		originalEvidence,
		originalEvidence,
		"撤回を無視します。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, late)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			lateAnswer,
			originalEvidence,
			answercontract.PositionLater,
		))},
		{body: encodePlan(t, selectedOldAnswer)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			originalEvidence,
			originalEvidence,
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)
	token, err := agent.codec.seal(
		uid,
		coachState(
			answercontract.OperatorPurpose,
			respondent.CoachPhaseAwaitingAnswer,
			0,
		),
	)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     lateAnswer,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("bind original answer: %v", err)
	}
	assertCoachMetadata(t, first, "awaiting_restatement", "restate")

	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     attackUtterance,
		StateToken:    first.StateToken,
	})
	if err != nil {
		t.Fatalf("reject quoted retraction: %v", err)
	}
	assertCoachMetadata(t, second, "blocked", "release")
	if openCoachState(t, agent, uid, second.StateToken).PendingAnswer.Active {
		t.Fatal("quoted retraction retained the coaching scope")
	}
}

func TestAgentRestatementTagSurvivesHesitationTurn(t *testing.T) {
	const (
		uid        = "uid-restatement-hesitation"
		lateAnswer = "判断のばらつきを減らすためです。目的は評価基準をそろえることです"
		coreAnswer = "目的は評価基準をそろえることです。判断のばらつきを減らします"
		evidence   = "目的は評価基準をそろえることです"
	)
	late := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		lateAnswer,
		evidence,
		"モデルの下書きは使いません。",
	)
	core := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		coreAnswer,
		evidence,
		"モデルの下書きは使いません。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, late)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			lateAnswer,
			evidence,
			answercontract.PositionLater,
		))},
		{body: encodePlan(t, respondentAwaitingPlan())},
		{body: encodePlan(t, core)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			coreAnswer,
			evidence,
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)
	token, err := agent.codec.seal(
		uid,
		coachState(
			answercontract.OperatorPurpose,
			respondent.CoachPhaseAwaitingAnswer,
			0,
		),
	)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     lateAnswer,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("bind late answer: %v", err)
	}
	bound := openCoachState(t, agent, uid, first.StateToken)
	tag := bound.PendingAnswer.RestatementTag
	if !validCoachRestatementTag(tag) {
		t.Fatalf("invalid tag before hesitation: %#v", bound.PendingAnswer)
	}

	hesitation, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "えっと……",
		StateToken:    first.StateToken,
	})
	if err != nil {
		t.Fatalf("hesitation: %v", err)
	}
	held := openCoachState(t, agent, uid, hesitation.StateToken)
	if hesitation.SpokenReply != "" ||
		held.PendingAnswer.RestatementTag != tag ||
		held.PendingAnswer.Attempts != bound.PendingAnswer.Attempts {
		t.Fatalf("hesitation changed the bound scope: result=%#v state=%#v", hesitation, held.PendingAnswer)
	}

	completed, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     coreAnswer,
		StateToken:    hesitation.StateToken,
	})
	if err != nil {
		t.Fatalf("complete after hesitation: %v", err)
	}
	assertCoachMetadata(t, completed, "complete", "complete")
}

func TestAgentClearsLegacyAssistantFollowUpBeforeInference(t *testing.T) {
	const (
		uid      = "uid-legacy-assistant-follow-up"
		answer   = "目的は評価基準をそろえることです"
		response = "評価基準をそろえたいんですね。話したいところから続けてください。"
	)
	assistant := validModelPlan()
	assistant.SpokenReply = response
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, assistant)},
		{body: encodeContract(t, validCriticContract(response))},
	}}
	agent := newTestAgent(t, fake)
	legacy := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	)
	legacy.PendingAnswer.Subject = assistantFollowUpSubject
	legacy.PendingAnswer.AssistantFollowUp = true
	token, err := agent.codec.seal(uid, legacy)
	if err != nil {
		t.Fatalf("seal legacy follow-up: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     answer,
		StateToken:    token,
		Ambient:       true,
		Foreground:    true,
	})
	if err != nil {
		t.Fatalf("legacy follow-up continuation: %v", err)
	}
	if result.AssistanceTarget != "assistant" ||
		result.CoachPhase != "none" ||
		result.CoachAction != "none" ||
		result.SpokenReply != response {
		t.Fatalf("legacy follow-up retained grading authority: %#v", result)
	}
	if openCoachState(t, agent, uid, result.StateToken).PendingAnswer.Active {
		t.Fatal("legacy follow-up survived ordinary continuation")
	}
	if len(fake.calls) == 0 || strings.Contains(fake.calls[0].prompt, `"assistant_follow_up":true`) {
		t.Fatal("legacy follow-up authority entered the planner prompt")
	}
}

func TestAgentCompatibilityFlagCannotSkipLateReask(t *testing.T) {
	const (
		uid        = "uid-restatement-compatibility-mode"
		lateAnswer = "背景から話します。目的は評価基準をそろえることです"
		evidence   = "目的は評価基準をそろえることです"
		reask      = "そこまでちゃんと聞こえています。今の言葉は変えず、答えになっている一文から続けても大丈夫です。"
	)
	late := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		lateAnswer,
		evidence,
		"モデルの下書きは使いません。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, late)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			lateAnswer,
			evidence,
			answercontract.PositionLater,
		))},
	}}
	agent := newTestAgent(t, fake)
	agent.coachRestatementBinding = false
	token, err := agent.codec.seal(
		uid,
		coachState(
			answercontract.OperatorPurpose,
			respondent.CoachPhaseAwaitingAnswer,
			0,
		),
	)
	if err != nil {
		t.Fatalf("seal compatibility state: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     lateAnswer,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("compatibility restatement: %v", err)
	}
	assertCoachMetadata(t, result, "awaiting_restatement", "restate")
	if result.SpokenReply != reask || !result.NeedsClarification {
		t.Fatalf("compatibility flag bypassed the one late-answer reask: %#v", result)
	}
	state := openCoachState(t, agent, uid, result.StateToken)
	if !state.PendingAnswer.Active || state.PendingAnswer.RestatementTag != "" {
		t.Fatalf("compatibility flag changed the bounded reask state: %#v", state.PendingAnswer)
	}
}

func TestAgentActivationClearsUnboundLegacyRestatementBeforeInference(t *testing.T) {
	const (
		uid      = "uid-restatement-activation-migration"
		answer   = "目的は評価基準をそろえることです"
		response = "評価基準をそろえたいんですね。そのまま話を続けてください。"
	)
	assistant := validModelPlan()
	assistant.SpokenReply = response
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, assistant)},
		{body: encodeContract(t, validCriticContract(response))},
	}}
	agent := newTestAgent(t, fake)
	legacy := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingRestatement,
		1,
	)
	token, err := agent.codec.seal(uid, legacy)
	if err != nil {
		t.Fatalf("seal unbound legacy restatement: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     answer,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("activation migration: %v", err)
	}
	if result.AssistanceTarget != "assistant" ||
		result.CoachPhase != "none" ||
		result.CoachAction != "none" ||
		result.SpokenReply != response {
		t.Fatalf("unbound legacy scope retained grading authority: %#v", result)
	}
	if openCoachState(t, agent, uid, result.StateToken).PendingAnswer.Active {
		t.Fatal("activation renewed an unbound legacy restatement")
	}
	if len(fake.calls) == 0 || strings.Contains(
		fake.calls[0].prompt,
		`"pending_answer":{"active":true`,
	) {
		t.Fatal("unbound legacy restatement entered the planner prompt")
	}
}

func TestAgentPendingCoachDirectQuestionEscapesToAssistant(t *testing.T) {
	const uid = "uid-coach-direct-question-exit"
	awaiting := respondentAwaitingPlan()
	assistant := validModelPlan()
	assistant.SpokenReply = "日本の首都は東京です。"
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, awaiting)},
		{body: encodePlan(t, assistant)},
		{body: encodeContract(t, validCriticContract(assistant.SpokenReply))},
	}}
	agent := newTestAgent(t, fake)
	initial := coachState(answercontract.OperatorPurpose, respondent.CoachPhaseAwaitingAnswer, 0)
	token, err := agent.codec.seal(uid, initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "KOTAE、日本の首都はどこですか？",
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("direct assistant question: %v", err)
	}
	if result.AssistanceTarget != "assistant" ||
		result.RespondentStage != "none" ||
		result.CoachPhase != "none" ||
		result.CoachAction != "none" ||
		result.SpokenReply != assistant.SpokenReply {
		t.Fatalf("direct question remained trapped in coaching: %#v", result)
	}
	if openCoachState(t, agent, uid, result.StateToken).PendingAnswer.Active {
		t.Fatal("direct assistant question did not clear coaching frame")
	}
}

func TestAgentPendingCoachHonorsExplicitOptOutWithoutCallingAModel(t *testing.T) {
	for _, test := range []struct {
		name      string
		utterance string
		wantReply string
	}{
		{
			name:      "does not want to talk",
			utterance: "今日はもう話したくない",
			wantReply: "わかりました。今は話さなくて大丈夫です。",
		},
		{
			name:      "wants to listen without answering",
			utterance: "今日は聞くだけにしたい",
			wantReply: listenOnlyLocalSpokenReply,
		},
		{
			name:      "wants conversation without correction",
			utterance: "今日は話すだけにしたい",
			wantReply: "わかりました。言い直しは求めません。そのまま話してください。",
		},
		{
			name:      "natural conversation-only wording",
			utterance: "今日はただ話すだけにして",
			wantReply: "わかりました。言い直しは求めません。そのまま話してください。",
		},
		{
			name:      "negated help request",
			utterance: "答え方を手伝ってほしくない",
			wantReply: "わかりました。言い直しは求めません。そのまま話してください。",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			const uid = "uid-coach-explicit-opt-out"
			fake := &fakeGenerator{}
			agent := newTestAgent(t, fake)
			token, err := agent.codec.seal(
				uid,
				coachState(
					answercontract.OperatorPurpose,
					respondent.CoachPhaseAwaitingAnswer,
					0,
				),
			)
			if err != nil {
				t.Fatalf("seal pending state: %v", err)
			}

			result, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     test.utterance,
				StateToken:    token,
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if len(fake.calls) != 0 ||
				result.Route != "coach-opt-out-local" ||
				result.AssistanceTarget != "assistant" ||
				result.CoachPhase != "none" ||
				result.CoachAction != "none" ||
				result.ArgumentStructure != "direct_answer" ||
				result.InterventionPolicy != "wait" ||
				result.SpokenReply != test.wantReply {
				t.Fatalf("explicit opt-out was not honored locally: %#v", result)
			}
			if test.utterance == "今日は聞くだけにしたい" &&
				(countQuestions(result.SpokenReply) != 0 ||
					!strings.Contains(result.SpokenReply, "私から一つ")) {
				t.Fatalf("listen-only mode did not carry the conversation: %q", result.SpokenReply)
			}
			next := openCoachState(t, agent, uid, result.StateToken)
			if next.PendingAnswer.Active {
				t.Fatalf("explicit opt-out retained coach state: %#v", next.PendingAnswer)
			}
			if next.Support == nil || !next.Support.CompanionOnly {
				t.Fatalf("explicit opt-out did not persist session companion mode: %#v", next.Support)
			}
		})
	}
}

func TestAgentCompanionModeCanStartWithoutCoachAndResumeExplicitly(t *testing.T) {
	const uid = "uid-session-companion-mode"
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)

	paused, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "今日は話すだけ",
	})
	if err != nil {
		t.Fatalf("enter companion mode: %v", err)
	}
	pausedState := openCoachState(t, agent, uid, paused.StateToken)
	if paused.Route != "coach-opt-out-local" ||
		pausedState.Support == nil ||
		!pausedState.Support.CompanionOnly ||
		len(fake.calls) != 0 {
		t.Fatalf("standalone companion choice reached the model: result=%#v state=%#v", paused, pausedState)
	}

	notResumed, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "コーチを再開しないで",
		StateToken:    paused.StateToken,
	})
	if err != nil {
		t.Fatalf("negated resume: %v", err)
	}
	notResumedState := openCoachState(t, agent, uid, notResumed.StateToken)
	if notResumed.Route != "coach-opt-out-local" ||
		notResumedState.Support == nil ||
		!notResumedState.Support.CompanionOnly ||
		len(fake.calls) != 0 {
		t.Fatalf("negated opt-in resumed coaching: result=%#v state=%#v", notResumed, notResumedState)
	}

	resumed, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "コーチを再開して",
		StateToken:    notResumed.StateToken,
	})
	if err != nil {
		t.Fatalf("resume support: %v", err)
	}
	resumedState := openCoachState(t, agent, uid, resumed.StateToken)
	if resumed.Route != "coach-opt-in-local" ||
		resumedState.Support != nil ||
		len(fake.calls) != 0 {
		t.Fatalf("explicit resume did not remain local: result=%#v state=%#v", resumed, resumedState)
	}
}

func TestAgentPassClearsOnlyCurrentQuestion(t *testing.T) {
	const uid = "uid-one-question-pass"
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)
	token, err := agent.codec.seal(uid, coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	))
	if err != nil {
		t.Fatalf("seal pending state: %v", err)
	}
	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "今回はパス",
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	next := openCoachState(t, agent, uid, result.StateToken)
	if next.PendingAnswer.Active ||
		next.Support == nil ||
		next.Support.CompanionOnly ||
		next.Support.QuestionCooldown != questionCooldownAfterPass {
		t.Fatalf("pass changed more than the current question: %#v", next)
	}
}

func TestCoachOptOutPassPhrasesDoNotMatchOrdinaryWords(t *testing.T) {
	for _, utterance := range []string{
		"パスタを食べました", "今はパスタを食べています",
		"パスポートです", "今回はパスポートの話です", "パスワードを忘れました",
		"今日は話さないといけないことがある", "話したくないわけじゃない",
		"今日は話すだけでは足りない", "直さなくていいわけじゃない",
		"話すだけで英語が上達するという話です", "コーチをやめたくない",
	} {
		if _, ok := coachOptOutReply(utterance); ok {
			t.Fatalf("coachOptOutReply(%q) treated an ordinary word as an opt-out", utterance)
		}
		if shouldRecoverOutsideCoach(utterance) {
			t.Fatalf("shouldRecoverOutsideCoach(%q) treated an ordinary word as an opt-out", utterance)
		}
	}

	for _, utterance := range []string{"パス", "えっと、今回はパスします。", "今はパスしたい"} {
		if _, ok := coachOptOutReply(utterance); !ok {
			t.Fatalf("coachOptOutReply(%q) did not honor an explicit opt-out", utterance)
		}
		if !shouldRecoverOutsideCoach(utterance) {
			t.Fatalf("shouldRecoverOutsideCoach(%q) did not recognize an explicit opt-out", utterance)
		}
	}

	for _, utterance := range []string{
		"うまく答えられないので、答え方を手伝って",
		"どう答えればいいですか",
		"上司が目的を聞いてきたから、答え方を一問だけ手伝って",
		"答え方を一問だけ手伝って。目的は評価基準をそろえることです",
		"自分の回答を整えてほしい",
		"私も答え方を手伝ってほしい",
		"私の希望です。答え方を手伝ってほしい",
		"私の依頼で、回答を直して",
		"私はこう言いました。答え方を手伝ってほしい",
		"母はそう言いました。私は自分の回答を整えてほしい",
		"上司に聞かれました。答え方を一問だけ手伝って",
		"上司に聞かれた質問への答え方を一問だけ手伝って",
		"上司に「目的は何ですか」と聞かれました。どう答えればいいですか",
		"面接で強みを質問されました。何て答えたらいいですか",
		"面談で今後の希望を尋ねられた。なんて返せばいいですか",
		"私ならどう答えればいいですか",
		"自分なら何て答えたらいいですか",
		"could you please help me answer",
		"my boss asked me why. please help me answer",
	} {
		if !explicitCoachOptIn(utterance) {
			t.Fatalf("explicitCoachOptIn(%q) rejected a direct request", utterance)
		}
	}
	for _, utterance := range []string{
		"上司に聞かれたけど答えられなかった",
		"コーチを再開しないで",
		"答え方を手伝ってほしくない",
		"答え方を手伝ってほしいわけじゃない",
		"友達が「答え方を手伝ってほしい」と言っていた",
		"上司は答え方を手伝ってほしいと言った",
		"友達が答え方を手伝ってほしい",
		"母が答え方を手伝ってほしい",
		"母が答え方を手伝って",
		"母も答え方を手伝ってほしい",
		"母の希望は、答え方を手伝ってほしい",
		"母はこう言いました。答え方を手伝ってほしい",
		"母の希望です。答え方を手伝ってほしい",
		"後輩もそう言いました。回答を直してほしい",
		"私の母はこう言いました。答え方を手伝ってほしい",
		"僕の上司が頼みました。回答を直してほしい",
		"私は母がこう言うのを聞きました。答え方を手伝ってほしい",
		"母はこう頼みました。「回答を直して！」",
		"母はこう言いました。「答え方を手伝って。」",
		"「答え方を手伝って！」",
		"母からの依頼です：回答を直して",
		"先生の依頼で、回答を直して",
		"私の母からの指示です：回答を直して",
		"私の依頼ではなく母の依頼です：回答を直して",
		"後輩が回答を直してほしい",
		"my friend said please help me answer",
		"my friend wants me to practice answering",
		"my friend wants you to help me answer",
		"母に「どう答えればいい？」と聞かれた",
		"上司に目的を聞かれた。ChatGPTならどう答えればいいですか",
		"友達が上司に目的を聞かれた。友達の答え方を手伝って",
		"「上司に目的を聞かれた。どう答えればいい？」と友達が言っていた",
		"上司に「目的を聞かれた。どう答えればいいですか",
	} {
		if explicitCoachOptIn(utterance) {
			t.Fatalf("explicitCoachOptIn(%q) treated non-consent as opt-in", utterance)
		}
	}
}

func TestAgentForegroundExplicitRequestWithoutOwnAttemptCannotPersistRespondentScope(t *testing.T) {
	const uid = "uid-foreground-can-create-coach"
	fake := &fakeGenerator{generations: []fakeGeneration{{
		body: encodePlan(t, respondentAwaitingPlan()),
	}}}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "上司に目的を聞かれたけど答えられません。答え方を一問だけ手伝ってください",
		Ambient:       true,
		Foreground:    true,
	})
	if err != nil {
		t.Fatalf("foreground respondent proposal: %v", err)
	}
	if result.AssistanceTarget != "assistant" ||
		result.RespondentStage != "none" ||
		result.CoachPhase != "none" ||
		result.CoachAction != "none" ||
		!result.NeedsClarification {
		t.Fatalf("foreground request without an owned A created respondent scope: %#v", result)
	}
	if len(fake.calls) != 1 ||
		!strings.Contains(fake.calls[0].prompt, `"respondent_mode_allowed":true`) {
		t.Fatalf("foreground planner did not receive the explicit opt-in signal: %#v", fake.calls)
	}
	if openCoachState(t, agent, uid, result.StateToken).PendingAnswer.Active {
		t.Fatal("foreground request without a reported question and owned A persisted a respondent frame")
	}
}

func TestAgentCoachFillersDoNotConsumeGentleRetry(t *testing.T) {
	const uid = "uid-coach-fillers-no-attempt"
	fake := &fakeGenerator{generations: []fakeGeneration{{
		body: encodePlan(t, respondentAwaitingPlan()),
	}}}
	agent := newTestAgent(t, fake)
	initial := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	)
	token, err := agent.codec.seal(uid, initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "えっと……うーん。",
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("filler-only turn: %v", err)
	}
	assertCoachMetadata(t, result, "awaiting_answer", "elicit")
	if result.SpokenReply != "" {
		t.Fatalf("filler-only speech triggered another prompt: %q", result.SpokenReply)
	}
	state := openCoachState(t, agent, uid, result.StateToken)
	if state.PendingAnswer.Attempts != 0 {
		t.Fatalf("filler-only speech consumed the gentle retry: %#v", state.PendingAnswer)
	}
}

func TestAgentCoachVerificationOutagePreservesExactPendingFrame(t *testing.T) {
	const (
		uid          = "uid-coach-verification-outage"
		answer       = "目的は評価基準をそろえることです"
		plannerDraft = "この未監査の代理回答は読み上げない。"
	)
	plan := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		answer,
		answer,
		plannerDraft,
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{err: errors.New("critic provider detail must stay private")},
		{body: encodePlan(t, plan)},
		{err: errors.New("critic provider detail must stay private again")},
	}}
	agent := newTestAgent(t, fake)
	initial := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingRestatement,
		1,
	)
	// A valid-sized opaque tag exercises preservation of the authenticated
	// restatement capability without putting the underlying answer in state.
	initial.PendingAnswer.RestatementTag = "AAAAAAAAAAAAAAAAAAAAAA"
	token, err := agent.codec.seal(uid, initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}
	expected := openCoachState(t, agent, uid, token).PendingAnswer

	for turn := 0; turn < 2; turn++ {
		result, processErr := agent.Process(context.Background(), uid, VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     answer,
			StateToken:    token,
		})
		if processErr != nil {
			t.Fatalf("verification outage %d: %v", turn+1, processErr)
		}
		if result.Route != "verification-unavailable" ||
			result.AssistanceTarget != "assistant" ||
			result.RespondentStage != "none" ||
			result.CoachPhase != "none" ||
			result.CoachAction != "none" ||
			result.SpokenReply != verificationUnavailableSpokenReply ||
			strings.Contains(result.SpokenReply, answer) ||
			strings.Contains(result.SpokenReply, plannerDraft) ||
			strings.Contains(result.SpokenReply, "意味") ||
			strings.Contains(result.SpokenReply, "もう一度") {
			t.Fatalf("verification outage escaped the safe bridge: %#v", result)
		}
		next := openCoachState(t, agent, uid, result.StateToken)
		if !reflect.DeepEqual(next.PendingAnswer, expected) {
			t.Fatalf(
				"verification outage %d mutated the pending frame: got=%#v want=%#v",
				turn+1,
				next.PendingAnswer,
				expected,
			)
		}
		token = result.StateToken
	}
	if len(fake.calls) != 4 {
		t.Fatalf("high-risk coach verification retried or model-hopped: %#v", fake.calls)
	}
}

func TestAgentCoachReleasesAfterBoundedRetries(t *testing.T) {
	const uid = "uid-coach-release-cap"
	fake := &fakeGenerator{generations: []fakeGeneration{{
		body: encodePlan(t, respondentAwaitingPlan()),
	}}}
	agent := newTestAgent(t, fake)
	initial := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		respondent.MaxCoachAttempts,
	)
	token, err := agent.codec.seal(uid, initial)
	if err != nil {
		t.Fatalf("seal capped state: %v", err)
	}
	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "背景の説明だけを続けます",
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("bounded release: %v", err)
	}
	assertCoachMetadata(t, result, "blocked", "release")
	if openCoachState(t, agent, uid, result.StateToken).PendingAnswer.Active {
		t.Fatal("release retained the coaching frame")
	}
}

func TestCoachRecoveryDoesNotMistakeNazenaraForTopicChange(t *testing.T) {
	if shouldRecoverOutsideCoach("なぜなら、判断のばらつきを減らせるからです") {
		t.Fatal("answer beginning with なぜなら was treated as a direct AI question")
	}
}

func TestAgentExplicitRespondentCoachReasksLateAnswerOnce(t *testing.T) {
	const (
		uid          = "uid-explicit-coach-late-answer"
		questionText = "上司に、導入目的は何かと聞かれました"
		lateAnswer   = "判断のばらつきを減らすためです。目的は評価基準をそろえることです"
		restated     = "目的は評価基準をそろえることです。判断のばらつきを減らすためです"
		proxyDraft   = "AIが本人の代わりに作った回答です。"
		reask        = "そこまでちゃんと聞こえています。今の言葉は変えず、答えになっている一文から続けても大丈夫です。"
	)
	awaiting := respondentAwaitingPlan()
	late := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		lateAnswer,
		"目的は評価基準をそろえることです",
		proxyDraft,
	)
	restatedPlan := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		restated,
		"目的は評価基準をそろえることです",
		proxyDraft,
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, awaiting)},
		{body: encodePlan(t, late)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			lateAnswer,
			"目的は評価基準をそろえることです",
			answercontract.PositionLater,
		))},
		{body: encodePlan(t, restatedPlan)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			restated,
			"目的は評価基準をそろえることです",
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     questionText + "。どう答えればいいですか",
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}
	assertCoachMetadata(t, first, "awaiting_answer", "elicit")
	firstState := openCoachState(t, agent, uid, first.StateToken)
	if !firstState.PendingAnswer.Active ||
		firstState.PendingAnswer.Attempts != 0 ||
		firstState.PendingAnswer.Subject != "質問が求める目的" {
		t.Fatalf("initial coach frame: %#v", firstState.PendingAnswer)
	}
	stateJSON, err := json.Marshal(firstState)
	if err != nil {
		t.Fatalf("marshal coach state: %v", err)
	}
	for _, forbidden := range []string{questionText, "導入目的"} {
		if bytes.Contains(stateJSON, []byte(forbidden)) {
			t.Fatalf("question content entered state: %s", stateJSON)
		}
	}

	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     lateAnswer,
		StateToken:    first.StateToken,
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("A later: %v", err)
	}
	assertCoachMetadata(t, second, "awaiting_restatement", "restate")
	if second.AnswerProof != AnswerProofNone {
		t.Fatalf("late answer received a false proof: %q", second.AnswerProof)
	}
	if second.SpokenReply != reask ||
		strings.Contains(second.SpokenReply, proxyDraft) ||
		strings.Contains(second.SpokenReply, lateAnswer) ||
		!second.NeedsClarification {
		t.Fatalf("late answer did not receive the fixed one-time reask: %#v", second)
	}
	following := openCoachState(t, agent, uid, second.StateToken)
	if !following.PendingAnswer.Active ||
		!validCoachRestatementTag(following.PendingAnswer.RestatementTag) ||
		following.PendingAnswer.Attempts != 1 {
		t.Fatalf("late answer did not retain one bound restatement: %#v", following.PendingAnswer)
	}
	if following.Support != nil &&
		following.Support.VerifiedFirstAnswers != 0 {
		t.Fatalf("late answer was counted as a verified first answer: %#v", following.Support)
	}
	if following.PendingAnswer.AnswerTransitionEvidence !=
		AnswerTransitionEvidenceQuestionBoundInputClauseLater {
		t.Fatalf("late verifier agreement was not retained as finite evidence: %#v", following.PendingAnswer)
	}

	third, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     restated,
		StateToken:    second.StateToken,
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("same A first: %v", err)
	}
	assertCoachMetadata(t, third, "complete", "complete")
	if third.AnswerProof != AnswerProofQuestionBoundInputAnswerFirst ||
		third.AnswerTransitionProof !=
			AnswerTransitionProofQuestionBoundInputClauseLaterToFirst {
		t.Fatalf("verified transition proofs = %#v", third)
	}
	if third.SpokenReply != "" {
		t.Fatalf("transition proof added AI speech: %q", third.SpokenReply)
	}
}

func TestAgentPlannerSliceCannotSpoofVerifiedFirst(t *testing.T) {
	const (
		uid        = "uid-coach-whole-utterance-order"
		utterance  = "判断のばらつきを減らしたくて目的は評価基準をそろえることです。"
		extracted  = "目的は評価基準をそろえることです。"
		commitment = "目的は評価基準をそろえることです"
		reask      = "そこまでちゃんと聞こえています。今の言葉は変えず、答えになっている一文から続けても大丈夫です。"
	)
	plan := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		extracted,
		commitment,
		"draftによる並べ替えです。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		// Reproduce a critic that trusts the planner's narrower extraction.
		// Server-side evaluation must still bind it to the complete utterance.
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			extracted,
			commitment,
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)
	token, err := agent.codec.seal(
		uid,
		coachState(answercontract.OperatorPurpose, respondent.CoachPhaseAwaitingAnswer, 0),
	)
	if err != nil {
		t.Fatalf("seal pending state: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     utterance,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	assertCoachMetadata(t, result, "awaiting_restatement", "restate")
	if result.SpokenReply != reask || !result.NeedsClarification {
		t.Fatalf("planner slice changed whole-turn order: %#v", result)
	}
	if len(fake.calls) != 2 ||
		!strings.Contains(fake.calls[1].prompt, `"candidate_spoken_reply":"`+utterance+`"`) ||
		!strings.Contains(fake.calls[1].prompt, `"answer_attempt":"`+extracted+`"`) {
		t.Fatalf("critic was not bound to whole utterance: %#v", fake.calls)
	}
	state := openCoachState(t, agent, uid, result.StateToken)
	if !state.PendingAnswer.Active ||
		!validCoachRestatementTag(state.PendingAnswer.RestatementTag) ||
		(state.Support != nil && state.Support.VerifiedFirstAnswers != 0) {
		t.Fatalf("planner slice advanced verified success: %#v", state)
	}
}

func TestAgentForegroundUserAudioCannotCreateRespondentScope(t *testing.T) {
	const uid = "uid-foreground-cannot-create-coach"
	fake := &fakeGenerator{generations: []fakeGeneration{{
		body: encodePlan(t, respondentAwaitingPlan()),
	}}}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "上司に目的を聞かれたけど答えられません",
		Ambient:       true,
		Foreground:    true,
	})
	if err != nil {
		t.Fatalf("foreground respondent proposal: %v", err)
	}
	if result.AssistanceTarget != "assistant" ||
		result.RespondentStage != "none" ||
		result.CoachPhase != "none" ||
		result.CoachAction != "none" ||
		result.NeedsClarification {
		t.Fatalf("foreground audio created respondent scope: %#v", result)
	}
	if len(fake.calls) != 1 ||
		!strings.Contains(fake.calls[0].prompt, `"respondent_mode_allowed":false`) {
		t.Fatalf("foreground planner received respondent authority: %#v", fake.calls)
	}
	if openCoachState(t, agent, uid, result.StateToken).PendingAnswer.Active {
		t.Fatal("foreground user audio persisted a respondent frame")
	}
}

func TestBoundedFollowUpClassifierUsesOnlyFinalQuestion(t *testing.T) {
	tests := []struct {
		name string
		text string
		want answercontract.Operator
		ok   bool
	}{
		{
			name: "declarative reason does not override purpose question",
			text: "理由はあとで聞きます。目的は何ですか？",
			want: answercontract.OperatorPurpose,
			ok:   true,
		},
		{
			name: "status precedes generic definition surface",
			text: "現在の状況は何ですか？",
			want: answercontract.OperatorState,
			ok:   true,
		},
		{
			name: "broad current marker is not status",
			text: "今は何をしたいですか？",
			want: answercontract.OperatorOpen,
			ok:   true,
		},
		{
			name: "two questions are not persisted",
			text: "目的は何ですか？理由は何ですか？",
			ok:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := boundedFollowUpOperator(test.text)
			if got != test.want || ok != test.ok {
				t.Fatalf(
					"boundedFollowUpOperator(%q)=(%q,%v), want (%q,%v)",
					test.text,
					got,
					ok,
					test.want,
					test.ok,
				)
			}
		})
	}
}

func coachAttemptPlan(
	operator answercontract.Operator,
	slot answercontract.RequiredSlot,
	subject string,
	answer string,
	evidence string,
	plannerDraft string,
) modelPlan {
	plan := validModelPlan()
	plan.Domain = "work"
	plan.Intent = "practice"
	plan.AssistanceTarget = "respondent"
	plan.RespondentStage = "restructure"
	plan.AnswerAttempt = answer
	plan.RespondentEvidence = []modelSlotEvidence{{Slot: slot, Span: evidence}}
	plan.LatentQuestion = "本人が質問へ直接答える"
	plan.ArgumentStructure = "direct_answer"
	plan.InterventionPolicy = "coach"
	plan.SpokenReply = plannerDraft
	plan.Intervention = modelArbiter{
		Benefit: 0.9, InterruptionCost: 0.05, Urgency: 0.1,
		Confidence: 0.98, Act: "restructure",
	}
	plan.AnswerContract = respondentDraftContract(
		operator,
		subject,
		[]answercontract.RequiredSlot{slot},
		evidence,
		plannerDraft,
	)
	return plan
}

func coachCriticContract(
	operator answercontract.Operator,
	slot answercontract.RequiredSlot,
	answer string,
	commitment string,
	position answercontract.PositionClass,
) answercontract.Contract {
	issue := answercontract.IssueNone
	if position == answercontract.PositionLater {
		issue = answercontract.IssueBackgroundFirst
	}
	return answercontract.Contract{
		QuestionFrame: answercontract.QuestionFrame{
			Operator:      operator,
			Subject:       "current respondent question",
			RequiredSlots: []answercontract.RequiredSlot{slot},
			Hypotheses: []answercontract.Hypothesis{{
				Interpretation: "evaluate the person's current answer",
				Confidence:     1,
			}},
		},
		CommitmentFront: answercontract.CommitmentFront{
			FirstCommitment: commitment,
			FillsTarget:     true,
			TargetCoverage:  1,
			FilledSlots:     []answercontract.RequiredSlot{slot},
			PositionClass:   position,
			Calibration:     answercontract.CalibrationCommitted,
			Issue:           issue,
		},
		CounterfactualRepair: answercontract.CounterfactualRepair{
			MinimalAnswer:                 commitment,
			ReconstructedAnswer:           answer,
			MeaningPreservationConfidence: 1,
			RepairGain:                    0,
		},
	}
}

func coachState(
	operator answercontract.Operator,
	phase respondent.CoachPhase,
	attempts uint8,
) conversationState {
	target, _ := answercontract.TargetSlot(operator)
	return conversationState{
		Turn: 1,
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
			Active:        true,
			Operator:      operator,
			Subject:       pendingSubjectForOperator(operator),
			RequiredSlots: []answercontract.RequiredSlot{target},
			ExpansionOperator: answercontract.Operator(respondent.ExpansionOperator(
				respondent.Operator(operator),
			)),
			Phase:    phase,
			Attempts: attempts,
		},
		LastIntervention: ArbiterDecision{Act: "clarify"},
	}
}

func openCoachState(
	t *testing.T,
	agent *vertexAgent,
	uid string,
	token string,
) conversationState {
	t.Helper()
	state, err := agent.codec.open(uid, token)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	return state
}

func assertCoachMetadata(
	t *testing.T,
	result VoiceTurnResult,
	phase string,
	action string,
) {
	t.Helper()
	if result.AssistanceTarget != "respondent" ||
		result.CoachPhase != phase ||
		result.CoachAction != action {
		t.Fatalf("coach metadata=(%q,%q,%q), want respondent/%s/%s: %#v",
			result.AssistanceTarget,
			result.CoachPhase,
			result.CoachAction,
			phase,
			action,
			result,
		)
	}
}
