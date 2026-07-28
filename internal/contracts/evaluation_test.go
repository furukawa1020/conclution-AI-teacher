package contracts

import (
	"strings"
	"testing"
)

func validResult() EvaluationResult {
	return EvaluationResult{
		Answered:              true,
		EstimatedConclusion:   "実施します",
		ConclusionStartRune:   0,
		ConclusionFirst:       true,
		DirectnessScore:       90,
		FirstSentenceComplete: true,
		CalibrationScore:      85,
		PrimaryIssue:          "none",
		Feedback:              "結論が先にあります。",
		RetryInstruction:      "同じ構造を保ってください。",
		Confidence:            0.9,
		EvidenceExcerpt:       "実施します",
	}
}

func TestEvaluationResultRejectsInventedEvidence(t *testing.T) {
	t.Parallel()

	result := validResult()
	result.EvidenceExcerpt = "回答にない引用"

	if err := result.Validate("実施します。理由は二つあります。"); err == nil {
		t.Fatal("Validate accepted an evidence excerpt invented by the model")
	}
}

func TestEvaluationResultBoundsModelControlledStrings(t *testing.T) {
	t.Parallel()

	result := validResult()
	result.Feedback = strings.Repeat("長", MaxFeedbackRunes+1)

	if err := result.Validate("実施します。"); err == nil {
		t.Fatal("Validate accepted oversized model output")
	}
}

func TestEvaluationResultAcceptsBoundedStructuredOutput(t *testing.T) {
	t.Parallel()

	if err := validResult().Validate("実施します。理由は二つあります。"); err != nil {
		t.Fatalf("Validate rejected valid output: %v", err)
	}
}
