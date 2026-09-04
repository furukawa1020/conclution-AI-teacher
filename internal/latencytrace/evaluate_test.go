package latencytrace

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const testRevision = "0123456789abcdef0123456789abcdef01234567"

func TestEvaluateAcceptsQualifiedThreeTransportSample(t *testing.T) {
	data := sampleNDJSON(t, 100, func(transport string, index int) map[string]any {
		speaker := int64(500)
		if index >= 95 {
			speaker = 900
		}
		return observation(transport, speaker, 400)
	})

	report, err := Evaluate(data, thresholdJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision != "accept" || report.Samples != 300 {
		t.Fatalf("decision=%q samples=%d failures=%v", report.Decision, report.Samples, report.FailureCodes)
	}
	if report.SpeechEndToSpeakerWrite == nil || report.SpeechEndToSpeakerWrite.P95MS != 500 || report.SpeechEndToSpeakerWrite.P99MS != 900 {
		t.Fatalf("unexpected percentiles: %#v", report.SpeechEndToSpeakerWrite)
	}
	if len(report.ObservationDigest) != 64 || len(report.ThresholdDigest) != 64 {
		t.Fatalf("missing reproducibility digests: %#v", report)
	}
	for _, group := range report.Groups {
		if group.Kind == "transport" && group.SpeechEndToSpeakerWrite == nil {
			t.Fatalf("qualified transport group has no percentiles: %#v", group)
		}
	}
}

func TestEvaluateWithholdsPercentilesUntilMinimumSample(t *testing.T) {
	data := sampleNDJSON(t, 99, func(transport string, index int) map[string]any {
		return observation(transport, 300, 200)
	})

	report, err := Evaluate(data, thresholdJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision != "insufficient" || report.SpeechEndToSpeakerWrite != nil || report.GestureToListening != nil {
		t.Fatalf("unqualified sample emitted a claim: %#v", report)
	}
	if !contains(report.FailureCodes, "minimum_total_not_met") {
		t.Fatalf("missing minimum failure: %v", report.FailureCodes)
	}
}

func TestEvaluateRequiresEveryTransport(t *testing.T) {
	lines := make([]string, 0, 300)
	for index := 0; index < 200; index++ {
		lines = append(lines, marshalLine(t, observation("http-buffered", 300, 200)))
	}
	for index := 0; index < 100; index++ {
		lines = append(lines, marshalLine(t, observation("http-stream", 300, 200)))
	}

	report, err := Evaluate([]byte(strings.Join(lines, "\n")+"\n"), thresholdJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision != "insufficient" || !contains(report.FailureCodes, "minimum_transport_not_met:native-live") {
		t.Fatalf("missing transport was accepted: %#v", report)
	}
	if report.SpeechEndToSpeakerWrite != nil {
		t.Fatal("global percentiles must be withheld for incomplete coverage")
	}
}

func TestEvaluateRejectsExceededBudget(t *testing.T) {
	data := sampleNDJSON(t, 100, func(transport string, index int) map[string]any {
		speaker := int64(700)
		if index >= 90 {
			speaker = 1100
		}
		return observation(transport, speaker, 1100)
	})

	report, err := Evaluate(data, thresholdJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision != "reject" {
		t.Fatalf("over-budget sample was not rejected: %#v", report)
	}
	for _, code := range []string{"speech_start_p50_exceeded", "speech_start_p95_exceeded", "listening_p95_exceeded"} {
		if !contains(report.FailureCodes, code) {
			t.Errorf("missing failure %q in %v", code, report.FailureCodes)
		}
	}
}

func TestEvaluateRejectsContentAndIdentityFields(t *testing.T) {
	for _, forbidden := range []string{"transcript", "caption", "uid", "token", "audio", "error"} {
		t.Run(forbidden, func(t *testing.T) {
			value := observation("native-live", 300, 200)
			value[forbidden] = "must-not-enter-trace"
			_, err := Evaluate(append(mustJSON(t, value), '\n'), thresholdJSON(t))
			if err != ErrInvalidObservation {
				t.Fatalf("forbidden field %q: got %v", forbidden, err)
			}
		})
	}
}

func TestEvaluateRejectsDuplicateKeys(t *testing.T) {
	line := marshalLine(t, observation("native-live", 300, 200))
	line = strings.Replace(line, `"revision":"`+testRevision+`"`, `"revision":"`+testRevision+`","revision":"`+testRevision+`"`, 1)
	_, err := Evaluate([]byte(line+"\n"), thresholdJSON(t))
	if err != ErrInvalidObservation {
		t.Fatalf("duplicate key: got %v", err)
	}
}

func TestEvaluateRejectsInvalidOrInconsistentDurations(t *testing.T) {
	tests := map[string]func(map[string]any){
		"negative":   func(value map[string]any) { value["gestureToListeningMs"] = -1 },
		"over-bound": func(value map[string]any) { value["gestureToListeningMs"] = maxObservationMS + 1 },
		"sum-mismatch": func(value map[string]any) {
			value["speechEndToSpeakerWriteMs"] = 301
		},
		"invalid-revision":  func(value map[string]any) { value["revision"] = "main" },
		"invalid-transport": func(value map[string]any) { value["transport"] = "webtransport" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := observation("native-live", 300, 200)
			mutate(value)
			_, err := Evaluate(append(mustJSON(t, value), '\n'), thresholdJSON(t))
			if err != ErrInvalidObservation {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestEvaluateAllowsUnavailableServerStages(t *testing.T) {
	value := observation("http-buffered", 300, 200)
	report, err := Evaluate(append(mustJSON(t, value), '\n'), oneTransportThresholdJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision != "insufficient" { // The fixed contract never permits claims below 100 samples.
		t.Fatalf("decision=%q", report.Decision)
	}
}

func TestEvaluateIsDeterministic(t *testing.T) {
	data := sampleNDJSON(t, 100, func(transport string, index int) map[string]any {
		return observation(transport, int64(250+index), int64(100+index))
	})
	thresholds := thresholdJSON(t)
	first, err := Evaluate(data, thresholds)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Evaluate(data, thresholds)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON := mustJSON(t, first)
	secondJSON := mustJSON(t, second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("reports differ:\n%s\n%s", firstJSON, secondJSON)
	}
}

func sampleNDJSON(t *testing.T, perTransport int, build func(string, int) map[string]any) []byte {
	t.Helper()
	var lines []string
	for _, transport := range []string{"http-buffered", "http-stream", "native-live"} {
		for index := 0; index < perTransport; index++ {
			lines = append(lines, marshalLine(t, build(transport, index)))
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func observation(transport string, speaker, listening int64) map[string]any {
	return map[string]any{
		"schemaVersion":             ObservationVersion,
		"revision":                  testRevision,
		"transport":                 transport,
		"route":                     "native-conversation",
		"deviceClass":               "desktop",
		"networkClass":              "typical",
		"gestureToListeningMs":      listening,
		"speechEndToSpeakerWriteMs": speaker,
		"stages": map[string]any{
			"speechEndToCommitSendMs":     speaker / 4,
			"commitSendToAckMs":           speaker / 4,
			"commitAckToFirstBinaryMs":    speaker / 4,
			"firstBinaryToSpeakerWriteMs": speaker - 3*(speaker/4),
			"serverCommitToDrainMs":       nil,
			"serverDrainToActivityEndMs":  nil,
			"activityEndToFinalMs":        nil,
			"finalToControlCommitMs":      nil,
			"controlCommitToFirstPcmMs":   nil,
		},
	}
}

func thresholdJSON(t *testing.T) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"schemaVersion":         ThresholdVersion,
		"minimumTotal":          300,
		"minimumPerTransport":   100,
		"maximumP50Ms":          600,
		"maximumP95Ms":          1000,
		"maximumListeningP95Ms": 1000,
		"requiredTransports":    []string{"http-buffered", "http-stream", "native-live"},
	})
}

func oneTransportThresholdJSON(t *testing.T) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"schemaVersion":         ThresholdVersion,
		"minimumTotal":          100,
		"minimumPerTransport":   100,
		"maximumP50Ms":          600,
		"maximumP95Ms":          1000,
		"maximumListeningP95Ms": 1000,
		"requiredTransports":    []string{"http-buffered"},
	})
}

func marshalLine(t *testing.T, value any) string {
	t.Helper()
	return string(mustJSON(t, value))
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func ExampleEvaluate() {
	fmt.Println(ReportVersion)
	// Output: kotae.voice-latency-report.v1
}
