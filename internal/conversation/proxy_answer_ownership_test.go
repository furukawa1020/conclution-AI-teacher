package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProxyAnswerRequestBecomesExplicitCoachAuthority(t *testing.T) {
	for _, utterance := range []string{
		"代わりに答えてください",
		"私の代わりに答えて",
		"私の代わりに「評価基準をそろえます」と答えて",
		"回答を作ってください",
		"面接の回答を作って",
		"この質問の回答を作って",
		"私の面接の回答を作って",
		"さっきの答えをそのまま読んで",
		"この答えをそのまま読み上げてください",
		"AIが代わりに答えて",
		"あなたが代わりに答えて",
		"KOTAEが回答を作って",
		"代わりに、答えて",
		"回答を 作って",
		"この答えを、そのまま読み上げて",
		"時間がないから、代わりに答えて",
		"今日は代わりに答えて",
		"もう少し短い回答を作って",
		"もっと自然な回答を作って",
		"はっきりした回答を作って",
		"がんばって考えた回答をそのまま読み上げて",
		"AIが丁寧な回答を作って",
		"母向けに回答を作って",
		"母のために回答を作って",
		"友達宛てに返事を作って",
	} {
		if !explicitProxyAnswerRequest(utterance) {
			t.Fatalf("explicitProxyAnswerRequest(%q) rejected an owned proxy request", utterance)
		}
		if !explicitCoachOptIn(utterance) {
			t.Fatalf("explicitCoachOptIn(%q) did not redirect proxy speech to coaching", utterance)
		}
	}
	for _, utterance := range []string{
		"上司に目的を聞かれました。代わりに答えてください",
		"母はこう言いました。代わりに答えて",
		"回答を作らないで。でも、代わりに答えて",
	} {
		if !explicitCoachOptIn(utterance) || !explicitProxyAnswerOptIn(utterance) {
			t.Fatalf("last owned proxy request was not authoritative: %q", utterance)
		}
	}
}

func TestProxyAnswerRequestDoesNotCaptureInformationOrThirdPartySpeech(t *testing.T) {
	for _, utterance := range []string{
		"問題の答えを教えてください",
		"母の代わりに答えて",
		"母が代わりに答えて",
		"彼が代わりに答えて",
		"母はAIが代わりに答えて",
		"母の回答を作って",
		"母の質問への回答を作って",
		"友達が聞かれた質問への回答を作って",
		"母が代わりに答えてと言いました",
		"代わりに答えないで",
		"回答を作らないで",
		"この答えをそのまま読まないで",
		"この答えを読んで",
		"「代わりに答えて」",
	} {
		if explicitProxyAnswerRequest(utterance) || explicitCoachOptIn(utterance) {
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
		"代わりに答えて。でも今はやめて",
		"回答を作って。いや、作らないで",
		"この答えを読み上げて。やっぱりやめて",
		"母はこう言いました。答え方を手伝ってほしい",
	} {
		if explicitProxyAnswerOptIn(utterance) {
			t.Fatalf("explicitProxyAnswerOptIn(%q) accepted non-authority", utterance)
		}
	}
}

func TestProxyAnswerRequestOpensOwnedSlotWithoutCallingModel(t *testing.T) {
	for index, utterance := range []string{
		"代わりに答えて",
		"回答を作って",
		"この答えをそのまま読んで",
		"私の代わりに「評価基準をそろえます」と答えて",
	} {
		fake := &fakeGenerator{}
		agent := newTestAgent(t, fake)
		uid := "uid-proxy-answer-ownership-" + string(rune('a'+index))
		result, err := agent.Process(context.Background(), uid, VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     utterance,
			RequestID:     "request-proxy-answer-ownership-" + string(rune('a'+index)),
			InputOrigin:   InputOriginCommittedVoice,
		})
		if err != nil {
			t.Fatalf("Process(%q): %v", utterance, err)
		}
		if result.Route != genericCoachLocalRoute ||
			result.AssistanceTarget != "respondent" ||
			result.CoachPhase != "awaiting_answer" ||
			result.CoachAction != "elicit" ||
			result.SpokenReply != genericCoachOpeningCue ||
			result.AnswerProof != AnswerProofNone ||
			result.AnswerProofCandidate != AnswerProofNone {
			t.Fatalf("proxy request did not become an owned answer slot: %#v", result)
		}
		if len(fake.calls) != 0 {
			t.Fatalf("proxy request reached the model before the user supplied A: %#v", fake.calls)
		}
		state, openErr := agent.codec.open(uid, result.StateToken)
		if openErr != nil {
			t.Fatalf("open state: %v", openErr)
		}
		if !state.PendingAnswer.Active || state.PendingAnswer.NativeCoachScopeTag == "" {
			t.Fatalf("proxy request did not create a bounded owned slot: %#v", state.PendingAnswer)
		}
		encoded, marshalErr := json.Marshal(state)
		if marshalErr != nil || bytes.Contains(encoded, []byte(utterance)) ||
			bytes.Contains(encoded, []byte("評価基準をそろえます")) {
			t.Fatalf("proxy prose entered bounded state: encoded=%s err=%v", encoded, marshalErr)
		}
	}
}

func TestProxyAnswerSafetyCannotSpeakModelAuthoredAnswer(t *testing.T) {
	const (
		uid         = "uid-proxy-answer-safety"
		ghostAnswer = "AIが本人の代わりに作った回答です。"
		ghostClaim  = "本人の回答はAIの代理文で確定した"
	)
	plan := validModelPlan()
	plan.InterventionPolicy = "safety"
	plan.SpokenReply = ghostAnswer
	plan.LatentQuestion = ghostAnswer
	plan.ThoughtStateDelta.Claims = []string{ghostClaim}
	plan.AnswerContract = validCriticContract(ghostAnswer)
	plan.Intervention = modelArbiter{
		Benefit: 0, InterruptionCost: 1, Urgency: 0.9,
		Confidence: 1, Act: "reflect",
	}
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		// The critic colludes with the planner's proxy A. Go-side scrubbing must
		// still make both speech and authenticated state independent of it.
		{body: encodeContract(t, validCriticContract(ghostAnswer))},
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
	if result.InterventionPolicy != "safety" ||
		result.SpokenReply != urgentSafetyFallbackSpokenReply ||
		strings.Contains(result.SpokenReply, ghostAnswer) ||
		strings.Contains(result.LatentQuestion, ghostAnswer) ||
		(result.AnswerProof != "" && result.AnswerProof != AnswerProofNone) ||
		(result.AnswerProofCandidate != "" && result.AnswerProofCandidate != AnswerProofNone) ||
		len(fake.calls) != 2 {
		t.Fatalf("proxy safety exception exposed model-authored A: result=%#v calls=%#v", result, fake.calls)
	}
	state, openErr := agent.codec.open(uid, result.StateToken)
	if openErr != nil {
		t.Fatalf("open state: %v", openErr)
	}
	encoded, marshalErr := json.Marshal(state)
	if marshalErr != nil || bytes.Contains(encoded, []byte(ghostAnswer)) ||
		bytes.Contains(encoded, []byte(ghostClaim)) || state.PendingAnswer.Active {
		t.Fatalf("proxy safety draft entered authenticated state: state=%s err=%v", encoded, marshalErr)
	}
}

func TestDativeAIQuestionStaysInsideOwnedAnswerWhileVocativeCanExit(t *testing.T) {
	for _, utterance := range []string{
		"AIに何を任せますか？",
		"AIに、何を任せますか？",
		"あなたに何を期待しますか？",
		"AIについて説明してください",
		"AIに関して説明してください",
		"AIに関する考えを説明して",
		"AIにおける役割を説明して",
		"AIに対する期待を説明してください",
		"AIによる結果を説明してください",
		"AIへの期待を説明してください",
		"KOTAEについて説明して",
		"あなたへの期待を説明してください",
		"AIには何を任せますか？",
		"AIへは何を任せますか？",
		"AIに「仕組みを説明して",
		"AIに「仕組みを説明して」",
	} {
		if explicitDirectQuestionOutsideCoach(utterance) {
			t.Fatalf("answer-shaped dative question %q escaped the owned slot", utterance)
		}
	}
	for _, utterance := range []string{
		"AI、何を任せますか？",
		"AI、何を任せるべき？",
		"AIに仕組みを教えて",
		"AIに仕組みを説明して",
		"AIに何を任せるべきか教えて",
		"KOTAE、今日何をすればいい？",
		"KOTAEに説明してください",
	} {
		if !explicitDirectQuestionOutsideCoach(utterance) {
			t.Fatalf("explicit assistant request %q could not exit the owned slot", utterance)
		}
	}
}
