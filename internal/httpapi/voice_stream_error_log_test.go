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
	"testing"
	"testing/synctest"
	"time"
)

type voiceStreamErrorLogResponse struct {
	*voiceStreamReadyLogResponse
	errorFlushes int
}

func (response *voiceStreamErrorLogResponse) Flush() {
	if strings.Contains(response.Body.String(), `"type":"error"`) {
		// Error delivery may itself be slow. Keep the existing outcome clock
		// boundary before this I/O, while the logger runs only after the flush.
		time.Sleep(29 * time.Millisecond)
		response.errorFlushes++
	}
	response.voiceStreamReadyLogResponse.Flush()
}

func TestVoiceStreamErrorsFlushBeforeBlockedLog(t *testing.T) {
	for _, test := range []struct {
		name       string
		strict     bool
		message    string
		code       string
		flushes    int
		durationMS int64
	}{
		{name: "timeout", message: "voice stream cancelled", code: "voice_turn_timeout", flushes: 2, durationMS: 1017},
		{name: "pipeline failure", message: "voice stream failed", code: "voice_turn_unavailable", flushes: 3, durationMS: 48},
		{name: "invalid result", message: "voice stream result rejected", code: "voice_turn_unavailable", flushes: 3},
		{name: "strict pipeline failure", strict: true, message: "voice stream failed", code: "voice_turn_unavailable", flushes: 2, durationMS: 48},
		{name: "strict invalid PCM", strict: true, message: "voice stream failed", code: "voice_turn_unavailable", flushes: 2, durationMS: 48},
		{name: "strict blocked result after PCM", strict: true, message: "voice stream result rejected", code: "voice_turn_unavailable", flushes: 2},
		{name: "cancelled", message: "voice stream cancelled", flushes: 1, durationMS: 48},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				records := make(chan slog.Record, 8)
				release := make(chan struct{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				service := newVoiceStreamReadyLogService(func() {
					if test.name == "timeout" {
						time.Sleep(time.Second)
						return
					}
					time.Sleep(31 * time.Millisecond)
					if test.name == "cancelled" {
						cancel()
					}
				})
				switch test.name {
				case "pipeline failure", "strict pipeline failure":
					service.err = errors.New("private provider diagnostic")
				case "invalid result":
					service.result.StateToken = ""
				case "strict invalid PCM":
					service.audio = [][]byte{{64}}
				case "strict blocked result after PCM":
					service.result = VoiceTurnResult{
						DetectedDomain: "unknown", AssistanceTarget: "assistant",
						RespondentStage: "none", CoachPhase: "none", CoachAction: "none",
						ResearchStatus: "none", ResearchRecords: []ResearchRecord{},
						PrivacyStatus: "blocked", Route: "strict-privacy-blocked",
					}
				}
				server := newVoiceStreamReadyLogServer(slog.New(voiceStreamReadyLogHandler{
					records: records, release: release,
				}), service)
				request := newVoiceStreamReadyLogRequest(ctx)
				if test.strict {
					request.Body = io.NopCloser(strings.NewReader(
						`{"audioBase64":"YQ==","mimeType":"audio/webm","sessionState":"",` +
							`"turnMode":"intentional","strictCloudMinimization":true}`,
					))
				}
				response := &voiceStreamErrorLogResponse{
					voiceStreamReadyLogResponse: &voiceStreamReadyLogResponse{
						ResponseRecorder: httptest.NewRecorder(),
					},
				}
				done := make(chan struct{})
				go func() {
					server.voiceTurnStream(response, request)
					close(done)
				}()
				defer func() {
					close(release)
					<-done
				}()
				record := <-records
				synctest.Wait()
				if record.Message != test.message {
					t.Fatalf("outcome log=%q want=%q", record.Message, test.message)
				}
				assertVoiceStreamReadyLog(t, record)
				if test.durationMS != 0 {
					var duration slog.Value
					record.Attrs(func(attr slog.Attr) bool {
						if attr.Key == "duration_ms" {
							duration = attr.Value
						}
						return true
					})
					if duration.Kind() != slog.KindInt64 || duration.Int64() != test.durationMS {
						t.Fatalf("duration_ms=%v want=%d before error I/O", duration, test.durationMS)
					}
				}
				select {
				case <-done:
					t.Fatal("fixture did not hold the outcome log")
				default:
				}
				body := response.Body.String()
				if response.Code != http.StatusOK || response.flushes != test.flushes {
					t.Fatalf("error delivery waited for the stopped log sink: status=%d flushes=%d want=%d body=%s",
						response.Code, response.flushes, test.flushes, body)
				}
				if test.code == "" {
					if response.errorFlushes != 0 || strings.Contains(body, `"type":"error"`) {
						t.Fatal("plain cancellation published an error frame")
					}
				} else {
					lines := strings.Split(strings.TrimSpace(body), "\n")
					var terminal map[string]any
					if err := json.Unmarshal([]byte(lines[len(lines)-1]), &terminal); err != nil {
						t.Fatalf("decode terminal frame: %v", err)
					}
					if response.errorFlushes != 1 || len(terminal) != 3 ||
						terminal["type"] != "error" || terminal["version"] != float64(voiceStreamVersion) ||
						terminal["code"] != test.code {
						t.Fatalf("error was not flushed before blocked logging: body=%s", body)
					}
				}
				for _, forbidden := range []string{
					`"type":"final"`, "sessionState", "opaque-final-state", "short reply", "private provider diagnostic",
				} {
					if strings.Contains(body, forbidden) {
						t.Fatalf("failed stream exposed %q: %s", forbidden, body)
					}
				}
				if test.strict && strings.Contains(body, `"type":"audio"`) {
					t.Fatalf("strict stream released PCM before validation: %s", body)
				}
			})
		})
	}
}
