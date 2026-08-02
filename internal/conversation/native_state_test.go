package conversation

import (
	"errors"
	"testing"
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
