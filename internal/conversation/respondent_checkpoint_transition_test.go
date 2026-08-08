package conversation

import (
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestRespondentCheckpointTransitionBindsRequestAndScope(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true
	const (
		uid       = "uid-checkpoint-request-binding"
		requestID = "request-checkpoint-one"
	)
	preparedToken, requiresStaged, err := agent.PrepareNativeState(uid, "")
	if err != nil || requiresStaged {
		t.Fatalf("prepare initial native state: staged=%v err=%v", requiresStaged, err)
	}
	prepared, err := agent.codec.open(uid, preparedToken)
	if err != nil {
		t.Fatalf("open prepared state: %v", err)
	}
	state := checkpointTestState(t, agent, 2, prepared)
	scope := voiceCheckpointScopeTag(state.PendingAnswer)
	token, err := agent.sealVoiceCheckpointState(uid, requestID, scope, state)
	if err != nil {
		t.Fatalf("seal checkpoint state: %v", err)
	}
	if err := agent.ValidateRespondentCheckpointTransition(
		uid,
		"",
		preparedToken,
		token,
		requestID,
		"respondent",
		"awaiting_answer",
		"awaiting_answer",
		"elicit",
	); err != nil {
		t.Fatalf("validate initial transition: %v", err)
	}
	if err := agent.ValidateRespondentCheckpointTransition(
		uid,
		"",
		preparedToken,
		token,
		"different-request",
		"respondent",
		"awaiting_answer",
		"awaiting_answer",
		"elicit",
	); err == nil {
		t.Fatal("checkpoint was reusable under a different request")
	}
}

func TestRespondentCheckpointTransitionAuthenticatesContinuingPreparedHop(
	t *testing.T,
) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true
	const (
		uid       = "uid-checkpoint-prepared-hop"
		requestID = "request-checkpoint-prepared-hop"
	)
	requestToken, err := agent.RefreshStateToken(uid, "")
	if err != nil {
		t.Fatalf("create continuing request state: %v", err)
	}
	preparedToken, requiresStaged, err := agent.PrepareNativeState(uid, requestToken)
	if err != nil || requiresStaged {
		t.Fatalf("prepare continuing state: staged=%v err=%v", requiresStaged, err)
	}
	prepared, err := agent.codec.open(uid, preparedToken)
	if err != nil {
		t.Fatalf("open prepared state: %v", err)
	}
	next := checkpointTestState(t, agent, prepared.Turn+1, prepared)
	scope := voiceCheckpointScopeTag(next.PendingAnswer)
	nextToken, err := agent.sealVoiceCheckpointState(
		uid,
		requestID,
		scope,
		next,
	)
	if err != nil {
		t.Fatalf("seal continuing checkpoint: %v", err)
	}
	if err := agent.ValidateRespondentCheckpointTransition(
		uid,
		requestToken,
		preparedToken,
		nextToken,
		requestID,
		"respondent",
		"awaiting_answer",
		"awaiting_answer",
		"elicit",
	); err != nil {
		t.Fatalf("validate continuing prepared hop: %v", err)
	}

	otherRequest, err := agent.RefreshStateToken(uid, "")
	if err != nil {
		t.Fatalf("create unrelated request state: %v", err)
	}
	otherPrepared, _, err := agent.PrepareNativeState(uid, otherRequest)
	if err != nil {
		t.Fatalf("create unrelated prepared state: %v", err)
	}
	otherPreparedState, err := agent.codec.open(uid, otherPrepared)
	if err != nil {
		t.Fatalf("open unrelated prepared state: %v", err)
	}
	otherNext := checkpointTestState(
		t,
		agent,
		otherPreparedState.Turn+1,
		otherPreparedState,
	)
	otherNextToken, err := agent.sealVoiceCheckpointState(
		uid,
		requestID,
		voiceCheckpointScopeTag(otherNext.PendingAnswer),
		otherNext,
	)
	if err != nil {
		t.Fatalf("seal unrelated checkpoint: %v", err)
	}
	if err := agent.ValidateRespondentCheckpointTransition(
		uid,
		requestToken,
		otherPrepared,
		otherNextToken,
		requestID,
		"respondent",
		"awaiting_answer",
		"awaiting_answer",
		"elicit",
	); err == nil {
		t.Fatal("checkpoint accepted a complete prepared chain from another session")
	}
}

func TestRespondentCheckpointTransitionRejectsScopeSwap(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true
	const (
		uid       = "uid-checkpoint-scope-swap"
		requestID = "request-checkpoint-next"
	)
	previous := checkpointTestState(t, agent, 1, conversationState{})
	previousToken, err := agent.sealState(uid, previous)
	if err != nil {
		t.Fatalf("seal previous: %v", err)
	}

	next := previous
	next.Turn++
	next.PendingAnswer.NativeCoachScopeTag = agent.nativeCoachScopeTag("other-scope")
	nextScope := voiceCheckpointScopeTag(next.PendingAnswer)
	nextToken, err := agent.sealVoiceCheckpointState(
		uid,
		requestID,
		nextScope,
		next,
	)
	if err != nil {
		t.Fatalf("seal swapped scope: %v", err)
	}
	if err := agent.ValidateRespondentCheckpointTransition(
		uid,
		previousToken,
		previousToken,
		nextToken,
		requestID,
		"respondent",
		"awaiting_answer",
		"awaiting_answer",
		"elicit",
	); err == nil {
		t.Fatal("checkpoint accepted a different active answer scope")
	}
}

func TestRespondentCheckpointTransitionCarriesCompletedQuestionBinding(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true
	const (
		uid       = "uid-checkpoint-complete"
		requestID = "request-checkpoint-complete"
	)
	previous := checkpointTestState(t, agent, 1, conversationState{})
	previousScope := voiceCheckpointScopeTag(previous.PendingAnswer)
	previousToken, err := agent.sealState(uid, previous)
	if err != nil {
		t.Fatalf("seal previous: %v", err)
	}
	next := previous
	next.Turn++
	next.PendingAnswer = emptyPendingAnswer()
	nextToken, err := agent.sealVoiceCheckpointState(
		uid,
		requestID,
		previousScope,
		next,
	)
	if err != nil {
		t.Fatalf("seal completion: %v", err)
	}
	if err := agent.ValidateRespondentCheckpointTransition(
		uid,
		previousToken,
		previousToken,
		nextToken,
		requestID,
		"respondent",
		"restructure",
		"complete",
		"complete",
	); err != nil {
		t.Fatalf("validate completion: %v", err)
	}
}

func checkpointTestState(
	t *testing.T,
	agent *vertexAgent,
	turn int,
	base conversationState,
) conversationState {
	t.Helper()
	if base.SessionID == "" {
		var err error
		base, err = agent.codec.ensureSessionID(base)
		if err != nil {
			t.Fatalf("ensure session: %v", err)
		}
	}
	base.Turn = turn
	base.Graph = ThoughtStateGraph{
		Goals:          []string{},
		Claims:         []string{},
		Grounds:        []string{},
		Assumptions:    []string{},
		Constraints:    []string{},
		OpenLoops:      []string{},
		Contradictions: []string{},
		Decisions:      []string{},
	}
	base.PendingAnswer = PendingAnswerFrame{
		Active:              true,
		Operator:            answercontract.OperatorOpen,
		Subject:             pendingSubjectForOperator(answercontract.OperatorOpen),
		RequiredSlots:       []answercontract.RequiredSlot{answercontract.SlotPosition},
		ExpansionOperator:   answercontract.OperatorCause,
		Phase:               respondent.CoachPhaseAwaitingAnswer,
		Attempts:            1,
		NativeCoachScopeTag: agent.nativeCoachScopeTag("current-scope"),
	}
	base.LastIntervention = ArbiterDecision{Confidence: 1, Act: "silent"}
	var err error
	base.PendingAnswer, err = normalizePendingAnswer(base.PendingAnswer)
	if err != nil {
		t.Fatalf("normalize checkpoint frame: %v; frame=%#v", err, base.PendingAnswer)
	}
	if _, err = normalizeConversationState(base); err != nil {
		t.Fatalf("normalize checkpoint state: %v; state=%#v", err, base)
	}
	return base
}
