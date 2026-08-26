package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/furukawa1020/conclution-ai-teacher/internal/asreval"
)

func main() {
	manifest := flag.String("manifest", "", "digest-fixed corpus manifest")
	root := flag.String("corpus-root", "", "private corpus root")
	baseline := flag.String("baseline", "", "baseline JSONL")
	challenger := flag.String("challenger", "", "challenger JSONL")
	minimum := flag.Int("minimum-per-bucket", 10, "minimum samples per fixed bucket")
	flag.Parse()
	if *manifest == "" || *root == "" || *baseline == "" || *challenger == "" {
		fail("asr_evaluation_arguments_invalid")
	}
	report, err := asreval.Evaluate(*manifest, *root, *baseline, *challenger, asreval.Thresholds{
		MinimumPerBucket:                  *minimum,
		QuietRecallPPM:                    850_000,
		MaximumNoSpeechFalseActivationPPM: 0,
		MaximumQuietCERPPM:                350_000,
		NormalCERNonInferiorityMarginPPM:  20_000,
		MinimumQuietCERImprovementPPM:     10_000,
	})
	if err != nil {
		fail(err.Error())
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(report); err != nil {
		fail("asr_evaluation_report_failed")
	}
	if report.Decision != "accept" {
		os.Exit(1)
	}
}

func fail(code string) {
	fmt.Fprintln(os.Stderr, code)
	os.Exit(2)
}
