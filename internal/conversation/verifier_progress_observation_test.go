package conversation

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestVerifierProgressAdapterDoesNotReadOrCopyReconstructedAnswer(t *testing.T) {
	gate, critic := verifiedFirstPair()
	critic.ReconstructedAnswer = "SECRET-reconstructed-answer"
	input := verifierProgressInput(
		respondent.DefaultVerifierProgressPosterior(),
		gate,
		critic,
		respondent.CoachPhaseAwaitingAnswer,
		0,
		false,
		false,
		true,
	)
	other := critic
	other.ReconstructedAnswer = "different-secret"
	otherInput := verifierProgressInput(
		respondent.DefaultVerifierProgressPosterior(),
		gate,
		other,
		respondent.CoachPhaseAwaitingAnswer,
		0,
		false,
		false,
		true,
	)
	if !reflect.DeepEqual(input, otherInput) {
		t.Fatal("reconstructed answer changed finite policy input")
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SECRET") ||
		strings.Contains(string(encoded), "different-secret") {
		t.Fatalf("reconstructed answer crossed boundary: %s", encoded)
	}
}

func TestMalformedVerifierMetricsProjectToWait(t *testing.T) {
	gate, critic := verifiedFirstPair()
	critic.Metrics.MeaningPreservation = math.NaN()
	input := verifierProgressInput(
		respondent.DefaultVerifierProgressPosterior(),
		gate,
		critic,
		respondent.CoachPhaseAwaitingAnswer,
		0,
		false,
		false,
		true,
	)
	if input.CriticSignal != respondent.VerifierSignalInvalid {
		t.Fatalf("malformed critic signal = %d", input.CriticSignal)
	}
	decision := respondent.PlanAnswerSupport(input)
	if decision.Action != respondent.AnswerSupportWait ||
		decision.VerifiedFirst || decision.NextAttempts != 0 {
		t.Fatalf("malformed metrics did not wait: %#v", decision)
	}
}

func TestVerifierProgressAdapterBoundsPhaseAndAttempts(t *testing.T) {
	gate, critic := verifiedFirstPair()
	input := verifierProgressInput(
		respondent.DefaultVerifierProgressPosterior(),
		gate,
		critic,
		respondent.CoachPhase("future"),
		255,
		true,
		true,
		true,
	)
	if input.Phase != respondent.AnswerPhaseInvalid ||
		input.Attempts != respondent.AnswerAttemptLimit ||
		!input.OneShot || !input.Hesitation {
		t.Fatalf("unbounded adapter result = %#v", input)
	}
}

func verifiedFirstPair() (respondent.Assessment, answercontract.Assessment) {
	return respondent.Assessment{
			Outcome:                    respondent.OutcomeKeep,
			OriginalTargetCoverage:     1,
			TargetCoverage:             1,
			OriginalCommitmentPosition: respondent.PositionFirst,
			CommitmentPosition:         respondent.PositionFirst,
			TargetSatisfied:            true,
			MeaningPreserved:           true,
		}, answercontract.Assessment{
			Metrics: answercontract.Metrics{
				HypothesisGap:           0.8,
				HypothesisEntropy:       0.1,
				TargetSlotCoverage:      1,
				CommitmentFrontPosition: answercontract.PositionFirst,
				MeaningPreservation:     1,
			},
			Outcome:         answercontract.OutcomeKeep,
			TargetSatisfied: true,
		}
}
