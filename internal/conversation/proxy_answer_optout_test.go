package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestExplicitProxyAnswerOptOutAcceptsOnlyOwnedFinalRefusal(t *testing.T) {
	for _, utterance := range []string{
		"代わりに答えないで",
		"代わりに答えないでください",
		"代わりには答えないで",
		"代わりに答えないでね",
		"代わりに答えなくていい",
		"代わりに答えなくて良い",
		"代わりに答えないで下さい",
		"代わりに答えないでほしい",
		"代わりに答えないで欲しいです",
		"かわりにこたえないで",
		"私の代わりに答えないで",
		"AIが代わりに回答しないで",
		"AIは代わりに答えないで",
		"KOTAEは回答を作らないで",
		"AIは私の回答を作らないで",
		"回答を作らないで",
		"回答は作らないで",
		"回答を作らなくていい",
		"回答を作ってほしくない",
		"回答を作って欲しくない",
		"回答をつくらないで",
		"この回答を作らないでください",
		"この回答を読み上げないで",
		"この回答、読み上げないで",
		"この回答を読みあげないで",
		"さっきの答えをそのまま読まないでください",
		"母向けの回答を作らないで",
		"友達宛ての返事を読み上げないで",
		"代わりに、答えないで",
		"回答を 作らないで",
		"この回答を、そのまま読み上げないで",
		"私の代わりに「評価基準をそろえます」と答えないで",
		"代わりに答えて。でも今はやめて",
		"代わりに答えて、やっぱりやめて",
		"代わりに答えてやっぱりやめて",
		"かわりにこたえて、やっぱりやめて",
		"回答を作って。いや、作らないでください",
		"回答を作って いや作らないで",
		"回答を作っていや作らないで",
		"かいとうをつくって、いやつくらないで",
		"この回答を読み上げて。やっぱり読み上げないで",
		"母はそう言いました。私は代わりに答えないで",
		"母はそう言いました。AIは代わりに答えないで",
		"母はそう言いました。KOTAEは回答を作らないで",
		"don't answer for me",
		"do not write my answer",
		"please don't read my answer aloud",
	} {
		t.Run(utterance, func(t *testing.T) {
			if !ExplicitProxyAnswerOptOut(utterance) {
				t.Fatalf("ExplicitProxyAnswerOptOut(%q) rejected the current speaker's final refusal", utterance)
			}
		})
	}
}

func TestKanaProxyOptInCannotBypassGuardedCoachRoute(t *testing.T) {
	for _, utterance := range []string{
		"かわりにこたえて",
		"かいとうをつくって",
		"へんじを読みあげて",
	} {
		t.Run(utterance, func(t *testing.T) {
			if !explicitProxyAnswerRequest(utterance) ||
				!ExplicitCoachOptIn(utterance) ||
				!explicitProxyAnswerOptIn(utterance) ||
				ExplicitProxyAnswerOptOut(utterance) {
				t.Fatalf("kana proxy request bypassed guarded coach route: %q", utterance)
			}
		})
	}
}

func TestEnglishProxyRefusalIsOptOutNotCoachOptIn(t *testing.T) {
	for _, utterance := range []string{
		"don't answer for me",
		"do not write my answer",
		"please don't read my answer aloud",
	} {
		t.Run(utterance, func(t *testing.T) {
			if ExplicitCoachOptIn(utterance) ||
				explicitProxyAnswerOptIn(utterance) ||
				!ExplicitProxyAnswerOptOut(utterance) {
				t.Fatalf("English proxy refusal crossed into positive coach authority: %q", utterance)
			}
		})
	}
}

func TestExplicitProxyAnswerOptOutRejectsQuotedReportedAndThirdPartySpeech(t *testing.T) {
	for _, utterance := range []string{
		"「代わりに答えないで」",
		"友達が「代わりに答えないで」と言っていた",
		"母が代わりに答えないでと言いました",
		"母はこう言いました。代わりに答えないで",
		"母が代わりに答えないで",
		"田中は回答を作らないで",
		"担当者も返事を読み上げないで",
		"母の回答を作らないで",
		"田中からの回答を読み上げないで",
		"母はAIが代わりに答えないで",
		"AIは母の回答を作らないで",
		"KOTAEは友達の返事を読み上げないで",
		"母の希望は、回答を作らないで",
		"母曰く、代わりに答えないで",
		"母によると、回答を作らないで",
		"友達によれば返事を読み上げないで",
		"例：代わりに答えないで",
		"ルール：回答を作らないで",
		"たとえば代わりに答えないで",
		"例として回答を作らないで",
		"引用すると代わりに答えないで",
		"母の話だと代わりに答えないで",
		"『代わりに答えないで",
		"\"don't answer for me\"",
		"my friend said don't write my answer",
		"my friend said. don't read my answer aloud",
	} {
		t.Run(utterance, func(t *testing.T) {
			if ExplicitProxyAnswerOptOut(utterance) {
				t.Fatalf("ExplicitProxyAnswerOptOut(%q) captured quoted, reported, or third-party speech", utterance)
			}
		})
	}
}

func TestProxyAnswerOptOutIsFixedLocalAndModelIndependent(t *testing.T) {
	const ghostAnswer = "評価基準をそろえるためです"
	for index, utterance := range []string{
		"代わりに答えないで",
		"回答を作らないで",
		"この回答を読み上げないで",
		"私の代わりに「" + ghostAnswer + "」と答えないで",
		"代わりに答えて、やっぱりやめて",
		"don't answer for me",
	} {
		t.Run(utterance, func(t *testing.T) {
			fake := &fakeGenerator{}
			agent := newTestAgent(t, fake)
			uid := "uid-proxy-answer-opt-out-local-" + string(rune('a'+index))
			result, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     utterance,
				RequestID:     "request-proxy-answer-opt-out-local",
				InputOrigin:   InputOriginCommittedVoice,
			})
			if err != nil {
				t.Fatalf("Process(%q): %v", utterance, err)
			}
			if len(fake.calls) != 0 ||
				result.Route != "proxy-answer-opt-out-local" ||
				result.AssistanceTarget != "assistant" ||
				result.RespondentStage != "none" ||
				result.CoachPhase != "none" ||
				result.CoachAction != "none" ||
				result.AnswerProof != AnswerProofNone ||
				result.AnswerProofCandidate != AnswerProofNone ||
				result.SpokenReply != proxyAnswerOptOutLocalSpokenReply {
				t.Fatalf("proxy refusal was not a fixed local assistant ack: result=%#v calls=%#v", result, fake.calls)
			}
			encodedResult, marshalErr := json.Marshal(result)
			if marshalErr != nil || bytes.Contains(encodedResult, []byte(ghostAnswer)) {
				t.Fatalf("quoted A reached local output: result=%s err=%v", encodedResult, marshalErr)
			}
			next, openErr := agent.codec.open(uid, result.StateToken)
			if openErr != nil {
				t.Fatalf("open local state: %v", openErr)
			}
			encodedState, marshalErr := json.Marshal(next)
			if marshalErr != nil || bytes.Contains(encodedState, []byte(ghostAnswer)) || next.PendingAnswer.Active {
				t.Fatalf("proxy refusal authored semantic state: state=%s err=%v", encodedState, marshalErr)
			}
		})
	}
}

func TestProxyAnswerOptOutPreservesExistingOwnedAnswerScope(t *testing.T) {
	const uid = "uid-proxy-answer-opt-out-preserves-scope"
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)
	initial := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingRestatement,
		1,
	)
	initial.PendingAnswer.RestatementTag = agent.coachContinuityTag("restatement-proof")
	initial.PendingAnswer.QuestionContinuityTag = agent.coachQuestionContinuityTag("question-proof")
	initial.PendingAnswer.ContinuityTag = agent.coachContinuityTag("answer-proof")
	wantPending := initial.PendingAnswer
	token, err := agent.codec.seal(uid, initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "代わりに答えないで",
		StateToken:    token,
		RequestID:     "request-proxy-answer-opt-out-preserves-scope",
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(fake.calls) != 0 || result.AssistanceTarget != "assistant" ||
		result.RespondentStage != "none" || result.AnswerProof != AnswerProofNone {
		t.Fatalf("active-scope refusal crossed the local boundary: result=%#v calls=%#v", result, fake.calls)
	}
	next, err := agent.codec.open(uid, result.StateToken)
	if err != nil {
		t.Fatalf("open next state: %v", err)
	}
	if !reflect.DeepEqual(next.PendingAnswer, wantPending) {
		t.Fatalf("proxy refusal changed the authenticated A scope:\n got: %#v\nwant: %#v", next.PendingAnswer, wantPending)
	}
	if next.Turn != initial.Turn+1 || !reflect.DeepEqual(next.Support, initial.Support) {
		t.Fatalf("proxy refusal changed unrelated control state: next=%#v initial=%#v", next, initial)
	}
}

func TestExplicitProxyAnswerOptOutRejectsKnowledgeAndUnrelatedRequests(t *testing.T) {
	for _, utterance := range []string{
		"問題の答えを教えて",
		"この問題に答えないで済む方法は？",
		"回答を作らないでくださいとはどういう意味？",
		"なぜ代わりに答えないでと言うの？",
		"この答えを読んで",
		"今日はもうやめて",
		"作らないで",
		"代わりに答えて",
	} {
		t.Run(utterance, func(t *testing.T) {
			if ExplicitProxyAnswerOptOut(utterance) {
				t.Fatalf("ExplicitProxyAnswerOptOut(%q) captured an unrelated or non-refusal turn", utterance)
			}
		})
	}
}

func TestProxyIntentBoundaryDoesNotSplitNarrativeConjunctions(t *testing.T) {
	for _, utterance := range []string{
		"この答えを読み上げてでも伝えたい",
		"この答えを読み上げて、でも伝えたい",
		"回答を作ってでも提出したい",
		"回答を作って、でも提出したい",
		"回答を作って でも提出したい",
		"回答を作らないででもできる方法を教えて",
		"回答を作らないで、でもできる方法を教えて",
		"回答を作ってもう提出した",
		"回答を作って、もう提出した",
		"回答を作って今は休んでいる",
		"回答を作って 今は休んでいる",
		"回答を作っていやになった",
		"回答を作って、いやになった",
	} {
		t.Run(utterance, func(t *testing.T) {
			if ExplicitCoachOptIn(utterance) ||
				explicitProxyAnswerOptIn(utterance) ||
				ExplicitProxyAnswerOptOut(utterance) {
				t.Fatalf("narrative conjunction became proxy authority: %q", utterance)
			}
		})
	}
}

func TestExplicitProxyAnswerOptOutUsesLastProxyIntent(t *testing.T) {
	for _, utterance := range []string{
		"回答を作らないで。でも、代わりに答えて",
		"回答を作らないで、でも代わりに答えて",
		"回答を作らないででも代わりに答えて",
		"代わりに答えないで。でも回答を作って",
		"代わりに答えないで いや回答を作って",
		"代わりに答えないでいや回答を作って",
		"この回答を読み上げないで。やっぱり読み上げて",
		"代わりに答えて。やめて。いや、答えて",
	} {
		t.Run(utterance, func(t *testing.T) {
			if ExplicitProxyAnswerOptOut(utterance) {
				t.Fatalf("ExplicitProxyAnswerOptOut(%q) ignored the later renewed request", utterance)
			}
			if !ExplicitCoachOptIn(utterance) || !explicitProxyAnswerOptIn(utterance) {
				t.Fatalf("renewed proxy intent did not enter the guarded coach route: %q", utterance)
			}
		})
	}
}
