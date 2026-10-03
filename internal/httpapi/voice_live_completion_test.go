package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/semanticshadow"
)

type blockedVoiceLiveCompletionLogHandler struct {
	entered chan slog.Record
	unblock <-chan struct{}
	calls   atomic.Int32
}

func (*blockedVoiceLiveCompletionLogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (handler *blockedVoiceLiveCompletionLogHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "voice live session completed" {
		handler.calls.Add(1)
		handler.entered <- record.Clone()
		<-handler.unblock
	}
	return nil
}

func (handler *blockedVoiceLiveCompletionLogHandler) WithAttrs([]slog.Attr) slog.Handler {
	return handler
}

func (handler *blockedVoiceLiveCompletionLogHandler) WithGroup(string) slog.Handler {
	return handler
}

type voiceLiveCompletionLeaseManager struct {
	*guard.MemoryVoiceLiveLeaseManager
	releases      atomic.Int32
	beforeRelease func()
}

func (manager *voiceLiveCompletionLeaseManager) Acquire(
	ctx context.Context, uid string, at time.Time, ttl time.Duration,
) (guard.VoiceLiveLease, error) {
	lease, err := manager.MemoryVoiceLiveLeaseManager.Acquire(ctx, uid, at, ttl)
	if err != nil {
		return nil, err
	}
	return &voiceLiveCompletionLease{VoiceLiveLease: lease, manager: manager}, nil
}

type voiceLiveCompletionLease struct {
	guard.VoiceLiveLease
	manager *voiceLiveCompletionLeaseManager
}

func (lease *voiceLiveCompletionLease) Release(ctx context.Context) error {
	lease.manager.releases.Add(1)
	if lease.manager.beforeRelease != nil {
		lease.manager.beforeRelease()
	}
	return lease.VoiceLiveLease.Release(ctx)
}

// Like nativeflow, this fake retires the current UID's unclaimed prepared
// session, not a particular generation. A stale cleanup can erase a new one.
type voiceLiveCompletionPreflightService struct {
	*liveTestVoiceService
	poolMu      sync.Mutex
	prepared    bool
	cancelCalls int
}

func (service *voiceLiveCompletionPreflightService) PrepareLive(
	ctx context.Context, _ string, _ time.Duration,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	service.poolMu.Lock()
	defer service.poolMu.Unlock()
	if service.prepared {
		return errors.New("prepared session already held")
	}
	service.prepared = true
	return nil
}

func (service *voiceLiveCompletionPreflightService) CancelPreparedLive(string) {
	service.poolMu.Lock()
	defer service.poolMu.Unlock()
	service.cancelCalls++
	service.prepared = false
}

func (service *voiceLiveCompletionPreflightService) ProcessLive(
	ctx context.Context,
	uid string,
	input VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
) (VoiceTurnResult, error) {
	service.poolMu.Lock()
	service.prepared = false
	service.poolMu.Unlock()
	return service.liveTestVoiceService.ProcessLive(ctx, uid, input, audio, onAudio)
}

type voiceLiveCompletionFixture struct {
	ctx        context.Context
	server     *httptest.Server
	logger     *blockedVoiceLiveCompletionLogHandler
	unblockLog func()
	returned   <-chan struct{}
	leases     *voiceLiveCompletionLeaseManager
	queue      *fakeLongTermMemoryQueue
	shadow     *semanticshadow.Dispatcher
	preflight  *voiceLiveCompletionPreflightService
}

func newVoiceLiveCompletionFixture(t *testing.T, strict, preflight bool) *voiceLiveCompletionFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	t.Cleanup(cancel)
	unblock := make(chan struct{})
	logger := &blockedVoiceLiveCompletionLogHandler{
		entered: make(chan slog.Record, 2), unblock: unblock,
	}
	queue := &fakeLongTermMemoryQueue{}
	shadow, err := semanticshadow.NewDispatcher(&blockingSemanticShadowExporter{started: make(chan struct{})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shadow.Close)
	service := &liveTestVoiceService{
		output: [][]byte{{64, 0, 65, 0}},
		result: VoiceTurnResult{
			Caption: "短い返答です。", DetectedDomain: "daily",
			AssistanceTarget: "assistant", RespondentStage: "none",
			CoachPhase: "none", CoachAction: "none",
			ResearchStatus: "none", ResearchRecords: []ResearchRecord{},
			PrivacyStatus: "clear", Route: "completion-test",
		},
	}
	if !strict {
		service.result.PrivacyStatus = ""
		service.result.StateToken = "opaque-final-state"
	}
	fixture := &voiceLiveCompletionFixture{
		ctx: ctx, logger: logger, unblockLog: sync.OnceFunc(func() { close(unblock) }),
		queue: queue, shadow: shadow,
		leases: &voiceLiveCompletionLeaseManager{
			MemoryVoiceLiveLeaseManager: guard.NewMemoryVoiceLiveLeaseManager(),
		},
	}
	var native VoiceTurnLiveService
	if preflight {
		fixture.preflight = &voiceLiveCompletionPreflightService{liveTestVoiceService: service}
		native = fixture.preflight
	}
	handler := NewWithVoice(
		slog.New(logger),
		&liveTestVerifier{principal: identity.Principal{
			UID: "user-123", AppID: "app-123", Provider: "custom",
			AuthMethod: "passkey-v1", AccountVerified: true,
		}}, &fakeLimiter{}, &fakeEvaluator{}, &fakeStore{},
		time.Second, 4*1024,
		VoiceOptions{
			Service: service, NativeLiveService: native,
			RateLimiter: &fakeLimiter{}, AppRateLimiter: &fakeLimiter{wantKey: "app:app-123"},
			LiveLeaseManager: fixture.leases, LiveHandshakeGate: NewVoiceLiveHandshakeGate(2),
			RequestTimeout: 4 * time.Second, MaxRequestBytes: 13 * 1024 * 1024,
			LongTermMemoryQueue: queue, SemanticShadow: shadow, SemanticShadowKey: make([]byte, 32),
		},
	)
	returned := make(chan struct{})
	fixture.returned = returned
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		close(returned)
	}))
	t.Cleanup(fixture.server.Close)
	// Unblock before server.Close even when an assertion fails.
	t.Cleanup(fixture.unblockLog)
	return fixture
}

func commitVoiceLiveCompletionTurn(t *testing.T, ctx context.Context, conn *websocket.Conn, commitAck bool) {
	t.Helper()
	if ready := readVoiceLiveJSON(t, ctx, conn); ready["type"] != "ready" {
		t.Fatalf("ready=%#v", ready)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, liveTestPCMFrame()); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"commit","version":1}`)); err != nil {
		t.Fatal(err)
	}
	if commitAck {
		if committed := readVoiceLiveJSON(t, ctx, conn); committed["type"] != "committed" {
			t.Fatalf("committed=%#v", committed)
		}
	}
	messageType, audio, err := conn.Read(ctx)
	if err != nil || messageType != websocket.MessageBinary || len(audio) != 4 {
		t.Fatalf("audio type=%v bytes=%d error=%v", messageType, len(audio), err)
	}
	if final := readVoiceLiveJSON(t, ctx, conn); final["type"] != "final" {
		t.Fatalf("final=%#v", final)
	}
}

func awaitBlockedVoiceLiveCompletionLog(t *testing.T, fixture *voiceLiveCompletionFixture) slog.Record {
	t.Helper()
	select {
	case record := <-fixture.logger.entered:
		return record
	case <-fixture.ctx.Done():
		t.Fatal("completion logger was not reached")
		return slog.Record{}
	}
}

func assertVoiceLiveCompletionSideEffects(t *testing.T, fixture *voiceLiveCompletionFixture, strict bool) {
	t.Helper()
	if got := fixture.leases.releases.Load(); got != 1 {
		t.Fatalf("lease releases=%d want=1", got)
	}
	if got := fixture.logger.calls.Load(); got != 1 {
		t.Fatalf("completion logs=%d want=1", got)
	}
	if got := fixture.shadow.Snapshot().Accepted; got != 1 {
		t.Fatalf("shadow observations=%d want=1", got)
	}
	wantMemory := int32(1)
	if strict {
		wantMemory = 0
	}
	if got := fixture.queue.calls.Load(); got != wantMemory {
		t.Fatalf("memory enqueues=%d want=%d", got, wantMemory)
	}
	if !strict && (fixture.queue.uid != "user-123" || fixture.queue.token != "opaque-final-state") {
		t.Fatal("memory binding changed")
	}
}

func finishBlockedVoiceLiveCompletion(t *testing.T, fixture *voiceLiveCompletionFixture) {
	t.Helper()
	select {
	case <-fixture.returned:
		t.Fatal("handler returned before its synchronous log finished")
	default:
	}
	fixture.unblockLog()
	select {
	case <-fixture.returned:
	case <-fixture.ctx.Done():
		t.Fatal("handler did not return after logger unblocked")
	}
}

func TestVoiceLiveCompletionLoggerCannotHoldCloseOrLease(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, proofMode := range []string{"not-negotiated", "received", "missing", "disconnect"} {
			t.Run(map[bool]string{false: "ordinary", true: "strict"}[strict]+"/"+proofMode, func(t *testing.T) {
				fixture := newVoiceLiveCompletionFixture(t, strict, false)
				conn, _, err := dialVoiceLive(fixture.ctx, fixture.server.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
				start := voiceLiveStartFrame{
					Type: "start", Version: voiceLiveVersion,
					IDToken: liveTestIDToken, AppCheckToken: liveTestAppCheckToken,
					TurnMode: VoiceTurnIntentional, SampleRateHz: voiceLiveSampleRateHz,
					StrictCloudMinimization: strict,
				}
				if proofMode != "not-negotiated" {
					start.LatencyProofVersion = pointerTo(1)
				}
				payload, err := json.Marshal(start)
				if err != nil {
					t.Fatal(err)
				}
				if err := conn.Write(fixture.ctx, websocket.MessageText, payload); err != nil {
					t.Fatal(err)
				}
				commitVoiceLiveCompletionTurn(t, fixture.ctx, conn, proofMode != "not-negotiated")
				wantProof := int64(-1)
				if proofMode == "received" {
					wantProof = 760
					if err := conn.Write(fixture.ctx, websocket.MessageText, []byte(
						`{"type":"latency","version":1,"speechEndToCommitSendMs":120,"speechEndToCommitAckMs":180,"speechEndToEstimatedAudibleMs":760}`,
					)); err != nil {
						t.Fatal(err)
					}
				}
				if proofMode == "disconnect" {
					conn.CloseNow()
				} else {
					// Read acknowledges the server's close; a browser does this
					// automatically even while its application awaits completion.
					_, _, err := conn.Read(fixture.ctx)
					if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
						t.Fatalf("close was held behind completion logging: %v", err)
					}
				}
				record := awaitBlockedVoiceLiveCompletionLog(t, fixture)
				assertVoiceLiveCompletionSideEffects(t, fixture, strict)
				foundProof, foundCancelled := false, false
				record.Attrs(func(attr slog.Attr) bool {
					if attr.Key == "speech_end_to_estimated_audible_ms" {
						foundProof = true
						if attr.Value.Int64() != wantProof {
							t.Errorf("audible proof=%d want=%d", attr.Value.Int64(), wantProof)
						}
					}
					if attr.Key == "cancelled" {
						foundCancelled = true
						if attr.Value.Bool() {
							t.Error("delivered final was recorded as cancelled")
						}
					}
					return true
				})
				if !foundProof || !foundCancelled {
					t.Fatal("completion log omitted proof or cancellation fields")
				}
				// The old handler is still blocked, but no longer owns this UID.
				nextLease, err := fixture.leases.MemoryVoiceLiveLeaseManager.Acquire(
					fixture.ctx, "user-123", time.Now(), time.Minute,
				)
				if err != nil {
					t.Fatalf("completion log retained UID lease: %v", err)
				}
				defer nextLease.Release(context.Background())
				finishBlockedVoiceLiveCompletion(t, fixture)
				assertVoiceLiveCompletionSideEffects(t, fixture, strict)
			})
		}
	}
}

func TestVoiceLiveCompletionCleanupCannotRetireNextPreparedSession(t *testing.T) {
	fixture := newVoiceLiveCompletionFixture(t, false, true)
	var cleanedBeforeRelease atomic.Bool
	fixture.leases.beforeRelease = func() {
		fixture.preflight.poolMu.Lock()
		defer fixture.preflight.poolMu.Unlock()
		cleanedBeforeRelease.Store(fixture.preflight.cancelCalls == 1 && !fixture.preflight.prepared)
	}
	conn, _, err := dialVoiceLive(fixture.ctx, fixture.server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writeVoiceLivePreflight(t, fixture.ctx, conn, 31)
	ready := readVoiceLiveJSON(t, fixture.ctx, conn)
	if ready["type"] != "preflight-ready" {
		t.Fatalf("preflight ready=%#v", ready)
	}
	leaseID, _ := ready["leaseId"].(string)
	writeVoiceLiveActivation(t, fixture.ctx, conn, leaseID, 31)
	commitVoiceLiveCompletionTurn(t, fixture.ctx, conn, false)
	_, _, err = conn.Read(fixture.ctx)
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("terminal error=%v", err)
	}
	_ = awaitBlockedVoiceLiveCompletionLog(t, fixture)
	assertVoiceLiveCompletionSideEffects(t, fixture, false)
	if !cleanedBeforeRelease.Load() {
		t.Fatal("old prepared-session cleanup did not precede lease release")
	}
	nextLease, err := fixture.leases.MemoryVoiceLiveLeaseManager.Acquire(
		fixture.ctx, "user-123", time.Now(), time.Minute,
	)
	if err != nil {
		t.Fatalf("next turn could not acquire released UID: %v", err)
	}
	defer nextLease.Release(context.Background())
	if err := fixture.preflight.PrepareLive(fixture.ctx, "user-123", time.Minute); err != nil {
		t.Fatal(err)
	}
	defer fixture.preflight.CancelPreparedLive("user-123")
	finishBlockedVoiceLiveCompletion(t, fixture)
	assertVoiceLiveCompletionSideEffects(t, fixture, false)
	fixture.preflight.poolMu.Lock()
	prepared, cancelCalls := fixture.preflight.prepared, fixture.preflight.cancelCalls
	fixture.preflight.poolMu.Unlock()
	if !prepared || cancelCalls != 1 {
		t.Fatalf("old cleanup touched next prepared session: prepared=%t cancel calls=%d", prepared, cancelCalls)
	}
}

func TestVoiceLiveCompletionLogPreservesPreCleanupTimingBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		completed := make(chan slog.Record, 1)
		server := &Server{logger: slog.New(voiceLiveCompletionLogHandler{completed: completed})}
		started := time.Now()
		time.Sleep(20 * time.Millisecond)
		completedAt := time.Now()
		// Represent a slow close handshake/lease store after optional proof.
		time.Sleep(5 * time.Second)
		server.logVoiceLiveSessionAt(
			context.Background(), started, completedAt, 2,
			started.Add(5*time.Millisecond), started.Add(10*time.Millisecond),
			1, voiceLivePCMFrameBytes, &voiceLiveOutputMetrics{}, emptyVoiceLiveTimings(), false,
		)
		record := <-completed
		want := map[string]int64{"total_ms": 20, "first_input_pcm_ms": 5, "commit_ms": 10}
		record.Attrs(func(attr slog.Attr) bool {
			if expected, ok := want[attr.Key]; ok {
				if attr.Value.Int64() != expected {
					t.Errorf("%s=%d want=%d", attr.Key, attr.Value.Int64(), expected)
				}
				delete(want, attr.Key)
			}
			return true
		})
		if len(want) != 0 {
			t.Fatalf("missing timing fields: %v", want)
		}
	})
}
