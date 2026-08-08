package conversation

import (
	"strings"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
	"golang.org/x/text/unicode/norm"
)

const qarcLocalRoute = "respondent-qarc-local"

type qarcRetrievalScope struct {
	operator         answercontract.Operator
	questionSubject  string
	questionInstance string
}

// completeQARCRetrievalStartLocal opens only a new, explicitly requested
// reported-question exercise. It makes no provider call and never receives an
// answer candidate. Provisional work is useful only because voiceflow keeps
// the decision and every synthesized byte behind its exact-final commit gate.
func (agent *vertexAgent) completeQARCRetrievalStartLocal(
	uid string,
	state conversationState,
	turn VoiceTurn,
) (VoiceTurnResult, bool, error) {
	if agent == nil ||
		!agent.stateV2Writes ||
		!agent.answerProofWrites ||
		!agent.retrievalPolicyEnabled ||
		state.PendingAnswer.Active ||
		turn.PDF != nil ||
		turn.ResearchDisabled ||
		passiveAmbientTurn(turn) ||
		!turn.OutputCancelable ||
		!turnExpectsResponse(turn) ||
		turn.RequestID == "" ||
		!explicitCoachOptIn(turn.Utterance) ||
		explicitReportedQuestionAndOwnAttempt(turn.Utterance) {
		return VoiceTurnResult{}, false, nil
	}

	endpointCommitted := !turn.Speculative &&
		turn.InputOrigin == InputOriginCommittedVoice &&
		turn.FloorEvidence == FloorEvidenceHybridCommitted
	commitProtected := turn.Speculative &&
		turn.InputOrigin == InputOriginProvisionalVoice &&
		turn.FloorEvidence == FloorEvidenceProvisionalCommitGate
	committedFloorPending := !turn.Speculative &&
		turn.InputOrigin == InputOriginCommittedVoice &&
		turn.FloorEvidence == FloorEvidenceUnknown
	if !endpointCommitted && !commitProtected && !committedFloorPending {
		return VoiceTurnResult{}, false, nil
	}

	scope, ok := boundedQARCRetrievalScope(turn.Utterance)
	if !ok {
		return VoiceTurnResult{}, false, nil
	}
	precisionPlan := modelPlan{ResearchAction: "none"}
	precisionPlan.AnswerContract.QuestionFrame.Operator = scope.operator
	if requiresFailClosedPrecision(turn, precisionPlan) {
		return VoiceTurnResult{}, false, nil
	}
	questionContinuityTag := agent.coachQuestionContinuityTag(scope.questionSubject)
	questionInstanceTag := agent.coachQuestionInstanceTag(
		state.SessionID,
		scope.questionInstance,
	)
	if questionContinuityTag == "" || questionInstanceTag == "" {
		return VoiceTurnResult{}, false, nil
	}

	qarcOperator, projected := respondent.ProjectQARCOperator(
		respondent.Operator(scope.operator),
	)
	if !projected {
		return VoiceTurnResult{}, false, nil
	}
	qarcDecision := respondent.DecideQARC(respondent.QARCObservation{
		Operator:           qarcOperator,
		ScopeBound:         true,
		OperatorConfidence: 1,
		EndpointCommitted:  endpointCommitted,
		CommitProtected:    commitProtected,
		NewQuestionScope:   true,
		AudioCancelable:    turn.OutputCancelable,
	})
	spokenReply, rendered := renderQARCCue(
		qarcDecision.TemplateID,
		qarcDecision.Slot,
	)
	target, targetOK := answercontract.TargetSlot(scope.operator)
	if !targetOK ||
		!rendered ||
		qarcDecision.Certificate.PolicyVersion != respondent.QARCPolicyVersion ||
		qarcDecision.Certificate.Action != qarcDecision.Action ||
		qarcDecision.Certificate.TemplateID != qarcDecision.TemplateID ||
		qarcDecision.Certificate.Slot != qarcDecision.Slot ||
		!qarcDecision.Certificate.NonInterference ||
		!qarcDecision.Certificate.FloorProtected {
		return VoiceTurnResult{}, false, nil
	}
	coachAction := respondent.CoachActionNone
	storedAttempts := uint8(0)
	switch qarcDecision.Action {
	case respondent.QARCWait:
		if spokenReply != "" {
			return VoiceTurnResult{}, false, nil
		}
	case respondent.QARCOperatorCue:
		if spokenReply == "" {
			return VoiceTurnResult{}, false, nil
		}
		coachAction = respondent.CoachActionElicit
		// This cue opens the answer slot; it does not consume the person's first
		// answer attempt. A first A-later observation therefore still gets the
		// controller's single fixed restatement invitation.
		storedAttempts = 0
	default:
		// After the explicit question has been bound, fail closed locally. A
		// controller anomaly must never fall through to model-authored speech.
		spokenReply = ""
	}

	requiredSlots := []answercontract.RequiredSlot{target}
	if scope.operator == answercontract.OperatorQuantity {
		requiredSlots = append(requiredSlots, answercontract.SlotUnit)
	}
	frame := PendingAnswerFrame{
		Active:        true,
		Operator:      scope.operator,
		Subject:       pendingSubjectForOperator(scope.operator),
		RequiredSlots: requiredSlots,
		ExpansionOperator: answercontract.Operator(respondent.ExpansionOperator(
			respondent.Operator(scope.operator),
		)),
		Phase:                 respondent.CoachPhaseAwaitingAnswer,
		Attempts:              storedAttempts,
		QuestionContinuityTag: questionContinuityTag,
		QuestionInstanceTag:   questionInstanceTag,
	}
	frame, err := normalizePendingAnswer(frame)
	if err != nil {
		return VoiceTurnResult{}, false, nil
	}

	hasCue := spokenReply != ""
	intervention := ArbiterDecision{
		Benefit:          qarcBinaryScore(hasCue),
		InterruptionCost: 0,
		Urgency:          0,
		Confidence:       1,
		Score:            qarcBinaryScore(hasCue),
		Act:              qarcOpeningAct(hasCue),
	}
	profile := conversationSupportValue(state.Support)
	profile.CompanionOnly = false
	profile.QuestionCooldown = 0
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
	stateToken, err := agent.sealVoiceCheckpointState(
		uid,
		turn.RequestID,
		frame.QuestionInstanceTag,
		nextState,
	)
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
		CoachAction:          string(coachAction),
		AnswerProof:          AnswerProofNone,
		AnswerProofCandidate: AnswerProofNone,
		ResearchStatus:       "none",
		ResearchRecords:      []ResearchRecord{},
		ArgumentStructure:    "direct_answer",
		InterventionPolicy:   "coach",
		SpokenReply:          spokenReply,
		Confidence:           1,
		Intervention:         intervention,
		SelfCorrectionGrace:  state.SelfCorrectionGrace,
		AnswerContract: answercontract.Metrics{
			CommitmentFrontPosition: answercontract.PositionAbsent,
		},
		Route:              qarcLocalRoute,
		NeedsClarification: true,
		StateToken:         stateToken,
	}, true, nil
}

func qarcBinaryScore(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func qarcOpeningAct(hasCue bool) string {
	if hasCue {
		return "clarify"
	}
	return "silent"
}

// boundedQARCRetrievalScope accepts only one latest reported question whose
// finite operator is unique. Its subject is recovered by the existing strict
// question-boundary grammar, not by a model. Longest-match selection keeps a
// modifier such as "新システムの" attached; equal longest matches fail closed.
func boundedQARCRetrievalScope(utterance string) (qarcRetrievalScope, bool) {
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	reportEnd := coachReportedQuestionEndOutsideQuote(source)
	if reportEnd <= 0 || reportEnd > len(source) ||
		coachReportedQuestionEndOutsideQuote(source[reportEnd:]) >= 0 ||
		!qarcReportedQuestionTailIsExplicitOptIn(source, reportEnd) {
		return qarcRetrievalScope{}, false
	}
	reported := strings.TrimSpace(source[:reportEnd])
	if reported == "" || utf8.RuneCountInString(reported) > 320 {
		return qarcRetrievalScope{}, false
	}
	operator, ok := boundedReportedCoachQuestionOperator(reported)
	if !ok {
		return qarcRetrievalScope{}, false
	}

	runes := []rune(reported)
	seen := make(map[string]struct{}, len(runes)*4)
	best := qarcRetrievalScope{}
	bestRunes := 0
	ambiguousBest := false
	for start := 0; start < len(runes); start++ {
		limit := start + 24
		if limit > len(runes) {
			limit = len(runes)
		}
		for end := start + 1; end <= limit; end++ {
			candidate := strings.ToLower(collapseSpace(
				norm.NFKC.String(string(runes[start:end])),
			))
			if candidate == "" {
				continue
			}
			if _, duplicate := seen[candidate]; duplicate {
				continue
			}
			seen[candidate] = struct{}{}
			subject, subjectOK := boundedCoachContinuityAnchor(
				candidate,
				utterance,
			)
			if !subjectOK || subject != candidate {
				continue
			}
			instance, instanceOK := boundedReportedCoachQuestionInstanceAnchor(
				utterance,
				subject,
			)
			if !instanceOK {
				continue
			}
			instanceOperator, operatorOK :=
				boundedReportedCoachQuestionOperator(instance)
			if !operatorOK || instanceOperator != operator {
				continue
			}
			length := utf8.RuneCountInString(subject)
			switch {
			case length > bestRunes:
				best = qarcRetrievalScope{
					operator:         operator,
					questionSubject:  subject,
					questionInstance: instance,
				}
				bestRunes = length
				ambiguousBest = false
			case length == bestRunes && bestRunes > 0 &&
				(subject != best.questionSubject ||
					instance != best.questionInstance):
				ambiguousBest = true
			}
		}
	}
	if bestRunes == 0 || ambiguousBest {
		return qarcRetrievalScope{}, false
	}
	return best, true
}

// qarcReportedQuestionTailIsExplicitOptIn keeps the local fast path narrower
// than the general coach detector: every non-empty clause after the reported
// question must independently be an explicit request for coaching. A bare
// answer, explanation, or other payload therefore routes through the model
// path instead of being silently discarded.
func qarcReportedQuestionTailIsExplicitOptIn(source string, reportEnd int) bool {
	if reportEnd < 0 || reportEnd > len(source) {
		return false
	}
	sawClause := false
	for _, clause := range strings.FieldsFunc(source[reportEnd:], func(r rune) bool {
		switch r {
		case '。', '！', '？', '!', '?', '.', '\n', '\r', ';', '；':
			return true
		default:
			return false
		}
	}) {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		sawClause = true
		if !explicitCoachOptInClause(clause) {
			return false
		}
	}
	return sawClause
}
