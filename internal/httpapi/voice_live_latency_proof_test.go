package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/semanticshadow"
)

type voiceLiveCompletionLogHandler struct {
	completed chan slog.Record
}

func (handler voiceLiveCompletionLogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (handler voiceLiveCompletionLogHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "voice live session completed" {
		handler.completed <- record.Clone()
	}
	return nil
}

func (handler voiceLiveCompletionLogHandler) WithAttrs([]slog.Attr) slog.Handler {
	return handler
}

func (handler voiceLiveCompletionLogHandler) WithGroup(string) slog.Handler {
	return handler
}

func TestStrictVoiceLiveCollectsLatencyProofAfterAudioAndFinal(t *testing.T) {
	exerciseVoiceLiveProofCompletion(t, true, false)
}

func TestVoiceLiveCompletesAfterFinalDisconnect(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "strict"}[strict], func(t *testing.T) {
			exerciseVoiceLiveProofCompletion(t, strict, true)
		})
	}
}

func exerciseVoiceLiveProofCompletion(t *testing.T, strict, disconnectAfterFinal bool) {
	t.Helper()
	completed := make(chan slog.Record, 1)
	memoryQueue := &fakeLongTermMemoryQueue{}
	dispatcher, err := semanticshadow.NewDispatcher(&blockingSemanticShadowExporter{started: make(chan struct{})})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	service := &liveTestVoiceService{
		output: [][]byte{{64, 0, 65, 0}},
		result: VoiceTurnResult{
			Caption:          "短い返答です。",
			DetectedDomain:   "daily",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			CoachPhase:       "none",
			CoachAction:      "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			PrivacyStatus:    "clear",
			Route:            "strict-test",
		},
	}
	if !strict {
		service.result.PrivacyStatus = ""
		service.result.StateToken = "opaque-final-state"
	}
	handler := NewWithVoice(
		slog.New(voiceLiveCompletionLogHandler{completed: completed}),
		&liveTestVerifier{principal: identity.Principal{
			UID: "user-123", AppID: "app-123", Provider: "custom",
			AuthMethod: "passkey-v1", AccountVerified: true,
		}}, &fakeLimiter{}, &fakeEvaluator{}, &fakeStore{},
		time.Second, 4*1024,
		VoiceOptions{
			Service: service, RateLimiter: &fakeLimiter{},
			AppRateLimiter:    &fakeLimiter{wantKey: "app:app-123"},
			LiveLeaseManager:  guard.NewMemoryVoiceLiveLeaseManager(),
			LiveHandshakeGate: NewVoiceLiveHandshakeGate(2),
			RequestTimeout:    2 * time.Second, MaxRequestBytes: 13 * 1024 * 1024,
			LongTermMemoryQueue: memoryQueue,
			SemanticShadow:      dispatcher, SemanticShadowKey: make([]byte, 32),
		},
	)
	returned := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		close(returned)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := dialVoiceLive(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	start, err := json.Marshal(voiceLiveStartFrame{
		Type: "start", Version: voiceLiveVersion,
		IDToken: liveTestIDToken, AppCheckToken: liveTestAppCheckToken,
		TurnMode: VoiceTurnIntentional, SampleRateHz: voiceLiveSampleRateHz,
		StrictCloudMinimization: strict, LatencyProofVersion: pointerTo(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, start); err != nil {
		t.Fatal(err)
	}
	if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
		t.Fatalf("ready=%#v", ready)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"commit","version":1}`)); err != nil {
		t.Fatal(err)
	}
	if committed := readVoiceLiveJSON(t, ctx, conn); committed["type"] != "committed" {
		t.Fatalf("committed=%#v", committed)
	}
	messageType, payload, err := conn.Read(ctx)
	if err != nil || messageType != websocket.MessageBinary || len(payload) != 4 {
		t.Fatalf("audio type=%v bytes=%d error=%v", messageType, len(payload), err)
	}
	if final := readVoiceLiveJSON(t, ctx, conn); final["type"] != "final" {
		t.Fatalf("final=%#v", final)
	}
	// A browser cannot prove audible output before receiving PCM. Waiting for
	// final as well also proves that optional telemetry does not hold final.
	var writeErr error
	wantProof := int64(760)
	if disconnectAfterFinal {
		wantProof = -1
		conn.CloseNow()
		select {
		case <-returned:
			if len(completed) != 1 {
				t.Fatal("handler returned without a completion log")
			}
		case <-ctx.Done():
			t.Fatal("handler did not exit after final disconnect")
		}
	} else {
		proof, err := json.Marshal(voiceLiveLatencyFrame{
			Type: "latency", Version: 1,
			SpeechEndToCommitSendMS: 120, SpeechEndToCommitAckMS: 180,
			SpeechEndToEstimatedAudibleMS: 760,
		})
		if err != nil {
			t.Fatal(err)
		}
		writeErr = conn.Write(ctx, websocket.MessageText, proof)
	}
	select {
	case record := <-completed:
		var got int64 = -1
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "speech_end_to_estimated_audible_ms" {
				got = attr.Value.Int64()
			}
			return true
		})
		if got != wantProof {
			t.Fatalf("logged audible proof=%d want=%d (proof write error=%v)", got, wantProof, writeErr)
		}
	case <-ctx.Done():
		t.Fatal("completion log was not emitted")
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if dispatcher.Snapshot().Accepted != 1 {
		t.Fatal("completed response lost its semantic shadow observation")
	}
	wantMemoryCalls := int32(1)
	if strict {
		wantMemoryCalls = 0
	}
	if got := memoryQueue.calls.Load(); got != wantMemoryCalls {
		t.Fatalf("memory enqueue calls=%d want=%d", got, wantMemoryCalls)
	}
	if !strict && (memoryQueue.uid != "user-123" || memoryQueue.token != service.result.StateToken) {
		t.Fatal("completed response lost its memory binding")
	}
	if disconnectAfterFinal {
		return
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("terminal error=%v", err)
	}
}

func TestVoiceLiveLatencyProofGraceSkipsSilentAndProvenOutput(t *testing.T) {
	for _, name := range []string{"no audio", "silent PCM", "proof already received"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				metrics := &voiceLiveOutputMetrics{}
				if name != "no audio" {
					metrics.frames = 1
				}
				if name == "proof already received" {
					metrics.firstOutputAt = time.Now()
					metrics.markLatencyProof(voiceLiveLatencyFrame{
						Type: "latency", Version: 1,
						SpeechEndToEstimatedAudibleMS: 760,
					})
				}
				started := time.Now()
				if !waitForVoiceLiveLatencyProof(context.Background(), metrics, nil, nil) {
					t.Fatal("successful response was cancelled")
				}
				if elapsed := time.Since(started); elapsed != 0 {
					t.Fatalf("unnecessary proof grace=%s", elapsed)
				}
			})
		})
	}
}

func TestVoiceLiveLatencyProofGraceStopsOnCancellationOrDisconnect(t *testing.T) {
	for _, name := range []string{"cancel", "disconnect", "already cancelled", "already disconnected"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				disconnected := make(chan struct{})
				metrics := &voiceLiveOutputMetrics{firstOutputAt: time.Now(), frames: 1}
				stop := func() {
					if name == "cancel" || name == "already cancelled" {
						cancel()
					} else {
						close(disconnected)
					}
				}
				alreadyStopped := name == "already cancelled" || name == "already disconnected"
				if alreadyStopped {
					stop()
				}
				started := time.Now()
				done := make(chan bool, 1)
				go func() {
					done <- waitForVoiceLiveLatencyProof(ctx, metrics, nil, disconnected)
				}()
				synctest.Wait()
				if !alreadyStopped {
					select {
					case <-done:
						t.Fatal("proof wait ended before cancellation")
					default:
					}
					stop()
					synctest.Wait()
				}
				select {
				case alive := <-done:
					if alive || time.Since(started) != 0 {
						t.Fatalf("alive=%t elapsed=%s", alive, time.Since(started))
					}
				default:
					t.Fatal("proof grace outlived cancellation")
				}
			})
		})
	}
}

func TestVoiceLiveLatencyProofGraceIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		metrics := &voiceLiveOutputMetrics{firstOutputAt: time.Now(), frames: 1}
		started := time.Now()
		if !waitForVoiceLiveLatencyProof(context.Background(), metrics, nil, nil) {
			t.Fatal("missing optional proof cancelled a successful response")
		}
		if elapsed := time.Since(started); elapsed != voiceLiveLatencyProofGrace {
			t.Fatalf("proof grace=%s want=%s", elapsed, voiceLiveLatencyProofGrace)
		}
	})
}
