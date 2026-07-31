package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
)

type fakeStreamingVoiceService struct {
	input  VoiceTurnInput
	result VoiceTurnResult
	audio  [][]byte
	err    error
	calls  int
}

func (service *fakeStreamingVoiceService) Process(
	_ context.Context,
	_ string,
	_ VoiceTurnInput,
) (VoiceTurnResult, error) {
	return VoiceTurnResult{}, errors.New("buffered voice endpoint was called")
}

func (service *fakeStreamingVoiceService) ProcessStream(
	ctx context.Context,
	uid string,
	input VoiceTurnInput,
	onAudio func([]byte) error,
) (VoiceTurnResult, error) {
	service.calls++
	if uid != "user-123" {
		return VoiceTurnResult{}, errors.New("unexpected uid")
	}
	service.input = input
	for _, chunk := range service.audio {
		if err := ctx.Err(); err != nil {
			return VoiceTurnResult{}, err
		}
		if err := onAudio(chunk); err != nil {
			return VoiceTurnResult{}, err
		}
	}
	return service.result, service.err
}

func testStreamingVoiceHandler(
	service VoiceTurnService,
	limiter guard.Limiter,
	appLimiter guard.Limiter,
) http.Handler {
	return NewWithVoice(
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		fakeVerifier{principal: identity.Principal{
			UID:   "user-123",
			AppID: "app-123",
			Roles: map[string]bool{"user": true},
		}},
		&fakeLimiter{},
		&fakeEvaluator{},
		&fakeStore{},
		2*time.Second,
		4*1024,
		VoiceOptions{
			Service:         service,
			RateLimiter:     limiter,
			AppRateLimiter:  appLimiter,
			RequestTimeout:  2 * time.Second,
			MaxRequestBytes: 13 * 1024 * 1024,
		},
	)
}

func TestVoiceStreamPublishesStateOnlyAfterOrderedPCM(t *testing.T) {
	t.Parallel()

	chunk0 := []byte{0, 0, 1, 0}
	chunk1 := []byte{2, 0, 3, 0}
	service := &fakeStreamingVoiceService{
		audio: [][]byte{chunk0, chunk1},
		result: VoiceTurnResult{
			Caption:          "Aです。理由を続けます。",
			StateToken:       "opaque-final-state",
			DetectedDomain:   "daily",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			CoachPhase:       "none",
			CoachAction:      "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "fast",
		},
	}
	handler := testStreamingVoiceHandler(
		service,
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	body := `{"audioBase64":"YXVkaW8=","mimeType":"audio/webm",` +
		`"sessionState":"","turnMode":"intentional"}`
	request := authenticatedRequest(http.MethodPost, voiceStreamPath, body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != allowedWebOrigin ||
		response.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" ||
		response.Header().Get("Content-Type") != "application/x-ndjson; charset=utf-8" {
		t.Fatalf("stream headers=%v", response.Header())
	}
	lines := strings.Split(strings.TrimSpace(response.Body.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("frames=%d body=%s", len(lines), response.Body.String())
	}
	var frames []map[string]any
	for _, line := range lines {
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("decode frame: %v: %s", err, line)
		}
		frames = append(frames, frame)
	}
	if frames[0]["type"] != "ready" ||
		frames[1]["type"] != "audio" ||
		frames[1]["sequence"] != float64(0) ||
		frames[1]["audioBase64"] != base64.StdEncoding.EncodeToString(chunk0) ||
		frames[2]["type"] != "audio" ||
		frames[2]["sequence"] != float64(1) ||
		frames[3]["type"] != "final" {
		t.Fatalf("unexpected frames=%#v", frames)
	}
	for index := 0; index < 3; index++ {
		if strings.Contains(lines[index], "opaque-final-state") {
			t.Fatalf("state escaped before final frame %d: %s", index, lines[index])
		}
	}
	if !strings.Contains(lines[3], `"sessionState":"opaque-final-state"`) ||
		!strings.Contains(lines[3], `"audioMimeType":"audio/L16"`) ||
		!strings.Contains(lines[3], `"coachPhase":"none"`) ||
		!strings.Contains(lines[3], `"coachAction":"none"`) {
		t.Fatalf("invalid final frame: %s", lines[3])
	}
	if service.calls != 1 ||
		service.input.RequestID == "" ||
		service.input.TurnMode != VoiceTurnIntentional {
		t.Fatalf("service calls=%d input=%+v", service.calls, service.input)
	}
}

func TestVoiceStreamFailureNeverPublishesStateOrPrivateError(t *testing.T) {
	t.Parallel()

	service := &fakeStreamingVoiceService{
		audio: [][]byte{{0, 0}},
		err:   errors.New("provider SECRET transcript"),
	}
	handler := testStreamingVoiceHandler(
		service,
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	request := authenticatedRequest(
		http.MethodPost,
		voiceStreamPath,
		`{"audioBase64":"YQ==","mimeType":"audio/webm",`+
			`"sessionState":"STATE-SECRET","turnMode":"intentional"}`,
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK ||
		!strings.Contains(body, `"type":"error"`) ||
		!strings.Contains(body, `"code":"voice_turn_unavailable"`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	for _, forbidden := range []string{
		"provider SECRET transcript",
		"STATE-SECRET",
		"user-123",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("stream exposed %q: %s", forbidden, body)
		}
	}
}

func TestVoiceStreamAcceptsSilentFinalWithEmptyAudioMIME(t *testing.T) {
	t.Parallel()

	service := &fakeStreamingVoiceService{
		result: VoiceTurnResult{
			StateToken:       "opaque-final-state",
			DetectedDomain:   "daily",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			CoachPhase:       "none",
			CoachAction:      "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "ambient-silent",
		},
	}
	handler := testStreamingVoiceHandler(
		service,
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	request := authenticatedRequest(
		http.MethodPost,
		voiceStreamPath,
		`{"audioBase64":"YQ==","mimeType":"audio/webm",`+
			`"sessionState":"","turnMode":"ambient"}`,
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK ||
		!strings.Contains(body, `"type":"final"`) ||
		!strings.Contains(body, `"audioMimeType":""`) ||
		strings.Contains(body, `"type":"audio"`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}

func TestVoiceStreamRejectsTheChunkAfterTheSharedCeiling(t *testing.T) {
	t.Parallel()

	audio := make([][]byte, voiceStreamMaxChunks+1)
	for index := range audio {
		audio[index] = []byte{0, 0}
	}
	service := &fakeStreamingVoiceService{
		audio: audio,
		result: VoiceTurnResult{
			Caption:          "Aです。",
			StateToken:       "must-not-be-published",
			DetectedDomain:   "daily",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			CoachPhase:       "none",
			CoachAction:      "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "fast",
		},
	}
	handler := testStreamingVoiceHandler(
		service,
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	request := authenticatedRequest(
		http.MethodPost,
		voiceStreamPath,
		`{"audioBase64":"YQ==","mimeType":"audio/webm",`+
			`"sessionState":"","turnMode":"intentional"}`,
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if count := strings.Count(body, `"type":"audio"`); count !=
		voiceStreamMaxChunks {
		t.Fatalf("audio frames=%d want=%d", count, voiceStreamMaxChunks)
	}
	if !strings.Contains(body, `"type":"error"`) ||
		strings.Contains(body, `"type":"final"`) ||
		strings.Contains(body, "must-not-be-published") {
		t.Fatalf("overflow stream did not fail closed: %s", body)
	}
}

func TestVoiceStreamCORSPreflightIsExact(t *testing.T) {
	t.Parallel()

	handler := testStreamingVoiceHandler(
		&fakeStreamingVoiceService{},
		&fakeLimiter{},
		&fakeLimiter{wantKey: "app:app-123"},
	)
	tests := []struct {
		name    string
		origin  string
		headers string
		want    int
	}{
		{
			name:    "exact",
			origin:  allowedWebOrigin,
			headers: "authorization, content-type, x-firebase-appcheck",
			want:    http.StatusNoContent,
		},
		{
			name:    "foreign origin",
			origin:  "https://evil.example",
			headers: "authorization, content-type, x-firebase-appcheck",
			want:    http.StatusForbidden,
		},
		{
			name:    "extra header",
			origin:  allowedWebOrigin,
			headers: "authorization, content-type, x-firebase-appcheck, x-extra",
			want:    http.StatusForbidden,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodOptions, voiceStreamPath, nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Access-Control-Request-Method", http.MethodPost)
			request.Header.Set("Access-Control-Request-Headers", test.headers)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.want == http.StatusNoContent &&
				response.Header().Get("Access-Control-Allow-Origin") != allowedWebOrigin {
				t.Fatalf("preflight headers=%v", response.Header())
			}
		})
	}
}
