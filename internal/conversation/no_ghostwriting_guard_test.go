package conversation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestExplicitAnswerHelpCannotBeActuatedAsAssistantGhostwriting(t *testing.T) {
	const (
		uid         = "uid-explicit-no-ghostwriting"
		ghostAnswer = "AIが本人の代わりに作った代理回答です。"
		utterance   = "上司に、導入目的は何かと聞かれました。" +
			"私の答えは、評価基準をそろえることです。" +
			"答え方を手伝ってください。"
	)
	if !explicitCoachOptIn(utterance) ||
		!explicitReportedQuestionAndOwnAttempt(utterance) {
		t.Fatal("fixture must contain explicit consent and the user's own A")
	}
	hostile := validModelPlan()
	hostile.SpokenReply = ghostAnswer
	hostile.AnswerContract.CounterfactualRepair.MinimalAnswer = ghostAnswer
	hostile.AnswerContract.CounterfactualRepair.ReconstructedAnswer = ghostAnswer
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, hostile)},
	}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     utterance,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.SpokenReply == ghostAnswer ||
		strings.Contains(result.SpokenReply, ghostAnswer) ||
		result.AnswerProof == AnswerProofQuestionBoundInputAnswerFirst ||
		result.AnswerProofCandidate == AnswerProofQuestionBoundInputAnswerFirst {
		t.Fatalf("model-authored answer reached actuator: %#v", result)
	}
	if result.Route != "planner-unavailable" || len(fake.calls) != 1 {
		t.Fatalf("hostile classification did not fail closed: result=%#v calls=%#v", result, fake.calls)
	}
	state, err := agent.codec.open(uid, result.StateToken)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if strings.Contains(string(encoded), ghostAnswer) {
		t.Fatalf("ghost answer entered state: %s", encoded)
	}
}

func TestProxyAnswerRequestsUseOwnedRespondentConsentBoundary(t *testing.T) {
	for _, utterance := range []string{
		"代わりに答えて",
		"私の代わりに答えてください",
		"回答を作って",
		"この答えをそのまま読んで",
		"その回答を読み上げてください",
		"AIが代わりに答えて",
		"あなたが代わりに答えてください",
		"KOTAEが回答を作って",
		"母はこう言いました。代わりに答えて",
	} {
		if !explicitCoachOptIn(utterance) || !ExplicitCoachOptIn(utterance) {
			t.Errorf("current-speaker proxy request was not intercepted: %q", utterance)
		}
	}

	for _, utterance := range []string{
		"問題の答えを教えて",
		"母の代わりに答えて",
		"母が代わりに答えて",
		"彼が代わりに答えて",
		"母はAIが代わりに答えて",
		"友達が「代わりに答えて」と言っていた",
		"「回答を作って」",
		"代わりに答えないで",
		"回答を作らないで",
		"この答えをそのまま読まないで",
		"母はこう言いました。答え方を手伝ってほしい",
	} {
		if explicitCoachOptIn(utterance) || ExplicitCoachOptIn(utterance) {
			t.Errorf("unowned, quoted, negated, or informational request gained respondent authority: %q", utterance)
		}
	}
}

func TestHostilePlannerCannotFulfillProxyAnswerRequest(t *testing.T) {
	for _, utterance := range []string{
		"代わりに答えて",
		"回答を作って",
		"この答えをそのまま読んで",
		"AIが代わりに答えて",
	} {
		t.Run(utterance, func(t *testing.T) {
			const ghostAnswer = "AIが本人の回答欄を奪って作った代理回答です。"
			hostile := validModelPlan()
			hostile.SpokenReply = ghostAnswer
			hostile.AnswerContract.CounterfactualRepair.MinimalAnswer = ghostAnswer
			hostile.AnswerContract.CounterfactualRepair.ReconstructedAnswer = ghostAnswer
			fake := &fakeGenerator{generations: []fakeGeneration{{
				body: encodePlan(t, hostile),
			}}}
			agent := newTestAgent(t, fake)
			// Exercise the inference guard as well as the normal local opening
			// path, so an older deployment configuration cannot speak the draft.
			agent.retrievalPolicyEnabled = false

			result, err := agent.Process(context.Background(), "uid-proxy-hostile-"+utterance, VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     utterance,
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if result.Route != "planner-unavailable" || len(fake.calls) != 1 ||
				strings.Contains(result.SpokenReply, ghostAnswer) ||
				result.AnswerProof != AnswerProofNone ||
				result.AnswerProofCandidate != AnswerProofNone {
				t.Fatalf("proxy request reached hostile assistant output: result=%#v calls=%#v", result, fake.calls)
			}
			state, openErr := agent.codec.open("uid-proxy-hostile-"+utterance, result.StateToken)
			if openErr != nil {
				t.Fatalf("open state: %v", openErr)
			}
			encoded, marshalErr := json.Marshal(state)
			if marshalErr != nil || strings.Contains(string(encoded), ghostAnswer) {
				t.Fatalf("ghost answer entered state: encoded=%s err=%v", encoded, marshalErr)
			}
		})
	}
}

func TestProblemAnswerQuestionRemainsOrdinaryAssistantRequest(t *testing.T) {
	const (
		uid       = "uid-problem-answer-ordinary-assistant"
		utterance = "問題の答えを教えて"
		reply     = "この問題だけでは条件が足りないので、問題文を教えてください。"
	)
	if explicitCoachOptIn(utterance) {
		t.Fatal("an informational answer question became respondent consent")
	}
	plan := validModelPlan()
	plan.SpokenReply = reply
	plan.AnswerContract = validCriticContract(reply)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, validCriticContract(reply))},
	}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     utterance,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.AssistanceTarget != "assistant" ||
		result.RespondentStage != "none" ||
		result.CoachPhase != "none" ||
		result.SpokenReply != reply || len(fake.calls) != 2 {
		t.Fatalf("ordinary assistant request was intercepted: result=%#v calls=%#v", result, fake.calls)
	}
	state, openErr := agent.codec.open(uid, result.StateToken)
	if openErr != nil || state.PendingAnswer.Active {
		t.Fatalf("ordinary assistant request opened an answer slot: state=%#v err=%v", state, openErr)
	}
}

func TestActiveAnswerSlotCannotBeActuatedAsAssistantGhostwriting(t *testing.T) {
	const (
		uid         = "uid-active-no-ghostwriting"
		ghostAnswer = "AIが回答スロットを奪って作った代理回答です。"
		opening     = "上司に、目的と費用と時期をまとめて聞かれました。" +
			"答え方を手伝ってください。"
	)
	hostile := validModelPlan()
	hostile.SpokenReply = ghostAnswer
	hostile.AnswerContract.CounterfactualRepair.MinimalAnswer = ghostAnswer
	hostile.AnswerContract.CounterfactualRepair.ReconstructedAnswer = ghostAnswer
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, hostile)},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     opening,
		RequestID:     "request-active-no-ghostwriting-open",
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil || first.Route != genericCoachLocalRoute || len(fake.calls) != 0 {
		t.Fatalf("open slot: result=%#v calls=%#v err=%v", first, fake.calls, err)
	}
	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "評価基準をそろえることです。",
		StateToken:    first.StateToken,
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("Process answer: %v", err)
	}
	if strings.Contains(second.SpokenReply, ghostAnswer) ||
		second.AnswerProof == AnswerProofQuestionBoundInputAnswerFirst ||
		second.AnswerProofCandidate == AnswerProofQuestionBoundInputAnswerFirst ||
		second.Route != "planner-unavailable" || len(fake.calls) != 1 {
		t.Fatalf("active scope was stolen: result=%#v calls=%#v", second, fake.calls)
	}
	state, err := agent.codec.open(uid, second.StateToken)
	if err != nil {
		t.Fatalf("open preserved state: %v", err)
	}
	if !state.PendingAnswer.Active || state.PendingAnswer.NativeCoachScopeTag == "" {
		t.Fatalf("hostile classification erased answer slot: %#v", state.PendingAnswer)
	}
}

func TestAnswerWordsCannotBeMisreadAsCoachScopeExit(t *testing.T) {
	for _, test := range []struct {
		name      string
		opening   string
		ownAnswer string
	}{
		{
			name: "answer contains assistant-like verb",
			opening: "上司に、導入目的と費用をまとめて聞かれました。" +
				"答え方を手伝ってください。",
			ownAnswer: "利用者に説明して納得してもらうことです",
		},
		{
			name: "answer itself is a question",
			opening: "上司に、顧客へ最初に何と聞くか質問されました。" +
				"答え方を手伝ってください。",
			ownAnswer: "今日は何にお困りですか？",
		},
		{
			name: "dative AI assignment question",
			opening: "上司に、AIへ任せる範囲を聞かれました。" +
				"答え方を手伝ってください。",
			ownAnswer: "AIに何を任せますか？",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			const ghostAnswer = "AIが本人より先に作った回答です。"
			if shouldRecoverOutsideCoach(test.ownAnswer) {
				t.Fatalf("a legitimate question-shaped A %q was treated as a scope exit", test.ownAnswer)
			}
			hostile := validModelPlan()
			hostile.SpokenReply = ghostAnswer
			hostile.AnswerContract.CounterfactualRepair.MinimalAnswer = ghostAnswer
			hostile.AnswerContract.CounterfactualRepair.ReconstructedAnswer = ghostAnswer
			fake := &fakeGenerator{generations: []fakeGeneration{{
				body: encodePlan(t, hostile),
			}}}
			agent := newTestAgent(t, fake)
			uid := "uid-active-answer-no-ghostwriting-" + test.name

			first, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     test.opening,
				RequestID:     "request-answer-no-ghostwriting-open",
				InputOrigin:   InputOriginCommittedVoice,
			})
			if err != nil || first.Route != genericCoachLocalRoute || len(fake.calls) != 0 {
				t.Fatalf("open slot: result=%#v calls=%#v err=%v", first, fake.calls, err)
			}
			second, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     test.ownAnswer,
				StateToken:    first.StateToken,
				InputOrigin:   InputOriginCommittedVoice,
			})
			if err != nil {
				t.Fatalf("Process answer: %v", err)
			}
			if strings.Contains(second.SpokenReply, ghostAnswer) ||
				second.AnswerProof == AnswerProofQuestionBoundInputAnswerFirst ||
				second.AnswerProofCandidate == AnswerProofQuestionBoundInputAnswerFirst ||
				second.Route != "planner-unavailable" || len(fake.calls) != 1 {
				t.Fatalf("answer wording reopened ghostwriting: result=%#v calls=%#v", second, fake.calls)
			}
			state, err := agent.codec.open(uid, second.StateToken)
			if err != nil {
				t.Fatalf("open preserved state: %v", err)
			}
			if !state.PendingAnswer.Active || state.PendingAnswer.NativeCoachScopeTag == "" {
				t.Fatalf("answer wording erased the open A slot: %#v", state.PendingAnswer)
			}
		})
	}
}

func TestCoachScopeExitRequiresExplicitDirectAddress(t *testing.T) {
	for _, utterance := range []string{
		"利用者に教えて理解してもらうことです",
		"相手がどう思うかを確認します",
		"目的は何ですかと聞かれました",
		"今日は何にお困りですか？",
		"AIに何を任せますか？",
		"AIに「仕組みを説明して",
		"AIに「仕組みを説明して」",
	} {
		if shouldRecoverOutsideCoach(utterance) {
			t.Fatalf("answer prose %q was treated as a scope exit", utterance)
		}
	}
	for _, utterance := range []string{
		"話題を変えてください",
		"KOTAEに、仕組みを説明してください",
		"KOTAE、今日は何に困っていますか？",
		"AI、何を任せるべき？",
		"AIに仕組みを教えて",
		"AIに仕組みを説明して",
		"今回はパスします",
	} {
		if !shouldRecoverOutsideCoach(utterance) {
			t.Fatalf("explicit scope exit %q was not recognized", utterance)
		}
	}
}

func TestExplicitAIAddressExitsActiveAnswerScope(t *testing.T) {
	for _, test := range []struct {
		name      string
		utterance string
	}{
		{name: "vocative question", utterance: "AI、何を任せるべき？"},
		{name: "dative teach command", utterance: "AIに仕組みを教えて"},
		{name: "dative explain command", utterance: "AIに仕組みを説明して"},
	} {
		t.Run(test.name, func(t *testing.T) {
			const reply = "役割と確認方法を分けて考えると整理できます。"
			awaiting := respondentAwaitingPlan()
			assistant := validModelPlan()
			assistant.SpokenReply = reply
			assistant.AnswerContract = validCriticContract(reply)
			fake := &fakeGenerator{generations: []fakeGeneration{
				{body: encodePlan(t, awaiting)},
				{body: encodePlan(t, assistant)},
				{body: encodeContract(t, validCriticContract(reply))},
			}}
			agent := newTestAgent(t, fake)
			uid := "uid-explicit-ai-scope-exit-" + test.name
			initial := coachState(
				answercontract.OperatorPurpose,
				respondent.CoachPhaseAwaitingAnswer,
				0,
			)
			token, sealErr := agent.codec.seal(uid, initial)
			if sealErr != nil {
				t.Fatalf("seal initial state: %v", sealErr)
			}

			result, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     test.utterance,
				StateToken:    token,
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if result.AssistanceTarget != "assistant" ||
				result.RespondentStage != "none" ||
				result.CoachPhase != "none" ||
				result.CoachAction != "none" ||
				result.SpokenReply != reply {
				t.Fatalf("explicit AI address remained trapped in answer scope: %#v", result)
			}
			state, openErr := agent.codec.open(uid, result.StateToken)
			if openErr != nil || state.PendingAnswer.Active {
				t.Fatalf("explicit AI address retained answer slot: state=%#v err=%v", state, openErr)
			}
		})
	}
}
