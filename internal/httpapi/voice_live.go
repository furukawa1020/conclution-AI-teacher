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
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
)

const (
	voiceLiveVersion             = 1
	voiceLiveSampleRateHz        = 16_000
	voiceLiveFirstFrameTimeout   = 2 * time.Second
	voiceLiveGuardTimeout        = 5 * time.Second
	voiceLiveReaderJoinTimeout   = 500 * time.Millisecond
	voiceLivePipelineJoinTimeout = 5 * time.Second
	// The client stops normal capture at 3m30s. The extra 30 seconds absorb
	// lead-in and scheduling jitter while keeping the provider stream a full
	// minute below Cloud Speech-to-Text's five-minute hard limit.
	voiceLiveMaxCaptureDuration = 4 * time.Minute
	voiceLiveMaxStartBytes      = 40 * 1024
	voiceLiveMaxPCMFrameBytes   = 15 * 1024
	voiceLivePCMFrameBytes      = 640
	voiceLiveMaxPCMFrames       = 12_000
	voiceLiveMaxPCMTotalBytes   = voiceLivePCMFrameBytes *
		voiceLiveMaxPCMFrames
	voiceLiveMaxTokenBytes = 8 * 1024
	// A process crash cannot run the normal release path. The lease therefore
	// expires shortly after the absolute connection deadline, while remaining
	// valid for every connection the server could still be processing.
	voiceLiveLeaseTTL = VoiceLiveConnectionTimeout + time.Minute
)

// VoiceLiveConnectionTimeout is the Go HTTP and Cloud Run request-timeout
// floor for a bounded live turn: authentication, at most four minutes of
// capture, and the independent post-commit processing budget all fit inside
// it. Cloud Run must be configured to the same value or higher.
const VoiceLiveConnectionTimeout = 6 * time.Minute

// DefaultVoiceLiveHandshakeLimit bounds unauthenticated WebSockets to half of
// the service's Cloud Run concurrency of four. At least two request slots
// therefore cannot be consumed by clients waiting to authenticate.
const DefaultVoiceLiveHandshakeLimit = 2

// VoiceLiveHandshakeGate is an instance-local, non-blocking semaphore. Its
// slot is held only until the first frame has been authenticated, never for a
// normal long-running live session.
type VoiceLiveHandshakeGate struct {
	slots chan struct{}
}

func NewVoiceLiveHandshakeGate(limit int) *VoiceLiveHandshakeGate {
	if limit <= 0 {
		return nil
	}
	return &VoiceLiveHandshakeGate{slots: make(chan struct{}, limit)}
}

func (gate *VoiceLiveHandshakeGate) tryAcquire() (func(), bool) {
	if gate == nil || gate.slots == nil || cap(gate.slots) == 0 {
		return nil, false
	}
	select {
	case gate.slots <- struct{}{}:
		var releaseOnce sync.Once
		return func() {
			releaseOnce.Do(func() {
				<-gate.slots
			})
		}, true
	default:
		return nil, false
	}
}

const (
	voiceLiveCodeAuthenticationFailed = "authentication_failed"
	voiceLiveCodeRateLimited          = "rate_limited"
	voiceLiveCodeAPIUnavailable       = "voice_api_unavailable"
	voiceLiveCodeResponseInvalid      = "voice_response_invalid"
	voiceLiveCodeTurnTooLarge         = "voice_turn_too_large"
	voiceLiveCodeNoSpeech             = "no_speech"
	voiceLiveCodeNativeFallback       = "voice_native_fallback"
	voiceLiveCodeTurnInvalid          = "voice_turn_invalid"
)

type voiceLiveStartFrame struct {
	Type                    string        `json:"type"`
	Version                 int           `json:"version"`
	IDToken                 string        `json:"idToken"`
	AppCheckToken           string        `json:"appCheckToken"`
	NativeCoachControl      bool          `json:"nativeCoachControl,omitempty"`
	SessionState            string        `json:"sessionState"`
	TurnMode                VoiceTurnMode `json:"turnMode"`
	SampleRateHz            int           `json:"sampleRateHz"`
	StrictCloudMinimization bool          `json:"strictCloudMinimization"`
	NativeAudio             bool          `json:"nativeAudio"`
}

type voiceLiveCommitFrame struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
}

type voiceLiveOutboundFrame struct {
	Type         string                `json:"type"`
	Version      int                   `json:"version"`
	Active       bool                  `json:"active,omitempty"`
	SessionState string                `json:"sessionState,omitempty"`
	Code         string                `json:"code,omitempty"`
	Result       *voiceLiveFinalResult `json:"result,omitempty"`
}

type voiceLiveFinalResult struct {
	AudioBase64      string           `json:"audioBase64"`
	AudioMIMEType    string           `json:"audioMimeType"`
	Caption          *string          `json:"caption"`
	SessionState     string           `json:"sessionState"`
	DetectedDomain   string           `json:"detectedDomain"`
	AssistanceTarget string           `json:"assistanceTarget"`
	RespondentStage  string           `json:"respondentStage"`
	CoachPhase       string           `json:"coachPhase"`
	CoachAction      string           `json:"coachAction"`
	ResearchStatus   string           `json:"researchStatus"`
	ResearchRecords  []ResearchRecord `json:"researchRecords"`
	PrivacyStatus    string           `json:"privacyStatus"`
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
	committed     bool
	firstOutputAt time.Time
	frames        int
	bytes         int
}

func (metrics *voiceLiveOutputMetrics) markCommitted() {
	metrics.mu.Lock()
	metrics.committed = true
	metrics.mu.Unlock()
}

func (metrics *voiceLiveOutputMetrics) deliver(
	ctx context.Context,
	conn *websocket.Conn,
	audio []byte,
) error {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if !metrics.committed ||
		len(audio) == 0 ||
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

func setVoiceLiveDeadlines(w http.ResponseWriter, deadline time.Time) error {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(deadline); err != nil {
		return err
	}
	if err := controller.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return nil
}

func (s *Server) finishVoiceLiveLease(
	liveRequestID string,
	cancelLive context.CancelFunc,
	liveLease guard.VoiceLiveLease,
	pipelineDone <-chan struct{},
) {
	cancelLive()
	if pipelineDone != nil {
		joinTimeout := s.voice.livePipelineJoinTimeout
		if joinTimeout <= 0 || joinTimeout > voiceLivePipelineJoinTimeout {
			joinTimeout = voiceLivePipelineJoinTimeout
		}
		joinTimer := time.NewTimer(joinTimeout)
		defer joinTimer.Stop()
		select {
		case <-pipelineDone:
		case <-joinTimer.C:
			s.logger.Error("voice live pipeline shutdown timed out",
				"request_id", liveRequestID,
				"error_class", "voice_live_pipeline_shutdown_timeout",
			)
			// The old worker may still own provider resources. Keep the UID
			// lease until its bounded Firestore TTL expires rather than permit
			// a second live pipeline to overlap it.
			return
		}
	}

	releaseCtx, cancelRelease := context.WithTimeout(
		context.Background(),
		voiceLiveGuardTimeout,
	)
	defer cancelRelease()
	if err := liveLease.Release(releaseCtx); err != nil {
		s.logger.ErrorContext(releaseCtx, "voice live lease release failed",
			"request_id", liveRequestID,
			"error_class", "voice_live_lease_store_failure",
		)
	}
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
		s.voice.LiveLeaseManager == nil ||
		s.voice.LiveHandshakeGate == nil ||
		s.voice.RequestTimeout <= 0 {
		writeProblem(w, http.StatusServiceUnavailable, "voice_unavailable", "Live voice conversation is not configured.")
		return
	}
	releaseHandshake, admitted := s.voice.LiveHandshakeGate.tryAcquire()
	if !admitted {
		writeProblem(w, http.StatusTooManyRequests, "voice_handshake_busy", "Live voice authentication is busy.")
		return
	}
	handshakeHeld := true
	releaseAuthenticationSlot := func() {
		if handshakeHeld {
			handshakeHeld = false
			releaseHandshake()
		}
	}
	defer releaseAuthenticationSlot()

	liveCtx, cancelLive := context.WithTimeout(
		r.Context(),
		VoiceLiveConnectionTimeout,
	)
	defer cancelLive()
	liveRequestID := requestIDFromContext(liveCtx)
	liveDeadline, ok := liveCtx.Deadline()
	if !ok || setVoiceLiveDeadlines(w, liveDeadline) != nil {
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
		liveCtx,
		voiceLiveFirstFrameTimeout,
	)
	messageType, payload, err := conn.Read(firstFrameCtx)
	cancelFirstFrame()
	if err != nil {
		return
	}
	var start voiceLiveStartFrame
	decodeErr := decodeStrictVoiceLiveJSON(payload, &start)
	clear(payload)
	if messageType != websocket.MessageText ||
		decodeErr != nil ||
		!validVoiceLiveStart(start) {
		releaseAuthenticationSlot()
		finishVoiceLiveWithError(
			liveCtx,
			conn,
			voiceLiveCodeResponseInvalid,
			websocket.StatusPolicyViolation,
		)
		return
	}

	verifyCtx, cancelVerify := context.WithTimeout(
		liveCtx,
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
	releaseAuthenticationSlot()
	if err != nil {
		finishVoiceLiveWithError(
			liveCtx,
			conn,
			voiceLiveCodeAuthenticationFailed,
			websocket.StatusPolicyViolation,
		)
		return
	}
	if s.voice.RequireRecentPasskey && !voiceAuthorized(principal, time.Now().UTC()) {
		finishVoiceLiveWithError(
			liveCtx,
			conn,
			voiceLiveCodeAuthenticationFailed,
			websocket.StatusPolicyViolation,
		)
		return
	}
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
	leaseCtx, cancelLease := context.WithTimeout(
		liveCtx,
		voiceLiveGuardTimeout,
	)
	liveLease, leaseErr := s.voice.LiveLeaseManager.Acquire(
		leaseCtx,
		principal.UID,
		time.Now().UTC(),
		voiceLiveLeaseTTL,
	)
	cancelLease()
	if leaseErr != nil {
		code := voiceLiveCodeAPIUnavailable
		if errors.Is(leaseErr, guard.ErrVoiceLiveLeaseHeld) {
			code = voiceLiveCodeRateLimited
		} else {
			s.logger.ErrorContext(liveCtx, "voice live lease guard failed",
				"request_id", liveRequestID,
				"error_class", "voice_live_lease_store_failure",
			)
		}
		finishVoiceLiveWithError(
			liveCtx,
			conn,
			code,
			websocket.StatusPolicyViolation,
		)
		return
	}
	var pipelineDone <-chan struct{}
	defer func() {
		s.finishVoiceLiveLease(
			liveRequestID,
			cancelLive,
			liveLease,
			pipelineDone,
		)
	}()
	if err := writeVoiceLiveJSON(liveCtx, conn, voiceLiveOutboundFrame{
		Type:    "ready",
		Version: voiceLiveVersion,
	}); err != nil {
		return
	}
	readyAt := time.Now()
	authReadyMS := readyAt.Sub(started).Milliseconds()
	conn.SetReadLimit(voiceLiveMaxPCMFrameBytes)
	processingDeadlineSignal := make(chan time.Time, 1)
	processingCommittedSignal := make(chan struct{})
	processingDeadlinePublished := false
	defer func() {
		if !processingDeadlinePublished {
			close(processingDeadlineSignal)
		}
	}()

	input := VoiceTurnInput{
		MIMEType:                "audio/L16",
		StateToken:              start.SessionState,
		RequestID:               requestIDFromContext(liveCtx),
		TurnMode:                start.TurnMode,
		StrictCloudMinimization: start.StrictCloudMinimization,
		NativeAudio:             start.NativeAudio,
		Ambient: start.TurnMode == VoiceTurnAmbient ||
			start.TurnMode == VoiceTurnForeground,
		Foreground:          start.TurnMode == VoiceTurnForeground,
		STTLocale:           "ja-JP",
		SchemaVersion:       voiceLiveVersion,
		ProcessingTimeout:   s.voice.RequestTimeout,
		ProcessingDeadline:  processingDeadlineSignal,
		ProcessingCommitted: processingCommittedSignal,
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
	strictOutput := &strictAudioBuffer{}
	defer strictOutput.clear()
	outcomeChannel := make(chan voiceLiveOutcome, 1)
	endpointChannel := make(chan struct{}, 1)
	pipelineDoneSignal := make(chan struct{})
	pipelineDone = pipelineDoneSignal
	go func() {
		defer close(pipelineDoneSignal)
		defer func() {
			if recovered := recover(); recovered != nil {
				outcomeChannel <- voiceLiveOutcome{
					err: errors.New("live voice pipeline panicked"),
				}
			}
		}()
		var controlGateMu sync.Mutex
		controlAttempted := false
		controlRejected := false
		controlCheckpoint := ""
		rejectControl := func() {
			controlGateMu.Lock()
			controlRejected = true
			controlGateMu.Unlock()
		}
		onAudio := func(audio []byte) error {
			controlGateMu.Lock()
			rejected := controlRejected
			controlGateMu.Unlock()
			if rejected {
				return errors.New("voice coach control state was rejected")
			}
			if input.StrictCloudMinimization {
				return strictOutput.append(audio)
			}
			return outputMetrics.deliver(
				liveCtx,
				conn,
				audio,
			)
		}
		selectedLiveService := liveService
		if input.NativeAudio && s.voice.NativeLiveService != nil {
			selectedLiveService = s.voice.NativeLiveService
		}
		var result VoiceTurnResult
		var processErr error
		if controlService, supportsControl :=
			selectedLiveService.(VoiceTurnLiveControlService); input.NativeAudio &&
			start.NativeCoachControl && supportsControl {
			result, processErr = controlService.ProcessLiveWithControl(
				liveCtx,
				principal.UID,
				input,
				audioInput,
				onAudio,
				func() {
					select {
					case endpointChannel <- struct{}{}:
					default:
					}
				},
				func(sessionState string) error {
					controlGateMu.Lock()
					if controlAttempted {
						controlRejected = true
						controlGateMu.Unlock()
						return errors.New("voice coach control was published more than once")
					}
					controlAttempted = true
					controlGateMu.Unlock()
					if !validVoiceLiveCoachSessionState(sessionState) {
						rejectControl()
						return errors.New("invalid voice coach session state")
					}
					if s.voice.CoachStateValidator == nil ||
						s.voice.CoachStateValidator.ValidateStateToken(
							principal.UID,
							sessionState,
						) != nil {
						rejectControl()
						return errors.New("unauthenticated voice coach session state")
					}
					if err := writeVoiceLiveJSON(
						liveCtx,
						conn,
						voiceLiveOutboundFrame{
							Type:         "coach",
							Version:      voiceLiveVersion,
							Active:       true,
							SessionState: sessionState,
						},
					); err != nil {
						rejectControl()
						return err
					}
					controlGateMu.Lock()
					controlCheckpoint = sessionState
					controlGateMu.Unlock()
					return nil
				},
			)
			controlGateMu.Lock()
			rejected := controlRejected
			checkpoint := controlCheckpoint
			controlGateMu.Unlock()
			if rejected {
				processErr = errors.New("voice coach control state was rejected")
			} else if processErr == nil && checkpoint != "" &&
				result.StateToken != checkpoint {
				processErr = errors.New("voice coach checkpoint did not match final state")
			}
		} else if endpointService, supportsEndpoint :=
			selectedLiveService.(VoiceTurnLiveEndpointService); supportsEndpoint {
			result, processErr = endpointService.ProcessLiveWithEndpoint(
				liveCtx,
				principal.UID,
				input,
				audioInput,
				onAudio,
				func() {
					select {
					case endpointChannel <- struct{}{}:
					default:
					}
				},
			)
		} else {
			result, processErr = selectedLiveService.ProcessLive(
				liveCtx,
				principal.UID,
				input,
				audioInput,
				onAudio,
			)
		}
		outcomeChannel <- voiceLiveOutcome{result: result, err: processErr}
	}()

	inputFrames := 0
	inputBytes := 0
	firstInputAt := time.Time{}
	commitAt := time.Time{}
	processingDeadline := time.Time{}
	captureDeadline := readyAt.Add(voiceLiveMaxCaptureDuration)
	captureCtx, cancelCapture := context.WithDeadline(liveCtx, captureDeadline)
	readChannel := make(chan voiceLiveRead, 1)
	readerContinue := make(chan bool)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			frameType, framePayload, readErr := conn.Read(captureCtx)
			select {
			case readChannel <- voiceLiveRead{
				messageType: frameType,
				payload:     framePayload,
				err:         readErr,
			}:
			case <-captureCtx.Done():
				clear(framePayload)
				return
			}
			if readErr != nil {
				return
			}
			select {
			case continueReading := <-readerContinue:
				if !continueReading {
					return
				}
			case <-captureCtx.Done():
				return
			}
		}
	}()
	readerJoined := false
	joinCaptureReader := func(cancelFirst bool) {
		if readerJoined {
			return
		}
		readerJoined = true
		if cancelFirst {
			cancelCapture()
		}
		timer := time.NewTimer(voiceLiveReaderJoinTimeout)
		defer timer.Stop()
		select {
		case <-readerDone:
		case <-timer.C:
			cancelLive()
		}
		cancelCapture()
		for {
			select {
			case unread := <-readChannel:
				clear(unread.payload)
			default:
				return
			}
		}
	}
	defer func() {
		joinCaptureReader(true)
	}()
	acknowledgeRead := func(continueReading bool) bool {
		select {
		case readerContinue <- continueReading:
			return true
		case <-readerDone:
			return false
		case <-captureCtx.Done():
			return false
		}
	}

	for commitAt.IsZero() {
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
				outcome.result,
			)
			return
		case <-endpointChannel:
			if err := writeVoiceLiveJSON(
				liveCtx,
				conn,
				voiceLiveOutboundFrame{
					Type:    "endpoint",
					Version: voiceLiveVersion,
				},
			); err != nil {
				cancelLive()
				return
			}
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
				if len(read.payload) != voiceLivePCMFrameBytes {
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
					inputFrames >= voiceLiveMaxPCMFrames ||
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
				if !acknowledgeRead(true) {
					cancelLive()
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
				// Close the broadcast before taking the shared deadline origin.
				// Any speculative context that passes both commit checks was
				// therefore created strictly before this timestamp.
				close(processingCommittedSignal)
				commitAt = time.Now()
				processingDeadline = commitAt.Add(s.voice.RequestTimeout)
				if liveDeadline, ok := liveCtx.Deadline(); ok &&
					liveDeadline.Before(processingDeadline) {
					processingDeadline = liveDeadline
				}
				processingDeadlineSignal <- processingDeadline
				close(processingDeadlineSignal)
				processingDeadlinePublished = true
				outputMetrics.markCommitted()
				strictOutput.markCommitted()
				if !acknowledgeRead(false) {
					cancelLive()
					return
				}
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
	joinCaptureReader(false)
	processingTimer := time.AfterFunc(
		time.Until(processingDeadline),
		cancelLive,
	)
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
		_, outputFrames, _ := outputMetrics.snapshot()
		if errors.Is(outcome.err, ErrVoiceNativeFallback) &&
			input.NativeAudio && outputFrames == 0 {
			code = voiceLiveCodeNativeFallback
		} else if errors.Is(outcome.err, ErrVoiceNotRecognized) ||
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
			outcome.result,
		)
		return
	}

	_, outputFrames, _ := outputMetrics.snapshot()
	spoke := outputFrames > 0
	if input.StrictCloudMinimization {
		spoke = strictOutput.spoke()
	}
	if err := validateStreamedVoiceResultForInput(input, outcome.result, spoke); err != nil {
		finishVoiceLiveWithError(
			liveCtx,
			conn,
			voiceLiveCodeResponseInvalid,
			websocket.StatusInternalError,
		)
		return
	}
	if input.StrictCloudMinimization && spoke {
		if err := strictOutput.release(func(audio []byte) error {
			return outputMetrics.deliver(liveCtx, conn, audio)
		}); err != nil {
			cancelLive()
			return
		}
	}
	finalResult := voiceLiveFinalResult{
		AudioBase64:      "",
		AudioMIMEType:    "",
		SessionState:     outcome.result.StateToken,
		DetectedDomain:   outcome.result.DetectedDomain,
		AssistanceTarget: outcome.result.AssistanceTarget,
		RespondentStage:  outcome.result.RespondentStage,
		CoachPhase:       outcome.result.CoachPhase,
		CoachAction:      outcome.result.CoachAction,
		ResearchStatus:   outcome.result.ResearchStatus,
		ResearchRecords:  outcome.result.ResearchRecords,
		PrivacyStatus:    outcome.result.PrivacyStatus,
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
		outcome.result,
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
		strings.TrimSpace(start.SessionState) != start.SessionState ||
		(start.StrictCloudMinimization && start.SessionState != "") ||
		(start.StrictCloudMinimization && start.NativeAudio) ||
		(start.NativeCoachControl && !start.NativeAudio) {
		return false
	}
	switch start.TurnMode {
	case VoiceTurnIntentional, VoiceTurnForeground, VoiceTurnAmbient:
		return true
	default:
		return false
	}
}

func validVoiceLiveCoachSessionState(value string) bool {
	return value != "" &&
		len(value) <= maxStateBytes &&
		utf8.ValidString(value) &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsFunc(value, unicode.IsControl)
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
		STTFirstInterimMS:   -1,
		STTFinalMS:          -1,
		ConversationMS:      -1,
		TTSFirstChunkMS:     -1,
		FinalToFirstAudioMS: -1,
		TTSReleaseMS:        -1,
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
	results ...VoiceTurnResult,
) {
	route := "unknown"
	coachActive := false
	if len(results) > 0 {
		route = results[0].Route
		if route == "" {
			route = "unknown"
		}
		coachActive = results[0].AssistanceTarget == "respondent"
	}
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
		"route", route,
		"coach_active", coachActive,
		"auth_ready_ms", finiteLatency(authReadyMS),
		"first_input_pcm_ms", firstInputMS,
		"commit_ms", commitMS,
		"stt_first_interim_ms", finiteLatency(timings.STTFirstInterimMS),
		"stt_final_ms", finiteLatency(timings.STTFinalMS),
		"conversation_ms", finiteLatency(timings.ConversationMS),
		"tts_first_chunk_ms", finiteLatency(timings.TTSFirstChunkMS),
		"final_to_first_audio_ms",
		finiteLatency(timings.FinalToFirstAudioMS),
		"spec_hit", timings.SpecHit,
		"spec_miss", timings.SpecMiss,
		"spec_cancel", timings.SpecCancel,
		"tts_prestarted", timings.TTSPrestarted,
		"tts_buffered_bytes", timings.TTSBufferedBytes,
		"tts_release_ms", finiteLatency(timings.TTSReleaseMS),
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
