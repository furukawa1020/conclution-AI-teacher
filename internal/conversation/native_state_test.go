package conversation

import (
	"errors"
	"strings"
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

func TestNativeExchangeContinuityIsEncryptedBoundedAndCallerBound(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	continuity := Agent(agent).(NativeConversationContinuity)
	preparer := Agent(agent).(NativeStatePreparer)

	prepared, staged, err := preparer.PrepareNativeState("continuity-user", "")
	if err != nil || staged {
		t.Fatalf("prepare = %q, %v, staged=%v", prepared, err, staged)
	}
	committed, err := continuity.CommitNativeExchange(
		"continuity-user",
		prepared,
		"昨日はゲームをしました",
		"どんなゲームが一番楽しかったですか？",
	)
	if err != nil || committed == "" || committed == prepared {
		t.Fatalf("commit = %q, %v", committed, err)
	}
	contextValue, err := continuity.NativeConversationContext(
		"continuity-user",
		committed,
	)
	if err != nil ||
		!strings.Contains(contextValue, "昨日はゲームをしました") ||
		!strings.Contains(contextValue, "どんなゲームが一番楽しかったですか？") ||
		utf8RuneCount(contextValue) > maxConversationSummaryRunes {
		t.Fatalf("context = %q, %v", contextValue, err)
	}
	if strings.Contains(committed, "ゲーム") {
		t.Fatal("encrypted state exposed conversation text")
	}
	if _, err := continuity.NativeConversationContext("other-user", committed); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("foreign context error = %v", err)
	}
}

func TestNativeExchangeKeepsTheTailAndRejectsPendingCoach(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	continuity := Agent(agent).(NativeConversationContinuity)
	preparer := Agent(agent).(NativeStatePreparer)
	prepared, _, err := preparer.PrepareNativeState("bounded-user", "")
	if err != nil {
		t.Fatal(err)
	}
	userTail := "利用者の答え"
	assistantTail := "次に答えてほしい質問ですか？"
	committed, err := continuity.CommitNativeExchange(
		"bounded-user",
		prepared,
		strings.Repeat("前", 300)+userTail,
		strings.Repeat("後", 300)+assistantTail,
	)
	if err != nil {
		t.Fatal(err)
	}
	contextValue, err := continuity.NativeConversationContext("bounded-user", committed)
	if err != nil || !strings.Contains(contextValue, userTail) ||
		!strings.Contains(contextValue, assistantTail) ||
		utf8RuneCount(contextValue) > maxConversationSummaryRunes {
		t.Fatalf("bounded context = %q, %v", contextValue, err)
	}

	pending := coachState(answercontract.OperatorPurpose, respondent.CoachPhaseAwaitingAnswer, 0)
	pending.Turn = 1
	pendingToken, err := agent.codec.seal("pending-continuity-user", pending)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := continuity.CommitNativeExchange(
		"pending-continuity-user",
		pendingToken,
		"答え",
		"質問",
	); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("pending commit error = %v", err)
	}
}

func utf8RuneCount(value string) int {
	return len([]rune(value))
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
