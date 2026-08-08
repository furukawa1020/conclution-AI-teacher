package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestExplicitComplexQuestionOpensContentFreeAnswerSlotLocally(t *testing.T) {
	const (
		uid       = "uid-generic-coach-local"
		utterance = "上司に、導入目的と費用と時期をまとめて聞かれました。" +
			"答え方を手伝ってください。"
	)
	if !explicitCoachOptIn(utterance) {
		t.Fatal("fixture is not explicit coach opt-in")
	}
	if _, bounded := boundedQARCRetrievalScope(utterance); bounded {
		t.Fatal("fixture unexpectedly entered the question-operator fast path")
	}
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     utterance,
		RequestID:     "request-generic-coach-local",
		InputOrigin:   InputOriginCommittedVoice,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(fake.calls) != 0 || result.Route != genericCoachLocalRoute ||
		result.SpokenReply != genericCoachOpeningCue ||
		result.AssistanceTarget != "respondent" ||
		result.RespondentStage != "awaiting_answer" ||
		result.CoachAction != string(respondent.CoachActionElicit) ||
		result.AnswerProof != AnswerProofNone ||
		result.AnswerProofCandidate != AnswerProofNone {
		t.Fatalf("result=%#v calls=%#v", result, fake.calls)
	}
	stored, err := agent.codec.open(uid, result.StateToken)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	frame := stored.PendingAnswer
	if !frame.Active || frame.Operator != answercontract.OperatorOpen ||
		frame.Phase != respondent.CoachPhaseAwaitingAnswer ||
		frame.Attempts != 0 || frame.NativeCoachScopeTag == "" ||
		frame.QuestionInstanceTag != "" ||
		frame.QuestionContinuityTag != "" ||
		frame.VerifierProgress != nil {
		t.Fatalf("frame=%#v", frame)
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if bytes.Contains(encoded, []byte(utterance)) ||
		bytes.Contains(encoded, []byte("導入目的")) {
		t.Fatalf("question content entered state: %s", encoded)
	}
}

func TestGenericCoachStartCannotPrecomputeWithoutCommitFence(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	state := conversationState{SessionID: "QUFBQUFBQUFBQUFBQUFBQQ"}
	_, handled, err := agent.completeGenericCoachStartLocal(
		"uid-generic-coach-fence",
		state,
		VoiceTurn{
			SchemaVersion:    SchemaVersion,
			Utterance:        "上司に複数のことを聞かれました。答え方を手伝ってください。",
			Speculative:      true,
			InputOrigin:      InputOriginProvisionalVoice,
			OutputCancelable: true,
			FloorEvidence:    FloorEvidenceUnknown,
		},
	)
	if err != nil || handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}
