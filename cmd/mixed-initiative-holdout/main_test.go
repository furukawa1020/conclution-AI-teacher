package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/initiativebench"
)

const testCommit = "568e771266b4f64626e41795ce8135a3663936a6"

func validAnnotationJSON(t *testing.T) []byte {
	t.Helper()
	set := initiativebench.HoldoutAnnotationSet{
		SchemaVersion: initiativebench.HoldoutAnnotationSchemaVersion,
		SourceSHA256:  strings.Repeat("a", 64),
		BlindingSeed:  20260915,
		Items: []initiativebench.AnnotatedHoldoutItem{{
			FixtureID: "holdout-0001",
			Situation: initiativebench.SituationQuestionRequired,
			Annotations: []initiativebench.RaterAnnotation{
				{RaterSlot: 1, Choice: initiativebench.AnnotationChoice(initiativebench.ActionAskOne)},
				{RaterSlot: 2, Choice: initiativebench.AnnotationChoice(initiativebench.ActionAskOne)},
			},
		}},
	}
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal annotations: %v", err)
	}
	return raw
}

func TestBuildReportBindsExactInputAndEvaluatorCommit(t *testing.T) {
	raw := validAnnotationJSON(t)
	first, err := buildReport(bytes.NewReader(raw), testCommit)
	if err != nil {
		t.Fatalf("first report: %v", err)
	}
	second, err := buildReport(bytes.NewReader(raw), testCommit)
	if err != nil {
		t.Fatalf("second report: %v", err)
	}
	wantDigest := sha256.Sum256(raw)
	if first.SchemaVersion != reportSchemaVersion || first.EvaluatorSourceCommit != testCommit ||
		first.AnnotationArtifactSHA256 != hex.EncodeToString(wantDigest[:]) ||
		first.Agreement.AnnotationCount != second.Agreement.AnnotationCount ||
		first.AnnotationArtifactSHA256 != second.AnnotationArtifactSHA256 {
		t.Fatalf("report is not reproducibly bound: %#v %#v", first, second)
	}
}

func TestExecuteWritesByteIdenticalReports(t *testing.T) {
	path := t.TempDir() + "/annotations.json"
	if err := os.WriteFile(path, validAnnotationJSON(t), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}
	args := []string{"--input", path, "--source-commit", testCommit}
	var first, second, stderr bytes.Buffer
	if code := execute(args, &first, &stderr); code != 0 {
		t.Fatalf("first execute code=%d stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	if code := execute(args, &second, &stderr); code != 0 {
		t.Fatalf("second execute code=%d stderr=%q", code, stderr.String())
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("identical inputs produced different report bytes")
	}
	var report reproducibleReport
	if err := json.Unmarshal(first.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Agreement.AmbiguousItemCount != 0 || report.Agreement.NominalAlphaBPS != nil ||
		len(report.Agreement.Consensus) != 1 || report.Agreement.Consensus[0].ExpectedAction == nil ||
		*report.Agreement.Consensus[0].ExpectedAction != initiativebench.ActionAskOne {
		t.Fatalf("unexpected agreement report: %#v", report.Agreement)
	}
}

func TestBuildReportRejectsInvalidCommitAndOversizedInput(t *testing.T) {
	if _, err := buildReport(bytes.NewReader(validAnnotationJSON(t)), "main"); err == nil {
		t.Fatal("unfixed evaluator revision accepted")
	}
	oversized := io.LimitReader(strings.NewReader(strings.Repeat("x", maximumInputBytes+1)), maximumInputBytes+1)
	if _, err := buildReport(oversized, testCommit); err == nil || err.Error() != "mixed_initiative_holdout_input_too_large" {
		t.Fatalf("oversized input not rejected: %v", err)
	}
}

func TestExecuteRejectsMissingArgumentsWithoutOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := execute(nil, &stdout, &stderr); code != 2 || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), "mixed_initiative_holdout_arguments_invalid") {
		t.Fatalf("unexpected invalid argument result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
