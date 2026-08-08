package conversation

import (
	"math"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

// verifierProgressInput is the sole adapter from content-bearing verifier
// objects to the respondent policy's finite input alphabet. It deliberately
// reads only control enums, booleans, and bounded metrics; reconstructed answer
// text and evidence spans never cross this boundary.
func verifierProgressInput(
	prior respondent.VerifierProgressPosterior,
	gate respondent.Assessment,
	critic answercontract.Assessment,
	phase respondent.CoachPhase,
	attempts uint8,
	oneShot bool,
	hesitation bool,
	verificationAvailable bool,
) respondent.AnswerControllerInput {
	return respondent.AnswerControllerInput{
		Prior:                 prior,
		GateSignal:            projectRespondentVerifierSignal(gate),
		CriticSignal:          projectCriticVerifierSignal(critic),
		Phase:                 projectVerifierProgressPhase(phase),
		Attempts:              projectVerifierProgressAttempts(attempts),
		OneShot:               oneShot,
		Hesitation:            hesitation,
		VerificationAvailable: verificationAvailable,
	}
}

func projectRespondentVerifierSignal(
	assessment respondent.Assessment,
) respondent.VerifierSignal {
	if !validRespondentVerifierOutcome(assessment.Outcome) ||
		!validRespondentVerifierPosition(assessment.OriginalCommitmentPosition) ||
		!validRespondentVerifierPosition(assessment.CommitmentPosition) ||
		!validVerifierProbability(assessment.OriginalTargetCoverage) ||
		!validVerifierProbability(assessment.TargetCoverage) {
		return respondent.VerifierSignalInvalid
	}
	for _, issue := range assessment.Issues {
		switch issue {
		case respondent.IssueInvalidContract:
			return respondent.VerifierSignalInvalid
		case respondent.IssueContentChanged:
			return respondent.VerifierSignalRejected
		}
	}
	if assessment.Outcome == respondent.OutcomeReject &&
		!(assessment.OriginalTargetCoverage == 1 &&
			assessment.OriginalCommitmentPosition == respondent.PositionLater) {
		return respondent.VerifierSignalRejected
	}
	for _, issue := range assessment.Issues {
		if issue == respondent.IssueAmbiguousQuestion {
			return respondent.VerifierSignalAmbiguous
		}
	}
	if assessment.OriginalTargetCoverage == 1 {
		switch assessment.OriginalCommitmentPosition {
		case respondent.PositionFirst:
			if assessment.TargetSatisfied &&
				assessment.Outcome == respondent.OutcomeKeep {
				return respondent.VerifierSignalFirst
			}
			return respondent.VerifierSignalAvailable
		case respondent.PositionLater:
			return respondent.VerifierSignalLater
		case respondent.PositionAbsent:
			return respondent.VerifierSignalAvailable
		}
	}
	// Partial target coverage is not a complete available answer.
	return respondent.VerifierSignalMissing
}

func projectCriticVerifierSignal(
	assessment answercontract.Assessment,
) respondent.VerifierSignal {
	metrics := assessment.Metrics
	if !validCriticVerifierOutcome(assessment.Outcome) ||
		!validCriticVerifierPosition(metrics.CommitmentFrontPosition) ||
		!validVerifierProbability(metrics.TargetSlotCoverage) ||
		!validVerifierProbability(metrics.HypothesisEntropy) ||
		!validVerifierProbability(metrics.MeaningPreservation) ||
		!validVerifierProbability(metrics.HypothesisGap) {
		return respondent.VerifierSignalInvalid
	}
	if assessment.Outcome == answercontract.OutcomeReject &&
		!(metrics.TargetSlotCoverage == 1 &&
			metrics.CommitmentFrontPosition != answercontract.PositionAbsent) {
		return respondent.VerifierSignalRejected
	}
	if assessment.Ambiguous {
		return respondent.VerifierSignalAmbiguous
	}
	if assessment.Outcome == answercontract.OutcomeReject {
		// The critic rejected an AI reconstruction while still finding the
		// complete target in the person's utterance. Mark it terminal but
		// unproved: only an independent Later signal may close the exercise.
		return respondent.VerifierSignalLater
	}
	if metrics.TargetSlotCoverage == 1 {
		switch metrics.CommitmentFrontPosition {
		case answercontract.PositionFirst:
			if assessment.TargetSatisfied &&
				assessment.Outcome == answercontract.OutcomeKeep {
				return respondent.VerifierSignalFirst
			}
			return respondent.VerifierSignalAvailable
		case answercontract.PositionLater:
			return respondent.VerifierSignalLater
		case answercontract.PositionAbsent:
			return respondent.VerifierSignalAvailable
		}
	}
	// Partial target coverage is not a complete available answer.
	return respondent.VerifierSignalMissing
}

func projectVerifierProgressPhase(phase respondent.CoachPhase) respondent.AnswerPhase {
	switch phase {
	case respondent.CoachPhaseNone:
		return respondent.AnswerPhaseNone
	case respondent.CoachPhaseAwaitingAnswer:
		return respondent.AnswerPhaseAwaitingAnswer
	case respondent.CoachPhaseAwaitingRestatement:
		return respondent.AnswerPhaseAwaitingRestatement
	case respondent.CoachPhaseExpanding:
		return respondent.AnswerPhaseExpanding
	case respondent.CoachPhaseComplete:
		return respondent.AnswerPhaseComplete
	case respondent.CoachPhaseBlocked:
		return respondent.AnswerPhaseBlocked
	default:
		return respondent.AnswerPhaseInvalid
	}
}

func projectVerifierProgressAttempts(attempts uint8) respondent.AnswerAttemptLevel {
	switch {
	case attempts == 0:
		return respondent.AnswerAttemptNone
	case attempts < respondent.MaxCoachAttempts:
		return respondent.AnswerAttemptOne
	default:
		return respondent.AnswerAttemptLimit
	}
}

func validRespondentVerifierOutcome(outcome respondent.Outcome) bool {
	switch outcome {
	case respondent.OutcomeKeep, respondent.OutcomeRestructure,
		respondent.OutcomeClarify, respondent.OutcomeReject:
		return true
	default:
		return false
	}
}

func validRespondentVerifierPosition(
	position respondent.CommitmentPosition,
) bool {
	switch position {
	case respondent.PositionFirst, respondent.PositionLater,
		respondent.PositionAbsent:
		return true
	default:
		return false
	}
}

func validCriticVerifierOutcome(outcome answercontract.Outcome) bool {
	switch outcome {
	case answercontract.OutcomeKeep, answercontract.OutcomeClarify,
		answercontract.OutcomeRestructure, answercontract.OutcomeReject:
		return true
	default:
		return false
	}
}

func validCriticVerifierPosition(position answercontract.PositionClass) bool {
	switch position {
	case answercontract.PositionFirst, answercontract.PositionLater,
		answercontract.PositionAbsent:
		return true
	default:
		return false
	}
}

func validVerifierProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
