package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
)

func writeVoiceLivePreflight(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	generation int64,
) {
	t.Helper()
	payload, err := json.Marshal(voiceLivePreflightFrame{
		Type:          "preflight",
		Version:       voiceLivePreflightVersion,
		IDToken:       liveTestIDToken,
		AppCheckToken: liveTestAppCheckToken,
		Generation:    generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

func writeVoiceLiveActivation(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	leaseID string,
	generation int64,
) {
	t.Helper()
	payload, err := json.Marshal(voiceLiveActivateFrame{
		Type:               "activate",
		Version:            voiceLivePreflightVersion,
		LeaseID:            leaseID,
		Generation:         generation,
		NativeCoachControl: true,
		SessionState:       "",
		TurnMode:           VoiceTurnIntentional,
		SampleRateHz:       voiceLiveSampleRateHz,
		NativeAudio:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

func newVoiceLivePreflightHandlerTestServer(
	t *testing.T,
	service VoiceTurnLiveService,
) *httptest.Server {
	t.Helper()
	return newVoiceLiveControlledTestServer(
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
}

func TestVoiceLivePreflightAuthenticatesBeforeBoundActivation(t *testing.T) {
	service := &strongReadyLiveTestService{
		started:        make(chan struct{}),
		readyPublished: make(chan struct{}),
		done:           make(chan struct{}),
	}
	server := newVoiceLivePreflightHandlerTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLivePreflight(t, ctx, conn, 31)
	ready := readVoiceLiveJSON(t, ctx, conn)
	if len(ready) != 5 ||
		ready["type"] != "preflight-ready" ||
		ready["version"] != float64(voiceLivePreflightVersion) ||
		ready["generation"] != float64(31) ||
		ready["expiresInMs"] != float64(voiceLivePreflightTTL.Milliseconds()) {
		t.Fatalf("preflight ready = %#v", ready)
	}
	leaseID, ok := ready["leaseId"].(string)
	if !ok || !validVoiceLiveLeaseID(leaseID) {
		t.Fatalf("preflight lease = %#v", ready["leaseId"])
	}
	select {
	case <-service.started:
		t.Fatal("provider pipeline started before activation")
	default:
	}

	writeVoiceLiveActivation(t, ctx, conn, leaseID, 31)
	if frame := readVoiceLiveJSON(t, ctx, conn); len(frame) != 2 ||
		frame["type"] != "ready" ||
		frame["version"] != float64(voiceLiveVersion) {
		t.Fatalf("strong ready = %#v", frame)
	}
	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("provider pipeline did not start after activation")
	}
}

func TestVoiceLivePreflightReadyWaitsForProviderSetup(t *testing.T) {
	allowPreflight := make(chan struct{})
	service := &strongReadyLiveTestService{
		preflightStarted: make(chan struct{}),
		allowPreflight:   allowPreflight,
		started:          make(chan struct{}),
		done:             make(chan struct{}),
	}
	server := newVoiceLivePreflightHandlerTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLivePreflight(t, ctx, conn, 32)
	readyResult := make(chan map[string]any, 1)
	go func() {
		readyResult <- readVoiceLiveJSON(t, ctx, conn)
	}()
	select {
	case <-service.preflightStarted:
	case <-time.After(time.Second):
		t.Fatal("provider preflight did not start")
	}
	select {
	case ready := <-readyResult:
		t.Fatalf("preflight-ready preceded SetupComplete: %#v", ready)
	case <-time.After(50 * time.Millisecond):
	}
	close(allowPreflight)
	select {
	case ready := <-readyResult:
		if ready["type"] != "preflight-ready" ||
			ready["generation"] != float64(32) {
			t.Fatalf("preflight ready = %#v", ready)
		}
	case <-time.After(time.Second):
		t.Fatal("preflight-ready did not follow provider setup")
	}
}

func TestVoiceLivePreflightProviderFailureFallsBackBeforeActivation(t *testing.T) {
	service := &strongReadyLiveTestService{
		preflightErr: errors.New("provider setup unavailable"),
		started:      make(chan struct{}),
		done:         make(chan struct{}),
	}
	server := newVoiceLivePreflightHandlerTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLivePreflight(t, ctx, conn, 33)
	terminal := readVoiceLiveJSON(t, ctx, conn)
	if terminal["type"] != "error" ||
		terminal["code"] != voiceLiveCodeAPIUnavailable {
		t.Fatalf("terminal = %#v", terminal)
	}
	select {
	case <-service.started:
		t.Fatal("failed provider preflight reached the activated pipeline")
	default:
	}
}

func TestVoiceLivePreflightRejectsCrossGenerationBeforeProvider(t *testing.T) {
	service := &strongReadyLiveTestService{
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
	server := newVoiceLivePreflightHandlerTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLivePreflight(t, ctx, conn, 40)
	ready := readVoiceLiveJSON(t, ctx, conn)
	leaseID, _ := ready["leaseId"].(string)
	writeVoiceLiveActivation(t, ctx, conn, leaseID, 41)
	terminal := readVoiceLiveJSON(t, ctx, conn)
	if terminal["type"] != "error" ||
		terminal["code"] != voiceLiveCodeResponseInvalid {
		t.Fatalf("terminal = %#v", terminal)
	}
	select {
	case <-service.started:
		t.Fatal("cross-generation activation reached provider")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestVoiceLivePreflightRejectsCrossLeaseBeforeProvider(t *testing.T) {
	service := &strongReadyLiveTestService{
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
	server := newVoiceLivePreflightHandlerTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeVoiceLivePreflight(t, ctx, conn, 50)
	_ = readVoiceLiveJSON(t, ctx, conn)
	wrongLeaseID, err := newVoiceLivePreflightLeaseID()
	if err != nil {
		t.Fatal(err)
	}
	writeVoiceLiveActivation(t, ctx, conn, wrongLeaseID, 50)
	terminal := readVoiceLiveJSON(t, ctx, conn)
	if terminal["type"] != "error" ||
		terminal["code"] != voiceLiveCodeResponseInvalid {
		t.Fatalf("terminal = %#v", terminal)
	}
	select {
	case <-service.started:
		t.Fatal("cross-lease activation reached provider")
	case <-time.After(50 * time.Millisecond):
	}
}
