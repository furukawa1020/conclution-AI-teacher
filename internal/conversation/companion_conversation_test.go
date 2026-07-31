package conversation

import (
	"context"
	"strings"
	"testing"
)

func lowConfidenceCompanionPlan() modelPlan {
	plan := validModelPlan()
	plan.Domain = "daily"
	plan.Intent = "other"
	plan.LatentQuestion = ""
	plan.ArgumentStructure = "clarifying_question"
	plan.InterventionPolicy = "clarify"
	plan.SpokenReply = "今の話を続けますか？"
	plan.Confidence = 0.5
	plan.Intervention.Act = "clarify"
	return plan
}

func TestConversationPromptMakesShortSpeechValidAndLetsAICarryMoreLoad(
	t *testing.T,
) {
	for _, required := range []string{
		"一語、短い相づち",
		"同じ発話の言い直しを求めない",
		"何について話すかを聞き返さず",
		"質問だけで返して尋問にしない",
		"AI側から足す",
		"companionでは内容への応答と話題一つ",
		"listenでは利用者の直接質問へAから答え",
		"二文から四文",
		"AI自身の実体験",
	} {
		if !strings.Contains(systemInstruction, required) {
			t.Fatalf("conversation support contract missing %q", required)
		}
	}
	for _, required := range []string{
		"一語の話題",
		"有効な会話の手掛かり",
		"機械的にRejectしない",
	} {
		if !strings.Contains(lacCriticSystemInstruction, required) {
			t.Fatalf("critic support contract missing %q", required)
		}
	}
}

func TestRepeatedLowConfidenceShortReplyDoesNotRepeatClarificationDemand(
	t *testing.T,
) {
	plan := lowConfidenceCompanionPlan()
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodePlan(t, plan)},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(
		context.Background(),
		"uid-short-reply-cooldown",
		VoiceTurn{SchemaVersion: SchemaVersion, Utterance: "ゲーム"},
	)
	if err != nil {
		t.Fatalf("first Process: %v", err)
	}
	if first.Route != "interpretation-clarify-fast" ||
		!first.NeedsClarification ||
		strings.Count(first.SpokenReply, "？") != 1 {
		t.Fatalf("first ambiguity did not use one easy choice: %#v", first)
	}

	second, err := agent.Process(
		context.Background(),
		"uid-short-reply-cooldown",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "うーん",
			StateToken:    first.StateToken,
		},
	)
	if err != nil {
		t.Fatalf("second Process: %v", err)
	}
	if second.Route != "interpretation-listen-fast" ||
		second.NeedsClarification ||
		second.Intervention.Act != "reflect" ||
		strings.ContainsAny(second.SpokenReply, "?？") ||
		second.SpokenReply != interpretationListenSpokenReply {
		t.Fatalf("short reply was asked to clarify again: %#v", second)
	}
}

func TestCompanionModeLowConfidenceReplyOffersContentWithoutQuestion(
	t *testing.T,
) {
	plan := lowConfidenceCompanionPlan()
	fake := &fakeGenerator{generations: []fakeGeneration{{body: encodePlan(t, plan)}}}
	agent := newTestAgent(t, fake)

	optOut, err := agent.Process(
		context.Background(),
		"uid-companion-no-question",
		VoiceTurn{SchemaVersion: SchemaVersion, Utterance: "ただ話したい"},
	)
	if err != nil {
		t.Fatalf("opt-out Process: %v", err)
	}
	result, err := agent.Process(
		context.Background(),
		"uid-companion-no-question",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "まあ",
			StateToken:    optOut.StateToken,
		},
	)
	if err != nil {
		t.Fatalf("companion Process: %v", err)
	}
	if result.Route != "interpretation-listen-fast" ||
		result.NeedsClarification ||
		strings.ContainsAny(result.SpokenReply, "?？") ||
		result.SpokenReply == "" {
		t.Fatalf("companion fallback became an interrogation: %#v", result)
	}
}
