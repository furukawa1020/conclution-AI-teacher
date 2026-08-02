package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/firebase/genkit/go/core"
	"github.com/furukawa1020/conclution-ai-teacher/internal/contracts"
	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/evaluation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/research"
	"github.com/furukawa1020/conclution-ai-teacher/internal/store"
)

const (
	maxAudioBytes    = 2 * 1024 * 1024
	maxDocumentBytes = 7 * 1024 * 1024
	maxStateBytes    = conversation.MaxStateTokenBytes
	maxCaptionRunes  = 480
	allowedWebOrigin = "https://kotae-ai.web.app"
	voiceStreamPath  = "/api/v1/voice/turns:stream"
	voiceLivePath    = "/api/v1/voice/live"

	voiceStreamVersion       = 1
	voiceStreamSampleRateHz  = 24_000
	voiceStreamMaxChunkBytes = 1 << 20
	voiceStreamMaxAudioBytes = 16 << 20
	voiceStreamMaxChunks     = 512
)

var (
	ErrVoiceNotRecognized = errors.New("voice was not recognized")
	ErrVoiceStateInvalid  = errors.New("voice state is invalid")
	// ErrVoiceNativeFallback requests one replay through the staged voice
	// service. It is valid only for an explicit native live turn before any
	// response audio crosses the wire.
	ErrVoiceNativeFallback = errors.New("voice native fallback required")
)

type VoiceDocument struct {
	MIMEType string
	Data     []byte
}

type VoiceTurnMode string

const (
	VoiceTurnIntentional VoiceTurnMode = "intentional"
	VoiceTurnForeground  VoiceTurnMode = "foreground"
	VoiceTurnAmbient     VoiceTurnMode = "ambient"
)

type VoiceTurnInput struct {
	Audio                   []byte
	MIMEType                string
	StateToken              string
	RequestID               string
	TurnMode                VoiceTurnMode
	Ambient                 bool
	Foreground              bool
	Document                *VoiceDocument
	STTLocale               string
	SchemaVersion           int
	StrictCloudMinimization bool
	// NativeAudio is an authenticated, client-selected live-only fast lane.
	// It is mutually exclusive with strict cloud minimization and is never
	// inferred for buffered HTTP turns.
	NativeAudio bool
	// ProcessingTimeout is server-authored. Live pipelines start this budget
	// at the audio commit boundary so downstream agents can observe the same
	// deadline that the transport enforces.
	ProcessingTimeout time.Duration
	// ProcessingDeadline carries the single server-authored absolute deadline
	// published at live audio commit. It is never decoded from client input.
	ProcessingDeadline <-chan time.Time
	// ProcessingCommitted is a broadcast-only server signal. A closed channel
	// means no new speculative model work may start.
	ProcessingCommitted <-chan struct{}
}

type VoiceTurnResult struct {
	Audio            []byte
	AudioMIMEType    string
	StateToken       string
	DetectedDomain   string
	AssistanceTarget string
	RespondentStage  string
	CoachPhase       string
	CoachAction      string
	ResearchStatus   string
	PrivacyStatus    string
	ResearchRecords  []ResearchRecord
	Route            string
	NeedsPaper       bool
	Caption          string
	LiveTimings      VoiceLiveTimings
}

type VoiceLiveTimings struct {
	STTFirstInterimMS   int64
	STTFinalMS          int64
	ConversationMS      int64
	TTSFirstChunkMS     int64
	FinalToFirstAudioMS int64
	SpecHit             int64
	SpecMiss            int64
	SpecCancel          int64
	TTSPrestarted       int64
	TTSBufferedBytes    int64
	TTSReleaseMS        int64
}

type ResearchRecord struct {
	Title     string `json:"title"`
	DOI       string `json:"doi"`
	URL       string `json:"url"`
	Published string `json:"published"`
	Source    string `json:"source"`
}

type VoiceTurnService interface {
	Process(ctx context.Context, uid string, input VoiceTurnInput) (VoiceTurnResult, error)
}

type VoiceTurnStreamService interface {
	ProcessStream(
		ctx context.Context,
		uid string,
		input VoiceTurnInput,
		onAudio func([]byte) error,
	) (VoiceTurnResult, error)
}

type VoiceTurnLiveService interface {
	ProcessLive(
		ctx context.Context,
		uid string,
		input VoiceTurnInput,
		audio <-chan []byte,
		onAudio func([]byte) error,
	) (VoiceTurnResult, error)
}

// VoiceTurnLiveEndpointService optionally reports a provider-confirmed speech
// endpoint while input remains open. The transport still owns the commit:
// endpoint observation never authorizes model output or state publication.
type VoiceTurnLiveEndpointService interface {
	ProcessLiveWithEndpoint(
		ctx context.Context,
		uid string,
		input VoiceTurnInput,
		audio <-chan []byte,
		onAudio func([]byte) error,
		onEndpoint func(),
	) (VoiceTurnResult, error)
}

type VoiceOptions struct {
	Service              VoiceTurnService
	NativeLiveService    VoiceTurnLiveService
	RateLimiter          guard.Limiter
	AppRateLimiter       guard.Limiter
	LiveLeaseManager     guard.VoiceLiveLeaseManager
	LiveHandshakeGate    *VoiceLiveHandshakeGate
	RequestTimeout       time.Duration
	MaxRequestBytes      int64
	RequireRecentPasskey bool

	// livePipelineJoinTimeout is test-configurable inside this package. The
	// public constructor clamps it to the production safety maximum.
	livePipelineJoinTimeout time.Duration
}

type Server struct {
	logger                   *slog.Logger
	verifier                 identity.Verifier
	rateLimiter              guard.Limiter
	evaluator                evaluation.Evaluator
	store                    store.EvaluationStore
	requestTimeout           time.Duration
	maxRequestBytes          int64
	voice                    VoiceOptions
	passkeys                 PasskeyService
	appVerifier              identity.AppVerifier
	passkeyClientRateLimiter guard.Limiter
	passkeyAppCircuitBreaker guard.Limiter
}

func New(
	logger *slog.Logger,
	verifier identity.Verifier,
	rateLimiter guard.Limiter,
	evaluator evaluation.Evaluator,
	evaluationStore store.EvaluationStore,
	requestTimeout time.Duration,
	maxRequestBytes int64,
) http.Handler {
	return NewWithVoice(
		logger,
		verifier,
		rateLimiter,
		evaluator,
		evaluationStore,
		requestTimeout,
		maxRequestBytes,
		VoiceOptions{},
	)
}

func NewWithVoice(
	logger *slog.Logger,
	verifier identity.Verifier,
	rateLimiter guard.Limiter,
	evaluator evaluation.Evaluator,
	evaluationStore store.EvaluationStore,
	requestTimeout time.Duration,
	maxRequestBytes int64,
	voice VoiceOptions,
) http.Handler {
	return NewWithVoiceAndPasskeys(
		logger,
		verifier,
		rateLimiter,
		evaluator,
		evaluationStore,
		requestTimeout,
		maxRequestBytes,
		voice,
		nil,
		nil,
		nil,
	)
}

func NewWithVoiceAndPasskeys(
	logger *slog.Logger,
	verifier identity.Verifier,
	rateLimiter guard.Limiter,
	evaluator evaluation.Evaluator,
	evaluationStore store.EvaluationStore,
	requestTimeout time.Duration,
	maxRequestBytes int64,
	voice VoiceOptions,
	passkeys PasskeyService,
	passkeyClientRateLimiter guard.Limiter,
	passkeyAppCircuitBreaker guard.Limiter,
) http.Handler {
	appVerifier, _ := verifier.(identity.AppVerifier)
	// The passkey branch predates the unauthenticated WebSocket admission gate.
	// Preserve its public constructor behavior without weakening the latest
	// lifecycle boundary: passkey-gated live voice receives the same bounded
	// default gate when an older caller did not yet provide one explicitly.
	if voice.RequireRecentPasskey && voice.LiveHandshakeGate == nil {
		voice.LiveHandshakeGate = NewVoiceLiveHandshakeGate(
			DefaultVoiceLiveHandshakeLimit,
		)
	}
	if voice.livePipelineJoinTimeout <= 0 ||
		voice.livePipelineJoinTimeout > voiceLivePipelineJoinTimeout {
		voice.livePipelineJoinTimeout = voiceLivePipelineJoinTimeout
	}
	server := &Server{
		logger:                   logger,
		verifier:                 verifier,
		rateLimiter:              rateLimiter,
		evaluator:                evaluator,
		store:                    evaluationStore,
		requestTimeout:           requestTimeout,
		maxRequestBytes:          maxRequestBytes,
		voice:                    voice,
		passkeys:                 passkeys,
		appVerifier:              appVerifier,
		passkeyClientRateLimiter: passkeyClientRateLimiter,
		passkeyAppCircuitBreaker: passkeyAppCircuitBreaker,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.health)
	mux.Handle("GET /api/v1/me", server.requireIdentity(http.HandlerFunc(server.me)))
	mux.Handle("POST /api/v1/evaluations", server.requireIdentity(http.HandlerFunc(server.evaluate)))
	mux.Handle("POST /api/v1/voice/turns", server.requireIdentity(server.requireFreshPasskey(http.HandlerFunc(server.voiceTurn))))
	mux.Handle(
		"POST "+voiceStreamPath,
		server.requireIdentity(server.requireFreshPasskey(http.HandlerFunc(server.voiceTurnStream))),
	)
	mux.HandleFunc("OPTIONS "+voiceStreamPath, server.voiceStreamPreflight)
	mux.HandleFunc("GET "+voiceLivePath, server.voiceLive)
	mux.Handle("POST "+passkeyRegistrationBeginPath, server.requireAppAttestation(http.HandlerFunc(server.beginPasskeyRegistration)))
	mux.Handle("POST "+passkeyRegistrationFinishPath, server.requireAppAttestation(http.HandlerFunc(server.finishPasskeyRegistration)))
	mux.Handle("POST "+passkeyAuthenticationBeginPath, server.requireAppAttestation(http.HandlerFunc(server.beginPasskeyAuthentication)))
	mux.Handle("POST "+passkeyAuthenticationFinishPath, server.requireAppAttestation(http.HandlerFunc(server.finishPasskeyAuthentication)))

	return server.voiceStreamCORS(
		server.recoverPanic(
			server.securityHeaders(
				server.requestContext(
					server.rejectCrossSiteWrites(mux),
				),
			),
		),
	)
}

type voiceTurnRequest struct {
	AudioBase64             string                `json:"audioBase64"`
	MIMEType                string                `json:"mimeType"`
	SessionState            string                `json:"sessionState"`
	TurnMode                VoiceTurnMode         `json:"turnMode"`
	StrictCloudMinimization bool                  `json:"strictCloudMinimization"`
	Document                *voiceDocumentRequest `json:"document,omitempty"`
}

type voiceDocumentRequest struct {
	Base64   string `json:"base64"`
	MIMEType string `json:"mimeType"`
	Name     string `json:"name"`
}

func (s *Server) voiceTurn(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
		return
	}
	if s.voice.Service == nil || s.voice.RateLimiter == nil ||
		s.voice.AppRateLimiter == nil ||
		s.voice.RequestTimeout <= 0 || s.voice.MaxRequestBytes <= 0 {
		writeProblem(w, http.StatusServiceUnavailable, "voice_unavailable", "Voice conversation is not configured.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.voice.RequestTimeout)
	defer cancel()
	started := time.Now()
	if !s.consumeVoiceQuota(w, ctx, principal, started.UTC()) {
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.voice.MaxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request voiceTurnRequest
	if err := decoder.Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "The voice request is invalid.")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Only one JSON value is allowed.")
		return
	}

	input, err := decodeVoiceTurn(request)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid_voice_turn", "The voice turn could not be accepted.")
		return
	}
	// RequestID is minted by the server middleware and never accepted from the
	// client JSON. Downstream capability leases bind privileged actions to this
	// exact authenticated request.
	input.RequestID = requestIDFromContext(ctx)
	defer clearVoiceInput(&input)

	result, err := s.voice.Service.Process(ctx, principal.UID, input)
	if err != nil {
		if errors.Is(err, ErrVoiceNotRecognized) || errors.Is(err, ErrVoiceStateInvalid) {
			writeProblem(w, http.StatusUnprocessableEntity, "voice_turn_invalid", "The voice turn could not be understood safely.")
			return
		}
		logAttributes := []any{
			"request_id", requestIDFromContext(ctx),
			"duration_ms", time.Since(started).Milliseconds(),
			"error_class", "voice_pipeline_failure",
		}
		if pipelineStage, classified := VoicePipelineStageOf(err); classified {
			logAttributes = append(
				logAttributes,
				"pipeline_stage", string(pipelineStage),
			)
		}
		s.logger.ErrorContext(ctx, "voice turn failed", logAttributes...)
		writeProblem(w, http.StatusBadGateway, "voice_turn_unavailable", "The voice turn could not be completed.")
		return
	}
	defer clear(result.Audio)

	if err := validateVoiceResultForInput(input, result); err != nil {
		s.logger.ErrorContext(ctx, "voice result rejected",
			"request_id", requestIDFromContext(ctx),
			"error_class", "invalid_voice_result",
		)
		writeProblem(w, http.StatusBadGateway, "voice_turn_unavailable", "The voice turn could not be completed.")
		return
	}

	var caption any
	if result.Caption != "" {
		caption = result.Caption
	}
	if err := writeJSON(w, http.StatusCreated, map[string]any{
		"audioBase64":      base64.StdEncoding.EncodeToString(result.Audio),
		"audioMimeType":    result.AudioMIMEType,
		"caption":          caption,
		"sessionState":     result.StateToken,
		"detectedDomain":   result.DetectedDomain,
		"assistanceTarget": result.AssistanceTarget,
		"respondentStage":  result.RespondentStage,
		"coachPhase":       result.CoachPhase,
		"coachAction":      result.CoachAction,
		"researchStatus":   result.ResearchStatus,
		"researchRecords":  result.ResearchRecords,
		"privacyStatus":    result.PrivacyStatus,
		"route":            result.Route,
		"needsPaper":       result.NeedsPaper,
	}); err != nil {
		s.logger.WarnContext(ctx, "voice response write failed",
			"request_id", requestIDFromContext(ctx),
			"route", result.Route,
			"error_class", "response_write_failure",
		)
		return
	}
	s.logger.InfoContext(ctx, "voice turn completed",
		"request_id", requestIDFromContext(ctx),
		"route", result.Route,
		"spoke", len(result.Audio) > 0,
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func (s *Server) consumeVoiceQuota(
	w http.ResponseWriter,
	ctx context.Context,
	principal identity.Principal,
	at time.Time,
) bool {
	quotaChecks := []struct {
		limiter guard.Limiter
		key     string
		scope   string
	}{
		{limiter: s.voice.RateLimiter, key: principal.UID, scope: "uid"},
		{limiter: s.voice.AppRateLimiter, key: "app:" + principal.AppID, scope: "app"},
	}
	quotaErrors := make([]error, len(quotaChecks))
	var quotaGroup sync.WaitGroup
	quotaGroup.Add(len(quotaChecks))
	for index := range quotaChecks {
		index := index
		go func() {
			defer quotaGroup.Done()
			check := quotaChecks[index]
			quotaErrors[index] = check.limiter.Consume(ctx, check.key, at)
		}()
	}
	quotaGroup.Wait()

	for _, err := range quotaErrors {
		if errors.Is(err, guard.ErrRateLimitExceeded) {
			writeProblem(w, http.StatusTooManyRequests, "rate_limit_exceeded", "The voice conversation limit has been reached.")
			return false
		}
	}
	for index, err := range quotaErrors {
		if err != nil {
			s.logger.ErrorContext(ctx, "voice rate-limit guard failed",
				"request_id", requestIDFromContext(ctx),
				"quota_scope", quotaChecks[index].scope,
				"error_class", "voice_rate_limit_store_failure",
			)
			writeProblem(w, http.StatusServiceUnavailable, "rate_limit_unavailable", "The voice service cannot safely accept this request.")
			return false
		}
	}
	return true
}

func decodeVoiceTurn(request voiceTurnRequest) (VoiceTurnInput, error) {
	mimeType := normalizedAudioMIME(request.MIMEType)
	if mimeType == "" || len(request.SessionState) > maxStateBytes {
		return VoiceTurnInput{}, errors.New("invalid voice metadata")
	}
	if request.StrictCloudMinimization &&
		(request.SessionState != "" || request.Document != nil) {
		return VoiceTurnInput{}, errors.New("strict voice metadata retained state or document")
	}
	ambient := false
	foreground := false
	switch request.TurnMode {
	case VoiceTurnIntentional:
	case VoiceTurnForeground:
		ambient = true
		foreground = true
	case VoiceTurnAmbient:
		ambient = true
	default:
		return VoiceTurnInput{}, errors.New("invalid turn mode")
	}
	audio, err := decodeBoundedBase64(request.AudioBase64, maxAudioBytes)
	if err != nil || len(audio) == 0 {
		clear(audio)
		return VoiceTurnInput{}, errors.New("invalid audio")
	}

	input := VoiceTurnInput{
		Audio:                   audio,
		MIMEType:                mimeType,
		StateToken:              request.SessionState,
		TurnMode:                request.TurnMode,
		Ambient:                 ambient,
		Foreground:              foreground,
		STTLocale:               "ja-JP",
		SchemaVersion:           1,
		StrictCloudMinimization: request.StrictCloudMinimization,
	}
	if request.Document == nil {
		return input, nil
	}
	if request.Document.MIMEType != "application/pdf" ||
		len([]rune(request.Document.Name)) > 180 {
		clearVoiceInput(&input)
		return VoiceTurnInput{}, errors.New("invalid document metadata")
	}
	document, err := decodeBoundedBase64(request.Document.Base64, maxDocumentBytes)
	if err != nil ||
		len(document) == 0 ||
		!bytes.HasPrefix(document, []byte("%PDF-")) {
		clear(document)
		clearVoiceInput(&input)
		return VoiceTurnInput{}, errors.New("invalid document")
	}
	input.Document = &VoiceDocument{
		MIMEType: "application/pdf",
		Data:     document,
	}
	return input, nil
}

func decodeBoundedBase64(value string, maximum int) ([]byte, error) {
	if value == "" || len(value) > base64.StdEncoding.EncodedLen(maximum) {
		return nil, errors.New("encoded data is outside bounds")
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(value)))
	n, err := base64.StdEncoding.Strict().Decode(decoded, []byte(value))
	if err != nil || n > maximum {
		clear(decoded)
		return nil, errors.New("invalid base64 data")
	}
	return decoded[:n], nil
}

func normalizedAudioMIME(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	switch strings.ToLower(mediaType) {
	case "audio/webm", "audio/ogg", "audio/mp4", "audio/wav", "audio/x-wav":
		return strings.ToLower(mediaType)
	default:
		return ""
	}
}

func validateVoiceResult(result VoiceTurnResult) error {
	if result.PrivacyStatus == "blocked" {
		if len(result.Audio) != 0 || result.AudioMIMEType != "" ||
			result.StateToken != "" || result.Caption != "" ||
			result.DetectedDomain != "unknown" ||
			result.AssistanceTarget != "assistant" ||
			result.RespondentStage != "none" ||
			result.CoachPhase != "none" || result.CoachAction != "none" ||
			result.ResearchStatus != "none" ||
			result.ResearchRecords == nil || len(result.ResearchRecords) != 0 ||
			result.Route != "strict-privacy-blocked" || result.NeedsPaper {
			return errors.New("blocked privacy result has unsafe fields")
		}
		return nil
	}
	if result.PrivacyStatus != "" && result.PrivacyStatus != "clear" {
		return errors.New("voice result has unknown privacy status")
	}
	preInferenceRecognitionResult := isPreInferenceRecognitionRoute(result.Route)
	if len(result.Audio) > maxAudioBytes ||
		(len(result.StateToken) == 0 && !preInferenceRecognitionResult &&
			result.PrivacyStatus != "clear") ||
		len(result.StateToken) > maxStateBytes ||
		len(result.DetectedDomain) == 0 ||
		len(result.DetectedDomain) > 40 ||
		(result.AssistanceTarget != "assistant" &&
			result.AssistanceTarget != "respondent") ||
		(result.RespondentStage != "none" &&
			result.RespondentStage != "awaiting_answer" &&
			result.RespondentStage != "restructure") ||
		!validCoachMetadata(
			result.AssistanceTarget,
			result.CoachPhase,
			result.CoachAction,
		) ||
		(result.ResearchStatus != "none" &&
			result.ResearchStatus != "needs_primary_evidence" &&
			result.ResearchStatus != "unavailable") ||
		len(result.Route) == 0 ||
		len(result.Route) > 80 ||
		!utf8.ValidString(result.Caption) ||
		utf8.RuneCountInString(result.Caption) > maxCaptionRunes {
		return errors.New("voice result is outside bounds")
	}
	if (result.AssistanceTarget == "assistant" &&
		result.RespondentStage != "none") ||
		(result.AssistanceTarget == "respondent" &&
			result.RespondentStage == "none") ||
		(result.ResearchStatus != "none" &&
			(result.AssistanceTarget != "assistant" ||
				result.RespondentStage != "none")) ||
		!validResearchRecords(result.ResearchStatus, result.ResearchRecords) {
		return errors.New("voice result has inconsistent metadata")
	}
	if result.PrivacyStatus == "clear" &&
		(result.StateToken != "" || result.ResearchStatus != "none" ||
			len(result.ResearchRecords) != 0) {
		return errors.New("strict clear result retained state or research")
	}
	if len(result.Audio) == 0 {
		if result.AudioMIMEType != "" || result.Caption != "" {
			return errors.New("silent result has audio metadata")
		}
		return nil
	}
	if result.Caption == "" {
		return errors.New("spoken result is missing its caption")
	}
	if result.AudioMIMEType != "audio/mpeg" && result.AudioMIMEType != "audio/ogg" {
		return errors.New("unsupported synthesized audio type")
	}
	return nil
}

func validateVoiceResultForInput(
	input VoiceTurnInput,
	result VoiceTurnResult,
) error {
	if err := validateVoiceResult(result); err != nil {
		return err
	}
	return validateVoiceResultMode(input, result)
}

func validateVoiceResultMode(
	input VoiceTurnInput,
	result VoiceTurnResult,
) error {
	if input.StrictCloudMinimization {
		if result.PrivacyStatus != "clear" && result.PrivacyStatus != "blocked" {
			return errors.New("strict request returned an uninspected result")
		}
		return nil
	}
	if result.PrivacyStatus != "" {
		return errors.New("ordinary request returned strict-mode metadata")
	}
	return nil
}

func validCoachMetadata(assistanceTarget string, phase string, action string) bool {
	if assistanceTarget == "assistant" {
		return phase == "none" && action == "none"
	}
	if assistanceTarget != "respondent" {
		return false
	}
	switch phase {
	case "awaiting_answer":
		return action == "elicit"
	case "awaiting_restatement":
		return action == "restate"
	case "expanding":
		return action == "expand"
	case "complete":
		return action == "complete"
	case "blocked":
		return action == "retry" || action == "release"
	default:
		return false
	}
}

func isPreInferenceRecognitionRoute(route string) bool {
	switch route {
	case "stt-clarify",
		"stt-silent",
		"stt-clarify-no-speech",
		"stt-clarify-low-confidence",
		"stt-silent-no-speech",
		"stt-silent-low-confidence":
		return true
	default:
		return false
	}
}

func validResearchRecords(status string, records []ResearchRecord) bool {
	if records == nil || len(records) > conversation.MaxResearchRecords {
		return false
	}
	if status != "needs_primary_evidence" && len(records) != 0 {
		return false
	}
	for _, record := range records {
		normalizedDOI, err := research.NormalizeDOI(record.DOI)
		if err != nil ||
			normalizedDOI != record.DOI ||
			record.URL == "" ||
			record.Source != "Crossref" ||
			!utf8.ValidString(record.Title) ||
			utf8.RuneCountInString(record.Title) > 300 ||
			!utf8.ValidString(record.URL) ||
			len(record.URL) > 600 ||
			!utf8.ValidString(record.Published) ||
			len(record.Published) > 40 ||
			!validPublicationValue(record.Published) {
			return false
		}
		expectedURL := (&url.URL{
			Scheme: "https",
			Host:   "doi.org",
			Path:   "/" + record.DOI,
		}).String()
		if record.URL != expectedURL {
			return false
		}
	}
	return true
}

func validPublicationValue(value string) bool {
	if value == "" {
		return true
	}
	for _, layout := range []string{
		"2006",
		"2006-01",
		time.DateOnly,
		time.RFC3339Nano,
	} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func clearVoiceInput(input *VoiceTurnInput) {
	if input == nil {
		return
	}
	clear(input.Audio)
	input.Audio = nil
	if input.Document != nil {
		clear(input.Document.Data)
		input.Document.Data = nil
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "kotae-api",
	})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authLevel":       "account",
		"accountVerified": principal.AccountVerified,
		"provider":        principal.Provider,
	})
}

func (s *Server) evaluate(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var input contracts.EvaluationInput
	if err := decoder.Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Only one JSON value is allowed.")
		return
	}
	if err := input.Validate(); err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid_input", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()

	started := time.Now()
	if err := s.rateLimiter.Consume(ctx, principal.UID, started.UTC()); err != nil {
		if errors.Is(err, guard.ErrRateLimitExceeded) {
			s.logger.WarnContext(ctx, "evaluation rate limit exceeded",
				"request_id", requestIDFromContext(ctx),
				"uid_hash", shortHash(principal.UID),
				"error_class", "rate_limit_exceeded",
			)
			writeProblem(w, http.StatusTooManyRequests, "rate_limit_exceeded", "The evaluation rate limit has been reached.")
			return
		}
		s.logger.ErrorContext(ctx, "evaluation rate-limit guard failed",
			"request_id", requestIDFromContext(ctx),
			"uid_hash", shortHash(principal.UID),
			"error_class", "rate_limit_store_failure",
		)
		writeProblem(w, http.StatusServiceUnavailable, "rate_limit_unavailable", "The evaluation service cannot safely accept this request.")
		return
	}

	result, err := s.evaluator.Evaluate(ctx, input)
	if err != nil {
		s.logger.ErrorContext(ctx, "evaluation failed",
			"request_id", requestIDFromContext(ctx),
			"uid_hash", shortHash(principal.UID),
			"duration_ms", time.Since(started).Milliseconds(),
			"error_class", "model_or_schema_failure",
			"provider_status", evaluationProviderStatus(err),
		)
		writeProblem(w, http.StatusBadGateway, "evaluation_unavailable", "The evaluation service is temporarily unavailable.")
		return
	}

	attemptID, err := s.store.Save(ctx, principal.UID, requestIDFromContext(ctx), input, result)
	if err != nil {
		s.logger.ErrorContext(ctx, "evaluation persistence failed",
			"request_id", requestIDFromContext(ctx),
			"uid_hash", shortHash(principal.UID),
			"duration_ms", time.Since(started).Milliseconds(),
			"error_class", "firestore_write_failure",
		)
		writeProblem(w, http.StatusServiceUnavailable, "persistence_unavailable", "The result could not be saved safely.")
		return
	}

	s.logger.InfoContext(ctx, "evaluation completed",
		"request_id", requestIDFromContext(ctx),
		"uid_hash", shortHash(principal.UID),
		"attempt_id", attemptID,
		"model_logical_id", result.ModelLogicalID,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	writeJSON(w, http.StatusCreated, map[string]any{
		"attemptId":  attemptID,
		"evaluation": result,
	})
}

func evaluationProviderStatus(err error) string {
	var genkitError *core.GenkitError
	if !errors.As(err, &genkitError) {
		return "internal"
	}
	switch genkitError.Status {
	case core.CANCELLED,
		core.UNKNOWN,
		core.INVALID_ARGUMENT,
		core.DEADLINE_EXCEEDED,
		core.NOT_FOUND,
		core.ALREADY_EXISTS,
		core.PERMISSION_DENIED,
		core.UNAUTHENTICATED,
		core.RESOURCE_EXHAUSTED,
		core.FAILED_PRECONDITION,
		core.ABORTED,
		core.OUT_OF_RANGE,
		core.UNIMPLEMENTED,
		core.INTERNAL,
		core.UNAVAILABLE,
		core.DATA_LOSS:
		return strings.ToLower(string(genkitError.Status))
	default:
		return "internal"
	}
}

func (s *Server) requireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders := r.Header.Values("Authorization")
		appCheckHeaders := r.Header.Values("X-Firebase-AppCheck")
		if len(authHeaders) != 1 || len(appCheckHeaders) != 1 {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
			return
		}

		authFields := strings.Fields(authHeaders[0])
		appCheckHeader := strings.TrimSpace(appCheckHeaders[0])
		if len(authFields) != 2 ||
			!strings.EqualFold(authFields[0], "Bearer") ||
			authFields[1] == "" ||
			len(authFields[1]) > 8*1024 ||
			appCheckHeader == "" ||
			len(appCheckHeader) > 8*1024 {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
			return
		}

		principal, err := s.verifier.Verify(
			r.Context(),
			authFields[1],
			appCheckHeader,
		)
		if err != nil {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication or app attestation failed.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
	})
}

func (s *Server) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID)))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		resourcePolicy := "same-origin"
		if (r.URL.Path == voiceStreamPath ||
			r.URL.Path == voiceLivePath) &&
			r.Header.Get("Origin") == allowedWebOrigin {
			resourcePolicy = "cross-origin"
		}
		w.Header().Set("Cross-Origin-Resource-Policy", resourcePolicy)
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rejectCrossSiteWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			origins := r.Header.Values("Origin")
			directVoiceStream := r.URL.Path == voiceStreamPath &&
				len(origins) == 1 &&
				origins[0] == allowedWebOrigin
			if len(origins) != 1 ||
				origins[0] != allowedWebOrigin ||
				(strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") &&
					!directVoiceStream) {
				writeProblem(w, http.StatusForbidden, "cross_site_request", "Cross-site writes are not allowed.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.ErrorContext(r.Context(), "request panic recovered",
					"request_id", requestIDFromContext(r.Context()),
					"error_class", "panic",
				)
				writeProblem(w, http.StatusInternalServerError, "internal_error", "An internal error occurred.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(payload)
}

func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(status),
		"status": status,
		"code":   code,
		"detail": detail,
	})
}

type principalContextKey struct{}
type requestIDContextKey struct{}

func principalFromContext(ctx context.Context) (identity.Principal, bool) {
	value, ok := ctx.Value(principalContextKey{}).(identity.Principal)
	return value, ok
}

func requestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey{}).(string)
	return value
}

func newRequestID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(value[:])
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
