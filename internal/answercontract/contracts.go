// Package answercontract implements the Latent Answer Contract (LAC).
//
// LAC values may contain short current-turn text used to audit an answer.
// Callers must not place a Contract in persistent or cross-turn state. Metrics
// and Outcome contain no source text and are safe to expose as diagnostics.
package answercontract

import "errors"

const (
	MaxSubjectRunes         = 160
	MaxHypothesisRunes      = 160
	MaxFirstCommitmentRunes = 240
	MaxMinimalAnswerRunes   = 240
	MaxReconstructedRunes   = 480
	MaxRequiredSlots        = 5
	MaxHypotheses           = 3

	HypothesisGapThreshold       = 0.20
	HighEntropyThreshold         = 0.88
	LowTopHypothesisThreshold    = 0.65
	TargetCoverageThreshold      = 0.80
	CoverageTolerance            = 0.05
	RepairGainThreshold          = 0.20
	MeaningPreservationThreshold = 0.86
	InferredCommitmentThreshold  = 0.90
)

var ErrInvalidContract = errors.New("answercontract: invalid contract")

type Operator string

const (
	OperatorBoolean    Operator = "boolean"
	OperatorChoice     Operator = "choice"
	OperatorQuantity   Operator = "quantity"
	OperatorState      Operator = "state"
	OperatorCause      Operator = "cause"
	OperatorProcedure  Operator = "procedure"
	OperatorDefinition Operator = "definition"
	OperatorComparison Operator = "comparison"
	OperatorEvidence   Operator = "evidence"
	OperatorOpen       Operator = "open"
)

type RequiredSlot string

const (
	SlotPolarity    RequiredSlot = "polarity"
	SlotSelection   RequiredSlot = "selection"
	SlotQuantity    RequiredSlot = "quantity"
	SlotState       RequiredSlot = "state"
	SlotCause       RequiredSlot = "cause"
	SlotProcedure   RequiredSlot = "procedure"
	SlotDefinition  RequiredSlot = "definition"
	SlotComparison  RequiredSlot = "comparison"
	SlotEvidence    RequiredSlot = "evidence"
	SlotPosition    RequiredSlot = "position"
	SlotUnit        RequiredSlot = "unit"
	SlotCondition   RequiredSlot = "condition"
	SlotUncertainty RequiredSlot = "uncertainty"
	SlotScope       RequiredSlot = "scope"
)

type Hypothesis struct {
	Interpretation string  `json:"interpretation"`
	Confidence     float64 `json:"confidence"`
}

type QuestionFrame struct {
	Operator      Operator       `json:"operator"`
	Subject       string         `json:"subject"`
	RequiredSlots []RequiredSlot `json:"required_slots"`
	Hypotheses    []Hypothesis   `json:"hypotheses"`
}

type PositionClass string

const (
	PositionFirst  PositionClass = "first"
	PositionLater  PositionClass = "later"
	PositionAbsent PositionClass = "absent"
)

type Calibration string

const (
	CalibrationCommitted   Calibration = "committed"
	CalibrationConditional Calibration = "conditional"
	CalibrationUncertain   Calibration = "uncertain"
	CalibrationAbstain     Calibration = "abstain"
)

type Issue string

const (
	IssueNone                 Issue = "none"
	IssueTargetMissing        Issue = "target_missing"
	IssueMissingRequiredSlot  Issue = "missing_required_slot"
	IssueReasonOnly           Issue = "reason_only"
	IssueBackgroundFirst      Issue = "background_first"
	IssueConditionSeparated   Issue = "condition_separated"
	IssueQuestionRestatement  Issue = "question_restatement"
	IssueAmbiguousCommitment  Issue = "ambiguous_commitment"
	IssueAnswerTypeMismatch   Issue = "answer_type_mismatch"
	IssueUnsupportedCertainty Issue = "unsupported_certainty"
	IssueInsufficientEvidence Issue = "insufficient_evidence"
	IssueContradiction        Issue = "contradiction"
	IssueMeaningChanged       Issue = "meaning_changed"
	IssueNotEvaluable         Issue = "not_evaluable"
)

type CommitmentFront struct {
	FirstCommitment string         `json:"first_commitment"`
	FillsTarget     bool           `json:"fills_target"`
	TargetCoverage  float64        `json:"target_coverage"`
	FilledSlots     []RequiredSlot `json:"filled_slots"`
	PositionClass   PositionClass  `json:"position_class"`
	Calibration     Calibration    `json:"calibration"`
	Issue           Issue          `json:"issue"`
}

type CounterfactualRepair struct {
	MinimalAnswer                 string  `json:"minimal_answer"`
	ReconstructedAnswer           string  `json:"reconstructed_answer"`
	MeaningPreservationConfidence float64 `json:"meaning_preservation_confidence"`
	RepairGain                    float64 `json:"repair_gain"`
}

type Contract struct {
	QuestionFrame        QuestionFrame        `json:"question_frame"`
	CommitmentFront      CommitmentFront      `json:"commitment_front"`
	CounterfactualRepair CounterfactualRepair `json:"counterfactual_repair"`
}

type Metrics struct {
	HypothesisGap           float64       `json:"hypothesis_gap"`
	HypothesisEntropy       float64       `json:"hypothesis_entropy"`
	TargetSlotCoverage      float64       `json:"target_slot_coverage"`
	CommitmentFrontPosition PositionClass `json:"commitment_front_position"`
	MeaningPreservation     float64       `json:"meaning_preservation"`
}

type Outcome string

const (
	OutcomeKeep        Outcome = "keep"
	OutcomeClarify     Outcome = "clarify"
	OutcomeRestructure Outcome = "restructure"
	OutcomeReject      Outcome = "reject"
)

type Assessment struct {
	Metrics             Metrics
	Outcome             Outcome
	Ambiguous           bool
	TargetSatisfied     bool
	RepairAccepted      bool
	ReconstructedAnswer string
}

func TargetSlot(operator Operator) (RequiredSlot, bool) {
	switch operator {
	case OperatorBoolean:
		return SlotPolarity, true
	case OperatorChoice:
		return SlotSelection, true
	case OperatorQuantity:
		return SlotQuantity, true
	case OperatorState:
		return SlotState, true
	case OperatorCause:
		return SlotCause, true
	case OperatorProcedure:
		return SlotProcedure, true
	case OperatorDefinition:
		return SlotDefinition, true
	case OperatorComparison:
		return SlotComparison, true
	case OperatorEvidence:
		return SlotEvidence, true
	case OperatorOpen:
		return SlotPosition, true
	default:
		return "", false
	}
}
