// Package respondent gates answer-first rewrites of a person's own answer.
//
// The package is deliberately narrower than a generative answer evaluator. It
// does not decide whether an answer is factually correct, and it does not infer
// missing meaning. A caller supplies a question frame and exact spans from the
// person's answer that fill its required slots. Gate then permits only a
// conservative reordering of the answer's existing semantic clauses.
package respondent

// Operator is the kind of answer directly requested by a question.
type Operator string

const (
	OperatorBoolean    Operator = "boolean"
	OperatorChoice     Operator = "choice"
	OperatorQuantity   Operator = "quantity"
	OperatorState      Operator = "state"
	OperatorCause      Operator = "cause"
	OperatorPurpose    Operator = "purpose"
	OperatorProcedure  Operator = "procedure"
	OperatorDefinition Operator = "definition"
	OperatorComparison Operator = "comparison"
	OperatorEvidence   Operator = "evidence"
	OperatorOpen       Operator = "open"
)

// Slot is one required part of an answer.
type Slot string

const (
	SlotPolarity    Slot = "polarity"
	SlotSelection   Slot = "selection"
	SlotQuantity    Slot = "quantity"
	SlotState       Slot = "state"
	SlotCause       Slot = "cause"
	SlotPurpose     Slot = "purpose"
	SlotProcedure   Slot = "procedure"
	SlotDefinition  Slot = "definition"
	SlotComparison  Slot = "comparison"
	SlotEvidence    Slot = "evidence"
	SlotPosition    Slot = "position"
	SlotUnit        Slot = "unit"
	SlotCondition   Slot = "condition"
	SlotUncertainty Slot = "uncertainty"
	SlotScope       Slot = "scope"
)

// QuestionFrame is produced by the component interpreting the other person's
// question. Ambiguous must be set when that component cannot select one safe
// interpretation.
type QuestionFrame struct {
	Operator      Operator `json:"operator"`
	Subject       string   `json:"subject"`
	RequiredSlots []Slot   `json:"required_slots"`
	Ambiguous     bool     `json:"ambiguous"`
}

// SlotBinding binds a required slot to an exact, single-clause span in the
// person's original answer. Gate never accepts evidence that exists only in a
// proposed reconstruction.
type SlotBinding struct {
	Slot Slot   `json:"slot"`
	Span string `json:"span"`
}

// AnswerAttempt is the person's answer before restructuring.
//
// ProtectedSpans is for proper names and other Japanese content that cannot be
// identified reliably from surface form alone. Every protected span must occur
// in the original answer and remain in a reconstruction.
type AnswerAttempt struct {
	Text           string        `json:"text"`
	SlotEvidence   []SlotBinding `json:"slot_evidence"`
	ProtectedSpans []string      `json:"protected_spans,omitempty"`
}

// Input contains the interpreted question, the person's answer, and an
// optional answer-first reconstruction. Reconstruction must be made only by
// reordering existing semantic clauses.
type Input struct {
	Frame          QuestionFrame `json:"frame"`
	Attempt        AnswerAttempt `json:"attempt"`
	Reconstruction string        `json:"reconstruction,omitempty"`
}

// Outcome is the gate's authoritative action.
type Outcome string

const (
	OutcomeKeep        Outcome = "keep"
	OutcomeRestructure Outcome = "restructure"
	OutcomeClarify     Outcome = "clarify"
	OutcomeReject      Outcome = "reject"
)

// CommitmentPosition describes where the target answer first appears.
type CommitmentPosition string

const (
	PositionFirst  CommitmentPosition = "first"
	PositionLater  CommitmentPosition = "later"
	PositionAbsent CommitmentPosition = "absent"
)

// Issue is a content-free reason code. It is safe to record as a metric, but
// the original answer and evidence spans are not.
type Issue string

const (
	IssueInvalidContract      Issue = "invalid_contract"
	IssueAmbiguousQuestion    Issue = "ambiguous_question"
	IssueRequiredSlotMissing  Issue = "required_slot_missing"
	IssueRequiredSlotLost     Issue = "required_slot_lost"
	IssueCommitmentAbsent     Issue = "commitment_absent"
	IssueCommitmentNotFirst   Issue = "commitment_not_first"
	IssueReconstructionNeeded Issue = "reconstruction_needed"
	IssueNumberChanged        Issue = "number_changed"
	IssueNegationChanged      Issue = "negation_changed"
	IssueConditionChanged     Issue = "condition_changed"
	IssueUncertaintyChanged   Issue = "uncertainty_changed"
	IssueProperContentChanged Issue = "proper_content_changed"
	IssueProtectedSpanChanged Issue = "protected_span_changed"
	IssueContentChanged       Issue = "content_changed"
)

// Assessment contains only derived control values and reason codes.
type Assessment struct {
	Outcome                    Outcome            `json:"outcome"`
	OriginalTargetCoverage     float64            `json:"original_target_coverage"`
	TargetCoverage             float64            `json:"target_coverage"`
	OriginalCommitmentPosition CommitmentPosition `json:"original_commitment_position"`
	CommitmentPosition         CommitmentPosition `json:"commitment_position"`
	TargetSatisfied            bool               `json:"target_satisfied"`
	MeaningPreserved           bool               `json:"meaning_preserved"`
	Issues                     []Issue            `json:"issues"`
}

// TargetSlot maps a question operator to the slot that must be committed to
// before supporting explanation.
func TargetSlot(operator Operator) (Slot, bool) {
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
	case OperatorPurpose:
		return SlotPurpose, true
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
