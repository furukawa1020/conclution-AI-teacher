package store

import (
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/contracts"
)

func TestResultWithoutFreeformText(t *testing.T) {
	t.Parallel()

	original := contracts.EvaluationResult{
		EstimatedConclusion: "はい、公開できます。",
		EvidenceExcerpt:     "はい、公開できます",
		CalibrationScore:    88,
		Feedback:            "判断が先にあります。",
		RetryInstruction:    "条件を一つ添えてください。",
		ModelLogicalID:      "fast-judge-v1",
	}

	stored := resultWithoutFreeformText(original)
	if stored.EstimatedConclusion != "" ||
		stored.EvidenceExcerpt != "" ||
		stored.Feedback != "" ||
		stored.RetryInstruction != "" {
		t.Fatal("stored result must not retain freeform model or answer-derived text")
	}
	if stored.CalibrationScore != original.CalibrationScore ||
		stored.ModelLogicalID != original.ModelLogicalID {
		t.Fatal("bounded metric and version fields must be preserved")
	}
	if original.EstimatedConclusion == "" ||
		original.EvidenceExcerpt == "" ||
		original.Feedback == "" ||
		original.RetryInstruction == "" {
		t.Fatal("redaction must not mutate the active-session result")
	}
}
