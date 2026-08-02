package conversation

// RefreshStateToken validates the caller-bound state and advances an empty or
// existing semantic envelope without adding current-turn content. The method
// is deliberately local and model-free: each native provider connection is
// one turn, while this encrypted token remains only a safe legacy-fallback and
// session lease. It never claims to encode a native-audio transcript.
func (agent *vertexAgent) RefreshStateToken(uid string, token string) (string, error) {
	if agent == nil || agent.codec == nil || !validUID(uid) {
		return "", ErrInvalidStateToken
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
	var err error
	if token != "" {
		state, err = agent.codec.open(uid, token)
		if err != nil {
			return "", err
		}
	}
	state, err = agent.codec.ensureSessionID(state)
	if err != nil || state.Turn >= maxStateTurns {
		return "", ErrInvalidStateToken
	}
	state.Turn++
	return agent.codec.seal(uid, state)
}
