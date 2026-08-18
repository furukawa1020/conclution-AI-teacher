package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

type voiceStreamFrame struct {
	Type         string         `json:"type"`
	Version      int            `json:"version"`
	Sequence     *int           `json:"sequence,omitempty"`
	AudioBase64  string         `json:"audioBase64,omitempty"`
	SampleRateHz *int           `json:"sampleRateHz,omitempty"`
	Result       map[string]any `json:"result,omitempty"`
	Code         string         `json:"code,omitempty"`
}

func (s *Server) voiceTurnStream(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
		return
	}
	streamService, ok := s.voice.Service.(VoiceTurnStreamService)
	if !ok || s.voice.RateLimiter == nil ||
		s.voice.AppRateLimiter == nil ||
		s.voice.RequestTimeout <= 0 ||
		s.voice.MaxRequestBytes <= 0 {
		writeProblem(w, http.StatusServiceUnavailable, "voice_unavailable", "Voice conversation is not configured.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "voice_stream_unavailable", "Streaming voice is not available.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.voice.RequestTimeout)
	defer cancel()
	started := time.Now()
	if !s.consumeVoiceQuota(w, ctx, principal, started.UTC()) {
		return
	}
	input, ok := s.decodeVoiceStreamRequest(w, r, ctx)
	if !ok {
		return
	}
	input.GuestExperience = principal.IsGuest()
	defer clearVoiceInput(&input)

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(w)
	if err := encoder.Encode(voiceStreamFrame{
		Type:    "ready",
		Version: voiceStreamVersion,
	}); err != nil {
		return
	}
	flusher.Flush()
	s.logger.InfoContext(ctx, "voice stream ready",
		"request_id", requestIDFromContext(ctx),
		"duration_ms", time.Since(started).Milliseconds(),
	)

	sequence := 0
	totalAudioBytes := 0
	firstAudioAt := time.Time{}
	strictOutput := &strictAudioBuffer{}
	defer strictOutput.clear()
	strictOutput.markCommitted()
	deliverAudio := func(audio []byte) error {
		if len(audio) == 0 ||
			len(audio) > voiceStreamMaxChunkBytes ||
			len(audio)%2 != 0 ||
			sequence >= voiceStreamMaxChunks ||
			len(audio) > voiceStreamMaxAudioBytes-totalAudioBytes {
			return errors.New("streamed audio is outside bounds")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if firstAudioAt.IsZero() {
			firstAudioAt = time.Now()
		}
		frame := voiceStreamFrame{
			Type:         "audio",
			Version:      voiceStreamVersion,
			Sequence:     &sequence,
			AudioBase64:  base64.StdEncoding.EncodeToString(audio),
			SampleRateHz: pointerTo(voiceStreamSampleRateHz),
		}
		if err := encoder.Encode(frame); err != nil {
			return err
		}
		flusher.Flush()
		totalAudioBytes += len(audio)
		sequence++
		return nil
	}
	onAudio := deliverAudio
	if input.StrictCloudMinimization {
		onAudio = strictOutput.append
	}
	result, err := streamService.ProcessStream(
		ctx,
		principal.UID,
		input,
		onAudio,
	)
	if err != nil {
		if ctx.Err() != nil {
			s.logger.InfoContext(ctx, "voice stream cancelled",
				"request_id", requestIDFromContext(ctx),
				"duration_ms", time.Since(started).Milliseconds(),
				"audio_chunks", sequence,
			)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				_ = encoder.Encode(voiceStreamFrame{
					Type:    "error",
					Version: voiceStreamVersion,
					Code:    "voice_turn_timeout",
				})
				flusher.Flush()
			}
			return
		}
		logAttributes := []any{
			"request_id", requestIDFromContext(ctx),
			"duration_ms", time.Since(started).Milliseconds(),
			"error_class", "voice_pipeline_failure",
			"audio_chunks", sequence,
		}
		if pipelineStage, classified := VoicePipelineStageOf(err); classified {
			logAttributes = append(
				logAttributes,
				"pipeline_stage", string(pipelineStage),
			)
		}
		s.logger.ErrorContext(ctx, "voice stream failed", logAttributes...)
		_ = encoder.Encode(voiceStreamFrame{
			Type:    "error",
			Version: voiceStreamVersion,
			Code:    "voice_turn_unavailable",
		})
		flusher.Flush()
		return
	}
	defer clear(result.Audio)

	spoke := sequence > 0
	if input.StrictCloudMinimization {
		spoke = strictOutput.spoke()
	}
	if err := validateStreamedVoiceResultForInput(input, result, spoke); err != nil {
		s.logger.ErrorContext(ctx, "voice stream result rejected",
			"request_id", requestIDFromContext(ctx),
			"error_class", "invalid_voice_result",
			"audio_chunks", sequence,
		)
		_ = encoder.Encode(voiceStreamFrame{
			Type:    "error",
			Version: voiceStreamVersion,
			Code:    "voice_turn_unavailable",
		})
		flusher.Flush()
		return
	}
	if input.StrictCloudMinimization && spoke {
		if err := strictOutput.release(deliverAudio); err != nil {
			return
		}
	}

	var caption any
	audioMIMEType := ""
	if spoke {
		caption = result.Caption
		audioMIMEType = "audio/L16"
	}
	finalResult := map[string]any{
		"audioBase64":      "",
		"audioMimeType":    audioMIMEType,
		"caption":          caption,
		"sessionState":     result.StateToken,
		"detectedDomain":   result.DetectedDomain,
		"assistanceTarget": result.AssistanceTarget,
		"respondentStage":  result.RespondentStage,
		"coachPhase":       result.CoachPhase,
		"coachAction":      result.CoachAction,
		"answerProof":      normalizedAnswerProof(result.AnswerProof),
		"answerTransitionProof": normalizedAnswerTransitionProof(
			result.AnswerTransitionProof,
		),
		"guestAFirstOutcome": normalizedGuestAFirstOutcome(result.GuestAFirstOutcome),
		"researchStatus":     result.ResearchStatus,
		"researchRecords":    result.ResearchRecords,
		"privacyStatus":      result.PrivacyStatus,
		"route":              result.Route,
		"needsPaper":         result.NeedsPaper,
	}
	if err := encoder.Encode(voiceStreamFrame{
		Type:    "final",
		Version: voiceStreamVersion,
		Result:  finalResult,
	}); err != nil {
		s.logger.WarnContext(ctx, "voice stream final write failed",
			"request_id", requestIDFromContext(ctx),
			"route", result.Route,
			"error_class", "response_write_failure",
		)
		return
	}
	flusher.Flush()
	s.observeSemanticShadow(ctx, result)
	s.enqueueLongTermMemory(principal, result)

	firstAudioMS := int64(-1)
	if !firstAudioAt.IsZero() {
		firstAudioMS = firstAudioAt.Sub(started).Milliseconds()
	}
	s.logger.InfoContext(ctx, "voice stream completed",
		"request_id", requestIDFromContext(ctx),
		"route", result.Route,
		"spoke", spoke,
		"duration_ms", time.Since(started).Milliseconds(),
		"first_audio_ms", firstAudioMS,
		"audio_chunks", sequence,
		"audio_bytes", totalAudioBytes,
	)
}

func pointerTo(value int) *int {
	return &value
}

func (s *Server) decodeVoiceStreamRequest(
	w http.ResponseWriter,
	r *http.Request,
	ctx context.Context,
) (VoiceTurnInput, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return VoiceTurnInput{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.voice.MaxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request voiceTurnRequest
	if err := decoder.Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "The voice request is invalid.")
		return VoiceTurnInput{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Only one JSON value is allowed.")
		return VoiceTurnInput{}, false
	}
	input, err := decodeVoiceTurn(request)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid_voice_turn", "The voice turn could not be accepted.")
		return VoiceTurnInput{}, false
	}
	input.RequestID = requestIDFromContext(ctx)
	return input, true
}

func validateStreamedVoiceResult(result VoiceTurnResult, spoke bool) error {
	if len(result.Audio) != 0 || result.AudioMIMEType != "" {
		return errors.New("streamed result contains buffered audio")
	}
	if !spoke {
		return validateVoiceResult(result)
	}
	if result.Caption == "" {
		return errors.New("streamed spoken result is missing its caption")
	}
	bufferedShape := result
	bufferedShape.Audio = []byte{0}
	bufferedShape.AudioMIMEType = "audio/mpeg"
	return validateVoiceResult(bufferedShape)
}

func validateStreamedVoiceResultForInput(
	input VoiceTurnInput,
	result VoiceTurnResult,
	spoke bool,
) error {
	if err := validateStreamedVoiceResult(result, spoke); err != nil {
		return err
	}
	return validateVoiceResultMode(input, result)
}

func (s *Server) voiceStreamPreflight(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != allowedWebOrigin ||
		r.Header.Get("Access-Control-Request-Method") != http.MethodPost ||
		!validVoiceStreamPreflightHeaders(
			r.Header.Get("Access-Control-Request-Headers"),
		) {
		writeProblem(w, http.StatusForbidden, "cross_site_request", "Cross-site writes are not allowed.")
		return
	}
	w.Header().Set(
		"Access-Control-Allow-Headers",
		"Authorization, Content-Type, X-Firebase-AppCheck",
	)
	w.Header().Set("Access-Control-Allow-Methods", http.MethodPost)
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

func validVoiceStreamPreflightHeaders(value string) bool {
	required := map[string]bool{
		"authorization":       false,
		"content-type":        false,
		"x-firebase-appcheck": false,
	}
	for _, raw := range strings.Split(value, ",") {
		header := strings.ToLower(strings.TrimSpace(raw))
		if _, allowed := required[header]; !allowed {
			return false
		}
		required[header] = true
	}
	for _, present := range required {
		if !present {
			return false
		}
	}
	return true
}

func (s *Server) voiceStreamCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == voiceStreamPath &&
			r.Header.Get("Origin") == allowedWebOrigin {
			w.Header().Set("Access-Control-Allow-Origin", allowedWebOrigin)
			w.Header().Add("Vary", "Origin")
		}
		next.ServeHTTP(w, r)
	})
}
