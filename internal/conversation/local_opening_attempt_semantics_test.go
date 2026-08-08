package conversation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestLocalCoachOpeningsReserveOneReaskForTheFirstLateAnswer(t *testing.T) {
	const (
		release = "大丈夫です。言い直さなくても、今のままで話を続けられます。"
	)
	tests := []struct {
		name         string
		opening      string
		openingCue   string
		openingRoute string
		reask        string
		operator     answercontract.Operator
		slot         answercontract.RequiredSlot
		subject      string
		lateAnswer   string
		commitment   string
	}{
		{
			name:         "generic open slot",
			opening:      "上司に、導入目的と費用と時期をまとめて聞かれました。答え方を手伝ってください。",
			openingCue:   genericCoachOpeningCue,
			openingRoute: genericCoachLocalRoute,
			reask:        "そこまでちゃんと聞こえています。今の言葉は変えず、答えになっている一文から続けても大丈夫です。",
			operator:     answercontract.OperatorOpen,
			slot:         answercontract.SlotPosition,
			subject:      pendingSubjectForOperator(answercontract.OperatorOpen),
			lateAnswer:   "背景を先に話すと、私は評価基準をそろえる案がよいと思います。",
			commitment:   "私は評価基準をそろえる案がよいと思います",
		},
		{
			name:         "question-bound QARC",
			opening:      "上司に、導入目的は何かと聞かれました。答え方を手伝ってください。",
			openingCue:   "目的を一つだけ。",
			openingRoute: qarcLocalRoute,
			reask:        "最初のひと言だけで大丈夫です。",
			operator:     answercontract.OperatorPurpose,
			slot:         answercontract.SlotPurpose,
			subject:      "導入目的",
			lateAnswer:   "判断のばらつきを減らすためです。目的は評価基準をそろえることです。",
			commitment:   "目的は評価基準をそろえることです",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const proxyDraft = "AIが本人の代わりに作った代理回答です。"
			latePlan := coachAttemptPlan(
				test.operator,
				test.slot,
				test.subject,
				test.lateAnswer,
				test.commitment,
				proxyDraft,
			)
			lateCritic := coachCriticContract(
				test.operator,
				test.slot,
				test.lateAnswer,
				test.commitment,
				answercontract.PositionLater,
			)
			fake := &fakeGenerator{generations: []fakeGeneration{
				{body: encodePlan(t, latePlan)},
				{body: encodeContract(t, lateCritic)},
				{body: encodePlan(t, latePlan)},
				{body: encodeContract(t, lateCritic)},
			}}
			agent := newTestAgent(t, fake)
			uid := "uid-local-opening-attempt-" + strings.ReplaceAll(test.name, " ", "-")

			opened, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion:    SchemaVersion,
				Utterance:        test.opening,
				RequestID:        "request-open",
				InputOrigin:      InputOriginCommittedVoice,
				OutputCancelable: true,
				FloorEvidence:    FloorEvidenceHybridCommitted,
			})
			if err != nil {
				t.Fatalf("open local coach: %v", err)
			}
			if opened.Route != test.openingRoute || opened.SpokenReply != test.openingCue ||
				opened.AnswerProof != AnswerProofNone || len(fake.calls) != 0 {
				t.Fatalf("opening result=%#v calls=%#v", opened, fake.calls)
			}
			openedState := openCoachState(t, agent, uid, opened.StateToken)
			if !openedState.PendingAnswer.Active || openedState.PendingAnswer.Attempts != 0 {
				t.Fatalf("opening consumed answer attempt: %#v", openedState.PendingAnswer)
			}

			reasked, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion:    SchemaVersion,
				Utterance:        test.lateAnswer,
				StateToken:       opened.StateToken,
				RequestID:        "request-late-first",
				InputOrigin:      InputOriginCommittedVoice,
				OutputCancelable: true,
				FloorEvidence:    FloorEvidenceHybridCommitted,
			})
			if err != nil {
				t.Fatalf("first late A: %v", err)
			}
			assertCoachMetadata(t, reasked, "awaiting_restatement", "restate")
			if reasked.SpokenReply != test.reask ||
				strings.Contains(reasked.SpokenReply, proxyDraft) ||
				strings.Contains(reasked.SpokenReply, test.lateAnswer) ||
				reasked.AnswerProof != AnswerProofNone ||
				reasked.AnswerProofCandidate != AnswerProofNone {
				t.Fatalf("first late A was not reasked safely: %#v", reasked)
			}
			reaskedState := openCoachState(t, agent, uid, reasked.StateToken)
			if !reaskedState.PendingAnswer.Active ||
				reaskedState.PendingAnswer.Phase != respondent.CoachPhaseAwaitingRestatement ||
				reaskedState.PendingAnswer.Attempts != 1 ||
				!validCoachRestatementTag(reaskedState.PendingAnswer.RestatementTag) {
				t.Fatalf("one reask was not bound: %#v", reaskedState.PendingAnswer)
			}
			encoded, err := json.Marshal(reaskedState)
			if err != nil {
				t.Fatalf("marshal restatement state: %v", err)
			}
			for _, forbidden := range []string{
				test.opening,
				test.lateAnswer,
				test.commitment,
				proxyDraft,
			} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("answer/question text entered state: %s", encoded)
				}
			}

			released, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion:    SchemaVersion,
				Utterance:        test.lateAnswer,
				StateToken:       reasked.StateToken,
				RequestID:        "request-late-second",
				InputOrigin:      InputOriginCommittedVoice,
				OutputCancelable: true,
				FloorEvidence:    FloorEvidenceHybridCommitted,
			})
			if err != nil {
				t.Fatalf("second late A: %v", err)
			}
			assertCoachMetadata(t, released, "blocked", "release")
			if released.SpokenReply != release ||
				strings.Contains(released.SpokenReply, proxyDraft) ||
				strings.Contains(released.SpokenReply, test.lateAnswer) ||
				released.AnswerProof != AnswerProofNone ||
				released.AnswerProofCandidate != AnswerProofNone {
				t.Fatalf("second failed attempt was not released safely: %#v", released)
			}
			if openCoachState(t, agent, uid, released.StateToken).PendingAnswer.Active {
				t.Fatal("release retained the answer scope")
			}
			if len(fake.calls) != 4 {
				t.Fatalf("model calls=%d, want two isolated planner/critic pairs", len(fake.calls))
			}
		})
	}
}
