package answercontract

import (
	"errors"
	"testing"
)

func TestEvaluateRejectsModelReportedCoverageSpoofing(t *testing.T) {
	contract := validContract()
	contract.QuestionFrame.RequiredSlots = []RequiredSlot{SlotPolarity, SlotCause}
	contract.CommitmentFront.FilledSlots = []RequiredSlot{SlotPolarity}
	contract.CommitmentFront.TargetCoverage = 1
	_, err := Evaluate(contract, "賛成です。")
	if !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("coverage spoof accepted: %v", err)
	}
}

func TestEvaluateRejectsInvalidHypothesisDistribution(t *testing.T) {
	tests := []struct {
		name       string
		hypotheses []Hypothesis
	}{
		{
			name: "not sorted",
			hypotheses: []Hypothesis{
				{Interpretation: "one", Confidence: 0.3},
				{Interpretation: "two", Confidence: 0.7},
			},
		},
		{
			name: "sum above one",
			hypotheses: []Hypothesis{
				{Interpretation: "one", Confidence: 0.7},
				{Interpretation: "two", Confidence: 0.4},
			},
		},
		{
			name: "duplicate",
			hypotheses: []Hypothesis{
				{Interpretation: "same", Confidence: 0.6},
				{Interpretation: "same", Confidence: 0.4},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := validContract()
			contract.QuestionFrame.Hypotheses = test.hypotheses
			if _, err := Evaluate(contract, "賛成です。"); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("invalid hypotheses accepted: %v", err)
			}
		})
	}
}

func TestEvaluateRejectsTargetFillClaimWithoutTargetSlot(t *testing.T) {
	contract := validContract()
	contract.CommitmentFront.FillsTarget = true
	contract.CommitmentFront.FilledSlots = nil
	contract.CommitmentFront.TargetCoverage = 0
	contract.CommitmentFront.PositionClass = PositionAbsent
	contract.CommitmentFront.FirstCommitment = ""
	contract.CommitmentFront.Issue = IssueTargetMissing
	if _, err := Evaluate(contract, "前置きだけです。"); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("false fills_target accepted: %v", err)
	}
}

func TestEvaluatePublishesServerDerivedGapAndEntropy(t *testing.T) {
	contract := validContract()
	contract.QuestionFrame.Hypotheses = []Hypothesis{
		{Interpretation: "yes", Confidence: 0.55},
		{Interpretation: "no", Confidence: 0.45},
	}
	assessment, err := Evaluate(contract, "賛成です。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome != OutcomeClarify ||
		assessment.Metrics.HypothesisGap != 0.10 ||
		assessment.Metrics.HypothesisEntropy < 0.99 {
		t.Fatalf("unexpected ambiguity metrics: %+v", assessment)
	}
}

func validContract() Contract {
	return Contract{
		QuestionFrame: QuestionFrame{
			Operator:      OperatorBoolean,
			Subject:       "この案への賛否",
			RequiredSlots: []RequiredSlot{SlotPolarity},
			Hypotheses: []Hypothesis{{
				Interpretation: "賛否を答える",
				Confidence:     1,
			}},
		},
		CommitmentFront: CommitmentFront{
			FirstCommitment: "賛成です",
			FillsTarget:     true,
			TargetCoverage:  1,
			FilledSlots:     []RequiredSlot{SlotPolarity},
			PositionClass:   PositionFirst,
			Calibration:     CalibrationCommitted,
			Issue:           IssueNone,
		},
		CounterfactualRepair: CounterfactualRepair{
			MinimalAnswer:                 "賛成です",
			ReconstructedAnswer:           "賛成です。",
			MeaningPreservationConfidence: 1,
			RepairGain:                    0,
		},
	}
}
