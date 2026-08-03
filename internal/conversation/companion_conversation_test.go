package conversation

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestConversationPromptMakesShortSpeechValidAndPrioritizesNextUserWords(
	t *testing.T,
) {
	for _, required := range []string{
		"一語、短い相づち",
		"同じ発話の言い直しを求めない",
		"何について話すかを聞き返さず",
		"質問だけで返して尋問にしない",
		"本人が次に話せる余白を優先",
		"短い相づち、ぼやき、考え途中には内容を短く受け取って本人へ話す番を返す",
		"companionでは内容への応答と話題一つ",
		"listenでは利用者の直接質問へAから答え",
		"原則一文から二文",
		"25〜70文字程度",
		"答えやすい任意の一問まで",
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

func TestLocalCompanionRepliesStayWithinAirtimeBudget(t *testing.T) {
	replies := map[string]string{
		"greeting":                 phaticLocalSpokenReply,
		"listen only":              listenOnlyLocalSpokenReply,
		"interpretation choice":    interpretationClarificationSpokenReply,
		"interpretation listening": interpretationListenSpokenReply,
		"planner unavailable":      plannerUnavailableSpokenReply,
		"verification unavailable": verificationUnavailableSpokenReply,
	}
	for turn := 0; turn < 4; turn++ {
		replies["proactive topic "+string(rune('0'+turn))] =
			proactiveTopicReply(turn)
	}
	for name, reply := range replies {
		t.Run(name, func(t *testing.T) {
			length := utf8.RuneCountInString(reply)
			if length < 25 || length > 70 {
				t.Fatalf("reply length = %d, want 25..70: %q", length, reply)
			}
			sentenceEnds := strings.Count(reply, "。") +
				strings.Count(reply, "？") + strings.Count(reply, "?")
			if sentenceEnds < 1 || sentenceEnds > 2 {
				t.Fatalf("sentence endings = %d, want 1..2: %q", sentenceEnds, reply)
			}
			questions := strings.Count(reply, "？") + strings.Count(reply, "?")
			if questions > 1 {
				t.Fatalf("questions = %d, want at most 1: %q", questions, reply)
			}
		})
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
