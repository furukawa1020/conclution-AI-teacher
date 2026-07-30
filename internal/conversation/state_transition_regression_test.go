package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentUngroundedRespondentAwaitingCannotTrapOrdinaryConversation(
	t *testing.T,
) {
	initialAnswer := directAssistantRegressionPlan("日本の首都は東京です。")
	misclassified := respondentAwaitingPlan()
	misclassified.ThoughtStateDelta.Claims = []string{
		"通常会話を他者への回答練習だと誤認した",
	}
	recovered := directAssistantRegressionPlan(
		"疲れているなら、まとまっていなくてもそのまま話して大丈夫です。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, initialAnswer)},
		{body: encodeContract(t, validCriticContract(initialAnswer.SpokenReply))},
		{body: encodePlan(t, misclassified)},
		// A corrective inference is optional. Implementations that recover with
		// one may consume this generation; a deterministic safe clarification
		// may stop before it.
		{body: encodePlan(t, recovered)},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(
		context.Background(),
		"uid-ordinary-transition",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "日本の首都はどこですか？",
		},
	)
	if err != nil {
		t.Fatalf("initial assistant turn: %v", err)
	}
	if first.Route != "fast" ||
		first.AssistanceTarget != "assistant" ||
		first.RespondentStage != "none" {
		t.Fatalf("initial direct answer was not an assistant turn: %#v", first)
	}

	secondUtterance := "今日は少し疲れたな。なんとなく話したい"
	second, err := agent.Process(
		context.Background(),
		"uid-ordinary-transition",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     secondUtterance,
			StateToken:    first.StateToken,
		},
	)
	if err != nil {
		t.Fatalf("ordinary continuation: %v", err)
	}
	if second.AssistanceTarget == "respondent" ||
		second.RespondentStage != "none" ||
		strings.HasPrefix(second.Route, "respondent-awaiting-") ||
		second.SpokenReply == respondentAwaitingSpokenReply {
		t.Fatalf("ordinary continuation entered respondent loop: %#v", second)
	}

	nextState, err := agent.codec.open(
		"uid-ordinary-transition",
		second.StateToken,
	)
	if err != nil {
		t.Fatalf("open continuation state: %v", err)
	}
	if nextState.PendingAnswer.Active ||
		len(nextState.PendingAnswer.RequiredSlots) != 0 {
		t.Fatalf("ungrounded respondent frame persisted: %#v", nextState.PendingAnswer)
	}
	encodedState, err := jsonMarshalRegressionState(nextState)
	if err != nil {
		t.Fatalf("marshal continuation state: %v", err)
	}
	if bytes.Contains(
		encodedState,
		[]byte("通常会話を他者への回答練習だと誤認した"),
	) {
		t.Fatalf("misclassified semantic delta entered state: %s", encodedState)
	}
}

func TestAgentPendingRespondentFrameUsesCorrectivePlannerForFreeConversation(
	t *testing.T,
) {
	awaiting := respondentAwaitingPlan()
	misclassified := respondentAwaitingPlan()
	misclassified.ThoughtStateDelta.OpenLoops = []string{
		"自由会話を保留質問への回答待ちだと誤認した",
	}
	corrected := directAssistantRegressionPlan(
		"疲れたという話ですね。まとまっていなくても、そのまま聞かせてください。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, awaiting)},
		{body: encodePlan(t, misclassified)},
		{body: encodePlan(t, corrected)},
		// The independent critic for the corrected assistant reply is generated
		// by fakeGenerator's safe default.
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(
		context.Background(),
		"uid-pending-free-conversation",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "上司に導入目的を聞かれたけど、答えがまとまりません",
		},
	)
	if err != nil {
		t.Fatalf("create legitimate pending frame: %v", err)
	}
	pending, err := agent.codec.open(
		"uid-pending-free-conversation",
		first.StateToken,
	)
	if err != nil {
		t.Fatalf("open pending state: %v", err)
	}
	if !pending.PendingAnswer.Active {
		t.Fatal("test setup did not create a pending respondent frame")
	}

	freeUtterance := "今日は少し疲れたな。答えの練習ではなく、ただ雑談したい"
	second, err := agent.Process(
		context.Background(),
		"uid-pending-free-conversation",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     freeUtterance,
			StateToken:    first.StateToken,
		},
	)
	if err != nil {
		t.Fatalf("free-conversation correction: %v", err)
	}
	if second.AssistanceTarget != "assistant" ||
		second.RespondentStage != "none" ||
		second.SpokenReply != corrected.SpokenReply ||
		strings.HasPrefix(second.Route, "respondent-awaiting-") {
		t.Fatalf("corrective assistant plan was not selected: %#v", second)
	}
	if len(fake.calls) != 4 ||
		strings.Contains(fake.calls[2].prompt, "<lac_critic_data>") ||
		!strings.Contains(fake.calls[3].prompt, "<lac_critic_data>") {
		t.Fatalf("expected first planner, corrective planner, then critic: %#v", fake.calls)
	}

	nextState, err := agent.codec.open(
		"uid-pending-free-conversation",
		second.StateToken,
	)
	if err != nil {
		t.Fatalf("open corrected state: %v", err)
	}
	if nextState.PendingAnswer.Active ||
		len(nextState.PendingAnswer.RequiredSlots) != 0 {
		t.Fatalf("corrective assistant did not clear pending frame: %#v", nextState.PendingAnswer)
	}
	encodedState, err := jsonMarshalRegressionState(nextState)
	if err != nil {
		t.Fatalf("marshal corrected state: %v", err)
	}
	if bytes.Contains(
		encodedState,
		[]byte("自由会話を保留質問への回答待ちだと誤認した"),
	) {
		t.Fatalf("misclassified first-plan delta entered state: %s", encodedState)
	}
}

func TestAgentStandaloneGreetingClearsPendingRespondentFrameLocally(
	t *testing.T,
) {
	awaiting := respondentAwaitingPlan()
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, awaiting)},
		// The regression implementation must not consume this generation.
		{body: encodePlan(t, awaiting)},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(
		context.Background(),
		"uid-greeting-reset",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "上司に導入目的を聞かれたけど、答えがまとまりません",
		},
	)
	if err != nil {
		t.Fatalf("create pending respondent frame: %v", err)
	}
	pending, err := agent.codec.open("uid-greeting-reset", first.StateToken)
	if err != nil {
		t.Fatalf("open pending state: %v", err)
	}
	if !pending.PendingAnswer.Active {
		t.Fatal("test setup did not create a pending respondent frame")
	}

	second, err := agent.Process(
		context.Background(),
		"uid-greeting-reset",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "こんにちはー",
			StateToken:    first.StateToken,
		},
	)
	if err != nil {
		t.Fatalf("standalone greeting reset: %v", err)
	}
	if second.Route != "phatic-local" ||
		second.AssistanceTarget != "assistant" ||
		second.RespondentStage != "none" ||
		len(fake.calls) != 1 {
		t.Fatalf("pending frame captured standalone greeting: %#v", second)
	}
	nextState, err := agent.codec.open("uid-greeting-reset", second.StateToken)
	if err != nil {
		t.Fatalf("open greeting-reset state: %v", err)
	}
	if nextState.PendingAnswer.Active ||
		len(nextState.PendingAnswer.RequiredSlots) != 0 {
		t.Fatalf("greeting did not clear pending frame: %#v", nextState.PendingAnswer)
	}
}

func TestAgentDirectAssistantQuestionClearsPendingRespondentFrame(
	t *testing.T,
) {
	awaiting := respondentAwaitingPlan()
	directAnswer := directAssistantRegressionPlan("日本の首都は東京です。")
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, awaiting)},
		{body: encodePlan(t, directAnswer)},
		{body: encodeContract(t, validCriticContract(directAnswer.SpokenReply))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(
		context.Background(),
		"uid-direct-question-reset",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "面接で目的を聞かれたけど、答えがまとまりません",
		},
	)
	if err != nil {
		t.Fatalf("create pending respondent frame: %v", err)
	}
	second, err := agent.Process(
		context.Background(),
		"uid-direct-question-reset",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "話を変えます。日本の首都はどこですか？",
			StateToken:    first.StateToken,
		},
	)
	if err != nil {
		t.Fatalf("direct assistant question: %v", err)
	}
	if second.Route != "fast" ||
		second.AssistanceTarget != "assistant" ||
		second.RespondentStage != "none" ||
		second.SpokenReply != directAnswer.SpokenReply {
		t.Fatalf("direct question remained trapped in respondent mode: %#v", second)
	}
	nextState, err := agent.codec.open(
		"uid-direct-question-reset",
		second.StateToken,
	)
	if err != nil {
		t.Fatalf("open direct-question state: %v", err)
	}
	if nextState.PendingAnswer.Active ||
		len(nextState.PendingAnswer.RequiredSlots) != 0 {
		t.Fatalf("direct question did not clear pending frame: %#v", nextState.PendingAnswer)
	}
}

func directAssistantRegressionPlan(reply string) modelPlan {
	plan := validModelPlan()
	plan.Domain = "daily"
	plan.Intent = "answer"
	plan.ArgumentStructure = "direct_answer"
	plan.SpokenReply = reply
	plan.AnswerContract = validCriticContract(reply)
	return plan
}

func jsonMarshalRegressionState(state conversationState) ([]byte, error) {
	// Keep the regression file's imports and diagnostics self-contained without
	// changing production visibility or adding transcript-bearing logging.
	return json.Marshal(state)
}
