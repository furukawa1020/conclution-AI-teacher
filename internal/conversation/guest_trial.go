package conversation

// This schema is read-first. No production route issues it until the trial
// API, transport and UI all enforce the same question and assistance scope.
// These are observations of one trial, never a learning or ability score.
type guestTrial struct {
	Version  int    `json:"version"`
	Question string `json:"question"`
	Scope    string `json:"scope"`
	Assisted bool   `json:"assisted"`
	Outcome  string `json:"outcome"`
}

const (
	guestTransferQuestionID = "rest_activity_v1"
	guestTransferQuestion   = "少し休めるとしたら、何をしたいですか？"
	guestTrialPending       = "pending"
	guestTrialAnswerFirst   = "answer_first"
	guestTrialUnverified    = "unverified"
	guestTrialStopped       = "stopped"
)

// Every existing consumer uses this boundary during the reader-first rollout.
// Merely understanding the new schema must not authorize ordinary/native
// generation, memory extraction or adaptation from an unassisted trial.
func (agent *vertexAgent) openConversationState(uid, token string) (conversationState, error) {
	if agent == nil || agent.codec == nil {
		return conversationState{}, ErrInvalidStateToken
	}
	state, err := agent.codec.open(uid, token)
	if err != nil {
		return conversationState{}, err
	}
	if state.GuestTrial != nil {
		return conversationState{}, ErrInvalidStateToken
	}
	return state, nil
}

func normalizeGuestTrial(trial *guestTrial) (*guestTrial, error) {
	if trial == nil {
		return nil, nil
	}
	if trial.Version != 1 || trial.Question != guestTransferQuestionID || !validCoachControlTag(trial.Scope) {
		return nil, ErrInvalidStateToken
	}
	switch trial.Outcome {
	case guestTrialPending, guestTrialAnswerFirst, guestTrialUnverified, guestTrialStopped:
	default:
		return nil, ErrInvalidStateToken
	}
	copy := *trial
	return &copy, nil
}

// A new session ID and a fresh state, rather than selective deletion, prevent
// prior summaries, proof tags, adaptation and support history from influencing
// the unassisted question. The previous state is not mutated.
func (agent *vertexAgent) newGuestTransferState() (conversationState, error) {
	if agent == nil || agent.codec == nil || len(agent.continuityKey) == 0 {
		return conversationState{}, ErrInvalidStateToken
	}
	state, err := agent.codec.ensureSessionID(conversationState{
		Turn:             1,
		PendingAnswer:    emptyPendingAnswer(),
		LastIntervention: ArbiterDecision{Act: "silent"},
	})
	if err != nil {
		return conversationState{}, err
	}
	state.GuestTrial = &guestTrial{
		Version: 1, Question: guestTransferQuestionID,
		Scope:   agent.coachQuestionInstanceTag(state.SessionID, guestTransferQuestion),
		Outcome: guestTrialPending,
	}
	return normalizeConversationState(state)
}

// Only an explicit, question-scoped control may call this transition. Never
// infer assistance consent from a model output or from silence. Once assisted,
// the trial cannot be reclassified as unassisted, even at completion.
func requestGuestTrialHelp(trial *guestTrial, scope string) (*guestTrial, error) {
	next, err := activeGuestTrial(trial, scope)
	if err != nil {
		return nil, err
	}
	next.Assisted = true
	return next, nil
}

func activeGuestTrial(trial *guestTrial, scope string) (*guestTrial, error) {
	next, err := normalizeGuestTrial(trial)
	if err != nil || next == nil || next.Scope != scope || next.Outcome != guestTrialPending {
		return nil, ErrInvalidStateToken
	}
	return next, nil
}

// The proof must come from the existing independent question-bound verifier,
// for this scope and finalized user voice. This does not mint a proof itself.
func completeGuestTrial(trial *guestTrial, scope string, turn VoiceTurn, frame PendingAnswerFrame, proof AnswerProof) (*guestTrial, error) {
	next, err := activeGuestTrial(trial, scope)
	if err != nil {
		return nil, err
	}
	if !turn.GuestExperience || turn.Speculative || turn.InputOrigin != InputOriginCommittedVoice || collapseSpace(turn.Utterance) == "" {
		return nil, ErrInvalidTurn
	}
	switch proof {
	case AnswerProofQuestionBoundInputAnswerFirst:
		if frame.QuestionInstanceTag != next.Scope {
			return nil, ErrInvalidStateToken
		}
		next.Outcome = guestTrialAnswerFirst
	case AnswerProofNone:
		next.Outcome = guestTrialUnverified
	default:
		return nil, ErrInvalidTurn
	}
	return next, nil
}

func stopGuestTrial(trial *guestTrial, scope string) (*guestTrial, error) {
	next, err := activeGuestTrial(trial, scope)
	if err != nil {
		return nil, err
	}
	next.Outcome = guestTrialStopped
	return next, nil
}
