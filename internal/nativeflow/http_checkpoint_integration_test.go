package nativeflow

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
	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/evaluation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
	"github.com/furukawa1020/conclution-ai-teacher/internal/store"
	"github.com/furukawa1020/conclution-ai-teacher/internal/voiceflow"
	"google.golang.org/genai"
)

const checkpointIntegrationUID = "local-development-user"

type checkpointIntegrationGenerator struct {
	mu    sync.Mutex
	calls int
}

func (generator *checkpointIntegrationGenerator) GenerateContent(
	context.Context,
	string,
	[]*genai.Content,
	*genai.GenerateContentConfig,
) (*genai.GenerateContentResponse, error) {
	generator.mu.Lock()
	generator.calls++
	generator.mu.Unlock()
	return nil, errors.New("checkpoint integration unexpectedly invoked a model")
}

func (generator *checkpointIntegrationGenerator) callCount() int {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	return generator.calls
}

type checkpointIntegrationSpeech struct {
	mu              sync.Mutex
	transcribeCalls int
}

func (speech *checkpointIntegrationSpeech) Transcribe(
	context.Context,
	[]byte,
) (string, float32, error) {
	speech.mu.Lock()
	speech.transcribeCalls++
	speech.mu.Unlock()
	return "", 0, errors.New("caption handoff must not run a second recognizer")
}

func (*checkpointIntegrationSpeech) Synthesize(
	context.Context,
	string,
) ([]byte, string, error) {
	return []byte{1, 0, 2, 0}, speechio.StreamingAudioContentType, nil
}

func (*checkpointIntegrationSpeech) StreamSynthesize(
	ctx context.Context,
	text string,
	onChunk speechio.StreamChunkHandler,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" || onChunk == nil {
		return "", errors.New("invalid checkpoint synthesis")
	}
	if err := onChunk([]byte{1, 0, 2, 0}); err != nil {
		return "", err
	}
	return speechio.StreamingAudioContentType, nil
}

func (speech *checkpointIntegrationSpeech) transcriptionCount() int {
	speech.mu.Lock()
	defer speech.mu.Unlock()
	return speech.transcribeCalls
}

type checkpointIntegrationOpener struct {
	mu       sync.Mutex
	sessions []nativevoice.Session
}

func (opener *checkpointIntegrationOpener) Open(
	context.Context,
) (nativevoice.Session, error) {
	opener.mu.Lock()
	defer opener.mu.Unlock()
	if len(opener.sessions) == 0 {
		return nil, errors.New("no Native Audio integration session")
	}
	session := opener.sessions[0]
	opener.sessions = opener.sessions[1:]
	return session, nil
}

type tamperedCheckpointLiveService struct {
	inner       *Service
	replacement string
}

func (service *tamperedCheckpointLiveService) ProcessLive(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
) (httpapi.VoiceTurnResult, error) {
	return service.inner.ProcessLive(ctx, uid, input, audio, onAudio)
}

func (service *tamperedCheckpointLiveService) ProcessLiveWithControl(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
	onEndpoint func(),
	onCoachActive func(httpapi.VoiceRespondentCheckpointTransition) error,
) (httpapi.VoiceTurnResult, error) {
	return service.inner.ProcessLiveWithControl(
		ctx,
		uid,
		input,
		audio,
		onAudio,
		onEndpoint,
		func(transition httpapi.VoiceRespondentCheckpointTransition) error {
			transition.PreviousSessionState = service.replacement
			return onCoachActive(transition)
		},
	)
}

type checkpointIntegrationFrame struct {
	kind string
	body map[string]any
	pcm  []byte
}

func TestNativeCaptionCheckpointUsesPreparedStateAtHTTPBoundary(t *testing.T) {
	for _, test := range []struct {
		name       string
		continuing bool
		tamper     bool
	}{
		{name: "initial"},
		{name: "continuing", continuing: true},
		{name: "tampered prepared state", continuing: true, tamper: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			generator := &checkpointIntegrationGenerator{}
			agent, err := conversation.NewAgent(
				generator,
				conversation.DefaultFastModel,
				conversation.DefaultPrecisionModel,
				bytes.Repeat([]byte{0x42}, 32),
			)
			if err != nil {
				t.Fatal(err)
			}
			preparer, ok := agent.(conversation.NativeStatePreparer)
			if !ok {
				t.Fatal("real conversation agent has no native state preparer")
			}
			validator, ok := agent.(httpapi.VoiceRespondentCheckpointValidator)
			if !ok {
				t.Fatal("real conversation agent has no checkpoint validator")
			}
			refresher, ok := agent.(conversation.StateTokenRefresher)
			if !ok {
				t.Fatal("real conversation agent has no state refresher")
			}

			requestState := ""
			if test.continuing {
				requestState, err = refresher.RefreshStateToken(
					checkpointIntegrationUID,
					"",
				)
				if err != nil {
					t.Fatalf("create continuing state: %v", err)
				}
			}
			speech := &checkpointIntegrationSpeech{}
			staged, err := voiceflow.New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			const caption = "My manager asked why this change was needed. How should I answer?"
			if !requiresRespondentCoach(caption) || !nativeAudioEligible(caption) {
				t.Fatal("integration caption must enter native respondent handoff")
			}
			opener := &checkpointIntegrationOpener{sessions: []nativevoice.Session{
				newScriptedSession(nativeCaptionEvent(caption)),
			}}
			nativeService, err := NewWithCaptionHandoff(opener, preparer, staged)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = nativeService.Close() })

			var liveService httpapi.VoiceTurnLiveService = nativeService
			if test.tamper {
				replacement, refreshErr := refresher.RefreshStateToken(
					checkpointIntegrationUID,
					"",
				)
				if refreshErr != nil {
					t.Fatalf("create signed tamper state: %v", refreshErr)
				}
				liveService = &tamperedCheckpointLiveService{
					inner:       nativeService,
					replacement: replacement,
				}
			}

			server := newNativeCheckpointHTTPServer(
				t,
				staged,
				liveService,
				validator,
			)
			frames := runNativeCheckpointHTTPVoiceTurn(t, server.URL, requestState)
			if test.tamper {
				assertTamperedCheckpointRejected(t, frames)
			} else {
				assertCheckpointAcceptedBeforeAudio(t, frames)
			}
			if generator.callCount() != 0 {
				t.Fatalf("model calls=%d, want 0", generator.callCount())
			}
			if speech.transcriptionCount() != 0 {
				t.Fatalf("second recognizer calls=%d, want 0", speech.transcriptionCount())
			}
		})
	}
}

func newNativeCheckpointHTTPServer(
	t *testing.T,
	staged httpapi.VoiceTurnService,
	native httpapi.VoiceTurnLiveService,
	validator httpapi.VoiceRespondentCheckpointValidator,
) *httptest.Server {
	t.Helper()
	uidLimiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 20, PerDay: 40})
	if err != nil {
		t.Fatal(err)
	}
	appLimiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 20, PerDay: 40})
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewWithVoice(
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		identity.DevelopmentVerifier{},
		uidLimiter,
		evaluation.DevelopmentEvaluator{},
		store.MemoryEvaluationStore{},
		time.Second,
		4*1024,
		httpapi.VoiceOptions{
			Service:              staged,
			NativeLiveService:    native,
			RateLimiter:          uidLimiter,
			AppRateLimiter:       appLimiter,
			LiveLeaseManager:     guard.NewMemoryVoiceLiveLeaseManager(),
			LiveHandshakeGate:    httpapi.NewVoiceLiveHandshakeGate(2),
			CoachStateValidator:  validator,
			RequestTimeout:       3 * time.Second,
			MaxRequestBytes:      13 * 1024 * 1024,
			RequireRecentPasskey: false,
		},
	)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func runNativeCheckpointHTTPVoiceTurn(
	t *testing.T,
	serverURL string,
	state string,
) []checkpointIntegrationFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header := make(http.Header)
	header.Set("Origin", "https://kotae-ai.web.app")
	conn, _, err := websocket.Dial(
		ctx,
		"ws"+strings.TrimPrefix(serverURL, "http")+"/api/v1/voice/live",
		&websocket.DialOptions{HTTPHeader: header},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	start := map[string]any{
		"type":                    "start",
		"version":                 1,
		"idToken":                 "header.payload.signature",
		"appCheckToken":           "header.payload.signature",
		"nativeCoachControl":      true,
		"sessionState":            state,
		"turnMode":                "intentional",
		"sampleRateHz":            16_000,
		"strictCloudMinimization": false,
		"nativeAudio":             true,
	}
	writeCheckpointIntegrationJSON(t, ctx, conn, start)
	ready := readCheckpointIntegrationFrame(t, ctx, conn)
	if ready.kind != "ready" {
		t.Fatalf("ready frame=%#v", ready)
	}
	if err := conn.Write(
		ctx,
		websocket.MessageBinary,
		make([]byte, nativevoice.InputFrameBytes),
	); err != nil {
		t.Fatal(err)
	}
	writeCheckpointIntegrationJSON(t, ctx, conn, map[string]any{
		"type": "commit", "version": 1,
	})

	frames := make([]checkpointIntegrationFrame, 0, 4)
	for len(frames) < 4 {
		frame := readCheckpointIntegrationFrame(t, ctx, conn)
		frames = append(frames, frame)
		if frame.kind == "final" || frame.kind == "error" {
			return frames
		}
	}
	t.Fatalf("voice turn did not terminate: %#v", frames)
	return nil
}

func writeCheckpointIntegrationJSON(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	value any,
) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

func readCheckpointIntegrationFrame(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
) checkpointIntegrationFrame {
	t.Helper()
	messageType, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType == websocket.MessageBinary {
		return checkpointIntegrationFrame{kind: "audio", pcm: payload}
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("decode frame: %v payload=%q", err, payload)
	}
	kind, _ := body["type"].(string)
	return checkpointIntegrationFrame{kind: kind, body: body}
}

func assertCheckpointAcceptedBeforeAudio(
	t *testing.T,
	frames []checkpointIntegrationFrame,
) {
	t.Helper()
	if len(frames) != 3 || frames[0].kind != "coach" ||
		frames[1].kind != "audio" || frames[2].kind != "final" ||
		len(frames[1].pcm) == 0 {
		t.Fatalf("frames=%#v", frames)
	}
	coachState, _ := frames[0].body["sessionState"].(string)
	result, _ := frames[2].body["result"].(map[string]any)
	finalState, _ := result["sessionState"].(string)
	if coachState == "" || coachState != finalState ||
		frames[0].body["assistanceTarget"] != "respondent" {
		t.Fatalf("coach=%#v final=%#v", frames[0].body, frames[2].body)
	}
}

func assertTamperedCheckpointRejected(
	t *testing.T,
	frames []checkpointIntegrationFrame,
) {
	t.Helper()
	if len(frames) != 1 || frames[0].kind != "error" ||
		frames[0].body["code"] != "voice_api_unavailable" {
		t.Fatalf("tampered checkpoint frames=%#v", frames)
	}
}
