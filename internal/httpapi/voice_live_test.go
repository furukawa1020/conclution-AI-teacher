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
}

func liveTestPCMFrame() []byte {
	frame := make([]byte, liveTestPCMFrameBytes)
	frame[0] = 1
	frame[2] = 2
	return frame
}

type liveTestVerifier struct {
	mu            sync.Mutex
	idToken       string
	appCheckToken string
	err           error
}

func (verifier *liveTestVerifier) Verify(
	_ context.Context,
	idToken string,
	appCheckToken string,
) (identity.Principal, error) {
	verifier.mu.Lock()
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
	mu              sync.Mutex
	input           VoiceTurnInput
	audio           [][]byte
	output          [][]byte
	result          VoiceTurnResult
	err             error
	waitForCancel   bool
	cancelObserved  chan struct{}
	cancelSignalOne sync.Once
}

type liveEndpointTestService struct {
	liveTestVoiceService
	endpointOnce sync.Once
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
			Service:         service,
			RateLimiter:     uidLimiter,
			AppRateLimiter:  appLimiter,
			RequestTimeout:  2 * time.Second,
			MaxRequestBytes: 13 * 1024 * 1024,
		},
	)
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
