package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func guestTrialAgent(t *testing.T) *vertexAgent {
	t.Helper()
	codec, err := NewStateCodec(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return &vertexAgent{codec: codec, continuityKey: bytes.Repeat([]byte{0x63}, 32)}
}

func TestGuestTrialFreshQuestionScopeAndEncryptedRoundTrip(t *testing.T) {
	agent := guestTrialAgent(t)
	first, err := agent.newGuestTransferState()
	if err != nil {
		t.Fatal(err)
	}
	second, err := agent.newGuestTransferState()
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID == second.SessionID || first.GuestTrial.Scope == second.GuestTrial.Scope {
		t.Fatal("new question reused a prior session or proof scope")
	}
	if first.PendingAnswer.Active || first.Support != nil || first.SessionMemory != nil || first.ConversationSummary != "" || len(first.Graph.Claims) != 0 {
		t.Fatal("fresh question contains prior assistance or conversation")
	}
	token, err := agent.codec.seal("guest-a", first)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := agent.codec.open("guest-a", token)
	if err != nil || !reflect.DeepEqual(opened.GuestTrial, first.GuestTrial) {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := agent.codec.open("guest-b", token); err == nil {
		t.Fatal("cross-user token accepted")
	}
	if _, err := agent.sealState("guest-a", first); err == nil {
		t.Fatal("reader-only revision issued trial")
	}
	if err := agent.ValidateStateToken("guest-a", token); err == nil {
		t.Fatal("ordinary validation accepted trial")
	}
	if _, _, err := agent.PrepareNativeState("guest-a", token); err == nil {
		t.Fatal("native preparation accepted trial")
	}
	if _, err := agent.RefreshStateToken("guest-a", token); err == nil {
		t.Fatal("native refresh downgraded trial")
	}
	if _, _, err := agent.LongTermMemoryCandidate("guest-a", token); err == nil {
		t.Fatal("memory extraction accepted trial")
	}
	// No generator is installed: processing must reject before any model call.
	if _, err := agent.Process(context.Background(), "guest-a", VoiceTurn{
		SchemaVersion: SchemaVersion, Utterance: "散歩したい", StateToken: token, GuestExperience: true,
	}); err == nil {
		t.Fatal("ordinary route accepted trial")
	}
	encoded, err := json.Marshal(first.GuestTrial)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(guestTransferQuestion)) {
		t.Fatal("trial stored question prose")
	}
}

func TestGuestTrialAssistanceCannotBecomeUnassisted(t *testing.T) {
	state, err := guestTrialAgent(t).newGuestTransferState()
	if err != nil {
		t.Fatal(err)
	}
	trial := state.GuestTrial
	assisted, err := requestGuestTrialHelp(trial, trial.Scope)
	if err != nil || !assisted.Assisted || trial.Assisted {
		t.Fatal("help did not create an owned assisted state")
	}
	turn := VoiceTurn{GuestExperience: true, InputOrigin: InputOriginCommittedVoice, Utterance: "散歩したい"}
	frame := PendingAnswerFrame{QuestionInstanceTag: trial.Scope}
	for _, input := range []*guestTrial{trial, assisted} {
		for _, proof := range []AnswerProof{AnswerProofNone, AnswerProofQuestionBoundInputAnswerFirst} {
			completed, err := completeGuestTrial(input, input.Scope, turn, frame, proof)
			if err != nil || completed.Assisted != input.Assisted {
				t.Fatal("completion changed assistance provenance")
			}
			if (completed.Outcome == guestTrialAnswerFirst) != (proof == AnswerProofQuestionBoundInputAnswerFirst) {
				t.Fatal("unverified speech was reported as an answer")
			}
			if _, err := requestGuestTrialHelp(completed, completed.Scope); err == nil {
				t.Fatal("terminal trial reopened")
			}
			if _, err := completeGuestTrial(completed, completed.Scope, turn, frame, proof); err == nil {
				t.Fatal("terminal result overwritten")
			}
		}
	}
	stopped, err := stopGuestTrial(assisted, trial.Scope)
	if err != nil || stopped.Outcome != guestTrialStopped || !stopped.Assisted {
		t.Fatal("stop lost assistance provenance")
	}
	if _, err := completeGuestTrial(stopped, trial.Scope, turn, frame, AnswerProofQuestionBoundInputAnswerFirst); err == nil {
		t.Fatal("stopped trial completed")
	}
}

func TestGuestTrialRejectsWrongQuestionAndUncommittedSpeech(t *testing.T) {
	agent := guestTrialAgent(t)
	a, err := agent.newGuestTransferState()
	if err != nil {
		t.Fatal(err)
	}
	b, err := agent.newGuestTransferState()
	if err != nil {
		t.Fatal(err)
	}
	turn := VoiceTurn{GuestExperience: true, InputOrigin: InputOriginCommittedVoice, Utterance: "散歩したい"}
	frame := PendingAnswerFrame{QuestionInstanceTag: b.GuestTrial.Scope}
	if _, err := completeGuestTrial(b.GuestTrial, a.GuestTrial.Scope, turn, frame, AnswerProofQuestionBoundInputAnswerFirst); err == nil {
		t.Fatal("previous question proof accepted")
	}
	oldFrame := PendingAnswerFrame{QuestionInstanceTag: a.GuestTrial.Scope}
	if _, err := completeGuestTrial(b.GuestTrial, b.GuestTrial.Scope, turn, oldFrame, AnswerProofQuestionBoundInputAnswerFirst); err == nil {
		t.Fatal("current request relabeled previous question evidence")
	}
	if _, err := requestGuestTrialHelp(b.GuestTrial, a.GuestTrial.Scope); err == nil {
		t.Fatal("previous question help accepted")
	}
	if _, err := stopGuestTrial(b.GuestTrial, a.GuestTrial.Scope); err == nil {
		t.Fatal("previous question stop accepted")
	}
	for _, mutate := range []func(*VoiceTurn){
		func(v *VoiceTurn) { v.Speculative = true },
		func(v *VoiceTurn) { v.InputOrigin = InputOriginProvisionalVoice },
		func(v *VoiceTurn) { v.InputOrigin = InputOriginUnknown },
		func(v *VoiceTurn) { v.GuestExperience = false },
		func(v *VoiceTurn) { v.Utterance = " " },
	} {
		candidate := turn
		mutate(&candidate)
		if _, err := completeGuestTrial(b.GuestTrial, b.GuestTrial.Scope, candidate, frame, AnswerProofQuestionBoundInputAnswerFirst); err == nil {
			t.Fatal("invalid voice provenance accepted")
		}
	}
}

func TestGuestTrialSchemaFailsClosedAndCopiesState(t *testing.T) {
	state, err := guestTrialAgent(t).newGuestTransferState()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*guestTrial){
		func(v *guestTrial) { v.Version = 2 },
		func(v *guestTrial) { v.Question = "arbitrary-question" },
		func(v *guestTrial) { v.Scope = "" },
		func(v *guestTrial) { v.Outcome = "ability_improved" },
	} {
		candidate := *state.GuestTrial
		mutate(&candidate)
		invalid := state
		invalid.GuestTrial = &candidate
		if _, err := normalizeConversationState(invalid); err == nil {
			t.Fatal("invalid trial accepted")
		}
	}
	normalized, err := normalizeConversationState(state)
	if err != nil {
		t.Fatal(err)
	}
	normalized.GuestTrial.Assisted = true
	if state.GuestTrial.Assisted {
		t.Fatal("normalized state aliases caller")
	}
	state.SessionID = ""
	if _, err := normalizeConversationState(state); err == nil {
		t.Fatal("trial without session accepted")
	}
}
