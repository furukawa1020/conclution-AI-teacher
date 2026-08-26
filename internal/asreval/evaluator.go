package asreval

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	SchemaVersion = "kotae.asr-evaluation-corpus.v1"
	ReportVersion = "kotae.asr-evaluation-report.v1"

	BucketQuietSpeech  = "quiet_speech"
	BucketMutter       = "mutter"
	BucketNormalSpeech = "normal_speech"
	BucketCough        = "cough"
	BucketHVAC         = "hvac"
	BucketPlaybackLeak = "playback_leak"
)

var (
	ErrInvalidManifest = errors.New("asr_evaluation_manifest_invalid")
	ErrInvalidAsset    = errors.New("asr_evaluation_asset_invalid")
	ErrInvalidResults  = errors.New("asr_evaluation_results_invalid")
	identifierPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

type Manifest struct {
	SchemaVersion         string   `json:"schemaVersion"`
	CorpusID              string   `json:"corpusId"`
	LicenseManifestPath   string   `json:"licenseManifestPath"`
	LicenseManifestSHA256 string   `json:"licenseManifestSha256"`
	PreprocessingID       string   `json:"preprocessingId"`
	Samples               []Sample `json:"samples"`
}

type Sample struct {
	ID              string `json:"id"`
	Bucket          string `json:"bucket"`
	AudioPath       string `json:"audioPath"`
	AudioSHA256     string `json:"audioSha256"`
	ReferencePath   string `json:"referencePath,omitempty"`
	ReferenceSHA256 string `json:"referenceSha256,omitempty"`
}

type Thresholds struct {
	MinimumPerBucket                  int `json:"minimumPerBucket"`
	QuietRecallPPM                    int `json:"quietRecallPpm"`
	MaximumNoSpeechFalseActivationPPM int `json:"maximumNoSpeechFalseActivationPpm"`
	MaximumQuietCERPPM                int `json:"maximumQuietCerPpm"`
	NormalCERNonInferiorityMarginPPM  int `json:"normalCerNonInferiorityMarginPpm"`
	MinimumQuietCERImprovementPPM     int `json:"minimumQuietCerImprovementPpm"`
}

type Result struct {
	SampleID   string `json:"sampleId"`
	Hypothesis string `json:"hypothesis"`
}

type Metrics struct {
	QuietRecallPPM             int            `json:"quietRecallPpm"`
	QuietCERPPM                int            `json:"quietCerPpm"`
	NormalCERPPM               int            `json:"normalCerPpm"`
	NoSpeechFalseActivationPPM int            `json:"noSpeechFalseActivationPpm"`
	CharacterErrorBuckets      map[string]int `json:"characterErrorBuckets"`
}

type Report struct {
	SchemaVersion string         `json:"schemaVersion"`
	CorpusDigest  string         `json:"corpusDigest"`
	CorpusID      string         `json:"corpusId"`
	SampleCounts  map[string]int `json:"sampleCounts"`
	Baseline      Metrics        `json:"baseline"`
	Challenger    Metrics        `json:"challenger"`
	Decision      string         `json:"decision"`
	FailureCodes  []string       `json:"failureCodes"`
}

type preparedSample struct {
	sample    Sample
	reference string
}

func Evaluate(manifestPath, corpusRoot, baselinePath, challengerPath string, thresholds Thresholds) (Report, error) {
	report := Report{SchemaVersion: ReportVersion, Decision: "reject"}
	if !validThresholds(thresholds) {
		return report, ErrInvalidManifest
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return report, ErrInvalidManifest
	}
	var manifest Manifest
	if err := decodeExact(manifestBytes, &manifest); err != nil || !validManifestHeader(manifest) {
		return report, ErrInvalidManifest
	}
	digest := sha256.Sum256(manifestBytes)
	report.CorpusDigest = hex.EncodeToString(digest[:])
	report.CorpusID = manifest.CorpusID

	prepared, counts, err := prepareCorpus(corpusRoot, manifest, thresholds.MinimumPerBucket)
	if err != nil {
		return report, err
	}
	report.SampleCounts = counts
	baseline, err := loadResults(baselinePath, prepared)
	if err != nil {
		return report, err
	}
	challenger, err := loadResults(challengerPath, prepared)
	if err != nil {
		return report, err
	}
	report.Baseline = measure(prepared, baseline)
	report.Challenger = measure(prepared, challenger)
	report.FailureCodes = decide(report.Baseline, report.Challenger, thresholds)
	if len(report.FailureCodes) == 0 {
		report.Decision = "accept"
	}
	return report, nil
}

func validThresholds(value Thresholds) bool {
	return value.MinimumPerBucket >= 10 &&
		value.QuietRecallPPM >= 0 && value.QuietRecallPPM <= 1_000_000 &&
		value.MaximumNoSpeechFalseActivationPPM >= 0 && value.MaximumNoSpeechFalseActivationPPM <= 1_000_000 &&
		value.MaximumQuietCERPPM >= 0 && value.MaximumQuietCERPPM <= 1_000_000 &&
		value.NormalCERNonInferiorityMarginPPM >= 0 && value.NormalCERNonInferiorityMarginPPM <= 1_000_000 &&
		value.MinimumQuietCERImprovementPPM >= 0 && value.MinimumQuietCERImprovementPPM <= 1_000_000
}

func validManifestHeader(value Manifest) bool {
	return value.SchemaVersion == SchemaVersion && identifierPattern.MatchString(value.CorpusID) &&
		identifierPattern.MatchString(value.PreprocessingID) && validDigest(value.LicenseManifestSHA256) &&
		value.LicenseManifestPath != "" && len(value.Samples) > 0
}

func prepareCorpus(root string, manifest Manifest, minimum int) (map[string]preparedSample, map[string]int, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, ErrInvalidAsset
	}
	licenseBytes, err := readBoundedAsset(root, manifest.LicenseManifestPath, manifest.LicenseManifestSHA256, 1<<20)
	if err != nil || len(bytes.TrimSpace(licenseBytes)) == 0 {
		return nil, nil, ErrInvalidAsset
	}
	prepared := make(map[string]preparedSample, len(manifest.Samples))
	counts := make(map[string]int, len(requiredBuckets()))
	for _, sample := range manifest.Samples {
		if !identifierPattern.MatchString(sample.ID) || !isBucket(sample.Bucket) || validDigest(sample.AudioSHA256) == false {
			return nil, nil, ErrInvalidManifest
		}
		if _, exists := prepared[sample.ID]; exists {
			return nil, nil, ErrInvalidManifest
		}
		audio, err := readBoundedAsset(root, sample.AudioPath, sample.AudioSHA256, 64<<20)
		if err != nil || !canonicalWAV(audio) {
			return nil, nil, ErrInvalidAsset
		}
		entry := preparedSample{sample: sample}
		if isSpeechBucket(sample.Bucket) {
			if sample.ReferencePath == "" || !validDigest(sample.ReferenceSHA256) {
				return nil, nil, ErrInvalidManifest
			}
			reference, err := readBoundedAsset(root, sample.ReferencePath, sample.ReferenceSHA256, 64<<10)
			if err != nil || normalize(string(reference)) == "" {
				return nil, nil, ErrInvalidAsset
			}
			entry.reference = string(reference)
		} else if sample.ReferencePath != "" || sample.ReferenceSHA256 != "" {
			return nil, nil, ErrInvalidManifest
		}
		prepared[sample.ID] = entry
		counts[sample.Bucket]++
	}
	for _, bucket := range requiredBuckets() {
		if counts[bucket] < minimum {
			return nil, nil, ErrInvalidManifest
		}
	}
	return prepared, counts, nil
}

func readBoundedAsset(root, name, expectedDigest string, maximum int64) ([]byte, error) {
	if name == "" || filepath.IsAbs(name) {
		return nil, ErrInvalidAsset
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return nil, ErrInvalidAsset
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, ErrInvalidAsset
	}
	if containsSymlink(root, relative) {
		return nil, ErrInvalidAsset
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalidAsset
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, ErrInvalidAsset
	}
	digest := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), expectedDigest) {
		return nil, ErrInvalidAsset
	}
	return data, nil
}

func containsSymlink(root, relative string) bool {
	current := root
	for _, component := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

func canonicalWAV(data []byte) bool {
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" || string(data[12:16]) != "fmt " {
		return false
	}
	le16 := func(offset int) uint16 { return uint16(data[offset]) | uint16(data[offset+1])<<8 }
	le32 := func(offset int) uint32 {
		return uint32(data[offset]) | uint32(data[offset+1])<<8 | uint32(data[offset+2])<<16 | uint32(data[offset+3])<<24
	}
	return le32(16) == 16 && le16(20) == 1 && le16(22) == 1 && le32(24) == 16_000 && le16(34) == 16 && string(data[36:40]) == "data" && int(le32(40))+44 == len(data)
}

func loadResults(path string, samples map[string]preparedSample) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalidResults
	}
	defer file.Close()
	results := make(map[string]string, len(samples))
	scanner := bufio.NewScanner(io.LimitReader(file, 16<<20))
	scanner.Buffer(make([]byte, 4096), 128<<10)
	for scanner.Scan() {
		var result Result
		if err := decodeExact(scanner.Bytes(), &result); err != nil || !identifierPattern.MatchString(result.SampleID) {
			return nil, ErrInvalidResults
		}
		if _, exists := samples[result.SampleID]; !exists {
			return nil, ErrInvalidResults
		}
		if _, exists := results[result.SampleID]; exists || len([]rune(result.Hypothesis)) > 4096 {
			return nil, ErrInvalidResults
		}
		results[result.SampleID] = result.Hypothesis
	}
	if scanner.Err() != nil || len(results) != len(samples) {
		return nil, ErrInvalidResults
	}
	return results, nil
}

func measure(samples map[string]preparedSample, results map[string]string) Metrics {
	metrics := Metrics{CharacterErrorBuckets: map[string]int{"exact": 0, "minor": 0, "major": 0, "miss": 0}}
	quietTotal, quietRecognized, quietReferenceChars, quietErrors := 0, 0, 0, 0
	normalReferenceChars, normalErrors := 0, 0
	noSpeechTotal, falseActivations := 0, 0
	ids := make([]string, 0, len(samples))
	for id := range samples {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		sample := samples[id]
		hypothesis := normalize(results[id])
		if !isSpeechBucket(sample.sample.Bucket) {
			noSpeechTotal++
			if hypothesis != "" {
				falseActivations++
			}
			continue
		}
		reference := normalize(sample.reference)
		errors := editDistance([]rune(reference), []rune(hypothesis))
		characters := len([]rune(reference))
		bucket := errorBucket(errors, characters)
		metrics.CharacterErrorBuckets[bucket]++
		if sample.sample.Bucket == BucketNormalSpeech {
			normalErrors += errors
			normalReferenceChars += characters
		} else {
			quietTotal++
			quietErrors += errors
			quietReferenceChars += characters
			if errors*2 <= characters {
				quietRecognized++
			}
		}
	}
	metrics.QuietRecallPPM = fractionPPM(quietRecognized, quietTotal)
	metrics.QuietCERPPM = fractionPPM(quietErrors, quietReferenceChars)
	metrics.NormalCERPPM = fractionPPM(normalErrors, normalReferenceChars)
	metrics.NoSpeechFalseActivationPPM = fractionPPM(falseActivations, noSpeechTotal)
	return metrics
}

func decide(baseline, challenger Metrics, thresholds Thresholds) []string {
	var failures []string
	if challenger.QuietRecallPPM < thresholds.QuietRecallPPM {
		failures = append(failures, "quiet_recall_below_floor")
	}
	if challenger.NoSpeechFalseActivationPPM > thresholds.MaximumNoSpeechFalseActivationPPM {
		failures = append(failures, "no_speech_false_activation_above_ceiling")
	}
	if challenger.QuietCERPPM > thresholds.MaximumQuietCERPPM {
		failures = append(failures, "quiet_cer_above_ceiling")
	}
	if challenger.NormalCERPPM > baseline.NormalCERPPM+thresholds.NormalCERNonInferiorityMarginPPM {
		failures = append(failures, "normal_speech_regressed")
	}
	if baseline.QuietCERPPM-challenger.QuietCERPPM < thresholds.MinimumQuietCERImprovementPPM {
		failures = append(failures, "quiet_cer_improvement_insufficient")
	}
	return failures
}

func normalize(value string) string {
	value = norm.NFKC.String(strings.ToLower(strings.TrimSpace(value)))
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return r
	}, value)
}

func editDistance(left, right []rune) int {
	row := make([]int, len(right)+1)
	for index := range row {
		row[index] = index
	}
	for i, a := range left {
		previous := row[0]
		row[0] = i + 1
		for j, b := range right {
			old := row[j+1]
			cost := 0
			if a != b {
				cost = 1
			}
			row[j+1] = min(row[j+1]+1, row[j]+1, previous+cost)
			previous = old
		}
	}
	return row[len(right)]
}

func errorBucket(errors, characters int) string {
	if errors == 0 {
		return "exact"
	}
	if errors*5 <= characters {
		return "minor"
	}
	if errors*2 <= characters {
		return "major"
	}
	return "miss"
}

func fractionPPM(numerator, denominator int) int {
	if denominator <= 0 {
		return 1_000_000
	}
	value := int64(numerator) * 1_000_000 / int64(denominator)
	if value > 1_000_000 {
		return 1_000_000
	}
	return int(value)
}

func requiredBuckets() []string {
	return []string{BucketQuietSpeech, BucketMutter, BucketNormalSpeech, BucketCough, BucketHVAC, BucketPlaybackLeak}
}
func isBucket(value string) bool {
	for _, bucket := range requiredBuckets() {
		if value == bucket {
			return true
		}
	}
	return false
}
func isSpeechBucket(value string) bool {
	return value == BucketQuietSpeech || value == BucketMutter || value == BucketNormalSpeech
}
func validDigest(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == sha256.Size*2 && err == nil && value == strings.ToLower(value)
}

func decodeExact(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalidManifest
	}
	return nil
}
