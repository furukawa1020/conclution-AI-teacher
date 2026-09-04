package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/furukawa1020/conclution-ai-teacher/internal/latencytrace"
)

func main() {
	observationsPath := flag.String("observations", "", "content-free voice latency NDJSON")
	thresholdsPath := flag.String("thresholds", "config/voice-latency-slo.json", "fixed threshold JSON")
	flag.Parse()
	if *observationsPath == "" || *thresholdsPath == "" {
		fail("voice_latency_arguments_invalid")
	}
	observations, err := os.ReadFile(*observationsPath)
	if err != nil {
		fail("voice_latency_observations_unavailable")
	}
	thresholds, err := os.ReadFile(*thresholdsPath)
	if err != nil {
		fail("voice_latency_thresholds_unavailable")
	}
	report, err := latencytrace.Evaluate(observations, thresholds)
	if err != nil {
		fail(err.Error())
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(report); err != nil {
		fail("voice_latency_report_failed")
	}
	if report.Decision != "accept" {
		os.Exit(1)
	}
}

func fail(code string) {
	fmt.Fprintln(os.Stderr, code)
	os.Exit(2)
}
