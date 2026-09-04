// Package latencytrace evaluates content-free voice latency observations.
package latencytrace

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"sort"
)

const (
	ObservationVersion = "kotae.voice-latency-trace.v1"
	ThresholdVersion   = "kotae.voice-latency-thresholds.v1"
	ReportVersion      = "kotae.voice-latency-report.v1"
	maxObservationMS   = int64(120_000)
	maxObservations    = 100_000
	maxLineBytes       = 64 << 10
)

var (
	ErrInvalidConfig      = errors.New("voice_latency_config_invalid")
	ErrInvalidObservation = errors.New("voice_latency_observation_invalid")
	revisionPattern       = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type Stages struct {
	SpeechEndToCommitSendMS     *int64 `json:"speechEndToCommitSendMs"`
	CommitSendToAckMS           *int64 `json:"commitSendToAckMs"`
	CommitAckToFirstBinaryMS    *int64 `json:"commitAckToFirstBinaryMs"`
	FirstBinaryToSpeakerWriteMS *int64 `json:"firstBinaryToSpeakerWriteMs"`
	ServerCommitToDrainMS       *int64 `json:"serverCommitToDrainMs"`
	ServerDrainToActivityEndMS  *int64 `json:"serverDrainToActivityEndMs"`
	ActivityEndToFinalMS        *int64 `json:"activityEndToFinalMs"`
	FinalToControlCommitMS      *int64 `json:"finalToControlCommitMs"`
	ControlCommitToFirstPCMMS   *int64 `json:"controlCommitToFirstPcmMs"`
}

type Observation struct {
	SchemaVersion             string `json:"schemaVersion"`
	Revision                  string `json:"revision"`
	Transport                 string `json:"transport"`
	Route                     string `json:"route"`
	DeviceClass               string `json:"deviceClass"`
	NetworkClass              string `json:"networkClass"`
	GestureToListeningMS      int64  `json:"gestureToListeningMs"`
	SpeechEndToSpeakerWriteMS int64  `json:"speechEndToSpeakerWriteMs"`
	Stages                    Stages `json:"stages"`
}

type Thresholds struct {
	SchemaVersion         string   `json:"schemaVersion"`
	MinimumTotal          int      `json:"minimumTotal"`
	MinimumPerTransport   int      `json:"minimumPerTransport"`
	MaximumP50MS          int64    `json:"maximumP50Ms"`
	MaximumP95MS          int64    `json:"maximumP95Ms"`
	MaximumListeningP95MS int64    `json:"maximumListeningP95Ms"`
	RequiredTransports    []string `json:"requiredTransports"`
}

type Percentiles struct {
	P50MS int64 `json:"p50Ms"`
	P95MS int64 `json:"p95Ms"`
	P99MS int64 `json:"p99Ms"`
}

type Group struct {
	Kind                    string       `json:"kind"`
	Value                   string       `json:"value"`
	Samples                 int          `json:"samples"`
	SpeechEndToSpeakerWrite *Percentiles `json:"speechEndToSpeakerWrite"`
	GestureToListening      *Percentiles `json:"gestureToListening"`
}

type Report struct {
	SchemaVersion           string       `json:"schemaVersion"`
	ObservationDigest       string       `json:"observationDigest"`
	ThresholdDigest         string       `json:"thresholdDigest"`
	Samples                 int          `json:"samples"`
	Decision                string       `json:"decision"`
	FailureCodes            []string     `json:"failureCodes"`
	SpeechEndToSpeakerWrite *Percentiles `json:"speechEndToSpeakerWrite"`
	GestureToListening      *Percentiles `json:"gestureToListening"`
	Groups                  []Group      `json:"groups"`
}

var observationKeys = []string{
	"deviceClass", "gestureToListeningMs", "networkClass", "revision",
	"route", "schemaVersion", "speechEndToSpeakerWriteMs", "stages", "transport",
}

var stageKeys = []string{
	"activityEndToFinalMs", "commitAckToFirstBinaryMs", "commitSendToAckMs",
	"controlCommitToFirstPcmMs", "finalToControlCommitMs",
	"firstBinaryToSpeakerWriteMs", "serverCommitToDrainMs",
	"serverDrainToActivityEndMs", "speechEndToCommitSendMs",
}

var thresholdKeys = []string{
	"maximumListeningP95Ms", "maximumP50Ms", "maximumP95Ms",
	"minimumPerTransport", "minimumTotal", "requiredTransports", "schemaVersion",
}

func LoadThresholds(data []byte) (Thresholds, error) {
	var value Thresholds
	if !exactKeys(data, thresholdKeys) || decodeExact(data, &value) != nil || !validThresholds(value) {
		return Thresholds{}, ErrInvalidConfig
	}
	return value, nil
}

func Evaluate(observationData, thresholdData []byte) (Report, error) {
	report := Report{
		SchemaVersion:     ReportVersion,
		ObservationDigest: digest(observationData),
		ThresholdDigest:   digest(thresholdData),
		Decision:          "insufficient",
		FailureCodes:      []string{},
		Groups:            []Group{},
	}
	thresholds, err := LoadThresholds(thresholdData)
	if err != nil {
		return report, err
	}
	observations, err := decodeObservations(observationData)
	if err != nil {
		return report, err
	}
	report.Samples = len(observations)
	report.Groups = summarizeGroups(observations, thresholds.MinimumPerTransport)
	if len(observations) < thresholds.MinimumTotal {
		report.FailureCodes = append(report.FailureCodes, "minimum_total_not_met")
		return report, nil
	}
	for _, transport := range thresholds.RequiredTransports {
		if countTransport(observations, transport) < thresholds.MinimumPerTransport {
			report.FailureCodes = append(report.FailureCodes, "minimum_transport_not_met:"+transport)
		}
	}
	if len(report.FailureCodes) > 0 {
		return report, nil
	}
	speaker := make([]int64, 0, len(observations))
	listening := make([]int64, 0, len(observations))
	for _, observation := range observations {
		speaker = append(speaker, observation.SpeechEndToSpeakerWriteMS)
		listening = append(listening, observation.GestureToListeningMS)
	}
	report.SpeechEndToSpeakerWrite = percentiles(speaker)
	report.GestureToListening = percentiles(listening)
	report.Decision = "accept"
	if report.SpeechEndToSpeakerWrite.P50MS > thresholds.MaximumP50MS {
		report.FailureCodes = append(report.FailureCodes, "speech_start_p50_exceeded")
	}
	if report.SpeechEndToSpeakerWrite.P95MS > thresholds.MaximumP95MS {
		report.FailureCodes = append(report.FailureCodes, "speech_start_p95_exceeded")
	}
	if report.GestureToListening.P95MS > thresholds.MaximumListeningP95MS {
		report.FailureCodes = append(report.FailureCodes, "listening_p95_exceeded")
	}
	if len(report.FailureCodes) > 0 {
		report.Decision = "reject"
	}
	return report, nil
}

func decodeObservations(data []byte) ([]Observation, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxLineBytes)
	values := []Observation{}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || len(values) >= maxObservations {
			return nil, ErrInvalidObservation
		}
		var raw map[string]json.RawMessage
		if !exactKeys(line, observationKeys) || decodeExact(line, &raw) != nil ||
			!exactKeys(raw["stages"], stageKeys) {
			return nil, ErrInvalidObservation
		}
		var value Observation
		if decodeExact(line, &value) != nil || !validObservation(value) {
			return nil, ErrInvalidObservation
		}
		values = append(values, value)
	}
	if scanner.Err() != nil || len(values) == 0 {
		return nil, ErrInvalidObservation
	}
	return values, nil
}

func validObservation(value Observation) bool {
	if value.SchemaVersion != ObservationVersion || !revisionPattern.MatchString(value.Revision) ||
		!oneOf(value.Transport, "http-buffered", "http-stream", "native-live") ||
		!oneOf(value.Route, "http-fallback", "initial-answer-support", "continuing-coach", "native-conversation", "strict-local") ||
		!oneOf(value.DeviceClass, "desktop", "mobile", "unknown") ||
		!oneOf(value.NetworkClass, "fast", "typical", "constrained", "unknown") ||
		!validMS(value.GestureToListeningMS) || !validMS(value.SpeechEndToSpeakerWriteMS) {
		return false
	}
	client := []*int64{
		value.Stages.SpeechEndToCommitSendMS, value.Stages.CommitSendToAckMS,
		value.Stages.CommitAckToFirstBinaryMS, value.Stages.FirstBinaryToSpeakerWriteMS,
	}
	var total int64
	for _, stage := range client {
		if stage == nil || !validMS(*stage) {
			return false
		}
		total += *stage
	}
	if total != value.SpeechEndToSpeakerWriteMS {
		return false
	}
	server := []*int64{
		value.Stages.ServerCommitToDrainMS, value.Stages.ServerDrainToActivityEndMS,
		value.Stages.ActivityEndToFinalMS, value.Stages.FinalToControlCommitMS,
		value.Stages.ControlCommitToFirstPCMMS,
	}
	for _, stage := range server {
		if stage != nil && !validMS(*stage) {
			return false
		}
	}
	return true
}

func validThresholds(value Thresholds) bool {
	if value.SchemaVersion != ThresholdVersion || value.MinimumTotal < 100 ||
		value.MinimumPerTransport < 100 || value.MaximumP50MS <= 0 ||
		value.MaximumP95MS < value.MaximumP50MS ||
		value.MaximumListeningP95MS <= 0 || len(value.RequiredTransports) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, transport := range value.RequiredTransports {
		if !oneOf(transport, "http-buffered", "http-stream", "native-live") || seen[transport] {
			return false
		}
		seen[transport] = true
	}
	return value.MinimumTotal >= value.MinimumPerTransport*len(value.RequiredTransports)
}

func summarizeGroups(values []Observation, minimum int) []Group {
	type groupValues struct{ speaker, listening []int64 }
	groups := map[string]*groupValues{}
	add := func(kind, value string, observation Observation) {
		key := kind + "\x00" + value
		if groups[key] == nil {
			groups[key] = &groupValues{}
		}
		groups[key].speaker = append(groups[key].speaker, observation.SpeechEndToSpeakerWriteMS)
		groups[key].listening = append(groups[key].listening, observation.GestureToListeningMS)
	}
	for _, value := range values {
		add("transport", value.Transport, value)
		add("route", value.Route, value)
		add("device", value.DeviceClass, value)
		add("network", value.NetworkClass, value)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]Group, 0, len(keys))
	for _, key := range keys {
		value := groups[key]
		separator := bytes.IndexByte([]byte(key), 0)
		group := Group{Kind: key[:separator], Value: key[separator+1:], Samples: len(value.speaker)}
		if group.Samples >= minimum {
			group.SpeechEndToSpeakerWrite = percentiles(value.speaker)
			group.GestureToListening = percentiles(value.listening)
		}
		result = append(result, group)
	}
	return result
}

func percentiles(values []int64) *Percentiles {
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	nearest := func(percent float64) int64 {
		index := int(math.Ceil(percent*float64(len(ordered)))) - 1
		if index < 0 {
			index = 0
		}
		return ordered[index]
	}
	return &Percentiles{P50MS: nearest(.50), P95MS: nearest(.95), P99MS: nearest(.99)}
}

func countTransport(values []Observation, target string) int {
	count := 0
	for _, value := range values {
		if value.Transport == target {
			count++
		}
	}
	return count
}

func validMS(value int64) bool { return value >= 0 && value <= maxObservationMS }

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func exactKeys(data []byte, expected []string) bool {
	if !uniqueJSONKeys(data) {
		return false
	}
	var value map[string]json.RawMessage
	if decodeExact(data, &value) != nil || len(value) != len(expected) {
		return false
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return equalStrings(keys, expected)
}

func uniqueJSONKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if !uniqueJSONValue(decoder) {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

func uniqueJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			if !uniqueJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !uniqueJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func decodeExact(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidObservation
	}
	return nil
}
