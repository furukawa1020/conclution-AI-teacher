package conversation

import "github.com/furukawa1020/conclution-ai-teacher/internal/respondent"

// qarcCoachAttempt keeps the verifier-progress controller authoritative for
// terminal, fail-closed WAIT, and scope-release decisions. QARC may choose the
// fixed cue only after that controller has explicitly authorized an elicitation
// or restatement.
func qarcCoachAttempt(
	operator respondent.Operator,
	turn VoiceTurn,
	abstained bool,
	input respondent.AnswerControllerInput,
) respondent.CoachDecision {
	control := respondent.PlanAnswerSupport(input)
	policyDecision := respondent.GuideAttemptWithVerifierProgress(
		operator,
		abstained,
		input,
	)
	switch control.Action {
	case respondent.AnswerSupportComplete,
		respondent.AnswerSupportWait,
		respondent.AnswerSupportRelease:
		return policyDecision
	case respondent.AnswerSupportElicit,
		respondent.AnswerSupportRestate:
		// Continue below. The content-free controller has authorized one
		// bounded prompt; QARC chooses whether the current floor can carry it.
	default:
		// GuideAttemptWithVerifierProgress maps an unknown action to a bounded
		// release, so preserve that fail-closed result.
		return policyDecision
	}

	phase := policyDecision.Phase
	attempts := coachAttemptsFromVerifierProgress(input.Attempts)
	decision := qarcCoachIntervention(
		operator,
		phase,
		attempts,
		turn,
		true,
		false,
	)
	decision.Posterior = control.Posterior
	decision.VerifierProgressUpdated = true
	decision.ReasonCode = control.ReasonCode
	return decision
}

// qarcCoachPrompt handles a bound scope for which no verifier observation is
// available yet. Filler-only speech is represented as hesitation and therefore
// cannot consume an attempt or trigger release, even at the attempt limit.
func qarcCoachPrompt(
	operator respondent.Operator,
	phase respondent.CoachPhase,
	attempts uint8,
	turn VoiceTurn,
	hasSubstantiveAttempt bool,
) respondent.CoachDecision {
	return qarcCoachIntervention(
		operator,
		phase,
		attempts,
		turn,
		hasSubstantiveAttempt,
		!hasSubstantiveAttempt,
	)
}

func qarcCoachIntervention(
	operator respondent.Operator,
	phase respondent.CoachPhase,
	attempts uint8,
	turn VoiceTurn,
	hasSubstantiveAttempt bool,
	hesitationOnly bool,
) respondent.CoachDecision {
	base := respondent.CoachDecision{
		Phase:       normalizedQARCCoachPhase(phase),
		Action:      respondent.CoachActionNone,
		Attempts:    attempts,
		KeepPending: true,
	}
	qarcOperator, ok := respondent.ProjectQARCOperator(operator)
	if !ok {
		return base
	}
	endpointCommitted := !turn.Speculative &&
		turn.InputOrigin == InputOriginCommittedVoice &&
		turn.FloorEvidence == FloorEvidenceHybridCommitted
	commitProtected := turn.Speculative &&
		turn.InputOrigin == InputOriginProvisionalVoice &&
		turn.FloorEvidence == FloorEvidenceProvisionalCommitGate
	decision := respondent.DecideQARC(respondent.QARCObservation{
		Operator:              qarcOperator,
		ScopeBound:            true,
		OperatorConfidence:    1,
		EndpointCommitted:     endpointCommitted,
		CommitProtected:       commitProtected,
		HasSubstantiveAttempt: hasSubstantiveAttempt,
		HesitationOnly:        hesitationOnly,
		Attempts:              attempts,
		AudioCancelable:       turn.OutputCancelable,
	})
	if decision.Certificate.PolicyVersion != respondent.QARCPolicyVersion ||
		decision.Certificate.Action != decision.Action ||
		decision.Certificate.TemplateID != decision.TemplateID ||
		decision.Certificate.Slot != decision.Slot ||
		!decision.Certificate.NonInterference {
		return base
	}
	spokenReply, rendered := renderQARCCue(
		decision.TemplateID,
		decision.Slot,
	)
	if !rendered {
		return base
	}

	switch decision.Action {
	case respondent.QARCWait:
		return base
	case respondent.QARCRelease:
		return respondent.CoachDecision{
			Phase:       respondent.CoachPhaseBlocked,
			Action:      respondent.CoachActionRelease,
			SpokenReply: spokenReply,
			Attempts:    respondent.MaxCoachAttempts,
			KeepPending: false,
		}
	case respondent.QARCOperatorCue, respondent.QARCNeutralCue:
		if spokenReply == "" || !decision.Certificate.FloorProtected {
			return base
		}
		nextAttempts := attempts
		if nextAttempts < respondent.MaxCoachAttempts {
			nextAttempts++
		}
		base.SpokenReply = spokenReply
		base.Attempts = nextAttempts
		switch base.Phase {
		case respondent.CoachPhaseAwaitingRestatement:
			base.Action = respondent.CoachActionRestate
		case respondent.CoachPhaseExpanding:
			base.Action = respondent.CoachActionExpand
		default:
			base.Phase = respondent.CoachPhaseAwaitingAnswer
			base.Action = respondent.CoachActionElicit
		}
		return base
	default:
		return base
	}
}

func coachPhaseFromVerifierProgress(phase respondent.AnswerPhase) respondent.CoachPhase {
	switch phase {
	case respondent.AnswerPhaseNone:
		return respondent.CoachPhaseNone
	case respondent.AnswerPhaseAwaitingAnswer:
		return respondent.CoachPhaseAwaitingAnswer
	case respondent.AnswerPhaseAwaitingRestatement:
		return respondent.CoachPhaseAwaitingRestatement
	case respondent.AnswerPhaseExpanding:
		return respondent.CoachPhaseExpanding
	case respondent.AnswerPhaseComplete:
		return respondent.CoachPhaseComplete
	case respondent.AnswerPhaseBlocked:
		return respondent.CoachPhaseBlocked
	default:
		return respondent.CoachPhaseBlocked
	}
}

func coachAttemptsFromVerifierProgress(attempts respondent.AnswerAttemptLevel) uint8 {
	switch attempts {
	case respondent.AnswerAttemptNone:
		return 0
	case respondent.AnswerAttemptOne:
		return 1
	default:
		return respondent.MaxCoachAttempts
	}
}

func normalizedQARCCoachPhase(phase respondent.CoachPhase) respondent.CoachPhase {
	switch phase {
	case respondent.CoachPhaseAwaitingAnswer,
		respondent.CoachPhaseAwaitingRestatement,
		respondent.CoachPhaseExpanding,
		respondent.CoachPhaseBlocked:
		return phase
	default:
		return respondent.CoachPhaseAwaitingAnswer
	}
}

func qarcBoundQuestionScope(frame PendingAnswerFrame) bool {
	return frame.Active &&
		frame.QuestionInstanceTag != "" &&
		frame.QuestionContinuityTag != ""
}

// qarcTurnHasSpeechAuthority prevents a silent/text compatibility turn from
// entering a policy whose non-WAIT actions assume an exact endpoint or an
// exact-final speculative commit fence plus cancelable audio. Callers fall
// back to the verifier-progress policy when this authority is absent.
func qarcTurnHasSpeechAuthority(turn VoiceTurn) bool {
	if !turn.OutputCancelable {
		return false
	}
	return (!turn.Speculative &&
		turn.InputOrigin == InputOriginCommittedVoice) ||
		(turn.Speculative &&
			turn.InputOrigin == InputOriginProvisionalVoice)
}
