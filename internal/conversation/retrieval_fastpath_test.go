package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestAgentQARCRetrievalFastPathCommittedMakesNoModelCall(t *testing.T) {
	const (
		uid       = "uid-qarc-committed"
		question  = "上司に、導入目的は何かと聞かれました"
		utterance = question + "。どう答えればいいですか"
	)
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion:    SchemaVersion,
		Utterance:        utterance,
		RequestID:        "request-qarc-committed",
		InputOrigin:      InputOriginCommittedVoice,
		OutputCancelable: true,
		FloorEvidence:    FloorEvidenceHybridCommitted,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("local retrieval invoked the model: %#v", fake.calls)
	}
	if result.Route != qarcLocalRoute ||
		result.SpokenReply != "目的を一つだけ。" ||
		result.AssistanceTarget != "respondent" ||
		result.RespondentStage != "awaiting_answer" ||
		result.CoachPhase != string(respondent.CoachPhaseAwaitingAnswer) ||
		result.CoachAction != string(respondent.CoachActionElicit) ||
		result.AnswerProof != AnswerProofNone ||
		!result.NeedsClarification {
		t.Fatalf("result=%#v", result)
	}

	stored, err := agent.codec.open(uid, result.StateToken)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	frame := stored.PendingAnswer
	instance := "導入目的は何かと聞かれました"
	if !frame.Active ||
		frame.Operator != answercontract.OperatorPurpose ||
		frame.Subject != pendingSubjectForOperator(answercontract.OperatorPurpose) ||
		frame.Phase != respondent.CoachPhaseAwaitingAnswer ||
		frame.Attempts != 0 ||
		len(frame.RequiredSlots) != 1 ||
		frame.RequiredSlots[0] != answercontract.SlotPurpose ||
		frame.QuestionContinuityTag !=
			agent.coachQuestionContinuityTag("導入目的") ||
		frame.QuestionInstanceTag !=
			agent.coachQuestionInstanceTag(stored.SessionID, instance) ||
		frame.ContinuityTag != "" ||
		frame.VerifierProgress != nil {
		t.Fatalf("frame=%#v", frame)
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	for _, forbidden := range []string{question, utterance, "導入目的"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("raw question entered state: %s", encoded)
		}
	}
}

func TestAgentQARCQuantityCueMatchesStoredQuantityAndUnitContract(t *testing.T) {
	const (
		uid       = "uid-qarc-quantity-contract"
		utterance = "上司に、必要な件数は何件かと聞かれました。答え方を手伝ってください。"
	)
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion:    SchemaVersion,
		Utterance:        utterance,
		RequestID:        "request-qarc-quantity-contract",
		InputOrigin:      InputOriginCommittedVoice,
		OutputCancelable: true,
		FloorEvidence:    FloorEvidenceHybridCommitted,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(fake.calls) != 0 || result.Route != qarcLocalRoute ||
		result.SpokenReply != "数字と単位だけ。" {
		t.Fatalf("result=%#v calls=%#v", result, fake.calls)
	}
	stored, err := agent.codec.open(uid, result.StateToken)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	frame := stored.PendingAnswer
	if !frame.Active || frame.Operator != answercontract.OperatorQuantity ||
		frame.Attempts != 0 || len(frame.RequiredSlots) != 2 ||
		frame.RequiredSlots[0] != answercontract.SlotQuantity ||
		frame.RequiredSlots[1] != answercontract.SlotUnit {
		t.Fatalf("quantity frame=%#v", frame)
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if bytes.Contains(encoded, []byte(utterance)) ||
		bytes.Contains(encoded, []byte("必要な件数")) {
		t.Fatalf("quantity question entered state: %s", encoded)
	}
}

func TestAgentQARCRetrievalFastPathProvisionalIsCommitProtected(t *testing.T) {
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(
		context.Background(),
		"uid-qarc-provisional",
		VoiceTurn{
			SchemaVersion:    SchemaVersion,
			Utterance:        "上司に、導入目的は何かと聞かれました。どう答えればいいですか",
			RequestID:        "request-qarc-provisional",
			Speculative:      true,
			InputOrigin:      InputOriginProvisionalVoice,
			OutputCancelable: true,
			FloorEvidence:    FloorEvidenceProvisionalCommitGate,
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(fake.calls) != 0 || result.Route != qarcLocalRoute ||
		result.SpokenReply != "目的を一つだけ。" ||
		result.AnswerProof != AnswerProofNone ||
		result.AnswerProofCandidate != AnswerProofNone {
		t.Fatalf("result=%#v calls=%#v", result, fake.calls)
	}
}

func TestAgentQARCRetrievalFastPathAllowsExplicitForeground(t *testing.T) {
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(
		context.Background(),
		"uid-qarc-foreground",
		VoiceTurn{
			SchemaVersion:    SchemaVersion,
			Utterance:        "上司に、導入目的は何かと聞かれました。どう答えればいいですか",
			RequestID:        "request-qarc-foreground",
			Ambient:          true,
			Foreground:       true,
			InputOrigin:      InputOriginCommittedVoice,
			OutputCancelable: true,
			FloorEvidence:    FloorEvidenceHybridCommitted,
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(fake.calls) != 0 || result.Route != qarcLocalRoute ||
		result.SpokenReply != "目的を一つだけ。" {
		t.Fatalf("result=%#v calls=%#v", result, fake.calls)
	}
}

func TestBoundedQARCRetrievalScopeUsesLongestExactSubject(t *testing.T) {
	tests := []struct {
		utterance string
		subject   string
		instance  string
		operator  answercontract.Operator
		ok        bool
	}{
		{
			utterance: "上司に、導入目的は何かと聞かれました。どう答えればいいですか",
			subject:   "導入目的",
			instance:  "導入目的は何かと聞かれました",
			operator:  answercontract.OperatorPurpose,
			ok:        true,
		},
		{
			utterance: "会議で、新システムの導入目的について聞かれました。答え方を一問だけ手伝って",
			subject:   "新システムの導入目的",
			instance:  "新システムの導入目的について聞かれました",
			operator:  answercontract.OperatorPurpose,
			ok:        true,
		},
		{
			utterance: "予算の理由を聞かれました。次に導入目的を聞かれました。答え方を手伝って",
			ok:        false,
		},
		{
			utterance: "目的は何ですかと聞かれました。どう答えればいいですか",
			ok:        false,
		},
		{
			utterance: "上司に、導入目的は何かと聞かれました。コスト削減です。答え方を手伝って",
			ok:        false,
		},
	}
	for _, test := range tests {
		got, ok := boundedQARCRetrievalScope(test.utterance)
		if ok != test.ok || got.questionSubject != test.subject ||
			got.questionInstance != test.instance || got.operator != test.operator {
			t.Fatalf("scope(%q)=%#v,%v", test.utterance, got, ok)
		}
	}
}

func TestQARCRetrievalFastPathFailsClosedOutsideVoiceCommitBoundary(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	state := conversationState{
		SessionID: "QUFBQUFBQUFBQUFBQUFBQQ",
	}
	base := VoiceTurn{
		SchemaVersion:    SchemaVersion,
		Utterance:        "上司に、導入目的は何かと聞かれました。どう答えればいいですか",
		RequestID:        "request-qarc-negative",
		InputOrigin:      InputOriginCommittedVoice,
		OutputCancelable: true,
		FloorEvidence:    FloorEvidenceHybridCommitted,
	}
	tests := []struct {
		name  string
		state conversationState
		turn  VoiceTurn
	}{
		{name: "unknown origin", state: state, turn: withTurn(base, func(turn *VoiceTurn) {
			turn.InputOrigin = InputOriginUnknown
		})},
		{name: "missing request id", state: state, turn: withTurn(base, func(turn *VoiceTurn) {
			turn.RequestID = ""
		})},
		{name: "passive ambient", state: state, turn: withTurn(base, func(turn *VoiceTurn) {
			turn.Ambient = true
		})},
		{name: "output not cancelable", state: state, turn: withTurn(base, func(turn *VoiceTurn) {
			turn.OutputCancelable = false
		})},
		{name: "provisional buffer not cancelable", state: state, turn: withTurn(base, func(turn *VoiceTurn) {
			turn.Speculative = true
			turn.InputOrigin = InputOriginProvisionalVoice
			turn.OutputCancelable = false
		})},
		{name: "provisional input without commit gate", state: state, turn: withTurn(base, func(turn *VoiceTurn) {
			turn.Speculative = true
			turn.InputOrigin = InputOriginProvisionalVoice
			turn.FloorEvidence = FloorEvidenceUnknown
		})},
		{name: "strict", state: state, turn: withTurn(base, func(turn *VoiceTurn) {
			turn.ResearchDisabled = true
		})},
		{name: "own answer already present", state: state, turn: withTurn(base, func(turn *VoiceTurn) {
			turn.Utterance = "上司に、導入目的は何かと聞かれました。私の答えは評価基準をそろえることです。答え方を手伝って"
		})},
		{name: "bare answer already present", state: state, turn: withTurn(base, func(turn *VoiceTurn) {
			turn.Utterance = "上司に、導入目的は何かと聞かれました。評価基準をそろえることです。答え方を手伝って"
		})},
		{name: "precision domain", state: state, turn: withTurn(base, func(turn *VoiceTurn) {
			turn.Utterance = "医師に、治療目的は何かと聞かれました。どう答えればいいですか"
		})},
		{name: "active scope", state: withPendingRetrievalFrame(state), turn: base},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, handled, err := agent.completeQARCRetrievalStartLocal(
				"uid-qarc-negative",
				test.state,
				test.turn,
			)
			if err != nil || handled {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
		})
	}

	agent.retrievalPolicyEnabled = false
	if _, handled, err := agent.completeQARCRetrievalStartLocal(
		"uid-qarc-negative",
		state,
		base,
	); err != nil || handled {
		t.Fatalf("disabled rollout handled=%v err=%v", handled, err)
	}
}

func TestAgentQARCStartUnknownFloorIsHandledWithoutModelSpeech(t *testing.T) {
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)
	const uid = "uid-qarc-floor-wait"
	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion:    SchemaVersion,
		Utterance:        "上司に、導入目的は何かと聞かれました。答え方を手伝って",
		RequestID:        "request-qarc-floor-wait",
		InputOrigin:      InputOriginCommittedVoice,
		OutputCancelable: true,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(fake.calls) != 0 || result.Route != qarcLocalRoute ||
		result.SpokenReply != "" ||
		result.CoachAction != string(respondent.CoachActionNone) ||
		result.Intervention.Act != "silent" {
		t.Fatalf("result=%#v calls=%#v", result, fake.calls)
	}
	state, err := agent.codec.open(uid, result.StateToken)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	if !state.PendingAnswer.Active || state.PendingAnswer.Attempts != 0 {
		t.Fatalf("pending=%#v", state.PendingAnswer)
	}
}

func withTurn(base VoiceTurn, mutate func(*VoiceTurn)) VoiceTurn {
	mutate(&base)
	return base
}

func withPendingRetrievalFrame(state conversationState) conversationState {
	state.PendingAnswer = PendingAnswerFrame{Active: true}
	return state
}
