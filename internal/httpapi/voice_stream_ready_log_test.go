package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
)

type voiceStreamReadyLogHandler struct {
	records chan slog.Record
	release <-chan struct{}
}

func (handler voiceStreamReadyLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (handler voiceStreamReadyLogHandler) Handle(_ context.Context, record slog.Record) error {
	handler.records <- record.Clone()
	if handler.release != nil {
		<-handler.release
	}
	return nil
}

func (handler voiceStreamReadyLogHandler) WithAttrs([]slog.Attr) slog.Handler { return handler }
func (handler voiceStreamReadyLogHandler) WithGroup(string) slog.Handler      { return handler }

type voiceStreamReadyLogService struct {
	fakeStreamingVoiceService
	beforeAudio func()
}

func (service *voiceStreamReadyLogService) ProcessStream(
	ctx context.Context, uid string, input VoiceTurnInput, onAudio func([]byte) error,
) (VoiceTurnResult, error) {
	service.beforeAudio()
	return service.fakeStreamingVoiceService.ProcessStream(ctx, uid, input, onAudio)
}

type voiceStreamReadyLogResponse struct {
	*httptest.ResponseRecorder
	flushes   int
	failFinal bool
}

func (response *voiceStreamReadyLogResponse) Write(payload []byte) (int, error) {
	if response.failFinal && strings.Contains(string(payload), `"type":"final"`) {
		return 0, errors.New("fixture final write failure")
	}
	return response.ResponseRecorder.Write(payload)
}

func (response *voiceStreamReadyLogResponse) Flush() {
	response.flushes++
	if response.flushes == 1 {
		// The readiness clock must include the ready flush, but not the later
		// pipeline delay. synctest advances this clock without a real sleep.
		time.Sleep(17 * time.Millisecond)
	}
	response.ResponseRecorder.Flush()
}

func newVoiceStreamReadyLogService(beforeAudio func()) *voiceStreamReadyLogService {
	return &voiceStreamReadyLogService{
		beforeAudio: beforeAudio,
		fakeStreamingVoiceService: fakeStreamingVoiceService{
			audio: [][]byte{{64, 0, 65, 0}},
			result: VoiceTurnResult{
				Caption: "short reply", StateToken: "opaque-final-state",
				DetectedDomain: "daily", AssistanceTarget: "assistant",
				RespondentStage: "none", CoachPhase: "none", CoachAction: "none",
				ResearchStatus: "none", ResearchRecords: []ResearchRecord{}, Route: "fast",
			},
		},
	}
}

func newVoiceStreamReadyLogRequest(ctx context.Context) *http.Request {
	request := httptest.NewRequest(http.MethodPost, voiceStreamPath, strings.NewReader(
		`{"audioBase64":"YQ==","mimeType":"audio/webm","sessionState":"","turnMode":"intentional"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	return request.WithContext(context.WithValue(ctx, principalContextKey{}, identity.Principal{
		UID: "user-123", AppID: "app-123",
	}))
}

func newVoiceStreamReadyLogServer(logger *slog.Logger, service VoiceTurnService) *Server {
	return &Server{
		logger: logger,
		voice: VoiceOptions{
			Service: service, RateLimiter: &fakeLimiter{},
			AppRateLimiter: &fakeLimiter{wantKey: "app:app-123"},
			RequestTimeout: time.Second, MaxRequestBytes: 1024,
		},
	}
}

func TestVoiceStreamStartsPCMBeforeBlockedLog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := make(chan slog.Record, 8)
		release := make(chan struct{})
		started := make(chan struct{})
		service := newVoiceStreamReadyLogService(func() {
			close(started)
			time.Sleep(31 * time.Millisecond)
		})
		server := newVoiceStreamReadyLogServer(slog.New(voiceStreamReadyLogHandler{
			records: records, release: release,
		}), service)
		response := &voiceStreamReadyLogResponse{ResponseRecorder: httptest.NewRecorder()}
		done := make(chan struct{})
		go func() {
			server.voiceTurnStream(response, newVoiceStreamReadyLogRequest(context.Background()))
			close(done)
		}()
		defer func() {
			close(release)
			<-done
		}()
		record := <-records
		synctest.Wait()
		select {
		case <-started:
		default:
			t.Fatal("ProcessStream waited for the stopped log sink")
		}
		if response.flushes != 3 || !strings.Contains(response.Body.String(), `"type":"audio"`) {
			t.Fatal("PCM was not flushed before the stopped log sink")
		}
		if record.Message != "voice stream completed" {
			t.Fatalf("unexpected pre-completion log=%q", record.Message)
		}
		assertVoiceStreamReadyLog(t, record)
		select {
		case <-done:
			t.Fatal("fixture did not hold the completion log")
		default:
		}
	})
}

func assertVoiceStreamReadyLog(t *testing.T, record slog.Record) {
	t.Helper()
	var duration slog.Value
	record.Attrs(func(attr slog.Attr) bool {
		for _, private := range []string{"short reply", "opaque-final-state", "private provider diagnostic"} {
			if strings.Contains(attr.Value.String(), private) {
				t.Fatalf("log attribute %q exposed private fixture content", attr.Key)
			}
		}
		if attr.Key == "ready_duration_ms" {
			duration = attr.Value
		}
		return true
	})
	if duration.Kind() != slog.KindInt64 || duration.Int64() != 17 {
		t.Fatalf("ready_duration_ms=%v want=17 at the ready flush boundary", duration)
	}
}

func TestVoiceStreamOutcomeLogsKeepReadyBoundary(t *testing.T) {
	for _, test := range []struct {
		name    string
		message string
	}{
		{"completed", "voice stream completed"},
		{"cancelled", "voice stream cancelled"},
		{"failed", "voice stream failed"},
		{"result rejected", "voice stream result rejected"},
		{"final write failed", "voice stream final write failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				records := make(chan slog.Record, 8)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				service := newVoiceStreamReadyLogService(func() {
					time.Sleep(31 * time.Millisecond)
					if test.name == "cancelled" {
						cancel()
					}
				})
				if test.name == "failed" {
					service.err = errors.New("private provider diagnostic")
				}
				if test.name == "result rejected" {
					service.result.StateToken = ""
				}
				server := newVoiceStreamReadyLogServer(slog.New(voiceStreamReadyLogHandler{records: records}), service)
				response := &voiceStreamReadyLogResponse{
					ResponseRecorder: httptest.NewRecorder(),
					failFinal:        test.name == "final write failed",
				}
				server.voiceTurnStream(response, newVoiceStreamReadyLogRequest(ctx))
				if len(records) != 1 {
					t.Fatalf("log count=%d want=1 outcome log only", len(records))
				}
				record := <-records
				if record.Message != test.message {
					t.Fatalf("log message=%q want=%q", record.Message, test.message)
				}
				assertVoiceStreamReadyLog(t, record)
			})
		})
	}
}
