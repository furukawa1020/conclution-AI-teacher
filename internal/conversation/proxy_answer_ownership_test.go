package conversation

import (
	"context"
	"testing"
)

func TestProxyAnswerRequestBecomesExplicitCoachAuthority(t *testing.T) {
	for _, utterance := range []string{
		"上司に目的を聞かれました。代わりに答えてください",
		"回答を作ってください",
		"この答えをそのまま読み上げてください",
	} {
		if !explicitProxyAnswerRequest(utterance) {
			t.Fatalf("explicitProxyAnswerRequest(%q) rejected an owned proxy request", utterance)
		}
		if !explicitCoachOptIn(utterance) {
			t.Fatalf("explicitCoachOptIn(%q) did not redirect proxy speech to coaching", utterance)
		}
	}
}

func TestProxyAnswerRequestDoesNotCaptureInformationOrThirdPartySpeech(t *testing.T) {
	for _, utterance := range []string{
		"問題の答えを教えてください",
		"母が代わりに答えてと言いました",
		"代わりに答えないで",
		"「代わりに答えて」",
	} {
		if explicitProxyAnswerRequest(utterance) {
			t.Fatalf("explicitProxyAnswerRequest(%q) captured a non-owned request", utterance)
		}
	}
}

func TestProxyAnswerOptInRejectsQuotedAndNegatedDelegation(t *testing.T) {
	for _, utterance := range []string{
		"代わりに答えて",
		"母はこう言いました。代わりに答えて",
	} {
		if !explicitProxyAnswerOptIn(utterance) {
			t.Fatalf("explicitProxyAnswerOptIn(%q) rejected current authority", utterance)
		}
	}
	for _, utterance := range []string{
		"問題の答えを教えてください",
		"友達が「代わりに答えて」と言っていました",
		"代わりに答えないで",
	} {
		if explicitProxyAnswerOptIn(utterance) {
			t.Fatalf("explicitProxyAnswerOptIn(%q) accepted non-authority", utterance)
		}
	}
}

func TestProxyAnswerRequestOpensOwnedSlotWithoutCallingModel(t *testing.T) {
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(context.Background(), "uid-proxy-answer-ownership", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "上司に目的を聞かれました。代わりに答えてください",
		RequestID:     "request-proxy-answer-ownership",
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != genericCoachLocalRoute ||
		result.AssistanceTarget != "respondent" ||
		result.CoachPhase != "awaiting_answer" ||
		result.CoachAction != "elicit" ||
		result.SpokenReply != genericCoachOpeningCue {
		t.Fatalf("proxy request did not become an owned answer slot: %#v", result)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("proxy request reached the model before the user supplied A: %#v", fake.calls)
	}
	state, err := agent.codec.open("uid-proxy-answer-ownership", result.StateToken)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	if !state.PendingAnswer.Active || state.PendingAnswer.NativeCoachScopeTag == "" {
		t.Fatalf("proxy request did not create a bounded owned slot: %#v", state.PendingAnswer)
	}
}

func TestProxyAnswerSafetyCannotSpeakModelAuthoredAnswer(t *testing.T) {
	const ghostAnswer = "AIが本人の代わりに作った回答です。"
	plan := validModelPlan()
	plan.InterventionPolicy = "safety"
	plan.SpokenReply = ghostAnswer
	plan.Intervention = modelArbiter{
		Benefit: 0, InterruptionCost: 1, Urgency: 0.9,
		Confidence: 1, Act: "reflect",
	}
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, validCriticContract(urgentSafetyFallbackSpokenReply))},
	}}
	agent := newTestAgent(t, fake)
	agent.retrievalPolicyEnabled = false

	result, err := agent.Process(context.Background(), "uid-proxy-answer-safety", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "代わりに答えて",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.InterventionPolicy != "safety" ||
		result.SpokenReply != urgentSafetyFallbackSpokenReply ||
		result.SpokenReply == ghostAnswer ||
		len(fake.calls) != 2 {
		t.Fatalf("proxy safety exception exposed model-authored A: result=%#v calls=%#v", result, fake.calls)
	}
}

func TestDativeAIQuestionStaysInsideOwnedAnswerWhileVocativeCanExit(t *testing.T) {
	for _, utterance := range []string{
		"AIに何を任せますか？",
		"あなたに何を期待しますか？",
		"AIについて説明してください",
		"AIに関して説明してください",
		"AIには何を任せますか？",
		"AIへは何を任せますか？",
	} {
		if explicitDirectQuestionOutsideCoach(utterance) {
			t.Fatalf("answer-shaped dative question %q escaped the owned slot", utterance)
		}
	}
	for _, utterance := range []string{
		"AI、何を任せますか？",
		"KOTAE、今日何をすればいい？",
		"KOTAEに説明してください",
	} {
		if !explicitDirectQuestionOutsideCoach(utterance) {
			t.Fatalf("explicit assistant request %q could not exit the owned slot", utterance)
		}
	}
}
