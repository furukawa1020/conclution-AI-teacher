package conversation

import (
	"bytes"
	"context"
	"encoding/json"
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
	if bytes.Contains(stateJSON, []byte(followUp)) {
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
	completed, err := agent.codec.open(uid, second.StateToken)
	if err != nil {
		t.Fatalf("open ordinary continuation state: %v", err)
	}
	if completed.PendingAnswer.Active {
		t.Fatalf("ordinary continuation created a coach frame: %#v", completed.PendingAnswer)
	}
}

func TestAgentExplicitRespondentCoachRunsBoundedAnswerFirstSequence(t *testing.T) {
	const (
		uid          = "uid-explicit-coach-sequence"
		questionText = "上司に、導入目的は何かと聞かれました"
		lateAnswer   = "判断のばらつきを減らすためです。目的は評価基準をそろえることです"
		coreAnswer   = "目的は評価基準をそろえることです。判断のばらつきを減らします"
		proxyDraft   = "AIが本人の代わりに作った回答です。"
		naturalReply = "なるほど、そこが大事なんですね。その続きも聞かせてください。"
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
	})
	if err != nil {
		t.Fatalf("A later: %v", err)
	}
	assertCoachMetadata(t, second, "awaiting_restatement", "restate")
	if strings.Contains(second.SpokenReply, proxyDraft) || strings.Contains(second.SpokenReply, lateAnswer) {
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

func TestAgentCompatibilityModeAcceptsButDoesNotIssueRestatementTag(t *testing.T) {
	const (
		uid        = "uid-restatement-compatibility-mode"
		lateAnswer = "背景から話します。目的は評価基準をそろえることです"
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
	state := openCoachState(t, agent, uid, result.StateToken)
	if state.PendingAnswer.RestatementTag != "" {
		t.Fatalf("compatibility revision issued a new tag: %#v", state.PendingAnswer)
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
		Utterance:     "日本の首都はどこですか？",
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
			name:      "wants conversation without correction",
			utterance: "今日は話すだけにしたい",
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
				result.SpokenReply != test.wantReply {
				t.Fatalf("explicit opt-out was not honored locally: %#v", result)
			}
			next := openCoachState(t, agent, uid, result.StateToken)
			if next.PendingAnswer.Active {
				t.Fatalf("explicit opt-out retained coach state: %#v", next.PendingAnswer)
			}
		})
	}
}

func TestCoachOptOutPassPhrasesDoNotMatchOrdinaryWords(t *testing.T) {
	for _, utterance := range []string{
		"パスタを食べました", "今はパスタを食べています",
		"パスポートです", "今回はパスポートの話です", "パスワードを忘れました",
		"今日は話さないといけないことがある", "話したくないわけじゃない",
		"今日は話すだけでは足りない", "直さなくていいわけじゃない",
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
		!result.NeedsClarification {
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
