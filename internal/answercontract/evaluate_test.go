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

func TestEvaluateLowSingleHypothesisIsAmbiguous(t *testing.T) {
	contract := validContract()
	contract.QuestionFrame.Hypotheses = []Hypothesis{{
		Interpretation: "weak guess",
		Confidence:     0.10,
	}}
	assessment, err := Evaluate(contract, "賛成です。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !assessment.Ambiguous || assessment.Outcome != OutcomeClarify {
		t.Fatalf("low top hypothesis was accepted: %+v", assessment)
	}
}

func TestEvaluateDeterministicMeaningPreservationGate(t *testing.T) {
	tests := []struct {
		name          string
		operator      Operator
		required      []RequiredSlot
		original      string
		minimal       string
		reconstructed string
	}{
		{
			name:     "A to B",
			operator: OperatorChoice, required: []RequiredSlot{SlotSelection},
			original: "理由を述べたあと、A案です。",
			minimal:  "B案です", reconstructed: "B案です。理由は同じです。",
		},
		{
			name:     "can to cannot",
			operator: OperatorBoolean, required: []RequiredSlot{SlotPolarity},
			original: "条件を確認したところ対応できます。",
			minimal:  "対応できません", reconstructed: "対応できません。条件は確認済みです。",
		},
		{
			name:     "three days to five days",
			operator: OperatorQuantity, required: []RequiredSlot{SlotQuantity, SlotUnit},
			original: "確認したところ3日です。",
			minimal:  "5日です", reconstructed: "5日です。確認済みです。",
		},
		{
			name:     "causal claim removed",
			operator: OperatorBoolean, required: []RequiredSlot{SlotPolarity, SlotCause},
			original: "予算が不足しているから反対です。",
			minimal:  "反対です", reconstructed: "反対です。",
		},
		{
			name:     "condition subject changed",
			operator: OperatorBoolean, required: []RequiredSlot{SlotPolarity, SlotCondition},
			original: "雨なら中止します。",
			minimal:  "中止します", reconstructed: "雪なら中止します。",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, _ := TargetSlot(test.operator)
			contract := Contract{
				QuestionFrame: QuestionFrame{
					Operator:      test.operator,
					Subject:       "current question",
					RequiredSlots: append([]RequiredSlot(nil), test.required...),
					Hypotheses: []Hypothesis{{
						Interpretation: "single interpretation",
						Confidence:     1,
					}},
				},
				CommitmentFront: CommitmentFront{
					FirstCommitment: test.minimal,
					FillsTarget:     true,
					TargetCoverage:  1,
					FilledSlots:     append([]RequiredSlot(nil), test.required...),
					PositionClass:   PositionLater,
					Calibration:     CalibrationCommitted,
					Issue:           IssueBackgroundFirst,
				},
				CounterfactualRepair: CounterfactualRepair{
					MinimalAnswer:                 test.minimal,
					ReconstructedAnswer:           test.reconstructed,
					MeaningPreservationConfidence: 0.99,
					RepairGain:                    0.40,
				},
			}
			if !containsSlot(contract.CommitmentFront.FilledSlots, target) {
				t.Fatal("test setup does not fill the operator target")
			}
			assessment, err := Evaluate(contract, test.original)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if assessment.Outcome != OutcomeReject ||
				assessment.RepairAccepted ||
				assessment.Metrics.MeaningPreservation != 0 {
				t.Fatalf("meaning-changing repair accepted: %+v", assessment)
			}
		})
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
