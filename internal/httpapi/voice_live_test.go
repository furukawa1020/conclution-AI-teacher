package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
)

const (
	liveTestIDToken       = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1c2VyLTEyMyJ9.signature"
	liveTestAppCheckToken = "eyJhbGciOiJub25lIn0.eyJhcHBfaWQiOiJhcHAtMTIzIn0.signature"
	liveTestPCMFrameBytes = 640
)

func TestEmptyVoiceLiveTimingsMarksNativeWaterfallMissing(t *testing.T) {
	timings := emptyVoiceLiveTimings()
	if timings.CommitToServerDrainMS != -1 ||
		timings.ServerDrainToActivityEndMS != -1 ||
		timings.ActivityEndToFinalCaptionMS != -1 ||
		timings.FinalToRiskRouteGateMS != -1 ||
		timings.OutputCommitToFirstAudioMS != -1 {
		t.Fatalf("empty native waterfall = %+v", timings)
	}
}

func TestVoiceLiveLatencyProofIsExactOrderedAndOneShot(t *testing.T) {
	valid := voiceLiveLatencyFrame{
		Type: "latency", Version: 1,
		SpeechEndToCommitSendMS: 120, SpeechEndToCommitAckMS: 180,
		SpeechEndToEstimatedAudibleMS: 760,
	}
	if !validVoiceLiveLatencyFrame(valid) {
		t.Fatal("valid content-free latency proof was rejected")
	}
	for name, mutate := range map[string]func(*voiceLiveLatencyFrame){
		"type":             func(value *voiceLiveLatencyFrame) { value.Type = "commit" },
		"version":          func(value *voiceLiveLatencyFrame) { value.Version = 2 },
		"negative":         func(value *voiceLiveLatencyFrame) { value.SpeechEndToCommitSendMS = -1 },
		"send_after_ack":   func(value *voiceLiveLatencyFrame) { value.SpeechEndToCommitSendMS = 181 },
		"ack_after_total":  func(value *voiceLiveLatencyFrame) { value.SpeechEndToCommitAckMS = 761 },
		"over_ten_seconds": func(value *voiceLiveLatencyFrame) { value.SpeechEndToEstimatedAudibleMS = 10_001 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if validVoiceLiveLatencyFrame(candidate) {
				t.Fatal("invalid latency proof was accepted")
			}
		})
	}
	metrics := &voiceLiveOutputMetrics{}
	metrics.markLatencyProof(valid)
	duplicate := valid
	duplicate.SpeechEndToEstimatedAudibleMS = 900
	metrics.markLatencyProof(duplicate)
	if got := metrics.latencyProofSnapshot(); !got.valid ||
		got.speechEndToEstimatedAudibleMS != 760 {
		t.Fatalf("proof snapshot = %+v, want first proof only", got)
	}
}

func TestVoiceLiveLogIncludesContentFreeNativeWaterfall(t *testing.T) {
	var output bytes.Buffer
	server := &Server{logger: slog.New(slog.NewJSONHandler(&output, nil))}
	base := time.Unix(1_700_000_000, 0)
	metrics := &voiceLiveOutputMetrics{
		committed:     true,
		firstOutputAt: base.Add(40 * time.Millisecond),
		frames:        1,
		bytes:         2,
	}
	metrics.markLatencyProof(voiceLiveLatencyFrame{
		Type: "latency", Version: 1,
		SpeechEndToCommitSendMS:       120,
		SpeechEndToCommitAckMS:        180,
		SpeechEndToEstimatedAudibleMS: 760,
	})
	metrics.markNativeCommit(base)
	for stage, at := range map[VoiceNativeWaterfallStage]time.Time{
		VoiceNativeWaterfallServerDrain:        base.Add(7 * time.Millisecond),
		VoiceNativeWaterfallActivityEnd:        base.Add(11 * time.Millisecond),
		VoiceNativeWaterfallFinalCaption:       base.Add(20 * time.Millisecond),
		VoiceNativeWaterfallRiskRouteGate:      base.Add(23 * time.Millisecond),
		VoiceNativeWaterfallOutputCommit:       base.Add(25 * time.Millisecond),
		VoiceNativeWaterfallFirstMeaningfulPCM: base.Add(31 * time.Millisecond),
	} {
		metrics.markNativeBoundary(stage, at)
	}
	server.logVoiceLiveSession(
		context.Background(),
		base,
		-1,
		time.Time{},
		base,
		1,
		640,
		metrics,
		VoiceLiveTimings{
			CommitToServerDrainMS:       999,
			ServerDrainToActivityEndMS:  999,
			ActivityEndToFinalCaptionMS: 999,
			FinalToRiskRouteGateMS:      999,
			OutputCommitToFirstAudioMS:  999,
		},
		false,
	)
	var logged map[string]any
	if err := json.Unmarshal(output.Bytes(), &logged); err != nil {
		t.Fatalf("decode voice live log: %v", err)
	}
	for key, want := range map[string]float64{
		"commit_to_server_drain_ms":                   7,
		"server_drain_to_activity_end_ms":             4,
		"activity_end_to_final_caption_ms":            9,
		"final_to_risk_route_gate_ms":                 3,
		"output_commit_to_first_audio_ms":             6,
		"speech_end_to_server_commit_lower_ms":        120,
		"speech_end_to_server_commit_upper_ms":        180,
		"server_commit_to_estimated_audible_lower_ms": 580,
		"server_commit_to_estimated_audible_upper_ms": 640,
		"latency_proof_uncertainty_ms":                60,
		"speech_end_to_estimated_audible_ms":          760,
	} {
		if got := logged[key]; got != want {
			t.Fatalf("%s = %#v, want %.0f; log=%s", key, got, want, output.String())
		}
	}
}

func TestNativeWaterfallSnapshotPreservesPartialAndIgnoresLateMarkers(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	metrics := &voiceLiveOutputMetrics{}
	metrics.markNativeCommit(base)
	metrics.markNativeBoundary(
		VoiceNativeWaterfallServerDrain,
		base.Add(7*time.Millisecond),
	)
	_, _, _, snapshot := metrics.snapshotForLog()
	metrics.markNativeBoundary(
		VoiceNativeWaterfallActivityEnd,
		base.Add(11*time.Millisecond),
	)
	_, _, _, afterLateMarker := metrics.snapshotForLog()
	timings := nativeWaterfallTimings(snapshot)
	lateTimings := nativeWaterfallTimings(afterLateMarker)
	if timings.CommitToServerDrainMS != 7 ||
		timings.ServerDrainToActivityEndMS != -1 ||
		lateTimings.ServerDrainToActivityEndMS != -1 {
		t.Fatalf("snapshot=%+v after late marker=%+v", timings, lateTimings)
	}
}

func TestVoiceLiveLogForcesUnobservedNativeWaterfallToMinusOne(t *testing.T) {
	var output bytes.Buffer
	server := &Server{logger: slog.New(slog.NewJSONHandler(&output, nil))}
	base := time.Unix(1_700_000_000, 0)
	server.logVoiceLiveSession(
		context.Background(),
		base,
		-1,
		time.Time{},
		base,
		0,
		0,
		&voiceLiveOutputMetrics{},
		VoiceLiveTimings{
			CommitToServerDrainMS:       999,
			ServerDrainToActivityEndMS:  999,
			ActivityEndToFinalCaptionMS: 999,
			FinalToRiskRouteGateMS:      999,
			OutputCommitToFirstAudioMS:  999,
		},
		true,
	)
	var logged map[string]any
	if err := json.Unmarshal(output.Bytes(), &logged); err != nil {
		t.Fatalf("decode voice live log: %v", err)
	}
	for _, key := range []string{
		"commit_to_server_drain_ms",
		"server_drain_to_activity_end_ms",
		"activity_end_to_final_caption_ms",
		"final_to_risk_route_gate_ms",
		"output_commit_to_first_audio_ms",
	} {
		if got := logged[key]; got != float64(-1) {
			t.Fatalf("%s = %#v, want -1; log=%s", key, got, output.String())
		}
	}
}

type voiceLiveOutputMetricObservation struct {
	firstOutputAt time.Time
	frames        int
	bytes         int
	err           error
}

func exerciseVoiceLiveOutputMetrics(
	t *testing.T,
	chunks [][]byte,
) ([]voiceLiveOutputMetricObservation, [][]byte) {
	t.Helper()
	observations := make(chan []voiceLiveOutputMetricObservation, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, nil)
		if err != nil {
			observations <- []voiceLiveOutputMetricObservation{{err: err}}
			return
		}
		defer conn.CloseNow()
		metrics := &voiceLiveOutputMetrics{}
		metrics.markCommitted()
		result := make([]voiceLiveOutputMetricObservation, 0, len(chunks))
		for _, chunk := range chunks {
			deliveryErr := metrics.deliver(request.Context(), conn, chunk)
			firstOutputAt, frames, bytes := metrics.snapshot()
			result = append(result, voiceLiveOutputMetricObservation{
				firstOutputAt: firstOutputAt,
				frames:        frames,
				bytes:         bytes,
				err:           deliveryErr,
			})
		}
		observations <- result
		_ = conn.Close(websocket.StatusNormalClosure, "complete")
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(
		ctx,
		"ws"+strings.TrimPrefix(server.URL, "http"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	received := make([][]byte, 0, len(chunks))
	for {
		messageType, payload, readErr := conn.Read(ctx)
		if readErr != nil {
			if websocket.CloseStatus(readErr) != websocket.StatusNormalClosure {
				t.Fatal(readErr)
			}
			break
		}
		if messageType != websocket.MessageBinary {
			t.Fatalf("output metrics published non-binary message type %v", messageType)
		}
		received = append(received, append([]byte(nil), payload...))
	}
	select {
	case result := <-observations:
		return result, received
	case <-ctx.Done():
		t.Fatal("output metrics observations timed out")
		return nil, nil
	}
}

func TestVoiceLiveOutputMetricsStartsAtFirstPublishedMeaningfulPCM(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		meaningful []byte
	}{
		{name: "positive threshold", meaningful: []byte{33, 0}},
		{name: "negative threshold", meaningful: []byte{223, 255}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			chunks := [][]byte{
				{0, 0, 0, 0},
				{32, 0},
				{224, 255},
				test.meaningful,
				{33, 0},
			}
			observed, received := exerciseVoiceLiveOutputMetrics(t, chunks)
			if len(observed) != len(chunks) || len(received) != len(chunks) {
				t.Fatalf("observations=%d received=%d, want %d", len(observed), len(received), len(chunks))
			}
			for index := 0; index < 3; index++ {
				if observed[index].err != nil || !observed[index].firstOutputAt.IsZero() {
					t.Fatalf("non-meaningful chunk %d started audio clock: %+v", index, observed[index])
				}
			}
			firstMeaningfulAt := observed[3].firstOutputAt
			if observed[3].err != nil || firstMeaningfulAt.IsZero() {
				t.Fatalf("meaningful chunk did not start audio clock: %+v", observed[3])
			}
			if observed[4].err != nil || observed[4].firstOutputAt != firstMeaningfulAt {
				t.Fatalf("later meaningful chunk moved first audio clock: %+v", observed[4])
			}
			for index, chunk := range chunks {
				if !bytes.Equal(received[index], chunk) {
					t.Fatalf("published chunk %d=%v, want %v", index, received[index], chunk)
				}
			}
		})
	}
}

func TestVoiceLiveOutputMetricsAllSilentAndOddPCMNeverStartAudioClock(t *testing.T) {
	t.Parallel()
	chunks := [][]byte{
		{0, 0, 0, 0},
		{32, 0},
		{224, 255},
		{33, 0, 255},
	}
	observed, received := exerciseVoiceLiveOutputMetrics(t, chunks)
	if len(observed) != len(chunks) {
		t.Fatalf("observations=%d, want %d", len(observed), len(chunks))
	}
	for index := 0; index < 3; index++ {
		if observed[index].err != nil || !observed[index].firstOutputAt.IsZero() {
			t.Fatalf("silent chunk %d started audio clock: %+v", index, observed[index])
		}
	}
	if observed[3].err == nil || !observed[3].firstOutputAt.IsZero() {
		t.Fatalf("odd PCM changed output metrics: %+v", observed[3])
	}
	if observed[3].frames != observed[2].frames || observed[3].bytes != observed[2].bytes {
		t.Fatalf("odd PCM changed counters: before=%+v after=%+v", observed[2], observed[3])
	}
	if len(received) != 3 {
		t.Fatalf("published chunks=%d, want 3 valid silent chunks", len(received))
	}
}

func TestVoiceLiveBudgetsSupportThreeMinuteMonologueBelowProviderLimit(
	t *testing.T,
) {
	t.Parallel()
	const (
		providerStreamingLimit = 5 * time.Minute
		targetMonologue        = 3 * time.Minute
		pcmFrameDuration       = 20 * time.Millisecond
		maxPostCommitProcess   = 50 * time.Second
	)
	if voiceLiveMaxCaptureDuration < targetMonologue {
		t.Fatal("capture budget does not admit a three-minute monologue")
	}
	if voiceLiveMaxCaptureDuration >= providerStreamingLimit {
		t.Fatal("capture budget reaches the provider streaming limit")
	}
	frameBudget := time.Duration(voiceLiveMaxPCMFrames) * pcmFrameDuration
	if frameBudget < voiceLiveMaxCaptureDuration {
		t.Fatal("PCM frame budget ends before the capture deadline")
	}
	if voiceLiveMaxPCMTotalBytes !=
		voiceLiveMaxPCMFrames*voiceLivePCMFrameBytes {
		t.Fatal("PCM byte and frame bounds diverged")
	}
	minimumConnectionBudget := voiceLiveFirstFrameTimeout +
		2*voiceLiveGuardTimeout +
		voiceLiveMaxCaptureDuration +
		maxPostCommitProcess
	if VoiceLiveConnectionTimeout <= minimumConnectionBudget {
		t.Fatal("HTTP connection timeout has no cleanup margin")
	}
	if voiceLiveFirstFrameTimeout <= 0 ||
		voiceLiveFirstFrameTimeout > 2*time.Second {
		t.Fatal(`unauthenticated first-frame window is not tightly bounded`)
	}
	if DefaultVoiceLiveHandshakeLimit != 2 {
		t.Fatal(`default gate must reserve capacity for authenticated and HTTPS work`)
	}
}

func TestVoiceLiveHandshakeGateBoundsBurstAtDefaultCapacity(t *testing.T) {
	t.Parallel()
	gate := NewVoiceLiveHandshakeGate(DefaultVoiceLiveHandshakeLimit)

	releaseFirst, admitted := gate.tryAcquire()
	if !admitted {
		t.Fatal("first handshake was not admitted")
	}
	releaseSecond, admitted := gate.tryAcquire()
	if !admitted {
		t.Fatal("second handshake was not admitted")
	}
	if releaseUnexpected, admitted := gate.tryAcquire(); admitted {
		releaseUnexpected()
		t.Fatal("third handshake exceeded the bounded default capacity")
	}

	releaseFirst()
	releaseFirst()
	releaseReplacement, admitted := gate.tryAcquire()
	if !admitted {
		t.Fatal("released handshake slot was not reusable")
	}
	if releaseUnexpected, admitted := gate.tryAcquire(); admitted {
		releaseUnexpected()
		t.Fatal("replacement handshake allowed a burst above capacity")
	}

	releaseSecond()
	releaseReplacement()
}

func liveTestPCMFrame() []byte {
	frame := make([]byte, liveTestPCMFrameBytes)
	frame[0] = 1
	frame[2] = 2
	return frame
}

type liveTestVerifier struct {
	mu            sync.Mutex
	calls         int
	idToken       string
	appCheckToken string
	err           error
	principal     identity.Principal
}

type liveTestLease struct {
	mu           sync.Mutex
	releaseCalls int
	err          error
}

func (lease *liveTestLease) Release(context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.releaseCalls++
	return lease.err
}

type liveTestLeaseManager struct {
	mu           sync.Mutex
	acquireCalls int
	uids         []string
	err          error
	lease        *liveTestLease
}

type liveDeadlineResponseWriter struct {
	header        http.Header
	status        int
	readDeadline  time.Time
	writeDeadline time.Time
	readErr       error
	writeErr      error
}

func (writer *liveDeadlineResponseWriter) Header() http.Header {
	if writer.header == nil {
		writer.header = make(http.Header)
	}
	return writer.header
}

func (writer *liveDeadlineResponseWriter) Write(payload []byte) (int, error) {
	return len(payload), nil
}

func (writer *liveDeadlineResponseWriter) WriteHeader(status int) {
	writer.status = status
}

func (writer *liveDeadlineResponseWriter) SetReadDeadline(deadline time.Time) error {
	writer.readDeadline = deadline
	return writer.readErr
}

func (writer *liveDeadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	writer.writeDeadline = deadline
	return writer.writeErr
}

func (manager *liveTestLeaseManager) Acquire(
	_ context.Context,
	uid string,
	_ time.Time,
	_ time.Duration,
) (guard.VoiceLiveLease, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.acquireCalls++
	manager.uids = append(manager.uids, uid)
	if manager.err != nil {
		return nil, manager.err
	}
	if manager.lease == nil {
		manager.lease = &liveTestLease{}
	}
	return manager.lease, nil
}

func (verifier *liveTestVerifier) Verify(
	_ context.Context,
	idToken string,
	appCheckToken string,
) (identity.Principal, error) {
	verifier.mu.Lock()
	verifier.calls++
	verifier.idToken = idToken
	verifier.appCheckToken = appCheckToken
	verifier.mu.Unlock()
	if verifier.err != nil {
		return identity.Principal{}, verifier.err
	}
	if verifier.principal.UID != "" {
		return verifier.principal, nil
	}
	return identity.Principal{
		UID:   "user-123",
		AppID: "app-123",
		Roles: map[string]bool{"user": true},
	}, nil
}

func TestVoiceLiveRequiresFreshPasskeyWhenGateEnabled(t *testing.T) {
	service := &liveTestVoiceService{}
	verifier := &liveTestVerifier{principal: identity.Principal{
		UID:             "pk_user",
		AppID:           "app-123",
		Provider:        "custom",
		AuthMethod:      "passkey-v1",
		AuthTime:        time.Now().Add(-6 * time.Minute),
		AccountVerified: true,
	}}
	handler := NewWithVoice(
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		verifier,
		&fakeLimiter{},
		&fakeEvaluator{},
		&fakeStore{},
		time.Second,
		4*1024,
		VoiceOptions{
			Service:              service,
			RateLimiter:          &fakeLimiter{},
			AppRateLimiter:       &fakeLimiter{wantKey: "app:app-123"},
			LiveLeaseManager:     guard.NewMemoryVoiceLiveLeaseManager(),
			RequestTimeout:       2 * time.Second,
			MaxRequestBytes:      13 * 1024 * 1024,
			RequireRecentPasskey: true,
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveStart(t, ctx, conn)
	frame := readVoiceLiveJSON(t, ctx, conn)
	if frame["type"] != "error" || frame["code"] != voiceLiveCodeAuthenticationFailed {
		t.Fatalf("frame = %#v", frame)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.input.ProcessingTimeout != 0 {
		t.Fatal("stale passkey reached live voice service")
	}
}

type liveTestVoiceService struct {
	mu               sync.Mutex
	processLiveCalls int
	input            VoiceTurnInput
	audio            [][]byte
	output           [][]byte
	result           VoiceTurnResult
	err              error
	waitForCancel    bool
	cancelObserved   chan struct{}
	cancelSignalOne  sync.Once
}

type liveEndpointTestService struct {
	liveTestVoiceService
	endpointOnce sync.Once
}

type liveControlTestService struct {
	liveTestVoiceService
	basicCalls         int
	endpointCalls      int
	controlCalls       int
	controlPublishes   int
	withoutControlErr  error
	prepareErr         error
	controlState       *string
	controlPrevious    *string
	controlCheckpoint  *VoiceRespondentCheckpoint
	ignoreControlError bool
}

type liveCoachStateValidator struct{}

func (*liveCoachStateValidator) ValidateRespondentCheckpointTransition(
	uid string,
	_ string,
	preparedToken string,
	token string,
	requestID string,
	assistanceTarget string,
	respondentStage string,
	coachPhase string,
	coachAction string,
) error {
	if uid != "user-123" || requestID == "" || preparedToken == "" ||
		preparedToken == "cryptographically-invalid" ||
		preparedToken == "signed-for-other-user" ||
		token == "cryptographically-invalid" ||
		token == "signed-for-other-user" ||
		assistanceTarget != "respondent" ||
		(respondentStage != "awaiting_answer" && respondentStage != "restructure") ||
		!validCoachMetadata(assistanceTarget, coachPhase, coachAction) {
		return errors.New("invalid caller-bound state")
	}
	return nil
}

func validLiveCoachResult() VoiceTurnResult {
	return VoiceTurnResult{
		StateToken:       "signed-native-coach-state",
		DetectedDomain:   "daily",
		AssistanceTarget: "respondent",
		RespondentStage:  "awaiting_answer",
		CoachPhase:       "awaiting_answer",
		CoachAction:      "elicit",
		ResearchStatus:   "none",
		ResearchRecords:  []ResearchRecord{},
		Route:            VoiceNativeRespondentCoachRoute,
		Caption:          "one bounded question",
	}
}

type liveLifecycleTestService struct {
	started     chan struct{}
	canceled    chan struct{}
	allowReturn <-chan struct{}
	done        chan struct{}
}

func publishLiveTestInputReady(input VoiceTurnInput) {
	if input.OnInputReady != nil {
		input.OnInputReady()
	}
}

func (*liveLifecycleTestService) Process(
	context.Context,
	string,
	VoiceTurnInput,
) (VoiceTurnResult, error) {
	return VoiceTurnResult{}, errors.New("buffered voice method called")
}

func (service *liveLifecycleTestService) ProcessLive(
	ctx context.Context,
	_ string,
	_ VoiceTurnInput,
	audio <-chan []byte,
	_ func([]byte) error,
) (VoiceTurnResult, error) {
	close(service.started)
	defer close(service.done)
	for {
		select {
		case <-ctx.Done():
			close(service.canceled)
			if service.allowReturn != nil {
				<-service.allowReturn
			}
			return VoiceTurnResult{}, ctx.Err()
		case _, open := <-audio:
			if !open {
				<-ctx.Done()
				close(service.canceled)
				if service.allowReturn != nil {
					<-service.allowReturn
				}
				return VoiceTurnResult{}, ctx.Err()
			}
		}
	}
}

func (service *liveEndpointTestService) ProcessLiveWithEndpoint(
	ctx context.Context,
	uid string,
	input VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
	onEndpoint func(),
) (VoiceTurnResult, error) {
	if uid != "user-123" {
		return VoiceTurnResult{}, errors.New("unexpected uid")
	}
	service.mu.Lock()
	service.processLiveCalls++
	service.input = input
	service.mu.Unlock()
	publishLiveTestInputReady(input)
	for {
		select {
		case <-ctx.Done():
			service.signalCanceled()
			return VoiceTurnResult{}, ctx.Err()
		case chunk, open := <-audio:
			if !open {
				for _, output := range service.output {
					if err := onAudio(output); err != nil {
						return VoiceTurnResult{}, err
					}
				}
				return service.result, service.err
			}
			copied := append([]byte(nil), chunk...)
			service.mu.Lock()
			service.audio = append(service.audio, copied)
			service.mu.Unlock()
			service.endpointOnce.Do(onEndpoint)
		}
	}
}

func (service *liveControlTestService) ProcessLiveWithControl(
	ctx context.Context,
	uid string,
	input VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
	_ func(),
	onCoachActive func(VoiceRespondentCheckpointTransition) error,
) (VoiceTurnResult, error) {
	if uid != "user-123" {
		return VoiceTurnResult{}, errors.New("unexpected uid")
	}
	service.mu.Lock()
	service.processLiveCalls++
	service.input = input
	service.mu.Unlock()
	publishLiveTestInputReady(input)
	for {
		select {
		case <-ctx.Done():
			service.signalCanceled()
			return VoiceTurnResult{}, ctx.Err()
		case chunk, open := <-audio:
			if !open {
				if service.prepareErr != nil {
					return VoiceTurnResult{}, service.prepareErr
				}
				checkpoint := VoiceRespondentCheckpoint{
					SessionState:     service.result.StateToken,
					Route:            service.result.Route,
					AssistanceTarget: service.result.AssistanceTarget,
					RespondentStage:  service.result.RespondentStage,
					CoachPhase:       service.result.CoachPhase,
					CoachAction:      service.result.CoachAction,
				}
				if service.controlState != nil {
					checkpoint.SessionState = *service.controlState
				}
				if service.controlCheckpoint != nil {
					checkpoint = *service.controlCheckpoint
				}
				previous := "signed-prepared-native-state"
				if service.controlPrevious != nil {
					previous = *service.controlPrevious
				}
				transition := VoiceRespondentCheckpointTransition{
					PreviousSessionState: previous,
					Checkpoint:           checkpoint,
				}
				publishes := service.controlPublishes
				if publishes == 0 {
					publishes = 1
				}
				for range publishes {
					service.mu.Lock()
					service.controlCalls++
					service.mu.Unlock()
					if err := onCoachActive(transition); err != nil &&
						!service.ignoreControlError {
						return VoiceTurnResult{}, err
					}
				}
				for _, output := range service.output {
					if err := onAudio(output); err != nil {
						return VoiceTurnResult{}, err
					}
				}
				return service.result, service.err
			}
			copied := append([]byte(nil), chunk...)
			service.mu.Lock()
			service.audio = append(service.audio, copied)
			service.mu.Unlock()
		}
	}
}

func (service *liveControlTestService) ProcessLive(
	ctx context.Context,
	uid string,
	input VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
) (VoiceTurnResult, error) {
	service.mu.Lock()
	service.basicCalls++
	service.mu.Unlock()
	if service.withoutControlErr == nil {
		return service.liveTestVoiceService.ProcessLive(
			ctx,
			uid,
			input,
			audio,
			onAudio,
		)
	}
	if uid != "user-123" {
		return VoiceTurnResult{}, errors.New("unexpected uid")
	}
	service.mu.Lock()
	service.processLiveCalls++
	service.input = input
	service.mu.Unlock()
	publishLiveTestInputReady(input)
	for {
		select {
		case <-ctx.Done():
			service.signalCanceled()
			return VoiceTurnResult{}, ctx.Err()
		case chunk, open := <-audio:
			if !open {
				return VoiceTurnResult{}, service.withoutControlErr
			}
			copied := append([]byte(nil), chunk...)
			service.mu.Lock()
			service.audio = append(service.audio, copied)
			service.mu.Unlock()
		}
	}
}

func (service *liveControlTestService) ProcessLiveWithEndpoint(
	ctx context.Context,
	uid string,
	input VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
	_ func(),
) (VoiceTurnResult, error) {
	if uid != "user-123" {
		return VoiceTurnResult{}, errors.New("unexpected uid")
	}
	service.mu.Lock()
	service.endpointCalls++
	service.processLiveCalls++
	service.input = input
	service.mu.Unlock()
	publishLiveTestInputReady(input)
	for {
		select {
		case <-ctx.Done():
			service.signalCanceled()
			return VoiceTurnResult{}, ctx.Err()
		case chunk, open := <-audio:
			if !open {
				if service.withoutControlErr != nil {
					return VoiceTurnResult{}, service.withoutControlErr
				}
				for _, output := range service.output {
					if err := onAudio(output); err != nil {
						return VoiceTurnResult{}, err
					}
				}
				return service.result, service.err
			}
			copied := append([]byte(nil), chunk...)
			service.mu.Lock()
			service.audio = append(service.audio, copied)
			service.mu.Unlock()
		}
	}
}

func (service *liveTestVoiceService) Process(
	context.Context,
	string,
	VoiceTurnInput,
) (VoiceTurnResult, error) {
	return VoiceTurnResult{}, errors.New("buffered voice method called")
}

func (service *liveTestVoiceService) ProcessLive(
	ctx context.Context,
	uid string,
	input VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
) (VoiceTurnResult, error) {
	if uid != "user-123" {
		return VoiceTurnResult{}, errors.New("unexpected uid")
	}
	service.mu.Lock()
	service.processLiveCalls++
	service.input = input
	service.mu.Unlock()
	publishLiveTestInputReady(input)
	for {
		select {
		case <-ctx.Done():
			service.signalCanceled()
			return VoiceTurnResult{}, ctx.Err()
		case chunk, open := <-audio:
			if !open {
				goto committed
			}
			copied := append([]byte(nil), chunk...)
			service.mu.Lock()
			service.audio = append(service.audio, copied)
			service.mu.Unlock()
		}
	}

committed:
	if service.waitForCancel {
		<-ctx.Done()
		service.signalCanceled()
		return VoiceTurnResult{}, ctx.Err()
	}
	for _, chunk := range service.output {
		if err := onAudio(chunk); err != nil {
			return VoiceTurnResult{}, err
		}
	}
	return service.result, service.err
}

func (service *liveTestVoiceService) signalCanceled() {
	service.cancelSignalOne.Do(func() {
		if service.cancelObserved != nil {
			close(service.cancelObserved)
		}
	})
}

func newVoiceLiveTestServer(
	t *testing.T,
	service VoiceTurnService,
	verifier identity.Verifier,
	uidLimiter guard.Limiter,
	appLimiter guard.Limiter,
	leaseManagers ...guard.VoiceLiveLeaseManager,
) *httptest.Server {
	t.Helper()
	leaseManager := guard.VoiceLiveLeaseManager(
		guard.NewMemoryVoiceLiveLeaseManager(),
	)
	if len(leaseManagers) > 0 {
		leaseManager = leaseManagers[0]
	}
	return newVoiceLiveControlledTestServer(
		t,
		service,
		verifier,
		uidLimiter,
		appLimiter,
		leaseManager,
		NewVoiceLiveHandshakeGate(2),
		0,
		nil,
	)
}

func newVoiceLiveControlledTestServer(
	t *testing.T,
	service VoiceTurnService,
	verifier identity.Verifier,
	uidLimiter guard.Limiter,
	appLimiter guard.Limiter,
	leaseManager guard.VoiceLiveLeaseManager,
	handshakeGate *VoiceLiveHandshakeGate,
	pipelineJoinTimeout time.Duration,
	handlerReturned chan<- struct{},
	nativeLiveServices ...VoiceTurnLiveService,
) *httptest.Server {
	return newVoiceLiveControlledTestServerWithNativeReadyTimeout(
		t,
		service,
		verifier,
		uidLimiter,
		appLimiter,
		leaseManager,
		handshakeGate,
		pipelineJoinTimeout,
		0,
		handlerReturned,
		nativeLiveServices...,
	)
}

func newVoiceLiveControlledTestServerWithNativeReadyTimeout(
	t *testing.T,
	service VoiceTurnService,
	verifier identity.Verifier,
	uidLimiter guard.Limiter,
	appLimiter guard.Limiter,
	leaseManager guard.VoiceLiveLeaseManager,
	handshakeGate *VoiceLiveHandshakeGate,
	pipelineJoinTimeout time.Duration,
	nativeReadyTimeout time.Duration,
	handlerReturned chan<- struct{},
	nativeLiveServices ...VoiceTurnLiveService,
) *httptest.Server {
	t.Helper()
	var nativeLiveService VoiceTurnLiveService
	if len(nativeLiveServices) > 0 {
		nativeLiveService = nativeLiveServices[0]
	}
	handler := NewWithVoice(
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		verifier,
		&fakeLimiter{},
		&fakeEvaluator{},
		&fakeStore{},
		time.Second,
		4*1024,
		VoiceOptions{
			Service:                 service,
			NativeLiveService:       nativeLiveService,
			RateLimiter:             uidLimiter,
			AppRateLimiter:          appLimiter,
			LiveLeaseManager:        leaseManager,
			LiveHandshakeGate:       handshakeGate,
			CoachStateValidator:     &liveCoachStateValidator{},
			RequestTimeout:          2 * time.Second,
			MaxRequestBytes:         13 * 1024 * 1024,
			livePipelineJoinTimeout: pipelineJoinTimeout,
			liveNativeReadyTimeout:  nativeReadyTimeout,
		},
	)
	if handlerReturned != nil {
		inner := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inner.ServeHTTP(w, r)
			select {
			case handlerReturned <- struct{}{}:
			default:
			}
		})
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func dialVoiceLive(
	ctx context.Context,
	serverURL string,
	header http.Header,
) (*websocket.Conn, *http.Response, error) {
	if header == nil {
		header = make(http.Header)
	}
	if header.Get("Origin") == "" {
		header.Set("Origin", allowedWebOrigin)
	}
	return websocket.Dial(
		ctx,
		"ws"+strings.TrimPrefix(serverURL, "http")+voiceLivePath,
		&websocket.DialOptions{HTTPHeader: header},
	)
}

func writeVoiceLiveStart(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	writeVoiceLiveStartMode(t, ctx, conn, VoiceTurnIntentional)
}

func writeVoiceLiveStartMode(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	mode VoiceTurnMode,
) {
	t.Helper()
	frame := voiceLiveStartFrame{
		Type:          "start",
		Version:       voiceLiveVersion,
		IDToken:       liveTestIDToken,
		AppCheckToken: liveTestAppCheckToken,
		SessionState:  "",
		TurnMode:      mode,
		SampleRateHz:  voiceLiveSampleRateHz,
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

func writeVoiceLiveStrictStart(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	frame := voiceLiveStartFrame{
		Type:                    "start",
		Version:                 voiceLiveVersion,
		IDToken:                 liveTestIDToken,
		AppCheckToken:           liveTestAppCheckToken,
		TurnMode:                VoiceTurnIntentional,
		StrictCloudMinimization: true,
		SampleRateHz:            voiceLiveSampleRateHz,
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

func writeVoiceLiveNativeStart(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	writeVoiceLiveNativeStartWithCoachControl(t, ctx, conn, true)
}

func writeVoiceLiveLegacyNativeStart(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	writeVoiceLiveNativeStartWithCoachControl(t, ctx, conn, false)
}

func writeVoiceLiveNativeStartWithCoachControl(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	nativeCoachControl bool,
) {
	t.Helper()
	frame := voiceLiveStartFrame{
		Type:               "start",
		Version:            voiceLiveVersion,
		IDToken:            liveTestIDToken,
		AppCheckToken:      liveTestAppCheckToken,
		TurnMode:           VoiceTurnIntentional,
		SampleRateHz:       voiceLiveSampleRateHz,
		NativeAudio:        true,
		NativeCoachControl: nativeCoachControl,
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

type strongReadyLiveTestService struct {
	started         chan struct{}
	allowReady      <-chan struct{}
	readyPublished  chan struct{}
	audioReceived   chan struct{}
	done            chan struct{}
	failBeforeReady error

	startOnce  sync.Once
	readyOnce  sync.Once
	audioOnce  sync.Once
	doneOnce   sync.Once
	mu         sync.Mutex
	readyCalls int
}

func (*strongReadyLiveTestService) Process(
	context.Context,
	string,
	VoiceTurnInput,
) (VoiceTurnResult, error) {
	return VoiceTurnResult{}, errors.New("buffered voice method called")
}

func (service *strongReadyLiveTestService) ProcessLive(
	ctx context.Context,
	_ string,
	input VoiceTurnInput,
	audio <-chan []byte,
	_ func([]byte) error,
) (VoiceTurnResult, error) {
	service.startOnce.Do(func() {
		if service.started != nil {
			close(service.started)
		}
	})
	defer service.doneOnce.Do(func() {
		if service.done != nil {
			close(service.done)
		}
	})
	if service.failBeforeReady != nil {
		return VoiceTurnResult{}, service.failBeforeReady
	}
	if service.allowReady != nil {
		select {
		case <-service.allowReady:
		case <-ctx.Done():
			return VoiceTurnResult{}, ctx.Err()
		}
	}
	if input.OnInputReady == nil {
		return VoiceTurnResult{}, errors.New("missing strong input-ready callback")
	}
	input.OnInputReady()
	service.mu.Lock()
	service.readyCalls++
	service.mu.Unlock()
	service.readyOnce.Do(func() {
		if service.readyPublished != nil {
			close(service.readyPublished)
		}
	})
	for {
		select {
		case <-ctx.Done():
			return VoiceTurnResult{}, ctx.Err()
		case _, open := <-audio:
			if !open {
				return VoiceTurnResult{
					StateToken:       "sealed-strong-ready-state",
					DetectedDomain:   "unknown",
					AssistanceTarget: "assistant",
					RespondentStage:  "none",
					CoachPhase:       "none",
					CoachAction:      "none",
					ResearchStatus:   "none",
					ResearchRecords:  []ResearchRecord{},
					Route:            "native_audio",
				}, nil
			}
			service.audioOnce.Do(func() {
				if service.audioReceived != nil {
					close(service.audioReceived)
				}
			})
		}
	}
}

type voiceLiveWireRead struct {
	messageType websocket.MessageType
	payload     []byte
	err         error
}

func TestVoiceLiveNativeReadyWaitsForProviderAndStartsPCMReaderAfterward(
	t *testing.T,
) {
	allowReady := make(chan struct{})
	service := &strongReadyLiveTestService{
		started:        make(chan struct{}),
		allowReady:     allowReady,
		readyPublished: make(chan struct{}),
		audioReceived:  make(chan struct{}),
		done:           make(chan struct{}),
	}
	server := newVoiceLiveControlledTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		guard.NewMemoryVoiceLiveLeaseManager(),
		NewVoiceLiveHandshakeGate(2),
		0,
		nil,
		service,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveNativeStart(t, ctx, conn)
	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("Native pipeline did not begin provider preparation")
	}

	wireRead := make(chan voiceLiveWireRead, 1)
	go func() {
		messageType, payload, readErr := conn.Read(ctx)
		wireRead <- voiceLiveWireRead{
			messageType: messageType,
			payload:     payload,
			err:         readErr,
		}
	}()
	select {
	case frame := <-wireRead:
		t.Fatalf("frame arrived before provider readiness: %+v", frame)
	case <-time.After(75 * time.Millisecond):
	}

	// Even a protocol-violating early client frame must remain at the socket
	// boundary until the provider proves this exact turn is input-ready.
	if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.audioReceived:
		t.Fatal("PCM reached the service before strong ready")
	case <-time.After(75 * time.Millisecond):
	}

	close(allowReady)
	select {
	case <-service.readyPublished:
	case <-time.After(time.Second):
		t.Fatal("provider did not publish input readiness")
	}
	var first voiceLiveWireRead
	select {
	case first = <-wireRead:
	case <-time.After(time.Second):
		t.Fatal("browser did not receive ready after provider completion")
	}
	if first.err != nil || first.messageType != websocket.MessageText {
		t.Fatalf("ready wire frame=%+v", first)
	}
	var ready map[string]any
	if err := json.Unmarshal(first.payload, &ready); err != nil {
		t.Fatal(err)
	}
	if len(ready) != 2 || ready["type"] != "ready" ||
		ready["version"] != float64(voiceLiveVersion) {
		t.Fatalf("ready=%#v", ready)
	}
	select {
	case <-service.audioReceived:
	case <-time.After(time.Second):
		t.Fatal("PCM reader did not start after strong ready")
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{
		Type: "commit", Version: voiceLiveVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}
	if final := readVoiceLiveJSON(t, ctx, conn); final["type"] != "final" {
		t.Fatalf("final=%#v", final)
	}
	service.mu.Lock()
	readyCalls := service.readyCalls
	service.mu.Unlock()
	if readyCalls != 1 {
		t.Fatalf("input-ready callback calls=%d, want 1", readyCalls)
	}
}

func TestVoiceLiveNativeFailureBeforeInputReadySendsNoReadyAndReleasesLease(
	t *testing.T,
) {
	service := &strongReadyLiveTestService{
		started:         make(chan struct{}),
		done:            make(chan struct{}),
		failBeforeReady: errors.New("provider setup failed"),
	}
	lease := &liveTestLease{}
	leaseManager := &liveTestLeaseManager{lease: lease}
	handlerReturned := make(chan struct{}, 1)
	server := newVoiceLiveControlledTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		leaseManager,
		NewVoiceLiveHandshakeGate(2),
		0,
		handlerReturned,
		service,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveNativeStart(t, ctx, conn)
	terminal := readVoiceLiveJSON(t, ctx, conn)
	if terminal["type"] != "error" ||
		terminal["code"] != voiceLiveCodeAPIUnavailable {
		t.Fatalf("first frame=%#v, want provider error without ready", terminal)
	}
	if _, _, readErr := conn.Read(ctx); websocket.CloseStatus(readErr) !=
		websocket.StatusInternalError {
		t.Fatalf(
			"pre-ready failure close status=%v err=%v",
			websocket.CloseStatus(readErr),
			readErr,
		)
	}
	select {
	case <-handlerReturned:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish after pre-ready provider failure")
	}
	lease.mu.Lock()
	releaseCalls := lease.releaseCalls
	lease.mu.Unlock()
	service.mu.Lock()
	readyCalls := service.readyCalls
	service.mu.Unlock()
	if readyCalls != 0 || releaseCalls != 1 {
		t.Fatalf("ready calls=%d lease releases=%d", readyCalls, releaseCalls)
	}
}

func TestVoiceLiveNativeReadyDeadlineCancelsProviderAndReleasesLease(
	t *testing.T,
) {
	service := &strongReadyLiveTestService{
		started:    make(chan struct{}),
		allowReady: make(chan struct{}),
		done:       make(chan struct{}),
	}
	lease := &liveTestLease{}
	handlerReturned := make(chan struct{}, 1)
	server := newVoiceLiveControlledTestServerWithNativeReadyTimeout(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		&liveTestLeaseManager{lease: lease},
		NewVoiceLiveHandshakeGate(2),
		500*time.Millisecond,
		50*time.Millisecond,
		handlerReturned,
		service,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveNativeStart(t, ctx, conn)
	terminal := readVoiceLiveJSON(t, ctx, conn)
	if terminal["type"] != "error" ||
		terminal["code"] != voiceLiveCodeAPIUnavailable {
		t.Fatalf("deadline frame=%#v", terminal)
	}
	select {
	case <-service.done:
	case <-time.After(time.Second):
		t.Fatal("provider pipeline survived the strong-ready deadline")
	}
	select {
	case <-handlerReturned:
	case <-time.After(time.Second):
		t.Fatal("handler retained the timed-out Native turn")
	}
	lease.mu.Lock()
	releaseCalls := lease.releaseCalls
	lease.mu.Unlock()
	if releaseCalls != 1 {
		t.Fatalf("lease releases=%d, want 1", releaseCalls)
	}
}

func TestVoiceLiveNativeRequestFailsClosedWithoutNativeService(t *testing.T) {
	legacy := &liveTestVoiceService{}
	leaseManager := &liveTestLeaseManager{}
	server := newVoiceLiveControlledTestServer(
		t,
		legacy,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		leaseManager,
		NewVoiceLiveHandshakeGate(2),
		0,
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveNativeStart(t, ctx, conn)
	terminal := readVoiceLiveJSON(t, ctx, conn)
	if terminal["type"] != "error" ||
		terminal["code"] != voiceLiveCodeAPIUnavailable {
		t.Fatalf("unconfigured Native frame=%#v", terminal)
	}
	legacy.mu.Lock()
	processLiveCalls := legacy.processLiveCalls
	legacy.mu.Unlock()
	leaseManager.mu.Lock()
	acquireCalls := leaseManager.acquireCalls
	leaseManager.mu.Unlock()
	if processLiveCalls != 0 || acquireCalls != 0 {
		t.Fatalf(
			"legacy live calls=%d lease acquires=%d, want both 0",
			processLiveCalls,
			acquireCalls,
		)
	}
}

func TestVoiceLiveNativeLateReadyAfterDisconnectDoesNotLeakPipelineOrLease(
	t *testing.T,
) {
	allowReady := make(chan struct{})
	service := &strongReadyLiveTestService{
		started:        make(chan struct{}),
		allowReady:     allowReady,
		readyPublished: make(chan struct{}),
		done:           make(chan struct{}),
	}
	lease := &liveTestLease{}
	leaseManager := &liveTestLeaseManager{lease: lease}
	handlerReturned := make(chan struct{}, 1)
	server := newVoiceLiveControlledTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		leaseManager,
		NewVoiceLiveHandshakeGate(2),
		500*time.Millisecond,
		handlerReturned,
		service,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeVoiceLiveNativeStart(t, ctx, conn)
	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("Native pipeline did not start")
	}
	conn.CloseNow()
	close(allowReady)
	select {
	case <-service.done:
	case <-time.After(time.Second):
		t.Fatal("late-ready Native pipeline did not stop")
	}
	select {
	case <-handlerReturned:
	case <-time.After(time.Second):
		t.Fatal("handler retained the disconnected Native turn")
	}
	lease.mu.Lock()
	releaseCalls := lease.releaseCalls
	lease.mu.Unlock()
	if releaseCalls != 1 {
		t.Fatalf("lease releases=%d, want 1", releaseCalls)
	}
}

func TestVoiceLiveRoutesExplicitNativeAudioToNativeService(t *testing.T) {
	t.Parallel()
	legacy := &liveTestVoiceService{}
	native := &liveTestVoiceService{result: VoiceTurnResult{
		StateToken:       "sealed-native-state",
		DetectedDomain:   "unknown",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		CoachPhase:       "none",
		CoachAction:      "none",
		ResearchStatus:   "none",
		ResearchRecords:  []ResearchRecord{},
		Route:            "native_audio",
	}}
	server := newVoiceLiveControlledTestServer(
		t,
		legacy,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		guard.NewMemoryVoiceLiveLeaseManager(),
		NewVoiceLiveHandshakeGate(2),
		0,
		nil,
		native,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLiveNativeStart(t, ctx, conn)
	if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
		t.Fatalf("ready=%#v", ready)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
		t.Fatal(err)
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{Type: "commit", Version: voiceLiveVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}
	if final := readVoiceLiveJSON(t, ctx, conn); final["type"] != "final" {
		t.Fatalf("final=%#v", final)
	}

	legacy.mu.Lock()
	legacyCalls := legacy.processLiveCalls
	legacy.mu.Unlock()
	native.mu.Lock()
	nativeCalls := native.processLiveCalls
	nativeInput := native.input
	native.mu.Unlock()
	if legacyCalls != 0 || nativeCalls != 1 || !nativeInput.NativeAudio {
		t.Fatalf("legacy calls=%d native calls=%d input=%+v", legacyCalls, nativeCalls, nativeInput)
	}
}

func TestVoiceLiveCoachControlPrecedesNativeAudioAndCompletesNormally(t *testing.T) {
	t.Parallel()
	legacy := &liveTestVoiceService{}
	native := &liveControlTestService{liveTestVoiceService: liveTestVoiceService{
		output: [][]byte{{4, 0, 5, 0}},
		result: VoiceTurnResult{
			StateToken:       "signed-native-coach-state",
			DetectedDomain:   "daily",
			AssistanceTarget: "respondent",
			RespondentStage:  "awaiting_answer",
			CoachPhase:       "awaiting_answer",
			CoachAction:      "elicit",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "native-respondent-coach",
			Caption:          "まず、一番伝えたいことは何ですか？",
		},
	}}
	server := newVoiceLiveControlledTestServer(
		t,
		legacy,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		guard.NewMemoryVoiceLiveLeaseManager(),
		NewVoiceLiveHandshakeGate(2),
		0,
		nil,
		native,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLiveNativeStart(t, ctx, conn)
	if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
		t.Fatalf("ready=%#v", ready)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
		t.Fatal(err)
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{
		Type: "commit", Version: voiceLiveVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}

	messageType, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("first response type=%v, want coach text", messageType)
	}
	var coach map[string]any
	if err := json.Unmarshal(payload, &coach); err != nil {
		t.Fatal(err)
	}
	if len(coach) != 9 || coach["type"] != "coach" ||
		coach["version"] != float64(voiceLiveVersion) ||
		coach["active"] != true ||
		coach["sessionState"] != "signed-native-coach-state" ||
		coach["route"] != VoiceNativeRespondentCoachRoute ||
		coach["assistanceTarget"] != "respondent" ||
		coach["respondentStage"] != "awaiting_answer" ||
		coach["coachPhase"] != "awaiting_answer" ||
		coach["coachAction"] != "elicit" {
		t.Fatalf("coach=%#v", coach)
	}

	messageType, payload, err = conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageBinary ||
		!bytes.Equal(payload, []byte{4, 0, 5, 0}) {
		t.Fatalf("audio type=%v payload=%v", messageType, payload)
	}
	final := readVoiceLiveJSON(t, ctx, conn)
	if final["type"] != "final" {
		t.Fatalf("final=%#v", final)
	}
	finalResult, ok := final["result"].(map[string]any)
	if !ok || finalResult["sessionState"] != coach["sessionState"] {
		t.Fatalf("coach=%#v final=%#v", coach, final)
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
	}

	native.mu.Lock()
	controlCalls := native.controlCalls
	nativeCalls := native.processLiveCalls
	native.mu.Unlock()
	if controlCalls != 1 || nativeCalls != 1 {
		t.Fatalf("control calls=%d native calls=%d", controlCalls, nativeCalls)
	}
}

func TestVoiceLiveNativeCoachControlNegotiatesOldAndNewClients(t *testing.T) {
	legacy := &liveTestVoiceService{}
	native := &liveControlTestService{
		liveTestVoiceService: liveTestVoiceService{
			output: [][]byte{{4, 0, 5, 0}},
			result: VoiceTurnResult{
				StateToken:       "signed-native-coach-state",
				DetectedDomain:   "daily",
				AssistanceTarget: "respondent",
				RespondentStage:  "awaiting_answer",
				CoachPhase:       "awaiting_answer",
				CoachAction:      "elicit",
				ResearchStatus:   "none",
				ResearchRecords:  []ResearchRecord{},
				Route:            "native-respondent-coach",
				Caption:          "one bounded question",
			},
		},
		withoutControlErr: ErrVoiceNativeFallback,
	}
	handlerReturned := make(chan struct{}, 2)
	server := newVoiceLiveControlledTestServer(
		t,
		legacy,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		guard.NewMemoryVoiceLiveLeaseManager(),
		NewVoiceLiveHandshakeGate(2),
		0,
		handlerReturned,
		native,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	commit := func(conn *websocket.Conn) {
		t.Helper()
		if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
			t.Fatalf("ready=%#v", ready)
		}
		if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(voiceLiveCommitFrame{
			Type: "commit", Version: voiceLiveVersion,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
			t.Fatal(err)
		}
	}

	oldClient, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeVoiceLiveLegacyNativeStart(t, ctx, oldClient)
	commit(oldClient)
	terminal := readVoiceLiveJSON(t, ctx, oldClient)
	if terminal["type"] != "error" || terminal["code"] != voiceLiveCodeNativeFallback {
		t.Fatalf("old client terminal=%#v", terminal)
	}
	if _, _, err := oldClient.Read(ctx); websocket.CloseStatus(err) != websocket.StatusInternalError {
		t.Fatalf("old client close status=%v err=%v", websocket.CloseStatus(err), err)
	}
	select {
	case <-handlerReturned:
	case <-ctx.Done():
		t.Fatal("old client handler did not release its live lease")
	}
	native.mu.Lock()
	oldBasicCalls := native.basicCalls
	oldEndpointCalls := native.endpointCalls
	oldControlCalls := native.controlCalls
	native.mu.Unlock()
	if oldBasicCalls != 0 || oldEndpointCalls != 1 || oldControlCalls != 0 {
		t.Fatalf(
			"old client routes: basic=%d endpoint=%d control=%d, want 0/1/0",
			oldBasicCalls,
			oldEndpointCalls,
			oldControlCalls,
		)
	}

	newClient, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer newClient.CloseNow()
	writeVoiceLiveNativeStart(t, ctx, newClient)
	commit(newClient)
	coach := readVoiceLiveJSON(t, ctx, newClient)
	if coach["type"] != "coach" || coach["sessionState"] != "signed-native-coach-state" {
		t.Fatalf("new client coach=%#v", coach)
	}
	messageType, audio, err := newClient.Read(ctx)
	if err != nil || messageType != websocket.MessageBinary ||
		!bytes.Equal(audio, []byte{4, 0, 5, 0}) {
		t.Fatalf("new client audio type=%v payload=%v err=%v", messageType, audio, err)
	}
	if final := readVoiceLiveJSON(t, ctx, newClient); final["type"] != "final" {
		t.Fatalf("new client final=%#v", final)
	}

	native.mu.Lock()
	basicCalls := native.basicCalls
	endpointCalls := native.endpointCalls
	controlCalls := native.controlCalls
	nativeCalls := native.processLiveCalls
	native.mu.Unlock()
	if basicCalls != 0 || endpointCalls != 1 || controlCalls != 1 || nativeCalls != 2 {
		t.Fatalf(
			"all client routes: basic=%d endpoint=%d control=%d native=%d, want 0/1/1/2",
			basicCalls,
			endpointCalls,
			controlCalls,
			nativeCalls,
		)
	}
}

func TestVoiceLiveRejectsInvalidCoachCheckpointBeforeControlOrAudio(t *testing.T) {
	for name, invalidState := range map[string]string{
		"empty":              "",
		"oversize":           strings.Repeat("x", maxStateBytes+1),
		"bad signature":      "cryptographically-invalid",
		"different UID bind": "signed-for-other-user",
	} {
		t.Run(name, func(t *testing.T) {
			controlState := invalidState
			native := &liveControlTestService{
				liveTestVoiceService: liveTestVoiceService{
					output: [][]byte{{4, 0, 5, 0}},
					result: validLiveCoachResult(),
				},
				controlState:       &controlState,
				ignoreControlError: true,
			}
			server := newVoiceLiveControlledTestServer(
				t,
				&liveTestVoiceService{},
				&liveTestVerifier{},
				&fakeLimiter{},
				&fakeLimiter{wantKey: "app:app-123"},
				guard.NewMemoryVoiceLiveLeaseManager(),
				NewVoiceLiveHandshakeGate(2),
				0,
				nil,
				native,
			)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			conn, _, err := dialVoiceLive(ctx, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()

			writeVoiceLiveNativeStart(t, ctx, conn)
			if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
				t.Fatalf("ready=%#v", ready)
			}
			if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
				t.Fatal(err)
			}
			commit, err := json.Marshal(voiceLiveCommitFrame{
				Type: "commit", Version: voiceLiveVersion,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
				t.Fatal(err)
			}

			// The first post-ready frame is the terminal error. Receiving either a
			// coach JSON frame or binary audio here would expose an uncheckpointed
			// turn, even though this fake deliberately ignores the callback error.
			terminal := readVoiceLiveJSON(t, ctx, conn)
			if terminal["type"] != "error" ||
				terminal["code"] != voiceLiveCodeAPIUnavailable {
				t.Fatalf("terminal=%#v", terminal)
			}
		})
	}
}

func TestVoiceLiveRejectsInvalidPreparedCoachStateBeforeControlOrAudio(
	t *testing.T,
) {
	for name, invalidState := range map[string]string{
		"empty":              "",
		"oversize":           strings.Repeat("x", maxStateBytes+1),
		"bad signature":      "cryptographically-invalid",
		"different UID bind": "signed-for-other-user",
	} {
		t.Run(name, func(t *testing.T) {
			prepared := invalidState
			native := &liveControlTestService{
				liveTestVoiceService: liveTestVoiceService{
					output: [][]byte{{4, 0, 5, 0}},
					result: validLiveCoachResult(),
				},
				controlPrevious:    &prepared,
				ignoreControlError: true,
			}
			_, terminal := runRejectedVoiceLiveCoachTurn(t, native, false)
			if terminal["code"] != voiceLiveCodeAPIUnavailable {
				t.Fatalf("terminal=%#v", terminal)
			}
		})
	}
}

func TestVoiceLiveRejectsCoachCheckpointFinalStateMismatch(t *testing.T) {
	checkpoint := "signed-native-coach-state"
	result := validLiveCoachResult()
	result.StateToken = "different-final-state"
	native := &liveControlTestService{
		liveTestVoiceService: liveTestVoiceService{
			result: result,
		},
		controlState: &checkpoint,
	}
	server := newVoiceLiveControlledTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		guard.NewMemoryVoiceLiveLeaseManager(),
		NewVoiceLiveHandshakeGate(2),
		0,
		nil,
		native,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLiveNativeStart(t, ctx, conn)
	if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
		t.Fatalf("ready=%#v", ready)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
		t.Fatal(err)
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{
		Type: "commit", Version: voiceLiveVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}
	coach := readVoiceLiveJSON(t, ctx, conn)
	if coach["type"] != "coach" || coach["sessionState"] != checkpoint {
		t.Fatalf("coach=%#v", coach)
	}
	terminal := readVoiceLiveJSON(t, ctx, conn)
	if terminal["type"] != "error" ||
		terminal["code"] != voiceLiveCodeAPIUnavailable {
		t.Fatalf("terminal=%#v", terminal)
	}
}

func TestValidVoiceLiveCoachSessionState(t *testing.T) {
	tests := map[string]struct {
		value string
		want  bool
	}{
		"valid":         {value: "opaque.signed-state_123", want: true},
		"empty":         {value: ""},
		"oversize":      {value: strings.Repeat("x", maxStateBytes+1)},
		"trim mismatch": {value: " signed-state"},
		"control":       {value: "signed\x00state"},
		"invalid UTF-8": {value: string([]byte{0xff})},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := validVoiceLiveCoachSessionState(test.value); got != test.want {
				t.Fatalf("validVoiceLiveCoachSessionState()=%v, want %v", got, test.want)
			}
		})
	}
}

func TestVoiceLiveRejectsCoachCheckpointFinalMetadataMismatch(t *testing.T) {
	tests := map[string]func(*VoiceRespondentCheckpoint){
		"respondent stage": func(checkpoint *VoiceRespondentCheckpoint) {
			checkpoint.RespondentStage = "restructure"
		},
		"phase and action": func(checkpoint *VoiceRespondentCheckpoint) {
			checkpoint.CoachPhase = "complete"
			checkpoint.CoachAction = "complete"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			result := validLiveCoachResult()
			checkpoint, err := NewVoiceRespondentCheckpoint(result)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&checkpoint)
			native := &liveControlTestService{
				liveTestVoiceService: liveTestVoiceService{result: result},
				controlCheckpoint:    &checkpoint,
			}
			coach, terminal := runRejectedVoiceLiveCoachTurn(t, native, true)
			if coach["sessionState"] != checkpoint.SessionState ||
				terminal["code"] != voiceLiveCodeAPIUnavailable {
				t.Fatalf("coach=%#v terminal=%#v", coach, terminal)
			}
		})
	}
}

func TestVoiceLiveDuplicateCoachCheckpointFailsClosedBeforeAudio(t *testing.T) {
	native := &liveControlTestService{
		liveTestVoiceService: liveTestVoiceService{
			result: validLiveCoachResult(),
			output: [][]byte{{4, 0, 5, 0}},
		},
		controlPublishes:   2,
		ignoreControlError: true,
	}
	coach, terminal := runRejectedVoiceLiveCoachTurn(t, native, true)
	if coach["type"] != "coach" ||
		terminal["code"] != voiceLiveCodeAPIUnavailable {
		t.Fatalf("coach=%#v terminal=%#v", coach, terminal)
	}
	native.mu.Lock()
	controlCalls := native.controlCalls
	native.mu.Unlock()
	if controlCalls != 2 {
		t.Fatalf("control calls=%d, want 2", controlCalls)
	}
}

func runRejectedVoiceLiveCoachTurn(
	t *testing.T,
	native *liveControlTestService,
	wantCoach bool,
) (map[string]any, map[string]any) {
	t.Helper()
	server := newVoiceLiveControlledTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		guard.NewMemoryVoiceLiveLeaseManager(),
		NewVoiceLiveHandshakeGate(2),
		0,
		nil,
		native,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveNativeStart(t, ctx, conn)
	if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
		t.Fatalf("ready=%#v", ready)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
		t.Fatal(err)
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{
		Type: "commit", Version: voiceLiveVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}
	var coach map[string]any
	if wantCoach {
		coach = readVoiceLiveJSON(t, ctx, conn)
		if coach["type"] != "coach" {
			t.Fatalf("coach=%#v", coach)
		}
	}
	terminal := readVoiceLiveJSON(t, ctx, conn)
	if terminal["type"] != "error" {
		t.Fatalf("terminal=%#v", terminal)
	}
	return coach, terminal
}

func TestVoiceRespondentCheckpointAcceptsEveryFiniteLegalResultShape(
	t *testing.T,
) {
	stages := []string{"awaiting_answer", "restructure"}
	pairs := []struct {
		phase  string
		action string
	}{
		{phase: "awaiting_answer", action: "elicit"},
		{phase: "awaiting_restatement", action: "restate"},
		{phase: "expanding", action: "expand"},
		{phase: "complete", action: "complete"},
		{phase: "blocked", action: "retry"},
		{phase: "blocked", action: "release"},
	}
	for _, stage := range stages {
		for _, pair := range pairs {
			name := stage + "/" + pair.phase + "/" + pair.action
			t.Run(name, func(t *testing.T) {
				result := validLiveCoachResult()
				result.RespondentStage = stage
				result.CoachPhase = pair.phase
				result.CoachAction = pair.action
				checkpoint, err := NewVoiceRespondentCheckpoint(result)
				if err != nil {
					t.Fatal(err)
				}
				if !checkpoint.MatchesResult(result) {
					t.Fatalf("checkpoint=%+v result=%+v", checkpoint, result)
				}
			})
		}
	}
}

func TestVoiceRespondentCheckpointRejectsNonFiniteOrNonRespondentShapes(
	t *testing.T,
) {
	tests := map[string]func(*VoiceTurnResult){
		"empty state": func(result *VoiceTurnResult) { result.StateToken = "" },
		"wrong route": func(result *VoiceTurnResult) { result.Route = "caption-handoff" },
		"assistant": func(result *VoiceTurnResult) {
			result.AssistanceTarget = "assistant"
		},
		"unknown stage": func(result *VoiceTurnResult) {
			result.RespondentStage = "drafting"
		},
		"unknown phase": func(result *VoiceTurnResult) {
			result.CoachPhase = "future"
		},
		"mismatched action": func(result *VoiceTurnResult) {
			result.CoachAction = "complete"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			result := validLiveCoachResult()
			mutate(&result)
			if _, err := NewVoiceRespondentCheckpoint(result); err == nil {
				t.Fatalf("accepted invalid result: %+v", result)
			}
		})
	}
}

func TestVoiceLiveReturnsNativeFallbackOnlyBeforeAnyOutputAudio(t *testing.T) {
	t.Parallel()
	legacy := &liveTestVoiceService{}
	native := &liveControlTestService{
		liveTestVoiceService: liveTestVoiceService{},
		prepareErr:           ErrVoiceNativeFallback,
	}
	server := newVoiceLiveControlledTestServer(
		t,
		legacy,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		guard.NewMemoryVoiceLiveLeaseManager(),
		NewVoiceLiveHandshakeGate(2),
		0,
		nil,
		native,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLiveNativeStart(t, ctx, conn)
	if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
		t.Fatalf("ready=%#v", ready)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
		t.Fatal(err)
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{Type: "commit", Version: voiceLiveVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}
	terminal := readVoiceLiveJSON(t, ctx, conn)
	if terminal["type"] != "error" || terminal["code"] != voiceLiveCodeNativeFallback {
		t.Fatalf("terminal=%#v", terminal)
	}

	legacy.mu.Lock()
	legacyCalls := legacy.processLiveCalls
	legacy.mu.Unlock()
	native.mu.Lock()
	controlCalls := native.controlCalls
	nativeCalls := native.processLiveCalls
	native.mu.Unlock()
	if legacyCalls != 0 || controlCalls != 0 || nativeCalls != 1 {
		t.Fatalf(
			"legacy calls=%d control calls=%d native calls=%d; prep failure must release nothing",
			legacyCalls,
			controlCalls,
			nativeCalls,
		)
	}
}

func TestStrictVoiceLiveNeverReleasesAudioBeforeResultValidation(t *testing.T) {
	t.Parallel()
	service := &liveTestVoiceService{
		output: [][]byte{{4, 0, 5, 0}},
		result: VoiceTurnResult{
			DetectedDomain:   "unknown",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			CoachPhase:       "none",
			CoachAction:      "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			PrivacyStatus:    "blocked",
			Route:            "strict-privacy-blocked",
		},
	}
	server := newVoiceLiveTestServer(
		t,
		service,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLiveStrictStart(t, ctx, conn)
	if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
		t.Fatalf("ready=%#v", ready)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
		t.Fatal(err)
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{Type: "commit", Version: voiceLiveVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}
	messageType, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("strict live leaked binary audio before validation: type=%v bytes=%d", messageType, len(payload))
	}
	var frame map[string]any
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatal(err)
	}
	if frame["type"] != "error" || frame["code"] != voiceLiveCodeResponseInvalid {
		t.Fatalf("frame=%#v", frame)
	}
}

func TestVoiceLiveForegroundKeepsAmbientAuthorityAndExpectsReply(t *testing.T) {
	t.Parallel()
	service := &liveTestVoiceService{
		result: VoiceTurnResult{
			StateToken:       "sealed-foreground-state",
			DetectedDomain:   "daily",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			CoachPhase:       "none",
			CoachAction:      "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "foreground-test",
		},
	}
	server := newVoiceLiveTestServer(
		t,
		service,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLiveStartMode(t, ctx, conn, VoiceTurnForeground)
	ready := readVoiceLiveJSON(t, ctx, conn)
	if ready["type"] != "ready" {
		t.Fatalf("ready=%#v", ready)
	}
	if err := conn.Write(
		ctx,
		websocket.MessageBinary,
		liveTestPCMFrame(),
	); err != nil {
		t.Fatal(err)
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{
		Type:    "commit",
		Version: voiceLiveVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}
	final := readVoiceLiveJSON(t, ctx, conn)
	if final["type"] != "final" {
		t.Fatalf("final=%#v", final)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if service.input.TurnMode != VoiceTurnForeground ||
		!service.input.Ambient ||
		!service.input.Foreground {
		t.Fatalf("foreground authority mapping=%+v", service.input)
	}
}

func readVoiceLiveJSON(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
) map[string]any {
	t.Helper()
	messageType, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("message type=%v want text", messageType)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestVoiceLiveDeadlinesExtendOnlyTheValidatedLiveRoute(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2026, time.August, 1, 0, 7, 0, 0, time.UTC)
	writer := &liveDeadlineResponseWriter{}
	if err := setVoiceLiveDeadlines(writer, deadline); err != nil {
		t.Fatal(err)
	}
	if !writer.readDeadline.Equal(deadline) ||
		!writer.writeDeadline.Equal(deadline) {
		t.Fatalf(
			"live deadlines = read:%s write:%s; want %s",
			writer.readDeadline,
			writer.writeDeadline,
			deadline,
		)
	}
}

func TestVoiceLiveDeadlineFailureFailsClosedBeforeUpgrade(t *testing.T) {
	t.Parallel()
	leaseManager := &liveTestLeaseManager{}
	server := &Server{
		verifier: &liveTestVerifier{},
		voice: VoiceOptions{
			Service:           &liveTestVoiceService{},
			RateLimiter:       &fakeLimiter{},
			AppRateLimiter:    &fakeLimiter{wantKey: "app:app-123"},
			LiveLeaseManager:  leaseManager,
			LiveHandshakeGate: NewVoiceLiveHandshakeGate(1),
			RequestTimeout:    time.Second,
		},
	}
	request := httptest.NewRequest(http.MethodGet, voiceLivePath, nil)
	request.Header.Set("Origin", allowedWebOrigin)
	writer := &liveDeadlineResponseWriter{writeErr: errors.New("deadline unavailable")}

	server.voiceLive(writer, request)

	if writer.status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want %d", writer.status, http.StatusServiceUnavailable)
	}
	if writer.readDeadline.IsZero() || writer.writeDeadline.IsZero() {
		t.Fatal("both live deadline operations were not attempted")
	}
	leaseManager.mu.Lock()
	acquireCalls := leaseManager.acquireCalls
	leaseManager.mu.Unlock()
	if acquireCalls != 0 {
		t.Fatalf("lease acquisitions before failed upgrade = %d; want 0", acquireCalls)
	}
}

func TestVoiceLiveNilHandshakeGateFailsClosedBeforeUpgrade(t *testing.T) {
	t.Parallel()
	verifier := &liveTestVerifier{}
	uidLimiter := &fakeLimiter{}
	appLimiter := &fakeLimiter{wantKey: "app:app-123"}
	leaseManager := &liveTestLeaseManager{}
	handler := NewWithVoice(
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		verifier,
		&fakeLimiter{},
		&fakeEvaluator{},
		&fakeStore{},
		time.Second,
		4*1024,
		VoiceOptions{
			Service:          &liveTestVoiceService{},
			RateLimiter:      uidLimiter,
			AppRateLimiter:   appLimiter,
			LiveLeaseManager: leaseManager,
			RequestTimeout:   time.Second,
		},
	)
	request := httptest.NewRequest(http.MethodGet, voiceLivePath, nil)
	request.Header.Set("Origin", allowedWebOrigin)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want %d", response.Code, http.StatusServiceUnavailable)
	}
	verifier.mu.Lock()
	verifyCalls := verifier.calls
	verifier.mu.Unlock()
	leaseManager.mu.Lock()
	leaseCalls := leaseManager.acquireCalls
	leaseManager.mu.Unlock()
	if verifyCalls != 0 || uidLimiter.calls != 0 ||
		appLimiter.calls != 0 || leaseCalls != 0 {
		t.Fatalf(
			"nil-gate work verifier:%d uid:%d app:%d lease:%d; want zero",
			verifyCalls,
			uidLimiter.calls,
			appLimiter.calls,
			leaseCalls,
		)
	}
}

func TestVoiceLiveHandshakeGateRejectsBeforeUpgradeAndReleasesAfterAuth(
	t *testing.T,
) {
	t.Parallel()
	gate := NewVoiceLiveHandshakeGate(DefaultVoiceLiveHandshakeLimit)
	verifier := &liveTestVerifier{}
	uidLimiter := &fakeLimiter{}
	appLimiter := &fakeLimiter{wantKey: "app:app-123"}
	leaseManager := &liveTestLeaseManager{}
	service := &liveTestVoiceService{}
	server := newVoiceLiveControlledTestServer(
		t,
		service,
		verifier,
		uidLimiter,
		appLimiter,
		leaseManager,
		gate,
		0,
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	firstSlowConn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer firstSlowConn.CloseNow()
	secondSlowConn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer secondSlowConn.CloseNow()

	blockedConn, blockedResponse, blockedErr := dialVoiceLive(
		ctx,
		server.URL,
		nil,
	)
	if blockedErr == nil || blockedConn != nil || blockedResponse == nil ||
		blockedResponse.StatusCode != http.StatusTooManyRequests {
		t.Fatalf(
			"full gate conn=%v response=%v err=%v",
			blockedConn,
			blockedResponse,
			blockedErr,
		)
	}
	blockedResponse.Body.Close()
	verifier.mu.Lock()
	verifyCalls := verifier.calls
	verifier.mu.Unlock()
	leaseManager.mu.Lock()
	leaseCalls := leaseManager.acquireCalls
	leaseManager.mu.Unlock()
	service.mu.Lock()
	serviceCalls := service.processLiveCalls
	service.mu.Unlock()
	if verifyCalls != 0 || uidLimiter.calls != 0 || appLimiter.calls != 0 ||
		leaseCalls != 0 || serviceCalls != 0 {
		t.Fatalf(
			"full-gate work verifier:%d uid:%d app:%d lease:%d service:%d; want zero",
			verifyCalls,
			uidLimiter.calls,
			appLimiter.calls,
			leaseCalls,
			serviceCalls,
		)
	}
	firstSlowConn.CloseNow()

	var authenticatedConn *websocket.Conn
	deadline := time.Now().Add(time.Second)
	for authenticatedConn == nil && time.Now().Before(deadline) {
		candidate, response, dialErr := dialVoiceLive(ctx, server.URL, nil)
		if response != nil && dialErr != nil {
			response.Body.Close()
		}
		if dialErr == nil {
			authenticatedConn = candidate
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if authenticatedConn == nil {
		t.Fatal("gate slot was not released after the slow connection ended")
	}
	defer authenticatedConn.CloseNow()
	writeVoiceLiveStart(t, ctx, authenticatedConn)
	if ready := readVoiceLiveJSON(t, ctx, authenticatedConn); ready["type"] != "ready" {
		t.Fatalf("ready = %#v", ready)
	}

	// Authentication released the gate even though this long session remains.
	nextConn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatalf("authenticated session retained handshake gate: %v", err)
	}
	nextConn.CloseNow()
}

func TestVoiceLiveLeaseBackendFailureFailsClosedAfterQuota(t *testing.T) {
	t.Parallel()
	leaseManager := &liveTestLeaseManager{err: errors.New("Firestore unavailable")}
	uidLimiter := &fakeLimiter{}
	appLimiter := &fakeLimiter{wantKey: "app:app-123"}
	server := newVoiceLiveTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		uidLimiter,
		appLimiter,
		leaseManager,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveStart(t, ctx, conn)
	frame := readVoiceLiveJSON(t, ctx, conn)
	if frame["type"] != "error" ||
		frame["code"] != voiceLiveCodeAPIUnavailable {
		t.Fatalf("frame = %#v", frame)
	}
	leaseManager.mu.Lock()
	acquireCalls := leaseManager.acquireCalls
	leaseManager.mu.Unlock()
	if acquireCalls != 1 {
		t.Fatalf("lease acquisitions = %d; want 1", acquireCalls)
	}
	if uidLimiter.calls != 1 || appLimiter.calls != 1 {
		t.Fatalf(
			"quota calls before lease failure = uid:%d app:%d; want one each",
			uidLimiter.calls,
			appLimiter.calls,
		)
	}
}

func TestVoiceLiveUnauthenticatedStartDoesNotConsumeLeaseOrQuota(t *testing.T) {
	t.Parallel()
	leaseManager := &liveTestLeaseManager{}
	uidLimiter := &fakeLimiter{}
	appLimiter := &fakeLimiter{wantKey: "app:app-123"}
	server := newVoiceLiveTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{err: identity.ErrUnauthenticated},
		uidLimiter,
		appLimiter,
		leaseManager,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveStart(t, ctx, conn)
	frame := readVoiceLiveJSON(t, ctx, conn)
	if frame["type"] != "error" ||
		frame["code"] != voiceLiveCodeAuthenticationFailed {
		t.Fatalf("frame = %#v", frame)
	}
	leaseManager.mu.Lock()
	acquireCalls := leaseManager.acquireCalls
	leaseManager.mu.Unlock()
	if acquireCalls != 0 || uidLimiter.calls != 0 || appLimiter.calls != 0 {
		t.Fatalf(
			"unauthenticated guards = lease:%d uid:%d app:%d; want zero",
			acquireCalls,
			uidLimiter.calls,
			appLimiter.calls,
		)
	}
}

func TestVoiceLiveReleasesLeaseAfterConnectionEnds(t *testing.T) {
	t.Parallel()
	lease := &liveTestLease{}
	leaseManager := &liveTestLeaseManager{lease: lease}
	server := newVoiceLiveTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		leaseManager,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeVoiceLiveStart(t, ctx, conn)
	ready := readVoiceLiveJSON(t, ctx, conn)
	if ready["type"] != "ready" {
		t.Fatalf("ready = %#v", ready)
	}
	conn.CloseNow()

	deadline := time.Now().Add(2 * time.Second)
	for {
		lease.mu.Lock()
		releaseCalls := lease.releaseCalls
		lease.mu.Unlock()
		if releaseCalls == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("release calls = %d; want 1", releaseCalls)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestVoiceLiveLeaseReleasesImmediatelyBeforePipelineStarts(t *testing.T) {
	t.Parallel()
	lease := &liveTestLease{}
	ctx, cancel := context.WithCancel(context.Background())
	server := &Server{
		logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		voice: VoiceOptions{
			livePipelineJoinTimeout: 20 * time.Millisecond,
		},
	}

	server.finishVoiceLiveLease("request-before-worker", cancel, lease, nil)

	select {
	case <-ctx.Done():
	default:
		t.Fatal("live context was not canceled")
	}
	lease.mu.Lock()
	releaseCalls := lease.releaseCalls
	lease.mu.Unlock()
	if releaseCalls != 1 {
		t.Fatalf("release calls = %d; want 1", releaseCalls)
	}
}

func TestVoiceLiveDisconnectWaitsForPipelineBeforeLeaseRelease(t *testing.T) {
	t.Parallel()
	allowReturn := make(chan struct{})
	service := &liveLifecycleTestService{
		started:     make(chan struct{}),
		canceled:    make(chan struct{}),
		allowReturn: allowReturn,
		done:        make(chan struct{}),
	}
	lease := &liveTestLease{}
	leaseManager := &liveTestLeaseManager{lease: lease}
	handlerReturned := make(chan struct{}, 1)
	server := newVoiceLiveControlledTestServer(
		t,
		service,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		leaseManager,
		NewVoiceLiveHandshakeGate(1),
		500*time.Millisecond,
		handlerReturned,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeVoiceLiveStart(t, ctx, conn)
	if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
		t.Fatalf("ready = %#v", ready)
	}
	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not start")
	}
	conn.CloseNow()
	select {
	case <-service.canceled:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not observe cancellation")
	}
	select {
	case <-handlerReturned:
		t.Fatal("handler returned before the pipeline stopped")
	case <-time.After(40 * time.Millisecond):
	}
	lease.mu.Lock()
	releaseCalls := lease.releaseCalls
	lease.mu.Unlock()
	if releaseCalls != 0 {
		t.Fatalf("lease released while worker was running: %d", releaseCalls)
	}

	close(allowReturn)
	select {
	case <-service.done:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not stop")
	}
	select {
	case <-handlerReturned:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish after pipeline stopped")
	}
	lease.mu.Lock()
	releaseCalls = lease.releaseCalls
	lease.mu.Unlock()
	if releaseCalls != 1 {
		t.Fatalf("release calls after worker exit = %d; want 1", releaseCalls)
	}
}

func TestVoiceLivePipelineShutdownTimeoutIsBoundedAndKeepsLease(t *testing.T) {
	t.Parallel()
	allowReturn := make(chan struct{})
	service := &liveLifecycleTestService{
		started:     make(chan struct{}),
		canceled:    make(chan struct{}),
		allowReturn: allowReturn,
		done:        make(chan struct{}),
	}
	lease := &liveTestLease{}
	leaseManager := &liveTestLeaseManager{lease: lease}
	handlerReturned := make(chan struct{}, 1)
	server := newVoiceLiveControlledTestServer(
		t,
		service,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
		leaseManager,
		NewVoiceLiveHandshakeGate(1),
		50*time.Millisecond,
		handlerReturned,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeVoiceLiveStart(t, ctx, conn)
	if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
		t.Fatalf("ready = %#v", ready)
	}
	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not start")
	}
	closedAt := time.Now()
	conn.CloseNow()
	select {
	case <-service.canceled:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not observe cancellation")
	}
	select {
	case <-handlerReturned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pipeline shutdown wait was not bounded")
	}
	if elapsed := time.Since(closedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("handler shutdown took %s; want at most 500ms", elapsed)
	}
	lease.mu.Lock()
	releaseCalls := lease.releaseCalls
	lease.mu.Unlock()
	if releaseCalls != 0 {
		t.Fatalf("timed-out worker released lease: %d", releaseCalls)
	}

	close(allowReturn)
	select {
	case <-service.done:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not stop after test release")
	}
	time.Sleep(20 * time.Millisecond)
	lease.mu.Lock()
	releaseCalls = lease.releaseCalls
	lease.mu.Unlock()
	if releaseCalls != 0 {
		t.Fatalf("expired cleanup later released lease: %d", releaseCalls)
	}
}

func TestVoiceLiveAuthenticatesThenStreamsPCMAndFinalInOrder(t *testing.T) {
	t.Parallel()
	verifier := &liveTestVerifier{}
	service := &liveTestVoiceService{
		output: [][]byte{{4, 0, 5, 0}, {6, 0}},
		result: VoiceTurnResult{
			Caption:          "Aです。理由はBです。",
			StateToken:       "sealed-final-state",
			DetectedDomain:   "daily",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			CoachPhase:       "none",
			CoachAction:      "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "fast",
			LiveTimings: VoiceLiveTimings{
				STTFirstInterimMS: 1,
				STTFinalMS:        2,
				ConversationMS:    3,
				TTSFirstChunkMS:   4,
			},
		},
	}
	uidLimiter := &fakeLimiter{}
	appLimiter := &fakeLimiter{wantKey: "app:app-123"}
	server := newVoiceLiveTestServer(
		t,
		service,
		verifier,
		uidLimiter,
		appLimiter,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, response, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatalf("dial response=%v err=%v", response, err)
	}
	defer conn.CloseNow()
	if response == nil ||
		response.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" ||
		response.Header.Get("Sec-WebSocket-Extensions") != "" {
		t.Fatalf("WebSocket security headers=%v", response)
	}

	writeVoiceLiveStart(t, ctx, conn)
	ready := readVoiceLiveJSON(t, ctx, conn)
	if ready["type"] != "ready" ||
		ready["version"] != float64(voiceLiveVersion) ||
		len(ready) != 2 {
		t.Fatalf("ready=%#v", ready)
	}
	inputFrame := liveTestPCMFrame()
	if err := conn.Write(
		ctx,
		websocket.MessageBinary,
		inputFrame,
	); err != nil {
		t.Fatal(err)
	}
	commit, _ := json.Marshal(voiceLiveCommitFrame{
		Type:    "commit",
		Version: voiceLiveVersion,
	})
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}

	for index, want := range service.output {
		messageType, payload, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("audio %d: %v", index, err)
		}
		if messageType != websocket.MessageBinary ||
			string(payload) != string(want) {
			t.Fatalf("audio %d type=%v payload=%v", index, messageType, payload)
		}
	}
	final := readVoiceLiveJSON(t, ctx, conn)
	if final["type"] != "final" ||
		final["version"] != float64(voiceLiveVersion) {
		t.Fatalf("final=%#v", final)
	}
	result, ok := final["result"].(map[string]any)
	if !ok ||
		result["sessionState"] != "sealed-final-state" ||
		result["audioMimeType"] != "audio/L16" ||
		result["audioBase64"] != "" ||
		result["coachPhase"] != "none" ||
		result["coachAction"] != "none" ||
		result["answerProof"] != "none" ||
		result["caption"] != "Aです。理由はBです。" {
		t.Fatalf("final result=%#v", final["result"])
	}
	if uidLimiter.calls != 1 || appLimiter.calls != 1 {
		t.Fatalf("quota calls uid=%d app=%d", uidLimiter.calls, appLimiter.calls)
	}
	verifier.mu.Lock()
	gotIDToken := verifier.idToken
	gotAppCheckToken := verifier.appCheckToken
	verifier.mu.Unlock()
	if gotIDToken != liveTestIDToken ||
		gotAppCheckToken != liveTestAppCheckToken {
		t.Fatal("first-frame tokens were not verified")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	publishedDeadline, hasDeadline :=
		<-service.input.ProcessingDeadline
	_, deadlineStillOpen := <-service.input.ProcessingDeadline
	publishedCommitAt, hasCommitAt := <-service.input.ProcessingCommittedAt
	_, commitAtStillOpen := <-service.input.ProcessingCommittedAt
	commitPublished := false
	select {
	case <-service.input.ProcessingCommitted:
		commitPublished = true
	default:
	}
	if len(service.audio) != 1 ||
		string(service.audio[0]) != string(inputFrame) ||
		service.input.RequestID == "" ||
		service.input.ProcessingTimeout != 2*time.Second ||
		!hasDeadline ||
		publishedDeadline.IsZero() ||
		deadlineStillOpen ||
		!hasCommitAt ||
		publishedCommitAt.IsZero() ||
		commitAtStillOpen ||
		publishedDeadline.Before(publishedCommitAt) ||
		!commitPublished {
		t.Fatalf("live service audio=%v input=%+v", service.audio, service.input)
	}
}

func TestVoiceLiveEndpointKeepsSingleReaderUntilExplicitCommit(t *testing.T) {
	t.Parallel()
	service := &liveEndpointTestService{
		liveTestVoiceService: liveTestVoiceService{
			output: [][]byte{{5, 0}},
			result: VoiceTurnResult{
				Caption:          "確定後の回答",
				StateToken:       "sealed-endpoint-state",
				DetectedDomain:   "daily",
				AssistanceTarget: "assistant",
				RespondentStage:  "none",
				CoachPhase:       "none",
				CoachAction:      "none",
				ResearchStatus:   "none",
				ResearchRecords:  []ResearchRecord{},
				Route:            "fast",
			},
		},
	}
	server := newVoiceLiveTestServer(
		t,
		service,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveStart(t, ctx, conn)
	_ = readVoiceLiveJSON(t, ctx, conn)

	first := make([]byte, 640)
	first[0] = 1
	if err := conn.Write(
		ctx,
		websocket.MessageBinary,
		first,
	); err != nil {
		t.Fatal(err)
	}
	endpoint := readVoiceLiveJSON(t, ctx, conn)
	if endpoint["type"] != "endpoint" ||
		endpoint["version"] != float64(voiceLiveVersion) ||
		len(endpoint) != 2 {
		t.Fatalf("endpoint=%#v", endpoint)
	}

	// An endpoint is advisory. The same sole reader must accept more PCM and
	// only stop after the client explicitly commits.
	second := make([]byte, voiceLivePCMFrameBytes)
	second[0] = 2
	if err := conn.Write(
		ctx,
		websocket.MessageBinary,
		second,
	); err != nil {
		t.Fatal(err)
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{
		Type:    "commit",
		Version: voiceLiveVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(
		ctx,
		websocket.MessageText,
		commit,
	); err != nil {
		t.Fatal(err)
	}

	messageType, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageBinary ||
		string(payload) != string([]byte{5, 0}) {
		t.Fatalf("audio type=%v payload=%v", messageType, payload)
	}
	final := readVoiceLiveJSON(t, ctx, conn)
	if final["type"] != "final" {
		t.Fatalf("final=%#v", final)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.audio) != 2 ||
		len(service.audio[0]) != len(first) ||
		len(service.audio[1]) != len(second) {
		sizes := make([]int, 0, len(service.audio))
		for _, frame := range service.audio {
			sizes = append(sizes, len(frame))
		}
		t.Fatalf("endpoint service audio sizes=%v", sizes)
	}
}

func TestVoiceLiveRejectsWrongOriginAndHandshakeCredentials(t *testing.T) {
	t.Parallel()
	server := newVoiceLiveTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	badOrigin := make(http.Header)
	badOrigin.Set("Origin", "https://evil.example")
	if conn, response, err := dialVoiceLive(
		ctx,
		server.URL,
		badOrigin,
	); err == nil || conn != nil || response == nil ||
		response.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong origin conn=%v response=%v err=%v", conn, response, err)
	}

	headerCredentials := make(http.Header)
	headerCredentials.Set("Origin", allowedWebOrigin)
	headerCredentials.Set("Authorization", "Bearer secret")
	if conn, response, err := dialVoiceLive(
		ctx,
		server.URL,
		headerCredentials,
	); err == nil || conn != nil || response == nil ||
		response.StatusCode != http.StatusBadRequest {
		t.Fatalf("header auth conn=%v response=%v err=%v", conn, response, err)
	}

	if conn, response, err := websocket.Dial(
		ctx,
		"ws"+strings.TrimPrefix(server.URL, "http")+
			voiceLivePath+"?idToken=secret",
		&websocket.DialOptions{HTTPHeader: http.Header{
			"Origin": []string{allowedWebOrigin},
		}},
	); err == nil || conn != nil || response == nil ||
		response.StatusCode != http.StatusBadRequest {
		t.Fatalf("query auth conn=%v response=%v err=%v", conn, response, err)
	}
}

func TestVoiceLiveRejectsDuplicateStartKeysAndInvalidAudioOrder(t *testing.T) {
	t.Parallel()
	server := newVoiceLiveTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	duplicateStart := `{"type":"start","version":1,"version":1,` +
		`"idToken":"` + liveTestIDToken + `",` +
		`"appCheckToken":"` + liveTestAppCheckToken + `",` +
		`"sessionState":"","turnMode":"intentional","sampleRateHz":16000}`
	if err := conn.Write(
		ctx,
		websocket.MessageText,
		[]byte(duplicateStart),
	); err != nil {
		t.Fatal(err)
	}
	frame := readVoiceLiveJSON(t, ctx, conn)
	if frame["type"] != "error" ||
		frame["code"] != voiceLiveCodeResponseInvalid {
		t.Fatalf("duplicate-key frame=%#v", frame)
	}
	conn.CloseNow()

	conn, _, err = dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveStart(t, ctx, conn)
	_ = readVoiceLiveJSON(t, ctx, conn)
	if err := conn.Write(
		ctx,
		websocket.MessageBinary,
		[]byte{1},
	); err != nil {
		t.Fatal(err)
	}
	frame = readVoiceLiveJSON(t, ctx, conn)
	if frame["type"] != "error" ||
		frame["code"] != voiceLiveCodeResponseInvalid {
		t.Fatalf("odd PCM frame=%#v", frame)
	}
}

func TestVoiceLiveCommitWithoutAudioFailsClosed(t *testing.T) {
	t.Parallel()
	server := newVoiceLiveTestServer(
		t,
		&liveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveStart(t, ctx, conn)
	_ = readVoiceLiveJSON(t, ctx, conn)
	commit, _ := json.Marshal(voiceLiveCommitFrame{
		Type:    "commit",
		Version: voiceLiveVersion,
	})
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}
	frame := readVoiceLiveJSON(t, ctx, conn)
	if frame["type"] != "error" ||
		frame["code"] != voiceLiveCodeNoSpeech {
		t.Fatalf("empty commit frame=%#v", frame)
	}
}

func TestVoiceLiveDisconnectCancelsPipeline(t *testing.T) {
	t.Parallel()
	cancelObserved := make(chan struct{})
	service := &liveTestVoiceService{
		waitForCancel:  true,
		cancelObserved: cancelObserved,
	}
	server := newVoiceLiveTestServer(
		t,
		service,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeVoiceLiveStart(t, ctx, conn)
	_ = readVoiceLiveJSON(t, ctx, conn)
	if err := conn.Write(
		ctx,
		websocket.MessageBinary,
		liveTestPCMFrame(),
	); err != nil {
		t.Fatal(err)
	}
	commit, _ := json.Marshal(voiceLiveCommitFrame{
		Type:    "commit",
		Version: voiceLiveVersion,
	})
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}
	conn.CloseNow()
	select {
	case <-cancelObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline context was not canceled after disconnect")
	}
}

func TestVoiceLiveStartRequiresJWTAlphabetAndCanonicalState(t *testing.T) {
	t.Parallel()
	base := voiceLiveStartFrame{
		Type:          "start",
		Version:       voiceLiveVersion,
		IDToken:       liveTestIDToken,
		AppCheckToken: liveTestAppCheckToken,
		SessionState:  "v1.canonical-state",
		TurnMode:      VoiceTurnIntentional,
		SampleRateHz:  voiceLiveSampleRateHz,
	}
	if !validVoiceLiveStart(base) {
		t.Fatal("valid start frame was rejected")
	}
	native := base
	native.NativeAudio = true
	if !validVoiceLiveStart(native) {
		t.Fatal("legacy native audio start frame without coach capability was rejected")
	}
	nativeWithCoachControl := native
	nativeWithCoachControl.NativeCoachControl = true
	if !validVoiceLiveStart(nativeWithCoachControl) {
		t.Fatal("native audio start frame with coach capability was rejected")
	}
	for _, mutate := range []func(*voiceLiveStartFrame){
		func(frame *voiceLiveStartFrame) { frame.IDToken = "header.payload" },
		func(frame *voiceLiveStartFrame) { frame.IDToken = "header.pay+load.signature" },
		func(frame *voiceLiveStartFrame) { frame.IDToken = "header.payload.signature=" },
		func(frame *voiceLiveStartFrame) { frame.AppCheckToken = "header..signature" },
		func(frame *voiceLiveStartFrame) { frame.SessionState = " state" },
		func(frame *voiceLiveStartFrame) {
			frame.SessionState = strings.Repeat("x", maxStateBytes+1)
		},
		func(frame *voiceLiveStartFrame) {
			frame.SessionState = ""
			frame.NativeAudio = true
			frame.StrictCloudMinimization = true
		},
		func(frame *voiceLiveStartFrame) {
			frame.NativeCoachControl = true
			frame.NativeAudio = false
		},
	} {
		frame := base
		mutate(&frame)
		if validVoiceLiveStart(frame) {
			t.Fatalf("invalid start frame was accepted: %+v", frame)
		}
	}
}

func TestVoiceLiveRequiresExactTwentyMillisecondPCMFrames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		size     int
		accepted bool
	}{
		{name: "twenty milliseconds", size: 640, accepted: true},
		{name: "old client batch", size: 3_200},
		{name: "arbitrary even frame", size: 638},
		{name: "two frames batched", size: 1_280},
		{name: "one byte short", size: 639},
		{name: "one byte long", size: 641},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := &liveTestVoiceService{
				result: VoiceTurnResult{
					StateToken:       "sealed-state",
					DetectedDomain:   "daily",
					AssistanceTarget: "assistant",
					RespondentStage:  "none",
					CoachPhase:       "none",
					CoachAction:      "none",
					ResearchStatus:   "none",
					ResearchRecords:  []ResearchRecord{},
					Route:            "silent-fast",
				},
			}
			server := newVoiceLiveTestServer(
				t,
				service,
				&liveTestVerifier{},
				&fakeLimiter{},
				&fakeLimiter{wantKey: "app:app-123"},
			)
			ctx, cancel := context.WithTimeout(
				context.Background(),
				3*time.Second,
			)
			defer cancel()
			conn, _, err := dialVoiceLive(ctx, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			writeVoiceLiveStart(t, ctx, conn)
			_ = readVoiceLiveJSON(t, ctx, conn)
			if err := conn.Write(
				ctx,
				websocket.MessageBinary,
				make([]byte, test.size),
			); err != nil {
				t.Fatal(err)
			}
			if !test.accepted {
				frame := readVoiceLiveJSON(t, ctx, conn)
				if frame["type"] != "error" ||
					frame["code"] != voiceLiveCodeResponseInvalid {
					t.Fatalf(
						"%d-byte PCM response=%#v",
						test.size,
						frame,
					)
				}
				return
			}
			commit, err := json.Marshal(voiceLiveCommitFrame{
				Type:    "commit",
				Version: voiceLiveVersion,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.Write(
				ctx,
				websocket.MessageText,
				commit,
			); err != nil {
				t.Fatal(err)
			}
			frame := readVoiceLiveJSON(t, ctx, conn)
			if frame["type"] != "final" {
				t.Fatalf("%d-byte PCM response=%#v", test.size, frame)
			}
			service.mu.Lock()
			defer service.mu.Unlock()
			if len(service.audio) != 1 ||
				len(service.audio[0]) != test.size {
				t.Fatalf(
					"%d-byte PCM was not delivered: %v",
					test.size,
					service.audio,
				)
			}
		})
	}
}

func TestVoiceLiveSilentFinalContainsNoBinaryAudio(t *testing.T) {
	t.Parallel()
	service := &liveTestVoiceService{
		result: VoiceTurnResult{
			StateToken:       "sealed-silent-state",
			DetectedDomain:   "daily",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			CoachPhase:       "none",
			CoachAction:      "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "ambient-silent-fast",
		},
	}
	server := newVoiceLiveTestServer(
		t,
		service,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveStart(t, ctx, conn)
	_ = readVoiceLiveJSON(t, ctx, conn)
	if err := conn.Write(
		ctx,
		websocket.MessageBinary,
		liveTestPCMFrame(),
	); err != nil {
		t.Fatal(err)
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{
		Type:    "commit",
		Version: voiceLiveVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}

	messageType, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("silent result emitted binary frame type=%v", messageType)
	}
	var final map[string]any
	if err := json.Unmarshal(payload, &final); err != nil {
		t.Fatal(err)
	}
	result, ok := final["result"].(map[string]any)
	if final["type"] != "final" ||
		!ok ||
		result["audioBase64"] != "" ||
		result["audioMimeType"] != "" ||
		result["caption"] != nil ||
		result["sessionState"] != "sealed-silent-state" {
		t.Fatalf("silent final=%#v", final)
	}
}

type precommitOutputLiveTestVoiceService struct {
	attempted chan error
}

func (*precommitOutputLiveTestVoiceService) Process(
	context.Context,
	string,
	VoiceTurnInput,
) (VoiceTurnResult, error) {
	return VoiceTurnResult{}, errors.New("buffered voice method called")
}

func (service *precommitOutputLiveTestVoiceService) ProcessLive(
	_ context.Context,
	_ string,
	_ VoiceTurnInput,
	_ <-chan []byte,
	onAudio func([]byte) error,
) (VoiceTurnResult, error) {
	err := onAudio([]byte{1, 0})
	service.attempted <- err
	return VoiceTurnResult{}, err
}

func TestVoiceLiveRejectsPipelineOutputBeforeCommit(t *testing.T) {
	t.Parallel()
	service := &precommitOutputLiveTestVoiceService{
		attempted: make(chan error, 1),
	}
	server := newVoiceLiveTestServer(
		t,
		service,
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveStart(t, ctx, conn)
	_ = readVoiceLiveJSON(t, ctx, conn)

	frame := readVoiceLiveJSON(t, ctx, conn)
	if frame["type"] != "error" ||
		frame["code"] != voiceLiveCodeAPIUnavailable {
		t.Fatalf("precommit output response=%#v", frame)
	}
	select {
	case attemptErr := <-service.attempted:
		if attemptErr == nil {
			t.Fatal("precommit output callback was accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("precommit output callback did not return")
	}
}

type panickingLiveTestVoiceService struct{}

func (panickingLiveTestVoiceService) Process(
	context.Context,
	string,
	VoiceTurnInput,
) (VoiceTurnResult, error) {
	return VoiceTurnResult{}, errors.New("buffered voice method called")
}

func (panickingLiveTestVoiceService) ProcessLive(
	_ context.Context,
	_ string,
	_ VoiceTurnInput,
	audio <-chan []byte,
	_ func([]byte) error,
) (VoiceTurnResult, error) {
	for range audio {
	}
	panic("test live pipeline panic")
}

func TestVoiceLivePipelinePanicFailsClosedWithoutBinaryAudio(t *testing.T) {
	t.Parallel()
	server := newVoiceLiveTestServer(
		t,
		panickingLiveTestVoiceService{},
		&liveTestVerifier{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLiveStart(t, ctx, conn)
	_ = readVoiceLiveJSON(t, ctx, conn)
	if err := conn.Write(
		ctx,
		websocket.MessageBinary,
		liveTestPCMFrame(),
	); err != nil {
		t.Fatal(err)
	}
	commit, err := json.Marshal(voiceLiveCommitFrame{
		Type:    "commit",
		Version: voiceLiveVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, commit); err != nil {
		t.Fatal(err)
	}

	frame := readVoiceLiveJSON(t, ctx, conn)
	if frame["type"] != "error" ||
		frame["code"] != voiceLiveCodeAPIUnavailable {
		t.Fatalf("pipeline panic response=%#v", frame)
	}
}
