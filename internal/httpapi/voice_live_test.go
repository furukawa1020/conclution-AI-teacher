package httpapi

import (
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
		t.Fatal(`capture budget does not admit a three-minute monologue`)
	}
	if voiceLiveMaxCaptureDuration >= providerStreamingLimit {
		t.Fatal(`capture budget reaches the provider streaming limit`)
	}
	frameBudget := time.Duration(voiceLiveMaxPCMFrames) * pcmFrameDuration
	if frameBudget < voiceLiveMaxCaptureDuration {
		t.Fatal(`PCM frame budget ends before the capture deadline`)
	}
	if voiceLiveMaxPCMTotalBytes !=
		voiceLiveMaxPCMFrames*voiceLivePCMFrameBytes {
		t.Fatal(`PCM byte and frame bounds diverged`)
	}
	minimumConnectionBudget := voiceLiveFirstFrameTimeout +
		2*voiceLiveGuardTimeout +
		voiceLiveMaxCaptureDuration +
		maxPostCommitProcess
	if VoiceLiveConnectionTimeout <= minimumConnectionBudget {
		t.Fatal(`HTTP connection timeout has no cleanup margin`)
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
	return identity.Principal{
		UID:   "user-123",
		AppID: "app-123",
		Roles: map[string]bool{"user": true},
	}, nil
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

type liveLifecycleTestService struct {
	started     chan struct{}
	canceled    chan struct{}
	allowReturn <-chan struct{}
	done        chan struct{}
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
) *httptest.Server {
	t.Helper()
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
			RateLimiter:             uidLimiter,
			AppRateLimiter:          appLimiter,
			LiveLeaseManager:        leaseManager,
			LiveHandshakeGate:       handshakeGate,
			RequestTimeout:          2 * time.Second,
			MaxRequestBytes:         13 * 1024 * 1024,
			livePipelineJoinTimeout: pipelineJoinTimeout,
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
	for _, mutate := range []func(*voiceLiveStartFrame){
		func(frame *voiceLiveStartFrame) { frame.IDToken = "header.payload" },
		func(frame *voiceLiveStartFrame) { frame.IDToken = "header.pay+load.signature" },
		func(frame *voiceLiveStartFrame) { frame.IDToken = "header.payload.signature=" },
		func(frame *voiceLiveStartFrame) { frame.AppCheckToken = "header..signature" },
		func(frame *voiceLiveStartFrame) { frame.SessionState = " state" },
		func(frame *voiceLiveStartFrame) {
			frame.SessionState = strings.Repeat("x", maxStateBytes+1)
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
