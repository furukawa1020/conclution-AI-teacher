package asreval

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluateAcceptsImprovedQuietSpeechWithoutNormalRegressionOrFalseActivation(t *testing.T) {
	fixture := newFixture(t)
	report, err := Evaluate(fixture.manifest, fixture.root, fixture.baseline, fixture.challenger, testThresholds())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.Decision != "accept" || len(report.FailureCodes) != 0 {
		t.Fatalf("decision = %q, failures = %v", report.Decision, report.FailureCodes)
	}
	if report.Challenger.QuietRecallPPM != 1_000_000 || report.Challenger.NoSpeechFalseActivationPPM != 0 {
		t.Fatalf("challenger metrics = %+v", report.Challenger)
	}
	if report.Baseline.QuietCERPPM <= report.Challenger.QuietCERPPM {
		t.Fatalf("quiet CER did not improve: baseline=%d challenger=%d", report.Baseline.QuietCERPPM, report.Challenger.QuietCERPPM)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"こんにちは", "聞こえました", "baseline hypothesis", "challenger hypothesis"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("report leaked content %q: %s", forbidden, encoded)
		}
	}
}

func TestEvaluateRejectsNoSpeechActivationAndNormalRegression(t *testing.T) {
	fixture := newFixture(t)
	lines := readResultLines(t, fixture.challenger)
	for index := range lines {
		if strings.Contains(lines[index], `"sampleId":"cough-00"`) {
			lines[index] = `{"sampleId":"cough-00","hypothesis":"hallucinated content"}`
		}
		if strings.Contains(lines[index], `"sampleId":"normal_speech-00"`) {
			lines[index] = `{"sampleId":"normal_speech-00","hypothesis":""}`
		}
	}
	writeFile(t, fixture.challenger, []byte(strings.Join(lines, "\n")+"\n"))
	report, err := Evaluate(fixture.manifest, fixture.root, fixture.baseline, fixture.challenger, testThresholds())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.Decision != "reject" || !contains(report.FailureCodes, "no_speech_false_activation_above_ceiling") {
		t.Fatalf("decision = %q, failures = %v", report.Decision, report.FailureCodes)
	}
	if !contains(report.FailureCodes, "normal_speech_regressed") {
		t.Fatalf("normal regression not reported: %v", report.FailureCodes)
	}
}

func TestEvaluateFailsClosedForDigestTraversalUnknownFieldAndMissingResult(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture evalFixture)
		want   error
	}{
		{
			name: "asset digest",
			mutate: func(t *testing.T, fixture evalFixture) {
				path := filepath.Join(fixture.root, "audio", "quiet_speech-00.wav")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data[len(data)-1] ^= 1
				writeFile(t, path, data)
			},
			want: ErrInvalidAsset,
		},
		{
			name: "path traversal",
			mutate: func(t *testing.T, fixture evalFixture) {
				var manifest Manifest
				decodeFile(t, fixture.manifest, &manifest)
				manifest.Samples[0].AudioPath = "../outside.wav"
				writeJSON(t, fixture.manifest, manifest)
			},
			want: ErrInvalidAsset,
		},
		{
			name: "unknown result field",
			mutate: func(t *testing.T, fixture evalFixture) {
				lines := readResultLines(t, fixture.challenger)
				lines[0] = `{"sampleId":"quiet_speech-00","hypothesis":"こんにちは","transcript":"leak"}`
				writeFile(t, fixture.challenger, []byte(strings.Join(lines, "\n")+"\n"))
			},
			want: ErrInvalidResults,
		},
		{
			name: "missing result",
			mutate: func(t *testing.T, fixture evalFixture) {
				lines := readResultLines(t, fixture.challenger)
				writeFile(t, fixture.challenger, []byte(strings.Join(lines[1:], "\n")+"\n"))
			},
			want: ErrInvalidResults,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			test.mutate(t, fixture)
			_, err := Evaluate(fixture.manifest, fixture.root, fixture.baseline, fixture.challenger, testThresholds())
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestFiniteCharacterErrorBuckets(t *testing.T) {
	if got := errorBucket(editDistance([]rune("こんにちは"), []rune("こんにちは")), 5); got != "exact" {
		t.Fatalf("exact = %q", got)
	}
	if got := errorBucket(editDistance([]rune("こんにちは"), []rune("こんにちわ")), 5); got != "minor" {
		t.Fatalf("minor = %q", got)
	}
	if got := errorBucket(2, 5); got != "major" {
		t.Fatalf("major = %q", got)
	}
	if got := errorBucket(editDistance([]rune("こんにちは"), nil), 5); got != "miss" {
		t.Fatalf("miss = %q", got)
	}
}

type evalFixture struct {
	root, manifest, baseline, challenger string
}

func newFixture(t *testing.T) evalFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "audio"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	license := []byte(`{"schemaVersion":"kotae.audio-license-manifest.v1","redistributed":false,"uses":["asr_evaluation"]}`)
	writeFile(t, filepath.Join(root, "licenses.json"), license)
	if _, err := readBoundedAsset(root, "licenses.json", digest(license), 1<<20); err != nil {
		t.Fatalf("license fixture invalid: %v", err)
	}
	manifest := Manifest{
		SchemaVersion:         SchemaVersion,
		CorpusID:              "licensed-real-audio-v1",
		LicenseManifestPath:   "licenses.json",
		LicenseManifestSHA256: digest(license),
		PreprocessingID:       "pcm16k-mono-v1",
	}
	var baseline, challenger strings.Builder
	for _, bucket := range requiredBuckets() {
		for index := 0; index < 10; index++ {
			id := fmt.Sprintf("%s-%02d", bucket, index)
			audio := wavFixture(int16(index + 1))
			if !canonicalWAV(audio) {
				t.Fatal("WAV fixture is not canonical")
			}
			audioName := filepath.ToSlash(filepath.Join("audio", id+".wav"))
			writeFile(t, filepath.Join(root, filepath.FromSlash(audioName)), audio)
			if _, err := readBoundedAsset(root, audioName, digest(audio), 64<<20); err != nil {
				t.Fatalf("audio fixture invalid: %v", err)
			}
			sample := Sample{ID: id, Bucket: bucket, AudioPath: audioName, AudioSHA256: digest(audio)}
			baselineHypothesis, challengerHypothesis := "", ""
			if isSpeechBucket(bucket) {
				reference := []byte("こんにちは")
				referenceName := filepath.ToSlash(filepath.Join("references", id+".txt"))
				writeFile(t, filepath.Join(root, filepath.FromSlash(referenceName)), reference)
				if _, err := readBoundedAsset(root, referenceName, digest(reference), 64<<10); err != nil {
					t.Fatalf("reference fixture invalid: %v", err)
				}
				sample.ReferencePath = referenceName
				sample.ReferenceSHA256 = digest(reference)
				challengerHypothesis = "こんにちは"
				baselineHypothesis = "こんにちは"
				if bucket != BucketNormalSpeech {
					baselineHypothesis = "こん"
				}
			}
			manifest.Samples = append(manifest.Samples, sample)
			fmt.Fprintf(&baseline, "{\"sampleId\":%q,\"hypothesis\":%q}\n", id, baselineHypothesis)
			fmt.Fprintf(&challenger, "{\"sampleId\":%q,\"hypothesis\":%q}\n", id, challengerHypothesis)
		}
	}
	manifestPath := filepath.Join(root, "manifest.json")
	writeJSON(t, manifestPath, manifest)
	baselinePath := filepath.Join(root, "baseline.jsonl")
	challengerPath := filepath.Join(root, "challenger.jsonl")
	writeFile(t, baselinePath, []byte(baseline.String()))
	writeFile(t, challengerPath, []byte(challenger.String()))
	return evalFixture{root: root, manifest: manifestPath, baseline: baselinePath, challenger: challengerPath}
}

func testThresholds() Thresholds {
	return Thresholds{MinimumPerBucket: 10, QuietRecallPPM: 850_000, MaximumNoSpeechFalseActivationPPM: 0, MaximumQuietCERPPM: 350_000, NormalCERNonInferiorityMarginPPM: 20_000, MinimumQuietCERImprovementPPM: 10_000}
}

func wavFixture(sample int16) []byte {
	data := make([]byte, 320)
	for index := 0; index < len(data); index += 2 {
		binary.LittleEndian.PutUint16(data[index:], uint16(sample))
	}
	wav := make([]byte, 44+len(data))
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], 16_000)
	binary.LittleEndian.PutUint32(wav[28:32], 32_000)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(len(data)))
	copy(wav[44:], data)
	return wav
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, data)
}
func decodeFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}
func readResultLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
