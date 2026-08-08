package conversation

import (
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestQARCCoachAttemptPreservesVerifierConflictAsSilentWait(t *testing.T) {
	input := qarcControllerInput(
		respondent.VerifierSignalFirst,
		respondent.VerifierSignalLater,
	)
	decision := qarcCoachAttempt(
		respondent.OperatorPurpose,
		qarcCommittedTurn(),
		false,
		input,
	)
	if decision.Action != respondent.CoachActionNone ||
		decision.SpokenReply != "" ||
		!decision.KeepPending ||
		decision.Attempts != 0 ||
		decision.ReasonCode != respondent.AnswerReasonVerificationInconclusive {
		t.Fatalf("conflicting verifier decision = %#v", decision)
	}
}

func TestQARCCoachAttemptPreservesRejectedScopeAsRelease(t *testing.T) {
	input := qarcControllerInput(
		respondent.VerifierSignalRejected,
		respondent.VerifierSignalMissing,
	)
	decision := qarcCoachAttempt(
		respondent.OperatorPurpose,
		qarcCommittedTurn(),
		false,
		input,
	)
	if decision.Action != respondent.CoachActionRelease ||
		decision.KeepPending ||
		decision.ReasonCode != respondent.AnswerReasonScopeRejected {
		t.Fatalf("rejected scope decision = %#v", decision)
	}
}

func TestQARCCoachAttemptUsesCueOnlyAfterElicitAuthorization(t *testing.T) {
	input := qarcControllerInput(
		respondent.VerifierSignalMissing,
		respondent.VerifierSignalMissing,
	)
	decision := qarcCoachAttempt(
		respondent.OperatorPurpose,
		qarcCommittedTurn(),
		false,
		input,
	)
	if decision.Action != respondent.CoachActionElicit ||
		decision.SpokenReply == "" ||
		!decision.KeepPending ||
		decision.Attempts != 1 ||
		decision.ReasonCode != respondent.AnswerReasonTargetMissing {
		t.Fatalf("authorized elicitation decision = %#v", decision)
	}
}

func qarcControllerInput(
	gate respondent.VerifierSignal,
	critic respondent.VerifierSignal,
) respondent.AnswerControllerInput {
	return respondent.AnswerControllerInput{
		Prior:                 respondent.DefaultVerifierProgressPosterior(),
		GateSignal:            gate,
		CriticSignal:          critic,
		Phase:                 respondent.AnswerPhaseAwaitingAnswer,
		Attempts:              respondent.AnswerAttemptNone,
		VerificationAvailable: true,
	}
}

func qarcCommittedTurn() VoiceTurn {
	return VoiceTurn{
		InputOrigin:      InputOriginCommittedVoice,
		OutputCancelable: true,
		FloorEvidence:    FloorEvidenceHybridCommitted,
	}
}
