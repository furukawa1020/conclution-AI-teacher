package conversation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProxyAnswerGrammarKeepsOnlyCurrentSpeakerFinalIntent(t *testing.T) {
	for _, utterance := range []string{
		"私の代わりに「評価基準をそろえるためです」と答えて",
		"面接の回答を作って",
		"この質問の回答を作って",
		"私の面接の回答を作って",
		"さっきの答えをそのまま読んで",
		"時間がないから代わりに答えて",
		"今日は代わりに答えて",
		"もう少し短い回答を作って",
		"もっと自然な回答を作って",
		"もっと丁寧な回答を作って",
		"とても自然な回答を作って",
		"はっきりした回答を作って",
		"がんばって考えた回答をそのまま読み上げて",
		"長さは短く回答を作って",
		"AIが丁寧な回答を作って",
		"母向けに回答を作って",
		"母のために回答を作って",
		"この丁寧な回答を作って",
		"面接の短い回答を作って",
		"母向けの丁寧な回答を作って",
		"母のための丁寧な回答を作って",
		"友達用の短い回答を作って",
		"母への丁寧な返事を作って",
		"友達宛ての自然な返事を作って",
		"代わりに、答えて",
		"代わりに 答えて",
		"回答を、作って",
		"回答を 作ってください",
		"この答えを、そのまま読み上げて",
		"回答を作らないで。でも、代わりに答えて",
	} {
		t.Run("owned_"+utterance, func(t *testing.T) {
			if !explicitCoachOptIn(utterance) || !explicitProxyAnswerOptIn(utterance) {
				t.Fatalf("current speaker's final proxy intent was not intercepted: %q", utterance)
			}
		})
	}

	for _, utterance := range []string{
		"母の質問への回答を作って",
		"友達が聞かれた質問への回答を作って",
		"母の質問への回答をそのまま読み上げて",
		"母の回答を作って",
		"友達が代わりに答えて",
		"田中が丁寧な回答を作って",
		"担当者が短い回答を作って",
		"利用者が自然な回答を作って",
		"田中も丁寧な回答を作って",
		"担当者も短い回答を作って",
		"母の丁寧な回答を作って",
		"田中の短い回答を作って",
		"担当者の自然な回答を作って",
		"母からの丁寧な回答を作って",
		"田中からの短い回答を作って",
		"回答を読んで",
		"代わりに答えてください。でも今はやめて",
		"回答を作って。いや、作らないで",
		"この回答を読み上げて。やっぱりやめて",
		"『代わりに答えて』と母が言いました",
		"代わりに答えないで",
		"問題の答えを教えて",
	} {
		t.Run("not_owned_"+utterance, func(t *testing.T) {
			if explicitCoachOptIn(utterance) || explicitProxyAnswerOptIn(utterance) {
				t.Fatalf("unowned, ambiguous, or retracted proxy intent gained authority: %q", utterance)
			}
		})
	}
}

func TestAssistantDativeCompoundsCannotEscapeOwnedAnswerScope(t *testing.T) {
	for _, utterance := range []string{
		"AIについて説明して",
		"AIに関する考えを説明してください",
		"AIにおける役割を説明して",
		"AIへの期待を説明してください",
		"KOTAEについて説明して",
		"あなたへの期待を説明してください",
	} {
		if explicitDirectQuestionOutsideCoach(utterance) {
			t.Fatalf("dative compound escaped the person's active A slot: %q", utterance)
		}
	}
}

func TestUrgentProxySafetyScrubsModelAnswerFromOutputAndState(t *testing.T) {
	const (
		uid        = "uid-proxy-safety-state-isolation"
		ghostVoice = "AIが本人の代わりに完成させた回答です。"
		ghostState = "本人のAとして次のターンにも保存する偽の主張"
	)
	plan := validModelPlan()
	plan.InterventionPolicy = "safety"
	plan.SpokenReply = ghostVoice
	plan.AnswerContract = validCriticContract(ghostVoice)
	plan.ThoughtStateDelta.Claims = []string{ghostState}
	plan.ThoughtStateDelta.Grounds = []string{ghostVoice}
	plan.ConversationSummary = ghostState
	plan.Intervention = modelArbiter{
		Benefit: 0, InterruptionCost: 1, Urgency: 0.95,
		Confidence: 1, Act: "reflect",
	}
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, validCriticContract(urgentSafetyFallbackSpokenReply))},
	}}
	agent := newTestAgent(t, fake)
	agent.retrievalPolicyEnabled = false

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "代わりに答えて",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.SpokenReply != urgentSafetyFallbackSpokenReply ||
		result.AnswerProof != AnswerProofNone ||
		result.AnswerProofCandidate != AnswerProofNone {
		t.Fatalf("urgent proxy safety did not use the trusted non-answer actuator: %#v", result)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	state, err := agent.codec.open(uid, result.StateToken)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	encodedState, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, ghost := range []string{ghostVoice, ghostState} {
		if strings.Contains(string(encodedResult), ghost) || strings.Contains(string(encodedState), ghost) {
			t.Fatalf("model-authored proxy A survived safety isolation: result=%s state=%s", encodedResult, encodedState)
		}
	}
}
