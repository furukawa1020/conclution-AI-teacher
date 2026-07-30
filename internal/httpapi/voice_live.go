package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
)

const (
	voiceLiveVersion            = 1
	voiceLiveSampleRateHz       = 16_000
	voiceLiveFirstFrameTimeout  = 3 * time.Second
	voiceLiveGuardTimeout       = 5 * time.Second
	voiceLiveMaxCaptureDuration = 55 * time.Second
	voiceLiveMaxStartBytes      = 40 * 1024
	voiceLiveMaxPCMFrameBytes   = 15 * 1024
	voiceLiveMaxPCMTotalBytes   = maxAudioBytes
	voiceLiveMaxTokenBytes      = 8 * 1024
)

const (
	voiceLiveCodeAuthenticationFailed = "authentication_failed"
	voiceLiveCodeRateLimited          = "rate_limited"
	voiceLiveCodeAPIUnavailable       = "voice_api_unavailable"
	voiceLiveCodeResponseInvalid      = "voice_response_invalid"
	voiceLiveCodeTurnTooLarge         = "voice_turn_too_large"
	voiceLiveCodeNoSpeech             = "no_speech"
	voiceLiveCodeTurnInvalid          = "voice_turn_invalid"
)

type voiceLiveStartFrame struct {
	Type          string        `json:"type"`
	Version       int           `json:"version"`
	IDToken       string        `json:"idToken"`
	AppCheckToken string        `json:"appCheckToken"`
	SessionState  string        `json:"sessionState"`
	TurnMode      VoiceTurnMode `json:"turnMode"`
	SampleRateHz  int           `json:"sampleRateHz"`
}

type voiceLiveCommitFrame struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
}

type voiceLiveOutboundFrame struct {
	Type    string                `json:"type"`
	Version int                   `json:"version"`
	Code    string                `json:"code,omitempty"`
	Result  *voiceLiveFinalResult `json:"result,omitempty"`
}

type voiceLiveFinalResult struct {
	AudioBase64      string           `json:"audioBase64"`
	AudioMIMEType    string           `json:"audioMimeType"`
	Caption          *string          `json:"caption"`
	SessionState     string           `json:"sessionState"`
	DetectedDomain   string           `json:"detectedDomain"`
	AssistanceTarget string           `json:"assistanceTarget"`
	RespondentStage  string           `json:"respondentStage"`
	ResearchStatus   string           `json:"researchStatus"`
	ResearchRecords  []ResearchRecord `json:"researchRecords"`
	Route            string           `json:"route"`
	NeedsPaper       bool             `json:"needsPaper"`
}

type voiceLiveOutcome struct {
	result VoiceTurnResult
	err    error
}

type voiceLiveRead struct {
	messageType websocket.MessageType
	payload     []byte
	err         error
}

type voiceLiveOutputMetrics struct {
	mu            sync.Mutex
	firstOutputAt time.Time
	frames        int
	bytes         int
}

func (metrics *voiceLiveOutputMetrics) deliver(
	ctx context.Context,
	conn *websocket.Conn,
	audio []byte,
) error {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if len(audio) == 0 ||
		len(audio)%2 != 0 ||
		len(audio) > voiceStreamMaxChunkBytes ||
		metrics.frames >= voiceStreamMaxChunks ||
		len(audio) > voiceStreamMaxAudioBytes-metrics.bytes {
		return errors.New("live synthesized audio is outside bounds")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := conn.Write(ctx, websocket.MessageBinary, audio); err != nil {
		return err
	}
	if metrics.firstOutputAt.IsZero() {
		metrics.firstOutputAt = time.Now()
	}
	metrics.frames++
	metrics.bytes += len(audio)
	return nil
}

func (metrics *voiceLiveOutputMetrics) snapshot() (time.Time, int, int) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	return metrics.firstOutputAt, metrics.frames, metrics.bytes
}

func (s *Server) voiceLive(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || origins[0] != allowedWebOrigin {
		writeProblem(w, http.StatusForbidden, "cross_site_request", "Cross-site requests are not allowed.")
		return
	}
	if r.URL.RawQuery != "" ||
		len(r.Header.Values("Authorization")) != 0 ||
		len(r.Header.Values("X-Firebase-AppCheck")) != 0 ||
		r.Header.Get("Sec-WebSocket-Protocol") != "" {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Credentials are accepted only in the first WebSocket message.")
		return
	}
	liveService, ok := s.voice.Service.(VoiceTurnLiveService)
	if !ok || s.verifier == nil ||
		s.voice.RateLimiter == nil ||
		s.voice.AppRateLimiter == nil ||
		s.voice.RequestTimeout <= 0 {
		writeProblem(w, http.StatusServiceUnavailable, "voice_unavailable", "Live voice conversation is not configured.")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  []string{"kotae-ai.web.app"},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(voiceLiveMaxStartBytes)

	firstFrameCtx, cancelFirstFrame := context.WithTimeout(
		r.Context(),
		voiceLiveFirstFrameTimeout,
	)
	messageType, payload, err := conn.Read(firstFrameCtx)
	cancelFirstFrame()
	if err != nil {
		return
	}
	var start voiceLiveStartFrame
	if messageType != websocket.MessageText ||
		decodeStrictVoiceLiveJSON(payload, &start) != nil ||
		!validVoiceLiveStart(start) {
		finishVoiceLiveWithError(
			r.Context(),
			conn,
			voiceLiveCodeResponseInvalid,
			websocket.StatusPolicyViolation,
		)
		return
	}

	verifyCtx, cancelVerify := context.WithTimeout(
		r.Context(),
		voiceLiveGuardTimeout,
	)
	principal, err := s.verifier.Verify(
		verifyCtx,
		start.IDToken,
		start.AppCheckToken,
	)
	cancelVerify()
	start.IDToken = ""
	start.AppCheckToken = ""
	if err != nil {
		finishVoiceLiveWithError(
			r.Context(),
			conn,
			voiceLiveCodeAuthenticationFailed,
			websocket.StatusPolicyViolation,
		)
		return
	}

	liveCtx, cancelLive := context.WithTimeout(
		r.Context(),
		voiceLiveMaxCaptureDuration+s.voice.RequestTimeout,
	)
	defer cancelLive()
	quotaCtx, cancelQuota := context.WithTimeout(
		liveCtx,
		voiceLiveGuardTimeout,
	)
	code := s.consumeVoiceLiveQuota(
		quotaCtx,
		principal,
		time.Now().UTC(),
	)
	cancelQuota()
	if code != "" {
		finishVoiceLiveWithError(
			liveCtx,
			conn,
			code,
			websocket.StatusPolicyViolation,
		)
		return
	}
	if err := writeVoiceLiveJSON(liveCtx, conn, voiceLiveOutboundFrame{
		Type:    "ready",
		Version: voiceLiveVersion,
	}); err != nil {
		return
	}
	readyAt := time.Now()
	authReadyMS := readyAt.Sub(started).Milliseconds()
	conn.SetReadLimit(voiceLiveMaxPCMFrameBytes)

	input := VoiceTurnInput{
		MIMEType:      "audio/L16",
		StateToken:    start.SessionState,
		RequestID:     requestIDFromContext(liveCtx),
		TurnMode:      start.TurnMode,
		Ambient:       start.TurnMode == VoiceTurnAmbient,
		STTLocale:     "ja-JP",
		SchemaVersion: voiceLiveVersion,
	}
	start.SessionState = ""

	audioInput := make(chan []byte)
	audioInputClosed := false
	defer func() {
		if !audioInputClosed {
			close(audioInput)
		}
	}()

	outputMetrics := &voiceLiveOutputMetrics{}
	outcomeChannel := make(chan voiceLiveOutcome, 1)
	go func() {
		result, processErr := liveService.ProcessLive(
			liveCtx,
			principal.UID,
			input,
			audioInput,
			func(audio []byte) error {
				return outputMetrics.deliver(
					liveCtx,
					conn,
					audio,
				)
			},
		)
		outcomeChannel <- voiceLiveOutcome{result: result, err: processErr}
	}()

	inputFrames := 0
	inputBytes := 0
	firstInputAt := time.Time{}
	commitAt := time.Time{}
	captureDeadline := readyAt.Add(voiceLiveMaxCaptureDuration)
	captureCtx, cancelCapture := context.WithDeadline(liveCtx, captureDeadline)
	defer cancelCapture()

	for commitAt.IsZero() {
		readChannel := make(chan voiceLiveRead, 1)
		go func() {
			frameType, framePayload, readErr := conn.Read(captureCtx)
			readChannel <- voiceLiveRead{
				messageType: frameType,
				payload:     framePayload,
				err:         readErr,
			}
		}()

		select {
		case outcome := <-outcomeChannel:
			s.finishUnexpectedVoiceLiveOutcome(
				r.Context(),
				conn,
				outcome,
			)
			cancelLive()
			s.logVoiceLiveSession(
				liveCtx,
				started,
				authReadyMS,
				firstInputAt,
				commitAt,
				inputFrames,
				inputBytes,
				outputMetrics,
				outcome.result.LiveTimings,
				true,
			)
			return
		case read := <-readChannel:
			if read.err != nil {
				cancelLive()
				s.logVoiceLiveSession(
					liveCtx,
					started,
					authReadyMS,
					firstInputAt,
					commitAt,
					inputFrames,
					inputBytes,
					outputMetrics,
					emptyVoiceLiveTimings(),
					true,
				)
				return
			}
			switch read.messageType {
			case websocket.MessageBinary:
				if len(read.payload) == 0 ||
					len(read.payload)%2 != 0 {
					clear(read.payload)
					finishVoiceLiveWithError(
						liveCtx,
						conn,
						voiceLiveCodeResponseInvalid,
						websocket.StatusPolicyViolation,
					)
					cancelLive()
					return
				}
				if len(read.payload) > voiceLiveMaxPCMFrameBytes ||
					len(read.payload) >
						voiceLiveMaxPCMTotalBytes-inputBytes {
					clear(read.payload)
					finishVoiceLiveWithError(
						liveCtx,
						conn,
						voiceLiveCodeTurnTooLarge,
						websocket.StatusMessageTooBig,
					)
					cancelLive()
					return
				}
				select {
				case audioInput <- read.payload:
					if firstInputAt.IsZero() {
						firstInputAt = time.Now()
					}
					inputFrames++
					inputBytes += len(read.payload)
				case outcome := <-outcomeChannel:
					clear(read.payload)
					s.finishUnexpectedVoiceLiveOutcome(
						r.Context(),
						conn,
						outcome,
					)
					cancelLive()
					return
				case <-liveCtx.Done():
					clear(read.payload)
					return
				}
			case websocket.MessageText:
				var commit voiceLiveCommitFrame
				if decodeStrictVoiceLiveJSON(
					read.payload,
					&commit,
				) != nil ||
					commit.Type != "commit" ||
					commit.Version != voiceLiveVersion {
					finishVoiceLiveWithError(
						liveCtx,
						conn,
						voiceLiveCodeResponseInvalid,
						websocket.StatusPolicyViolation,
					)
					cancelLive()
					return
				}
				if inputFrames == 0 || inputBytes == 0 {
					close(audioInput)
					audioInputClosed = true
					finishVoiceLiveWithError(
						liveCtx,
						conn,
						voiceLiveCodeNoSpeech,
						websocket.StatusPolicyViolation,
					)
					cancelLive()
					return
				}
				commitAt = time.Now()
				close(audioInput)
				audioInputClosed = true
			default:
				finishVoiceLiveWithError(
					liveCtx,
					conn,
					voiceLiveCodeResponseInvalid,
					websocket.StatusPolicyViolation,
				)
				cancelLive()
				return
			}
		case <-captureCtx.Done():
			finishVoiceLiveWithError(
				r.Context(),
				conn,
				voiceLiveCodeAPIUnavailable,
				websocket.StatusPolicyViolation,
			)
			cancelLive()
			return
		}
	}
	cancelCapture()
	processingTimer := time.AfterFunc(s.voice.RequestTimeout, cancelLive)
	defer processingTimer.Stop()
	disconnectCtx := conn.CloseRead(liveCtx)

	var outcome voiceLiveOutcome
	select {
	case outcome = <-outcomeChannel:
	case <-disconnectCtx.Done():
		cancelLive()
		s.logVoiceLiveSession(
			liveCtx,
			started,
			authReadyMS,
			firstInputAt,
			commitAt,
			inputFrames,
			inputBytes,
			outputMetrics,
			emptyVoiceLiveTimings(),
			true,
		)
		return
	case <-liveCtx.Done():
		s.logVoiceLiveSession(
			liveCtx,
			started,
			authReadyMS,
			firstInputAt,
			commitAt,
			inputFrames,
			inputBytes,
			outputMetrics,
			emptyVoiceLiveTimings(),
			true,
		)
		return
	}
	if outcome.err != nil {
		code := voiceLiveCodeAPIUnavailable
		if errors.Is(outcome.err, ErrVoiceNotRecognized) ||
			errors.Is(outcome.err, ErrVoiceStateInvalid) {
			code = voiceLiveCodeTurnInvalid
		}
		finishVoiceLiveWithError(
			liveCtx,
			conn,
			code,
			websocket.StatusInternalError,
		)
		s.logVoiceLiveSession(
			liveCtx,
			started,
			authReadyMS,
			firstInputAt,
			commitAt,
			inputFrames,
			inputBytes,
			outputMetrics,
			outcome.result.LiveTimings,
			false,
		)
		return
	}

	_, outputFrames, _ := outputMetrics.snapshot()
	spoke := outputFrames > 0
	if err := validateStreamedVoiceResult(outcome.result, spoke); err != nil {
		finishVoiceLiveWithError(
			liveCtx,
			conn,
			voiceLiveCodeResponseInvalid,
			websocket.StatusInternalError,
		)
		return
	}
	finalResult := voiceLiveFinalResult{
		AudioBase64:      "",
		AudioMIMEType:    "",
		SessionState:     outcome.result.StateToken,
		DetectedDomain:   outcome.result.DetectedDomain,
		AssistanceTarget: outcome.result.AssistanceTarget,
		RespondentStage:  outcome.result.RespondentStage,
		ResearchStatus:   outcome.result.ResearchStatus,
		ResearchRecords:  outcome.result.ResearchRecords,
		Route:            outcome.result.Route,
		NeedsPaper:       outcome.result.NeedsPaper,
	}
	if spoke {
		finalResult.AudioMIMEType = "audio/L16"
		finalResult.Caption = &outcome.result.Caption
	}
	if err := writeVoiceLiveJSON(liveCtx, conn, voiceLiveOutboundFrame{
		Type:    "final",
		Version: voiceLiveVersion,
		Result:  &finalResult,
	}); err != nil {
		cancelLive()
		return
	}
	s.logVoiceLiveSession(
		liveCtx,
		started,
		authReadyMS,
		firstInputAt,
		commitAt,
		inputFrames,
		inputBytes,
		outputMetrics,
		outcome.result.LiveTimings,
		false,
	)
	_ = conn.Close(websocket.StatusNormalClosure, "complete")
}

func validVoiceLiveStart(start voiceLiveStartFrame) bool {
	if start.Type != "start" ||
		start.Version != voiceLiveVersion ||
		start.SampleRateHz != voiceLiveSampleRateHz ||
		!validVoiceLiveJWT(start.IDToken) ||
		!validVoiceLiveJWT(start.AppCheckToken) ||
		len(start.SessionState) > maxStateBytes ||
		!utf8.ValidString(start.SessionState) ||
		strings.TrimSpace(start.SessionState) != start.SessionState {
		return false
	}
	switch start.TurnMode {
	case VoiceTurnIntentional, VoiceTurnAmbient:
		return true
	default:
		return false
	}
}

func decodeStrictVoiceLiveJSON(payload []byte, destination any) error {
	if err := rejectDuplicateTopLevelJSONKeys(payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("WebSocket frame must contain exactly one JSON value")
	}
	return nil
}

func rejectDuplicateTopLevelJSONKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return errors.New("WebSocket JSON frame must be an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("WebSocket JSON object key is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("WebSocket JSON object contains a duplicate key")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("WebSocket frame contains trailing JSON")
		}
		return err
	}
	return nil
}

func validVoiceLiveJWT(value string) bool {
	if value == "" ||
		len(value) > voiceLiveMaxTokenBytes ||
		strings.TrimSpace(value) != value {
		return false
	}
	dots := 0
	segmentLength := 0
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character >= 'A' && character <= 'Z',
			character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '-',
			character == '_':
			segmentLength++
		case character == '.':
			if segmentLength == 0 {
				return false
			}
			dots++
			segmentLength = 0
		default:
			return false
		}
	}
	return dots == 2 && segmentLength > 0
}

func writeVoiceLiveJSON(
	ctx context.Context,
	conn *websocket.Conn,
	frame voiceLiveOutboundFrame,
) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

func finishVoiceLiveWithError(
	ctx context.Context,
	conn *websocket.Conn,
	code string,
	status websocket.StatusCode,
) {
	writeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_ = writeVoiceLiveJSON(writeCtx, conn, voiceLiveOutboundFrame{
		Type:    "error",
		Version: voiceLiveVersion,
		Code:    code,
	})
	_ = conn.Close(status, code)
}

func (s *Server) finishUnexpectedVoiceLiveOutcome(
	ctx context.Context,
	conn *websocket.Conn,
	outcome voiceLiveOutcome,
) {
	code := voiceLiveCodeAPIUnavailable
	if errors.Is(outcome.err, ErrVoiceNotRecognized) ||
		errors.Is(outcome.err, ErrVoiceStateInvalid) {
		code = voiceLiveCodeTurnInvalid
	}
	finishVoiceLiveWithError(
		ctx,
		conn,
		code,
		websocket.StatusInternalError,
	)
}

func (s *Server) consumeVoiceLiveQuota(
	ctx context.Context,
	principal identity.Principal,
	at time.Time,
) string {
	checks := []struct {
		limiter guard.Limiter
		key     string
		scope   string
	}{
		{s.voice.RateLimiter, principal.UID, "uid"},
		{s.voice.AppRateLimiter, "app:" + principal.AppID, "app"},
	}
	errs := make([]error, len(checks))
	var group sync.WaitGroup
	group.Add(len(checks))
	for index := range checks {
		index := index
		go func() {
			defer group.Done()
			errs[index] = checks[index].limiter.Consume(
				ctx,
				checks[index].key,
				at,
			)
		}()
	}
	group.Wait()
	for _, err := range errs {
		if errors.Is(err, guard.ErrRateLimitExceeded) {
			return voiceLiveCodeRateLimited
		}
	}
	for index, err := range errs {
		if err != nil {
			s.logger.ErrorContext(ctx, "voice live rate-limit guard failed",
				"request_id", requestIDFromContext(ctx),
				"quota_scope", checks[index].scope,
				"error_class", "voice_rate_limit_store_failure",
			)
			return voiceLiveCodeAPIUnavailable
		}
	}
	return ""
}

func emptyVoiceLiveTimings() VoiceLiveTimings {
	return VoiceLiveTimings{
		STTFirstInterimMS: -1,
		STTFinalMS:        -1,
		ConversationMS:    -1,
		TTSFirstChunkMS:   -1,
	}
}

func (s *Server) logVoiceLiveSession(
	ctx context.Context,
	started time.Time,
	authReadyMS int64,
	firstInputAt time.Time,
	commitAt time.Time,
	inputFrames int,
	inputBytes int,
	outputMetrics *voiceLiveOutputMetrics,
	timings VoiceLiveTimings,
	cancelled bool,
) {
	firstOutputAt, outputFrames, outputBytes := outputMetrics.snapshot()
	firstInputMS := durationFrom(started, firstInputAt)
	commitMS := durationFrom(started, commitAt)
	commitToFirstAudioMS := int64(-1)
	if !commitAt.IsZero() && !firstOutputAt.IsZero() {
		commitToFirstAudioMS = firstOutputAt.Sub(commitAt).Milliseconds()
		if commitToFirstAudioMS < 0 {
			commitToFirstAudioMS = -1
		}
	}
	s.logger.InfoContext(ctx, "voice live session completed",
		"request_id", requestIDFromContext(ctx),
		"auth_ready_ms", finiteLatency(authReadyMS),
		"first_input_pcm_ms", firstInputMS,
		"commit_ms", commitMS,
		"stt_first_interim_ms", finiteLatency(timings.STTFirstInterimMS),
		"stt_final_ms", finiteLatency(timings.STTFinalMS),
		"conversation_ms", finiteLatency(timings.ConversationMS),
		"tts_first_chunk_ms", finiteLatency(timings.TTSFirstChunkMS),
		"commit_to_first_audio_ms", commitToFirstAudioMS,
		"total_ms", finiteLatency(time.Since(started).Milliseconds()),
		"input_frames", inputFrames,
		"input_bytes", inputBytes,
		"output_frames", outputFrames,
		"output_bytes", outputBytes,
		"cancelled", cancelled,
	)
}

func durationFrom(start time.Time, value time.Time) int64 {
	if value.IsZero() {
		return -1
	}
	return finiteLatency(value.Sub(start).Milliseconds())
}

func finiteLatency(value int64) int64 {
	if value < 0 {
		return -1
	}
	return value
}
