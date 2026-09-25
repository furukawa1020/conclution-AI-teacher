package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
)

func TestVoiceStreamFlushesFirstTwentyMillisecondsBeforeProviderRemainder(
	t *testing.T,
) {
	t.Parallel()

	audio := make([]byte, voiceStreamFirstAudioFrameBytes+200)
	for index := range audio {
		audio[index] = byte(index % 251)
	}
	service := &fakeStreamingVoiceService{
		audio: [][]byte{audio},
		result: VoiceTurnResult{
			Caption:          "短い返答です。",
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
		guard.Limiter(&fakeLimiter{}),
		guard.Limiter(&fakeLimiter{wantKey: "app:app-123"}),
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

	lines := strings.Split(strings.TrimSpace(response.Body.String()), "\n")
	if response.Code != http.StatusOK || len(lines) != 4 {
		t.Fatalf("status=%d frames=%d body=%s", response.Code, len(lines), response.Body.String())
	}
	var restored []byte
	for index, line := range lines[1:3] {
		var frame voiceStreamFrame
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(frame.AudioBase64)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Sequence == nil || *frame.Sequence != index {
			t.Fatalf("frame %d sequence=%v", index, frame.Sequence)
		}
		if index == 0 && len(decoded) != voiceStreamFirstAudioFrameBytes {
			t.Fatalf("first audio bytes=%d want=%d", len(decoded), voiceStreamFirstAudioFrameBytes)
		}
		restored = append(restored, decoded...)
	}
	if !bytes.Equal(restored, audio) {
		t.Fatal("split HTTP PCM did not reconstruct the provider chunk")
	}
}
