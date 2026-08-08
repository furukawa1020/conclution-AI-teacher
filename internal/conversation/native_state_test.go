package conversation

import (
	"errors"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestRefreshStateTokenIsModelFreeBoundedAndCallerBound(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	refresher, ok := Agent(agent).(StateTokenRefresher)
	if !ok {
		t.Fatal("production agent must expose the native state refresher")
	}

	first, err := refresher.RefreshStateToken("native-user", "")
	if err != nil || first == "" {
		t.Fatalf("first refresh = %q, %v", first, err)
	}
	firstState, err := agent.codec.open("native-user", first)
	if err != nil || firstState.Turn != 1 {
		t.Fatalf("first state = %+v, %v", firstState, err)
	}
	if firstState.ConversationSummary != "" ||
		len(firstState.Graph.Claims) != 0 ||
		firstState.PendingAnswer.Active {
		t.Fatalf("native lease retained content: %+v", firstState)
	}

	second, err := refresher.RefreshStateToken("native-user", first)
	if err != nil {
		t.Fatal(err)
	}
	secondState, err := agent.codec.open("native-user", second)
	if err != nil || secondState.Turn != 2 ||
		secondState.SessionID != firstState.SessionID {
		t.Fatalf("second state = %+v, %v", secondState, err)
	}
	if _, err := refresher.RefreshStateToken("other-user", second); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("cross-user refresh error = %v", err)
	}
}

func TestPrepareNativeStateKeepsSignedPendingCoachInStagedFlow(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	preparer, ok := Agent(agent).(NativeStatePreparer)
	if !ok {
		t.Fatal("production agent must expose the native state preparer")
	}

	const uid = "native-pending-user"
	state := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	)
	state.Turn = 7
	token, err := agent.codec.seal(uid, state)
	if err != nil {
		t.Fatal(err)
	}

	prepared, requiresStaged, err := preparer.PrepareNativeState(uid, token)
	if err != nil {
		t.Fatal(err)
	}
	if !requiresStaged || prepared != token {
		t.Fatalf("prepared=%q requires staged=%v", prepared, requiresStaged)
	}
	opened, err := agent.codec.open(uid, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Turn != state.Turn || !opened.PendingAnswer.Active ||
		opened.PendingAnswer.Phase != respondent.CoachPhaseAwaitingAnswer {
		t.Fatalf("pending state advanced or changed: %+v", opened)
	}
}

func TestPrepareNativeStateAdvancesOnlyCompanionState(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	preparer := Agent(agent).(NativeStatePreparer)

	prepared, requiresStaged, err := preparer.PrepareNativeState(
		"native-companion-user",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if requiresStaged || prepared == "" {
		t.Fatalf("prepared=%q requires staged=%v", prepared, requiresStaged)
	}
	opened, err := agent.codec.open("native-companion-user", prepared)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Turn != 1 || opened.PendingAnswer.Active {
		t.Fatalf("prepared companion state = %+v", opened)
	}
}
