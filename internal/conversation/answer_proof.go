package conversation

import "github.com/furukawa1020/conclution-ai-teacher/internal/respondent"

// answerProofForTurn fails closed. It publishes no question, answer, evidence
// span, tag, score, or identity assertion; all sensitive inputs remain inside
// the authenticated state and the current process invocation.
func answerProofForTurn(
	turn VoiceTurn,
	frame PendingAnswerFrame,
	decision respondent.CoachDecision,
	continuityVerified bool,
	assistanceTarget string,
	respondentStage string,
) AnswerProof {
	if turn.Speculative ||
		turn.InputOrigin != InputOriginCommittedVoice ||
		turn.PDF != nil ||
		assistanceTarget != "respondent" ||
		respondentStage != "restructure" ||
		!continuityVerified ||
		!decision.VerifiedFirst ||
		frame.AssistantFollowUp ||
		frame.Phase == respondent.CoachPhaseExpanding ||
		frame.QuestionInstanceTag == "" ||
		frame.QuestionContinuityTag == "" ||
		frame.ContinuityTag == "" {
		return AnswerProofNone
	}

	validTerminal := decision.Phase == respondent.CoachPhaseComplete &&
		decision.Action == respondent.CoachActionComplete
	validAuthorizedExpansion := decision.Phase == respondent.CoachPhaseExpanding &&
		decision.Action == respondent.CoachActionExpand
	if !validTerminal && !validAuthorizedExpansion {
		return AnswerProofNone
	}
	return AnswerProofQuestionBoundInputAnswerFirst
}
