package conversation

import (
	"strconv"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

const (
	genericCoachLocalRoute = "respondent-open-slot-local"
	genericCoachOpeningCue = "今ある自分の答えを、一言だけそのままどうぞ。"
)

// completeGenericCoachStartLocal is the bounded fallback when an explicit
// answer-help request cannot be reduced to one audited question operator. It
// stores no question or answer text, calls no model, and cannot mint an A-first
// proof. The opaque one-turn scope only keeps the next utterance in respondent
// mode so the person's own A reaches the verifier instead of a ghostwriter.
func (agent *vertexAgent) completeGenericCoachStartLocal(
	uid string,
	state conversationState,
	turn VoiceTurn,
) (VoiceTurnResult, bool, error) {
	if agent == nil || agent.codec == nil || !agent.stateV2Writes ||
		!agent.retrievalPolicyEnabled || state.PendingAnswer.Active ||
		turn.PDF != nil || passiveAmbientTurn(turn) ||
		turn.RequestID == "" || turn.InputOrigin == InputOriginUnknown ||
		!turnExpectsResponse(turn) || !explicitCoachOptIn(turn.Utterance) ||
		explicitReportedQuestionAndOwnAttempt(turn.Utterance) {
		return VoiceTurnResult{}, false, nil
	}
	if !turn.Speculative && turn.InputOrigin != InputOriginCommittedVoice {
		return VoiceTurnResult{}, false, nil
	}
	if turn.Speculative &&
		(!turn.OutputCancelable ||
			turn.InputOrigin != InputOriginProvisionalVoice ||
			turn.FloorEvidence != FloorEvidenceProvisionalCommitGate) {
		return VoiceTurnResult{}, false, nil
	}

	scopeTag := agent.nativeCoachScopeTag(
		state.SessionID + "\x00" + strconv.Itoa(state.Turn+1),
	)
	if scopeTag == "" {
		return VoiceTurnResult{}, true, ErrInvalidStateToken
	}
	expansionOperator := answercontract.Operator(
		respondent.ExpansionOperator(
			respondent.Operator(answercontract.OperatorOpen),
		),
	)
	frame, err := normalizePendingAnswer(PendingAnswerFrame{
		Active:            true,
		Operator:          answercontract.OperatorOpen,
		Subject:           pendingSubjectForOperator(answercontract.OperatorOpen),
		RequiredSlots:     []answercontract.RequiredSlot{answercontract.SlotPosition},
		ExpansionOperator: expansionOperator,
		Phase:             respondent.CoachPhaseAwaitingAnswer,
		// Opening the answer-shaped slot is not a failed answer attempt. The
		// person's first substantive A-later turn must still receive exactly one
		// bounded restatement invitation before the controller may release.
		Attempts:            0,
		NativeCoachScopeTag: scopeTag,
	})
	if err != nil {
		return VoiceTurnResult{}, true, ErrInvalidStateToken
	}
	profile := conversationSupportValue(state.Support)
	profile.CompanionOnly = false
	profile.QuestionCooldown = 0
	intervention := ArbiterDecision{
		Benefit:          1,
		InterruptionCost: 0,
		Urgency:          0,
		Confidence:       1,
		Score:            1,
		Act:              "clarify",
	}
	nextState := conversationState{
		SessionID:           state.SessionID,
		Turn:                state.Turn + 1,
		Graph:               state.Graph,
		ConversationSummary: "",
		DocumentSummary:     "",
		PendingAnswer:       frame,
		Support:             compactConversationSupport(profile),
		SelfCorrectionGrace: state.SelfCorrectionGrace,
		LastIntervention:    intervention,
	}
	var stateToken string
	if turn.RequestID != "" {
		stateToken, err = agent.sealVoiceCheckpointState(
			uid,
			turn.RequestID,
			frame.NativeCoachScopeTag,
			nextState,
		)
	} else {
		stateToken, err = agent.sealState(uid, nextState)
	}
	if err != nil {
		return VoiceTurnResult{}, true, err
	}
	return VoiceTurnResult{
		SchemaVersion:        SchemaVersion,
		Domain:               "other",
		Intent:               "practice",
		AssistanceTarget:     "respondent",
		RespondentStage:      "awaiting_answer",
		CoachPhase:           string(respondent.CoachPhaseAwaitingAnswer),
		CoachAction:          string(respondent.CoachActionElicit),
		AnswerProof:          AnswerProofNone,
		AnswerProofCandidate: AnswerProofNone,
		ResearchStatus:       "none",
		ResearchRecords:      []ResearchRecord{},
		ArgumentStructure:    "direct_answer",
		InterventionPolicy:   "coach",
		SpokenReply:          genericCoachOpeningCue,
		Confidence:           1,
		Intervention:         intervention,
		SelfCorrectionGrace:  state.SelfCorrectionGrace,
		AnswerContract: answercontract.Metrics{
			CommitmentFrontPosition: answercontract.PositionAbsent,
		},
		Route:              genericCoachLocalRoute,
		NeedsClarification: true,
		StateToken:         stateToken,
	}, true, nil
}
