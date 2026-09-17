package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/furukawa1020/conclution-ai-teacher/internal/initiativebench"
)

const (
	reportSchemaVersion = "kotae.mixed-initiative-holdout-report.v1"
	maximumInputBytes   = 16 << 20
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type reproducibleReport struct {
	SchemaVersion            string                                 `json:"schema_version"`
	EvaluatorSourceCommit    string                                 `json:"evaluator_source_commit"`
	AnnotationArtifactSHA256 string                                 `json:"annotation_artifact_sha256"`
	Agreement                initiativebench.HoldoutAgreementReport `json:"agreement"`
}

func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr))
}

func execute(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mixed-initiative-holdout", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "", "content-free holdout annotation JSON")
	sourceCommit := flags.String("source-commit", "", "40-character evaluator Git commit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *inputPath == "" || !commitPattern.MatchString(*sourceCommit) || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "mixed_initiative_holdout_arguments_invalid")
		return 2
	}

	input, err := os.Open(*inputPath)
	if err != nil {
		fmt.Fprintln(stderr, "mixed_initiative_holdout_input_unavailable")
		return 2
	}
	defer input.Close()

	report, err := buildReport(input, *sourceCommit)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, "mixed_initiative_holdout_report_failed")
		return 2
	}
	return 0
}

func buildReport(input io.Reader, sourceCommit string) (reproducibleReport, error) {
	if !commitPattern.MatchString(sourceCommit) {
		return reproducibleReport{}, errors.New("mixed_initiative_holdout_source_commit_invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(input, maximumInputBytes+1))
	if err != nil {
		return reproducibleReport{}, errors.New("mixed_initiative_holdout_input_read_failed")
	}
	if len(raw) > maximumInputBytes {
		return reproducibleReport{}, errors.New("mixed_initiative_holdout_input_too_large")
	}
	annotations, err := initiativebench.DecodeHoldoutAnnotations(bytes.NewReader(raw))
	if err != nil {
		return reproducibleReport{}, fmt.Errorf("mixed_initiative_holdout_input_invalid: %w", err)
	}
	agreement, err := initiativebench.EvaluateHoldoutAgreement(annotations)
	if err != nil {
		return reproducibleReport{}, fmt.Errorf("mixed_initiative_holdout_evaluation_failed: %w", err)
	}
	digest := sha256.Sum256(raw)
	return reproducibleReport{
		SchemaVersion:            reportSchemaVersion,
		EvaluatorSourceCommit:    sourceCommit,
		AnnotationArtifactSHA256: hex.EncodeToString(digest[:]),
		Agreement:                agreement,
	}, nil
}
