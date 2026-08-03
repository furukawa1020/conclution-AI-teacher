package conversation

import (
	"context"
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

func TestPrepareNativeCoachStateIssuesGenericOpaquePendingAuthority(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	preparer, ok := Agent(agent).(NativeCoachStatePreparer)
	if !ok {
		t.Fatal("production agent must expose the native coach state preparer")
	}

	const (
		uid       = "native-new-coach-user"
		utterance = "上司に「結局、何のために入れるの？」と聞かれた。答え方を一問だけ手伝って"
	)
	token, err := preparer.PrepareNativeCoachState(uid, "", utterance)
	if err != nil || token == "" {
		t.Fatalf("prepare native coach = %q, %v", token, err)
	}
	state, err := agent.codec.open(uid, token)
	if err != nil {
		t.Fatal(err)
	}
	frame := state.PendingAnswer
	if state.Turn != 1 || !frame.Active ||
		frame.Operator != answercontract.OperatorOpen ||
		frame.Subject != pendingSubjectForOperator(answercontract.OperatorOpen) ||
		len(frame.RequiredSlots) != 1 ||
		frame.RequiredSlots[0] != answercontract.SlotPosition ||
		frame.Phase != respondent.CoachPhaseAwaitingAnswer ||
		frame.Attempts != 0 || frame.NativeCoachScopeTag == "" ||
		frame.QuestionContinuityTag != "" || frame.ContinuityTag != "" ||
		frame.RestatementTag != "" {
		t.Fatalf("generic native coach frame = %+v", frame)
	}
	if frame.NativeCoachScopeTag == agent.coachQuestionContinuityTag(utterance) ||
		frame.NativeCoachScopeTag == agent.coachContinuityTag(utterance) {
		t.Fatal("native scope reused a question or answer continuity proof")
	}
	if prompt := pendingAnswerForPrompt(frame); prompt.Operator != answercontract.OperatorOpen ||
		prompt.Subject != pendingSubjectForOperator(answercontract.OperatorOpen) {
		t.Fatalf("generic prompt shape = %+v", prompt)
	}
	if _, err := agent.codec.open("other-native-user", token); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("cross-user coach state error = %v", err)
	}
	if _, err := preparer.PrepareNativeCoachState(uid, token, utterance); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("active coach was replaced: %v", err)
	}
}

func TestPrepareNativeCoachStateRejectsSpeechWithoutExplicitConsent(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	preparer := Agent(agent).(NativeCoachStatePreparer)
	if _, err := preparer.PrepareNativeCoachState(
		"native-no-consent-user",
		"",
		"上司に目的を聞かれた",
	); !errors.Is(err, ErrInvalidTurn) {
		t.Fatalf("non-consensual native coach state error = %v", err)
	}
}

func TestPreparedNativeCoachStateDrivesTheRealNextRespondentTurn(t *testing.T) {
	const (
		uid       = "native-coach-next-turn-user"
		request   = "上司に目的を聞かれました。答え方を一問だけ手伝ってください"
		answer    = "判断のばらつきを減らしたいです"
		modelText = "判断のばらつきを減らしたいです。"
	)
	plan := coachAttemptPlan(
		answercontract.OperatorOpen,
		answercontract.SlotPosition,
		pendingSubjectForOperator(answercontract.OperatorOpen),
		answer,
		answer,
		modelText,
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorOpen,
			answercontract.SlotPosition,
			answer,
			answer,
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)
	token, err := agent.PrepareNativeCoachState(uid, "", request)
	if err != nil {
		t.Fatalf("prepare Native coach state: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     answer,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("process staged Native coach answer: %v", err)
	}
	if result.Route == "planner-unavailable" ||
		result.Route == "verification-unavailable" ||
		result.AssistanceTarget != "respondent" ||
		result.RespondentStage != "restructure" ||
		result.CoachPhase != "complete" ||
		result.CoachAction != "complete" {
		t.Fatalf("next Native coach turn did not complete as respondent: %+v", result)
	}
	next, err := agent.codec.open(uid, result.StateToken)
	if err != nil {
		t.Fatalf("open completed state: %v", err)
	}
	if next.PendingAnswer.Active || next.PendingAnswer.NativeCoachScopeTag != "" {
		t.Fatalf("Native scope persisted after completion: %+v", next.PendingAnswer)
	}
}

func TestPreparedNativeCoachStateExchangesScopeForBoundRestatement(t *testing.T) {
	const (
		uid          = "native-coach-restatement-user"
		request      = "上司に目的を聞かれました。答え方を一問だけ手伝ってください"
		firstAnswer  = "背景として評価が人ごとに違います。判断のばらつきを減らしたいです"
		firstTarget  = "判断のばらつきを減らしたいです"
		different    = "売上を増やしたいです"
		plannerDraft = "判断のばらつきを減らしたいです。"
	)
	firstPlan := coachAttemptPlan(
		answercontract.OperatorOpen,
		answercontract.SlotPosition,
		pendingSubjectForOperator(answercontract.OperatorOpen),
		firstAnswer,
		firstTarget,
		plannerDraft,
	)
	differentPlan := coachAttemptPlan(
		answercontract.OperatorOpen,
		answercontract.SlotPosition,
		pendingSubjectForOperator(answercontract.OperatorOpen),
		different,
		different,
		different,
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, firstPlan)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorOpen,
			answercontract.SlotPosition,
			firstAnswer,
			firstTarget,
			answercontract.PositionLater,
		))},
		{body: encodePlan(t, differentPlan)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorOpen,
			answercontract.SlotPosition,
			different,
			different,
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)
	token, err := agent.PrepareNativeCoachState(uid, "", request)
	if err != nil {
		t.Fatalf("prepare Native coach state: %v", err)
	}

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     firstAnswer,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("process first staged answer: %v", err)
	}
	if first.CoachPhase != "awaiting_restatement" ||
		first.CoachAction != "restate" {
		t.Fatalf("first answer did not enter restatement: %+v", first)
	}
	restatementState, err := agent.codec.open(uid, first.StateToken)
	if err != nil {
		t.Fatalf("open restatement state: %v", err)
	}
	frame := restatementState.PendingAnswer
	if !frame.Active ||
		frame.Phase != respondent.CoachPhaseAwaitingRestatement ||
		frame.NativeCoachScopeTag != "" ||
		!validCoachRestatementTag(frame.RestatementTag) {
		t.Fatalf("Native scope was not exchanged for a restatement proof: %+v", frame)
	}

	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     different,
		StateToken:    first.StateToken,
	})
	if err != nil {
		t.Fatalf("process different restatement: %v", err)
	}
	if second.CoachPhase == "complete" || second.CoachAction == "complete" {
		t.Fatalf("different answer completed the bound restatement: %+v", second)
	}
	secondState, err := agent.codec.open(uid, second.StateToken)
	if err != nil {
		t.Fatalf("open rejected restatement state: %v", err)
	}
	if secondState.PendingAnswer.NativeCoachScopeTag != "" {
		t.Fatalf("Native scope returned after first substantive answer: %+v", secondState.PendingAnswer)
	}
}
