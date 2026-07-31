package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestAgentNaturalForegroundAnswerCompletesValidatedAssistantFollowUp(t *testing.T) {
	const (
		uid           = "uid-natural-one-shot-coach"
		followUp      = "理由はあとで聞きます。まず、目的は何ですか？"
		answer        = "目的は評価基準をそろえることです"
		plannerDraft  = "サーバ側の構造案内へ置換します。"
		questionTopic = "評価基準の導入目的"
	)
	clarify := validModelPlan()
	clarify.InterventionPolicy = "clarify"
	clarify.Intervention.Act = "clarify"
	clarify.SpokenReply = followUp
	clarify.AnswerContract = validCriticContract(followUp)
	attempt := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		questionTopic,
		answer,
		answer,
		plannerDraft,
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, clarify)},
		{body: encodeContract(t, validCriticContract(followUp))},
		{body: encodePlan(t, attempt)},
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
	armed, err := agent.codec.open(uid, first.StateToken)
	if err != nil {
		t.Fatalf("open armed state: %v", err)
	}
	if !armed.PendingAnswer.Active ||
		!armed.PendingAnswer.AssistantFollowUp ||
		armed.PendingAnswer.Operator != answercontract.OperatorPurpose ||
		armed.PendingAnswer.Subject != assistantFollowUpSubject {
		t.Fatalf("validated follow-up did not arm one-shot frame: %#v", armed.PendingAnswer)
	}
	armedJSON, err := json.Marshal(armed)
	if err != nil {
		t.Fatalf("marshal armed state: %v", err)
	}
	if bytes.Contains(armedJSON, []byte(followUp)) ||
		bytes.Contains(armedJSON, []byte(questionTopic)) {
		t.Fatalf("assistant question prose entered state: %s", armedJSON)
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
	if second.AssistanceTarget != "respondent" ||
		second.RespondentStage != "restructure" ||
		second.CoachPhase != "complete" ||
		second.CoachAction != "complete" ||
		strings.Contains(second.SpokenReply, plannerDraft) ||
		strings.Contains(second.SpokenReply, answer) {
		t.Fatalf("one-shot answer did not complete safely: %#v", second)
	}
	completed, err := agent.codec.open(uid, second.StateToken)
	if err != nil {
		t.Fatalf("open completed state: %v", err)
	}
	if completed.PendingAnswer.Active {
		t.Fatalf("one-shot frame survived completion: %#v", completed.PendingAnswer)
	}
}

func TestAgentExplicitRespondentCoachRunsBoundedAnswerFirstSequence(t *testing.T) {
	const (
		uid          = "uid-explicit-coach-sequence"
		questionText = "上司に、導入目的は何かと聞かれました"
		lateAnswer   = "判断のばらつきを減らすためです。目的は評価基準をそろえることです"
		coreAnswer   = "目的は評価基準をそろえることです。判断のばらつきを減らします"
		reasonAnswer = "理由は判断のばらつきを減らせるからです"
		proxyDraft   = "AIが本人の代わりに作った回答です。"
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
	reason := coachAttemptPlan(
		answercontract.OperatorCause,
		answercontract.SlotCause,
		"導入目的を支える理由",
		reasonAnswer,
		reasonAnswer,
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
		{body: encodePlan(t, reason)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorCause,
			answercontract.SlotCause,
			reasonAnswer,
			reasonAnswer,
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

	third, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     coreAnswer,
		StateToken:    second.StateToken,
	})
	if err != nil {
		t.Fatalf("A first: %v", err)
	}
	assertCoachMetadata(t, third, "expanding", "expand")
	expanding := openCoachState(t, agent, uid, third.StateToken)
	if !expanding.PendingAnswer.Active ||
		expanding.PendingAnswer.AssistantFollowUp ||
		expanding.PendingAnswer.Phase != respondent.CoachPhaseExpanding ||
		expanding.PendingAnswer.Operator != answercontract.OperatorPurpose ||
		expanding.PendingAnswer.ExpansionOperator != answercontract.OperatorCause {
		t.Fatalf("core answer did not enter the one bounded expansion: %#v", expanding.PendingAnswer)
	}

	fourth, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     reasonAnswer,
		StateToken:    third.StateToken,
	})
	if err != nil {
		t.Fatalf("natural follow-up answer: %v", err)
	}
	assertCoachMetadata(t, fourth, "complete", "complete")
	if strings.Contains(fourth.SpokenReply, proxyDraft) ||
		strings.Contains(fourth.SpokenReply, reasonAnswer) {
		t.Fatalf("proxy answer leaked at completion: %q", fourth.SpokenReply)
	}
	completed := openCoachState(t, agent, uid, fourth.StateToken)
	if completed.PendingAnswer.Active {
		t.Fatalf("completed sequence retained frame: %#v", completed.PendingAnswer)
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
				t.Fatalf("boundedFollowUpOperator(%q)=(%q,%v), want (%q,%v)", test.text, got, ok, test.want, test.ok)
			}
		})
	}
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
