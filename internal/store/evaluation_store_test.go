package store

import (
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/contracts"
)

func TestResultWithoutAnswerText(t *testing.T) {
	t.Parallel()

	original := contracts.EvaluationResult{
		EstimatedConclusion: "はい、公開できます。",
		EvidenceExcerpt:     "はい、公開できます",
		CalibrationScore:    88,
		Feedback:            "判断が先にあります。",
		RetryInstruction:    "条件を一つ添えてください。",
		ModelLogicalID:      "fast-judge-v1",
	}

	stored := resultWithoutAnswerText(original)
	if stored.EstimatedConclusion != "" || stored.EvidenceExcerpt != "" {
		t.Fatal("stored result must not retain answer-derived text")
	}
	if stored.CalibrationScore != original.CalibrationScore ||
		stored.Feedback != original.Feedback ||
		stored.RetryInstruction != original.RetryInstruction ||
		stored.ModelLogicalID != original.ModelLogicalID {
		t.Fatal("non-excerpt evaluation fields must be preserved")
	}
	if original.EstimatedConclusion == "" || original.EvidenceExcerpt == "" {
		t.Fatal("redaction must not mutate the active-session result")
	}
}
