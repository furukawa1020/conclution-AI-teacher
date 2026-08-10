package conversation

import (
	"crypto/subtle"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

// answerOwnershipYieldsFloor is the terminal non-generation boundary. A
// verified answer remains the person's answer only if KOTAE does not append a
// synthetic acknowledgement after the independently checked A-first turn.
// Provisional computation may use the private candidate, but only the
// voiceflow exact-final gate can publish the proof and its silent result.
func answerOwnershipYieldsFloor(
	proof AnswerProof,
	candidate AnswerProof,
	coachPhase string,
	coachAction string,
) bool {
	verified := proof == AnswerProofQuestionBoundInputAnswerFirst ||
		candidate == AnswerProofQuestionBoundInputAnswerFirst
	return verified &&
		coachPhase == string(respondent.CoachPhaseComplete) &&
		coachAction == string(respondent.CoachActionComplete)
}

// answerProofForTurn fails closed. It publishes no question, answer, evidence
// span, tag, score, or identity assertion; all sensitive inputs remain inside
// the authenticated state and the current process invocation.
func answerProofForTurn(
	turn VoiceTurn,
	frame PendingAnswerFrame,
	decision respondent.CoachDecision,
	continuityVerified bool,
	proofSpanBound bool,
	assistanceTarget string,
	respondentStage string,
) AnswerProof {
	if turn.Speculative || turn.InputOrigin != InputOriginCommittedVoice {
		return AnswerProofNone
	}
	return answerProofCandidateForTurn(
		turn,
		frame,
		decision,
		continuityVerified,
		proofSpanBound,
		assistanceTarget,
		respondentStage,
	)
}

// answerProofCandidateForTurn evaluates the deterministic and independent
// answer gates without claiming that provisional recognition is committed.
// The candidate remains process-private; only the voiceflow exact-final gate
// may promote a provisional candidate to the public fixed enum.
func answerProofCandidateForTurn(
	turn VoiceTurn,
	frame PendingAnswerFrame,
	decision respondent.CoachDecision,
	continuityVerified bool,
	proofSpanBound bool,
	assistanceTarget string,
	respondentStage string,
) AnswerProof {
	if (turn.InputOrigin != InputOriginCommittedVoice &&
		turn.InputOrigin != InputOriginProvisionalVoice) ||
		turn.ResearchDisabled ||
		turn.PDF != nil ||
		assistanceTarget != "respondent" ||
		respondentStage != "restructure" ||
		!continuityVerified ||
		!proofSpanBound ||
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

func answerTransitionEvidenceForLateTurn(
	frame PendingAnswerFrame,
	decision respondent.CoachDecision,
	gate respondent.Assessment,
	critic answercontract.Assessment,
) AnswerTransitionEvidence {
	if decision.Phase != respondent.CoachPhaseAwaitingRestatement ||
		decision.Action != respondent.CoachActionRestate ||
		!decision.KeepPending || frame.AssistantFollowUp ||
		frame.QuestionInstanceTag == "" ||
		frame.QuestionContinuityTag == "" || frame.ContinuityTag == "" ||
		projectRespondentVerifierSignal(gate) != respondent.VerifierSignalLater ||
		projectCriticVerifierSignal(critic) != respondent.VerifierSignalLater {
		return AnswerTransitionEvidenceNone
	}
	return AnswerTransitionEvidenceQuestionBoundInputClauseLater
}

func answerTransitionProofForTurn(
	turn VoiceTurn,
	previous PendingAnswerFrame,
	current PendingAnswerFrame,
	decision respondent.CoachDecision,
	answerProof AnswerProof,
	continuityVerified bool,
	proofSpanBound bool,
	assistanceTarget string,
	respondentStage string,
	enabled bool,
) AnswerTransitionProof {
	if turn.Speculative || turn.InputOrigin != InputOriginCommittedVoice {
		return AnswerTransitionProofNone
	}
	return answerTransitionProofCandidateForTurn(
		turn, previous, current, decision, answerProof, continuityVerified,
		proofSpanBound, assistanceTarget, respondentStage, enabled,
	)
}

func answerTransitionProofCandidateForTurn(
	turn VoiceTurn,
	previous PendingAnswerFrame,
	current PendingAnswerFrame,
	decision respondent.CoachDecision,
	answerProof AnswerProof,
	continuityVerified bool,
	proofSpanBound bool,
	assistanceTarget string,
	respondentStage string,
	enabled bool,
) AnswerTransitionProof {
	if !enabled ||
		previous.AnswerTransitionEvidence !=
			AnswerTransitionEvidenceQuestionBoundInputClauseLater ||
		previous.Phase != respondent.CoachPhaseAwaitingRestatement ||
		answerProof != AnswerProofQuestionBoundInputAnswerFirst ||
		answerProofCandidateForTurn(
			turn, current, decision, continuityVerified, proofSpanBound,
			assistanceTarget, respondentStage,
		) != AnswerProofQuestionBoundInputAnswerFirst ||
		!sameAnswerTransitionScope(previous, current) ||
		decision.Phase != respondent.CoachPhaseComplete ||
		decision.Action != respondent.CoachActionComplete {
		return AnswerTransitionProofNone
	}
	return AnswerTransitionProofQuestionBoundInputClauseLaterToFirst
}

func sameAnswerTransitionScope(left, right PendingAnswerFrame) bool {
	if !left.Active || !right.Active ||
		left.Operator != right.Operator ||
		len(left.RequiredSlots) != len(right.RequiredSlots) ||
		left.QuestionInstanceTag == "" || left.QuestionContinuityTag == "" ||
		left.ContinuityTag == "" || right.QuestionInstanceTag == "" ||
		right.QuestionContinuityTag == "" || right.ContinuityTag == "" ||
		subtle.ConstantTimeCompare(
			[]byte(left.QuestionInstanceTag), []byte(right.QuestionInstanceTag),
		) != 1 ||
		subtle.ConstantTimeCompare(
			[]byte(left.QuestionContinuityTag), []byte(right.QuestionContinuityTag),
		) != 1 ||
		subtle.ConstantTimeCompare(
			[]byte(left.ContinuityTag), []byte(right.ContinuityTag),
		) != 1 {
		return false
	}
	for index := range left.RequiredSlots {
		if left.RequiredSlots[index] != right.RequiredSlots[index] {
			return false
		}
	}
	return true
}
