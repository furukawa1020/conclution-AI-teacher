package conversation

import (
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
)

func TestGuestTrialTokenBoundToSharedSnapshot(t *testing.T) {
	agent := guestTrialAgent(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	agent.codec.now = func() time.Time { return now }
	owner := guesttrial.Owner{UID: "guest", AppID: "firebase-app"}
	snapshot := guesttrial.Snapshot{
		Version: 1, ID: strings.Repeat("a", 64), QuestionID: guesttrial.QuestionID,
		Question: guesttrial.QuestionText, Generation: 1, Status: guesttrial.Pending, ExpiresAt: now.Add(guesttrial.TTL),
	}
	token, err := agent.issueGuestTrialState(owner, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := agent.openGuestTrialState(owner, token, snapshot)
	if err != nil || opened.GuestTrial.Binding.ID != snapshot.ID {
		t.Fatal(err)
	}
	for _, other := range []guesttrial.Owner{{UID: "other", AppID: owner.AppID}, {UID: owner.UID, AppID: "other"}} {
		if _, err := agent.openGuestTrialState(other, token, snapshot); err == nil {
			t.Fatal("cross-owner token accepted")
		}
	}
	for _, mutate := range []func(*guesttrial.Snapshot){
		func(s *guesttrial.Snapshot) { s.ID = strings.Repeat("b", 64) },
		func(s *guesttrial.Snapshot) { s.Question = "別の質問" },
		func(s *guesttrial.Snapshot) { s.QuestionID = "other" },
		func(s *guesttrial.Snapshot) { s.Assisted = true; s.Generation = 2 },
		func(s *guesttrial.Snapshot) { s.Status = guesttrial.Stopped; s.Generation = 2 },
		func(s *guesttrial.Snapshot) { s.ExpiresAt = s.ExpiresAt.Add(-time.Second) },
	} {
		other := snapshot
		mutate(&other)
		if _, err := agent.openGuestTrialState(owner, token, other); err == nil {
			t.Fatal("different ledger snapshot accepted", other)
		}
	}
	if _, err := agent.openConversationState(owner.UID, token); err == nil {
		t.Fatal("trial bypassed ordinary generation barrier")
	}
	if _, _, err := agent.PrepareNativeState(owner.UID, token); err == nil {
		t.Fatal("trial bypassed native generation barrier")
	}
	assisted := snapshot
	assisted.Assisted, assisted.Generation = true, 2
	assistedToken, err := agent.issueGuestTrialState(owner, assisted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.openGuestTrialState(owner, assistedToken, assisted); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.openGuestTrialState(owner, assistedToken, snapshot); err == nil {
		t.Fatal("assistance history erased")
	}
	helped, err := requestGuestTrialHelp(opened.GuestTrial, opened.GuestTrial.Scope)
	if err != nil || helped.Binding.Generation != 2 || opened.GuestTrial.Binding.Generation != 1 {
		t.Fatal("help did not advance a copied binding", err)
	}
	if _, err := normalizeGuestTrial(helped); err != nil {
		t.Fatal("help created an invalid binding", err)
	}
	if _, err := requestGuestTrialHelp(helped, helped.Scope); err == nil {
		t.Fatal("duplicate help changed the generation")
	}
	for _, source := range []*guestTrial{opened.GuestTrial, helped} {
		stopped, err := stopGuestTrial(source, source.Scope)
		if err != nil || stopped.Binding.Generation != source.Binding.Generation+1 {
			t.Fatal("stop binding", err)
		}
		if _, err := normalizeGuestTrial(stopped); err != nil {
			t.Fatal(err)
		}
		completed, err := completeGuestTrial(source, source.Scope,
			VoiceTurn{GuestExperience: true, InputOrigin: InputOriginCommittedVoice, Utterance: "散歩したい"},
			PendingAnswerFrame{QuestionInstanceTag: source.Scope}, AnswerProofQuestionBoundInputAnswerFirst)
		if err != nil || completed.Binding.Generation != source.Binding.Generation+1 {
			t.Fatal("completion binding", err)
		}
		if _, err := normalizeGuestTrial(completed); err != nil {
			t.Fatal(err)
		}
	}
	now = snapshot.ExpiresAt
	if _, err := agent.openGuestTrialState(owner, token, snapshot); err == nil {
		t.Fatal("expired ledger accepted")
	}
}

func TestGuestTrialTokenRejectsUnboundOrChangedQuestionScope(t *testing.T) {
	agent := guestTrialAgent(t)
	now := time.Now()
	agent.codec.now = func() time.Time { return now }
	owner := guesttrial.Owner{UID: "guest", AppID: "app"}
	snapshot := guesttrial.Snapshot{
		Version: 1, ID: strings.Repeat("a", 64), QuestionID: guesttrial.QuestionID,
		Question: guesttrial.QuestionText, Generation: 1, Status: guesttrial.Pending, ExpiresAt: now.Add(guesttrial.TTL),
	}
	state, _ := agent.newGuestTransferState()
	unbound, err := agent.codec.seal(owner.UID, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.openGuestTrialState(owner, unbound, snapshot); err == nil {
		t.Fatal("read-first unbound state became a capability")
	}
	binding, _ := agent.guestTrialBindingFor(owner, snapshot)
	state.GuestTrial.Binding = &binding
	normalized, err := normalizeConversationState(state)
	if err != nil {
		t.Fatal(err)
	}
	normalized.GuestTrial.Binding.ID = strings.Repeat("b", 64)
	if state.GuestTrial.Binding.ID != snapshot.ID {
		t.Fatal("normalization aliases binding")
	}
	other, _ := agent.newGuestTransferState()
	state.GuestTrial.Scope = other.GuestTrial.Scope
	wrongScope, err := agent.codec.seal(owner.UID, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.openGuestTrialState(owner, wrongScope, snapshot); err == nil {
		t.Fatal("different question instance accepted")
	}
}
