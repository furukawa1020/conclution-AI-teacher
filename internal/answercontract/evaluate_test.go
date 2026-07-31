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

func TestEvaluateAllowsOnlyFixedNonPropositionalFillersBeforeCommitment(t *testing.T) {
	for _, answer := range []string{
		"えっと。賛成です。",
		"うーん。まあ、賛成です。",
		"um. 賛成です。",
	} {
		contract := validContract()
		contract.CounterfactualRepair.ReconstructedAnswer = answer
		assessment, err := Evaluate(contract, answer)
		if err != nil {
			t.Fatalf("Evaluate(%q): %v", answer, err)
		}
		if assessment.Outcome != OutcomeKeep || !assessment.TargetSatisfied {
			t.Fatalf("fixed fillers hid a first commitment for %q: %#v", answer, assessment)
		}
	}

	for _, answer := range []string{
		"たぶん。賛成です。",
		"条件次第です。賛成です。",
		"理由は費用です。賛成です。",
		"えっと理由は費用です。賛成です。",
	} {
		contract := validContract()
		contract.CounterfactualRepair.ReconstructedAnswer = answer
		assessment, err := Evaluate(contract, answer)
		if err != nil {
			t.Fatalf("Evaluate(%q): %v", answer, err)
		}
		if assessment.Outcome != OutcomeReject || assessment.TargetSatisfied {
			t.Fatalf("substantive preface was ignored for %q: %#v", answer, assessment)
		}
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

func TestEvaluateRejectsFabricatedFirstCommitment(t *testing.T) {
	contract := validContract()
	contract.CommitmentFront.FirstCommitment = "反対です"
	assessment, err := Evaluate(contract, "前置きだけで結論は述べていません。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome != OutcomeReject || assessment.TargetSatisfied {
		t.Fatalf("fabricated commitment passed: %+v", assessment)
	}
}

func TestEvaluateRejectsCommitmentReportedFirstWhenItAppearsAfterAPreface(t *testing.T) {
	contract := validContract()
	contract.QuestionFrame.Operator = OperatorState
	contract.QuestionFrame.Subject = "日本の首都"
	contract.QuestionFrame.RequiredSlots = []RequiredSlot{SlotState}
	contract.CommitmentFront.FirstCommitment = "東京です"
	contract.CommitmentFront.FilledSlots = []RequiredSlot{SlotState}
	contract.CommitmentFront.PositionClass = PositionFirst
	contract.CommitmentFront.Issue = IssueNone
	contract.CounterfactualRepair.MinimalAnswer = "東京です"
	contract.CounterfactualRepair.ReconstructedAnswer = "前置きです。東京です。"

	assessment, err := Evaluate(contract, "前置きです。東京です。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome != OutcomeReject || assessment.TargetSatisfied {
		t.Fatalf("later commitment spoof passed: %+v", assessment)
	}
}

func TestEvaluateNeverKeepsALaterAnswerWithoutAnAcceptedFrontRepair(t *testing.T) {
	contract := validContract()
	contract.CommitmentFront.PositionClass = PositionLater
	contract.CommitmentFront.Issue = IssueBackgroundFirst
	contract.CounterfactualRepair.RepairGain = 0

	assessment, err := Evaluate(contract, "前置きです。賛成です。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome == OutcomeKeep || assessment.TargetSatisfied {
		t.Fatalf("later answer was kept: %+v", assessment)
	}
}

func TestEvaluateRejectsRepairThatDoesNotMoveAToTheFront(t *testing.T) {
	contract := validContract()
	contract.CommitmentFront.PositionClass = PositionLater
	contract.CommitmentFront.Issue = IssueBackgroundFirst
	contract.CounterfactualRepair.MinimalAnswer = "賛成です"
	contract.CounterfactualRepair.ReconstructedAnswer = "前置きです。賛成です。"
	contract.CounterfactualRepair.MeaningPreservationConfidence = 0.99
	contract.CounterfactualRepair.RepairGain = 0.40

	assessment, err := Evaluate(contract, "前置きです。賛成です。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome != OutcomeReject || assessment.RepairAccepted {
		t.Fatalf("non-fronting repair passed: %+v", assessment)
	}
}

func TestEvaluateRejectsLaterRepairWhoseClaimedMinimumIsStillBackground(t *testing.T) {
	contract := validContract()
	contract.CommitmentFront.PositionClass = PositionLater
	contract.CommitmentFront.Issue = IssueBackgroundFirst
	contract.CounterfactualRepair.MinimalAnswer = "前置きです"
	contract.CounterfactualRepair.ReconstructedAnswer = "前置きです。賛成です。"
	contract.CounterfactualRepair.MeaningPreservationConfidence = 0.99
	contract.CounterfactualRepair.RepairGain = 0.40

	assessment, err := Evaluate(contract, "前置きです。賛成です。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome != OutcomeReject || assessment.RepairAccepted {
		t.Fatalf("background-first repair passed: %+v", assessment)
	}
}

func TestEvaluateBlockingIssueOutranksOtherwiseAcceptedRepair(t *testing.T) {
	contract := validContract()
	contract.CommitmentFront.PositionClass = PositionLater
	contract.CommitmentFront.Issue = IssueContradiction
	contract.CounterfactualRepair.MinimalAnswer = "賛成です"
	contract.CounterfactualRepair.ReconstructedAnswer = "賛成です。前置きです。"
	contract.CounterfactualRepair.MeaningPreservationConfidence = 0.99
	contract.CounterfactualRepair.RepairGain = 0.40

	assessment, err := Evaluate(contract, "前置きです。賛成です。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome != OutcomeReject {
		t.Fatalf("blocking issue was repaired into publication: %+v", assessment)
	}
}

func TestEvaluateCannotRestructureMissingRequiredSlots(t *testing.T) {
	contract := validContract()
	contract.QuestionFrame.RequiredSlots = []RequiredSlot{
		SlotPolarity,
		SlotCause,
	}
	contract.CommitmentFront.FilledSlots = []RequiredSlot{SlotPolarity}
	contract.CommitmentFront.TargetCoverage = 0.5
	contract.CommitmentFront.PositionClass = PositionLater
	contract.CommitmentFront.Issue = IssueMissingRequiredSlot
	contract.CounterfactualRepair.MinimalAnswer = "賛成です"
	contract.CounterfactualRepair.ReconstructedAnswer = "賛成です。前置きです。"
	contract.CounterfactualRepair.MeaningPreservationConfidence = 0.99
	contract.CounterfactualRepair.RepairGain = 0.40

	assessment, err := Evaluate(contract, "前置きです。賛成です。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome == OutcomeRestructure || assessment.TargetSatisfied {
		t.Fatalf("missing slot was repaired by reordering: %+v", assessment)
	}
}

func TestEvaluateRejectsRepairThatAddsAnUnprotectedJapaneseClaim(t *testing.T) {
	contract := validContract()
	contract.CommitmentFront.PositionClass = PositionLater
	contract.CommitmentFront.Issue = IssueBackgroundFirst
	contract.CounterfactualRepair.MinimalAnswer = "賛成です"
	contract.CounterfactualRepair.ReconstructedAnswer = "賛成です。実績は十分です。"
	contract.CounterfactualRepair.MeaningPreservationConfidence = 0.99
	contract.CounterfactualRepair.RepairGain = 0.40

	assessment, err := Evaluate(contract, "費用を考えると、賛成です。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome != OutcomeReject || assessment.RepairAccepted {
		t.Fatalf("new Japanese claim passed preservation: %+v", assessment)
	}
}

func TestEvaluateRejectsClausePermutationThatChangesReferenceOrder(t *testing.T) {
	contract := validContract()
	contract.CommitmentFront.FirstCommitment = "A案です"
	contract.CommitmentFront.PositionClass = PositionLater
	contract.CommitmentFront.Issue = IssueBackgroundFirst
	contract.CounterfactualRepair.MinimalAnswer = "A案です"
	contract.CounterfactualRepair.ReconstructedAnswer =
		"A案です。それが理由です。B案は高いです。"
	contract.CounterfactualRepair.MeaningPreservationConfidence = 0.99
	contract.CounterfactualRepair.RepairGain = 0.40

	assessment, err := Evaluate(
		contract,
		"B案は高いです。それが理由です。A案です。",
	)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome != OutcomeReject || assessment.RepairAccepted {
		t.Fatalf("reference-changing permutation passed: %+v", assessment)
	}
}

func TestEvaluateAcceptsStableFrontMoveForMultiClauseCommitment(t *testing.T) {
	contract := validContract()
	contract.QuestionFrame.Operator = OperatorState
	contract.QuestionFrame.Subject = "日本の首都"
	contract.QuestionFrame.RequiredSlots = []RequiredSlot{SlotState}
	contract.CommitmentFront.FirstCommitment = "答えは、東京です"
	contract.CommitmentFront.FilledSlots = []RequiredSlot{SlotState}
	contract.CommitmentFront.PositionClass = PositionLater
	contract.CommitmentFront.Issue = IssueBackgroundFirst
	contract.CounterfactualRepair.MinimalAnswer = "答えは、東京です"
	contract.CounterfactualRepair.ReconstructedAnswer =
		"答えは、東京です。背景です。補足です。"
	contract.CounterfactualRepair.MeaningPreservationConfidence = 0.99
	contract.CounterfactualRepair.RepairGain = 0.40

	assessment, err := Evaluate(
		contract,
		"背景です。答えは、東京です。補足です。",
	)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome != OutcomeRestructure ||
		!assessment.RepairAccepted {
		t.Fatalf("stable multi-clause front move was rejected: %+v", assessment)
	}
}

func TestEvaluateNeverKeepsPartialRequiredSlotCoverage(t *testing.T) {
	contract := validContract()
	contract.QuestionFrame.RequiredSlots = []RequiredSlot{
		SlotPolarity,
		SlotCause,
		SlotCondition,
		SlotUncertainty,
		SlotScope,
	}
	contract.CommitmentFront.FilledSlots = []RequiredSlot{
		SlotPolarity,
		SlotCause,
		SlotCondition,
		SlotUncertainty,
	}
	contract.CommitmentFront.TargetCoverage = 0.8
	contract.CommitmentFront.Issue = IssueMissingRequiredSlot
	assessment, err := Evaluate(contract, "賛成です。")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assessment.Outcome == OutcomeKeep || assessment.TargetSatisfied {
		t.Fatalf("partial slot coverage was treated as complete: %+v", assessment)
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
