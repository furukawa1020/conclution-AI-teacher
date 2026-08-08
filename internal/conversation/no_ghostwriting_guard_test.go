package conversation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
	} {
		if shouldRecoverOutsideCoach(utterance) {
			t.Fatalf("answer prose %q was treated as a scope exit", utterance)
		}
	}
	for _, utterance := range []string{
		"話題を変えてください",
		"KOTAEに、仕組みを説明してください",
		"KOTAE、今日は何に困っていますか？",
		"今回はパスします",
	} {
		if !shouldRecoverOutsideCoach(utterance) {
			t.Fatalf("explicit scope exit %q was not recognized", utterance)
		}
	}
}
