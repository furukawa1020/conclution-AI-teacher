package conversation

import "reflect"

// PrepareNativeState validates the caller-bound state before any native audio
// provider is opened. A signed active answer scope remains staged authority:
// return the original token without advancing or resealing it so a modified
// client cannot bypass respondent-coach continuity by setting NativeAudio.
func (agent *vertexAgent) PrepareNativeState(
	uid string,
	token string,
) (string, bool, error) {
	state, err := agent.openNativeState(uid, token)
	if err != nil {
		return "", false, err
	}
	if state.PendingAnswer.Active {
		return token, true, nil
	}
	refreshed, err := agent.advanceNativeState(uid, state)
	if err != nil {
		return "", false, err
	}
	return refreshed, false, nil
}

// RefreshStateToken validates the caller-bound state and advances an empty or
// existing semantic envelope without adding current-turn content. The method
// is deliberately local and model-free: each native provider connection is
// one turn, while this encrypted token remains only a safe legacy-fallback and
// session lease. It never claims to encode a native-audio transcript.
func (agent *vertexAgent) RefreshStateToken(uid string, token string) (string, error) {
	state, err := agent.openNativeState(uid, token)
	if err != nil {
		return "", err
	}
	return agent.advanceNativeState(uid, state)
}

func (agent *vertexAgent) openNativeState(
	uid string,
	token string,
) (conversationState, error) {
	if agent == nil || agent.codec == nil || !validUID(uid) {
		return conversationState{}, ErrInvalidStateToken
	}
	state := conversationState{
		Graph: ThoughtStateGraph{
			Goals:          []string{},
			Claims:         []string{},
			Grounds:        []string{},
			Assumptions:    []string{},
			Constraints:    []string{},
			OpenLoops:      []string{},
			Contradictions: []string{},
			Decisions:      []string{},
		},
		PendingAnswer: emptyPendingAnswer(),
		LastIntervention: ArbiterDecision{
			Act: "silent",
		},
	}
	if token != "" {
		var err error
		state, err = agent.codec.open(uid, token)
		if err != nil {
			return conversationState{}, err
		}
	}
	return state, nil
}

func (agent *vertexAgent) advanceNativeState(
	uid string,
	state conversationState,
) (string, error) {
	if agent == nil || agent.codec == nil || !validUID(uid) {
		return "", ErrInvalidStateToken
	}
	var err error
	state, err = agent.codec.ensureSessionID(state)
	if err != nil || state.Turn >= maxStateTurns {
		return "", ErrInvalidStateToken
	}
	state.Turn++
	return agent.codec.seal(uid, state)
}

// validatePreparedNativeStateTransition authenticates the process-local state
// preparation hop independently of the Native Audio service. A non-pending
// request may advance exactly one content-free turn; an active answer scope
// must remain the exact caller token. This proves the actual previous state
// used by a caption handoff without persisting a transcript or answer.
func (agent *vertexAgent) validatePreparedNativeStateTransition(
	uid string,
	requestToken string,
	preparedToken string,
) (conversationState, error) {
	if agent == nil || agent.codec == nil || preparedToken == "" {
		return conversationState{}, ErrInvalidStateToken
	}
	prepared, err := agent.codec.open(uid, preparedToken)
	if err != nil {
		return conversationState{}, ErrInvalidStateToken
	}

	requested, err := agent.openNativeState(uid, requestToken)
	if err != nil {
		return conversationState{}, ErrInvalidStateToken
	}
	if requestToken != "" && requested.PendingAnswer.Active {
		if preparedToken != requestToken {
			return conversationState{}, ErrInvalidStateToken
		}
		return prepared, nil
	}
	if requested.Turn >= maxStateTurns || prepared.Turn != requested.Turn+1 {
		return conversationState{}, ErrInvalidStateToken
	}
	if requestToken != "" && prepared.SessionID != requested.SessionID {
		return conversationState{}, ErrInvalidStateToken
	}

	expected := requested
	expected.Turn++
	if requestToken == "" {
		expected.SessionID = prepared.SessionID
	}
	expected.Version = prepared.Version
	expected.IssuedAt = prepared.IssuedAt
	expected.ExpiresAt = prepared.ExpiresAt
	expected, err = normalizeConversationState(expected)
	if err != nil || !reflect.DeepEqual(expected, prepared) {
		return conversationState{}, ErrInvalidStateToken
	}
	return prepared, nil
}
