package conversation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestExplicitCoachExpansionOptInIsExactOwnedSpeech(t *testing.T) {
	tests := []struct {
		name      string
		utterance string
		want      bool
	}{
		{name: "exact", utterance: "理由まで一問お願いします", want: true},
		{
			name:      "combined direct request",
			utterance: "答え方を一問だけ手伝ってください。理由まで一問お願いします。上司にはまだ答えていません",
			want:      true,
		},
		{name: "quoted", utterance: "友達が『理由まで一問お願いします』と言いました"},
		{name: "reported after comma", utterance: "理由まで一問お願いします、と友達が言いました"},
		{name: "negated", utterance: "理由まで一問お願いしますとは言っていません"},
		{name: "embedded owner", utterance: "母は理由まで一問お願いします"},
		{name: "near match", utterance: "理由も聞いてください"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := explicitCoachExpansionOptIn(test.utterance); got != test.want {
				t.Fatalf("explicitCoachExpansionOptIn(%q)=%t, want %t", test.utterance, got, test.want)
			}
		})
	}
}

func TestOrdinaryConversationNeverAcquiresExpansionCapability(t *testing.T) {
	plan := validModelPlan()
	plan.SpokenReply = "理由を一つに絞ると、話の芯が見えやすくなります。"
	plan.AnswerContract = validCriticContract(plan.SpokenReply)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, validCriticContract(plan.SpokenReply))},
	}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), "uid-expansion-ordinary", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     coachExpansionOptInPhrase,
	})
	if err != nil {
		t.Fatalf("ordinary expansion phrase: %v", err)
	}
	state := openCoachState(t, agent, "uid-expansion-ordinary", result.StateToken)
	if result.AssistanceTarget != "assistant" ||
		state.PendingAnswer.Active ||
		state.PendingAnswer.ExpansionOptIn {
		t.Fatalf("ordinary chat acquired respondent authority: result=%#v state=%#v", result, state)
	}
}

func TestExplicitExpansionRunsOnceAfterVerifiedFirstAnswer(t *testing.T) {
	const (
		uid        = "uid-expansion-once"
		question   = "上司に、導入目的は何かと聞かれました"
		coreAnswer = "目的は評価基準をそろえることです"
		reason     = "その理由は判断の基準がそろうからです"
	)
	core := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		coreAnswer,
		coreAnswer,
		"モデルが作った回答は使いません。",
	)
	expansion := coachAttemptPlan(
		answercontract.OperatorCause,
		answercontract.SlotCause,
		"導入目的",
		reason,
		reason,
		"モデルが作った理由は使いません。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, respondentAwaitingPlan())},
		{body: encodePlan(t, core)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			coreAnswer,
			coreAnswer,
			answercontract.PositionFirst,
		))},
		{body: encodePlan(t, expansion)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorCause,
			answercontract.SlotCause,
			reason,
			reason,
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance: question + "。答え方を一問だけ手伝ってください。" +
			coachExpansionOptInPhrase,
	})
	if err != nil {
		t.Fatalf("create opted-in coach: %v", err)
	}
	firstState := openCoachState(t, agent, uid, first.StateToken)
	if !firstState.PendingAnswer.Active ||
		!firstState.PendingAnswer.ExpansionOptIn ||
		firstState.PendingAnswer.QuestionContinuityTag == "" {
		t.Fatalf("explicit expansion capability was not question-scoped: %#v", firstState.PendingAnswer)
	}

	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     coreAnswer,
		StateToken:    first.StateToken,
	})
	if err != nil {
		t.Fatalf("verified A-first transition: %v", err)
	}
	assertCoachMetadata(t, second, "expanding", "expand")
	if !strings.Contains(second.SpokenReply, "『その理由は』") {
		t.Fatalf("bounded expansion did not teach its explicit answer form: %#v", second)
	}
	expandedState := openCoachState(t, agent, uid, second.StateToken)
	if !expandedState.PendingAnswer.Active ||
		expandedState.PendingAnswer.Phase != respondent.CoachPhaseExpanding ||
		!expandedState.PendingAnswer.ExpansionOptIn ||
		expandedState.PendingAnswer.Attempts != 0 ||
		expandedState.PendingAnswer.RestatementTag != "" ||
		expandedState.PendingAnswer.ContinuityTag != "" ||
		expandedState.Support == nil ||
		expandedState.Support.VerifiedFirstAnswers != 1 {
		t.Fatalf("transition retained the wrong capability or success state: %#v", expandedState)
	}

	third, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     reason,
		StateToken:    second.StateToken,
	})
	if err != nil {
		t.Fatalf("complete one expansion: %v", err)
	}
	assertCoachMetadata(t, third, "complete", "complete")
	if strings.HasSuffix(third.SpokenReply, "？") {
		t.Fatalf("expansion recursively asked another question: %#v", third)
	}
	completedState := openCoachState(t, agent, uid, third.StateToken)
	if completedState.PendingAnswer.Active ||
		completedState.Support == nil ||
		*completedState.Support != *expandedState.Support {
		t.Fatalf("expansion completion changed adaptive success twice: %#v", completedState)
	}

	stateJSON, err := json.Marshal(expandedState)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{question, coreAnswer, reason} {
		if strings.Contains(string(stateJSON), raw) {
			t.Fatalf("raw coaching content entered encrypted-state plaintext: %s", stateJSON)
		}
	}
	for _, call := range fake.calls {
		for _, forbidden := range []string{
			`"expansion_opt_in"`,
			firstState.PendingAnswer.QuestionContinuityTag,
			firstState.PendingAnswer.ContinuityTag,
		} {
			if forbidden != "" && strings.Contains(call.prompt, forbidden) {
				t.Fatalf("server-only expansion authority reached a model prompt: %s", forbidden)
			}
		}
	}
}

func TestStandaloneExpansionConsentAndFillerAreLocal(t *testing.T) {
	const uid = "uid-expansion-local-controls"
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)
	state := authorizedExpansionTestState(agent)
	state.PendingAnswer.Phase = respondent.CoachPhaseAwaitingAnswer
	state.PendingAnswer.ExpansionOptIn = false
	token, err := agent.codec.seal(uid, state)
	if err != nil {
		t.Fatal(err)
	}

	consented, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     coachExpansionOptInPhrase,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("standalone consent: %v", err)
	}
	consentedState := openCoachState(t, agent, uid, consented.StateToken)
	if consented.Route != "coach-expansion-opt-in-local" ||
		len(fake.calls) != 0 ||
		!consentedState.PendingAnswer.ExpansionOptIn ||
		consentedState.PendingAnswer.Attempts != state.PendingAnswer.Attempts {
		t.Fatalf("standalone consent consumed an attempt or model call: %#v %#v", consented, consentedState)
	}

	consentedState.PendingAnswer.Phase = respondent.CoachPhaseExpanding
	consentedState.PendingAnswer.ContinuityTag = ""
	expansionToken, err := agent.codec.seal(uid, consentedState)
	if err != nil {
		t.Fatal(err)
	}
	held, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "えっと、うーん",
		StateToken:    expansionToken,
	})
	if err != nil {
		t.Fatalf("expansion filler hold: %v", err)
	}
	heldState := openCoachState(t, agent, uid, held.StateToken)
	if held.Route != "coach-expansion-hold-local" ||
		held.SpokenReply != "" ||
		len(fake.calls) != 0 ||
		!heldState.PendingAnswer.Active ||
		heldState.PendingAnswer.Phase != respondent.CoachPhaseExpanding ||
		heldState.PendingAnswer.Attempts != consentedState.PendingAnswer.Attempts {
		t.Fatalf("filler repeated or consumed the one expansion: %#v %#v", held, heldState)
	}
}

func TestExpansionProviderFailurePreservesAuthorizedPhase(t *testing.T) {
	const (
		uid    = "uid-expansion-provider-failure"
		reason = "その理由は判断の基準がそろうからです"
	)
	plan := coachAttemptPlan(
		answercontract.OperatorCause,
		answercontract.SlotCause,
		"導入目的",
		reason,
		reason,
		"未検証の理由は読み上げません。",
	)
	generator := &transientCriticGenerator{
		plannerBody:    encodePlan(t, plan),
		criticFailures: 2,
	}
	agent := newTestAgent(t, generator)
	state := authorizedExpansionTestState(agent)
	token, err := agent.codec.seal(uid, state)
	if err != nil {
		t.Fatal(err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     reason,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("provider failure: %v", err)
	}
	after := openCoachState(t, agent, uid, result.StateToken)
	if result.Route != "verification-unavailable" ||
		!after.PendingAnswer.Active ||
		after.PendingAnswer.Phase != respondent.CoachPhaseExpanding ||
		!after.PendingAnswer.ExpansionOptIn ||
		after.PendingAnswer.Attempts != state.PendingAnswer.Attempts ||
		after.PendingAnswer.QuestionContinuityTag != state.PendingAnswer.QuestionContinuityTag {
		t.Fatalf("provider failure consumed or replaced expansion authority: %#v %#v", result, after)
	}
}

func authorizedExpansionTestState(agent *vertexAgent) conversationState {
	state := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseExpanding,
		0,
	)
	state.PendingAnswer.ExpansionOptIn = true
	state.PendingAnswer.QuestionContinuityTag =
		agent.coachQuestionContinuityTag("導入目的")
	state.PendingAnswer.ContinuityTag = ""
	state.Support = &conversationSupport{
		VerifiedFirstAnswers: 1,
		QuestionCooldown:     questionCooldownAfterAnswer,
	}
	return state
}
