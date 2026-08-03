package conversation

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
	"golang.org/x/text/unicode/norm"
)

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

// PrepareNativeCoachState synchronously establishes the only signed authority
// for a newly detected Native Respondent Coach turn. The frame is deliberately
// generic: it records an open answer position and a domain-separated opaque
// marker for this consent-gated issuance, never a fingerprint of the
// transcript, external question, generated prompt, or model-selected operator.
func (agent *vertexAgent) PrepareNativeCoachState(
	uid string,
	token string,
	utterance string,
) (string, error) {
	if agent == nil || agent.codec == nil || !agent.stateV2Writes ||
		!validUID(uid) {
		return "", ErrInvalidStateToken
	}
	normalizedUtterance := strings.ToLower(
		collapseSpace(norm.NFKC.String(utterance)),
	)
	if normalizedUtterance == "" ||
		utf8.RuneCountInString(normalizedUtterance) > MaxUtteranceRunes ||
		!explicitCoachOptIn(normalizedUtterance) {
		return "", ErrInvalidTurn
	}

	state, err := agent.openNativeState(uid, token)
	if err != nil || state.PendingAnswer.Active {
		if err != nil {
			return "", err
		}
		return "", ErrInvalidStateToken
	}
	state, err = agent.codec.ensureSessionID(state)
	if err != nil || state.Turn >= maxStateTurns {
		return "", ErrInvalidStateToken
	}

	// Bind this opaque marker only to the already-random session and the new
	// generic turn. Explicit consent is proven by this deterministic issuance
	// path and the enclosing authenticated state; no question or utterance
	// fingerprint belongs in the persisted capability.
	scopeAnchor := state.SessionID + "\x00" + strconv.Itoa(state.Turn+1)
	scopeTag := agent.nativeCoachScopeTag(scopeAnchor)
	if scopeTag == "" {
		return "", ErrInvalidStateToken
	}
	expansionOperator := answercontract.Operator(
		respondent.ExpansionOperator(
			respondent.Operator(answercontract.OperatorOpen),
		),
	)
	frame, err := normalizePendingAnswer(PendingAnswerFrame{
		Active:              true,
		Operator:            answercontract.OperatorOpen,
		Subject:             pendingSubjectForOperator(answercontract.OperatorOpen),
		RequiredSlots:       []answercontract.RequiredSlot{answercontract.SlotPosition},
		ExpansionOperator:   expansionOperator,
		Phase:               respondent.CoachPhaseAwaitingAnswer,
		Attempts:            0,
		NativeCoachScopeTag: scopeTag,
	})
	if err != nil {
		return "", ErrInvalidStateToken
	}
	profile := conversationSupportValue(state.Support)
	profile.CompanionOnly = false
	profile.QuestionCooldown = 0
	state.Support = compactConversationSupport(profile)
	state.PendingAnswer = frame
	state.Turn++
	sealed, err := agent.sealState(uid, state)
	if err != nil {
		return "", err
	}
	verified, err := agent.codec.open(uid, sealed)
	if err != nil || verified.Turn != state.Turn ||
		!verified.PendingAnswer.Active ||
		verified.PendingAnswer.Operator != answercontract.OperatorOpen ||
		verified.PendingAnswer.Phase != respondent.CoachPhaseAwaitingAnswer ||
		verified.PendingAnswer.NativeCoachScopeTag != scopeTag {
		return "", ErrInvalidStateToken
	}
	return sealed, nil
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
