package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type preparingHTTPSpeech struct {
	preparingStreamingSpeech
	transcriptionGate <-chan struct{}
}

func (speech *preparingHTTPSpeech) Transcribe(ctx context.Context, audio []byte) (string, float32, error) {
	select {
	case <-ctx.Done():
		return "", 0, ctx.Err()
	case <-speech.transcriptionGate:
		return speech.fakeSpeech.Transcribe(ctx, audio)
	}
}

func TestHTTPStreamingPreparesWhileRecognitionIsPending(t *testing.T) {
	for _, test := range []struct {
		name                                           string
		slowPreparation, cancelRecognition, silent     bool
		modelError, preparationError, recognitionError error
	}{
		{name: "ready"},
		{name: "slow preparation", slowPreparation: true},
		{name: "expired preparation", preparationError: context.DeadlineExceeded},
		{name: "cancel during recognition", cancelRecognition: true},
		{name: "silent decision", silent: true},
		{name: "model failure", modelError: errors.New("model unavailable")},
		{name: "recognition failure", recognitionError: errors.New("recognition unavailable")},
		{name: "recognition miss", recognitionError: speechio.ErrNoSpeech},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gate := make(chan struct{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				prepared := &fakePreparedSynthesis{chunks: [][]byte{{80, 0}}, err: test.preparationError}
				// Expiration happens before any PCM; never mix a partially emitted
				// prepared response with the ordinary provider on retry.
				if test.preparationError != nil {
					prepared.chunks = nil
				}
				speech := &preparingHTTPSpeech{
					preparingStreamingSpeech: preparingStreamingSpeech{
						fakeStreamingSpeech: fakeStreamingSpeech{fakeSpeech: fakeSpeech{transcript: "こんにちは", confidence: .99}, chunks: [][]byte{{90, 0}}},
						prepared:            prepared, prepareStarted: make(chan struct{}), prepareCanceled: make(chan struct{}), blockPrepare: test.slowPreparation,
					},
					transcriptionGate: gate,
				}
				decision := liveTestDecision("こんにちは。", "final-state")
				speech.transcribeErr = test.recognitionError
				if test.silent {
					decision.SpokenReply = ""
				}
				agent := &fakeAgent{result: decision, err: test.modelError}
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				type outcome struct {
					result httpapi.VoiceTurnResult
					err    error
				}
				done := make(chan outcome, 1)
				var output []byte
				go func() {
					result, err := pipeline.ProcessStream(ctx, "http-ready-test", httpapi.VoiceTurnInput{Audio: []byte("audio")}, func(chunk []byte) error {
						output = append(output, chunk...)
						return nil
					})
					done <- outcome{result, err}
				}()
				synctest.Wait()
				select {
				case <-speech.prepareStarted:
				default:
					t.Fatal("HTTP fallback did not prepare TTS while recognition was pending")
				}
				if prepared.calls != 0 || speech.streamCalls != 0 || agent.calls != 0 || len(output) != 0 {
					t.Fatal("content crossed configuration-only preparation before recognition")
				}
				if test.cancelRecognition {
					cancel()
				}
				close(gate)
				synctest.Wait()
				var finished outcome
				select {
				case finished = <-done:
				default:
					t.Fatal("HTTP response waited for unfinished configuration")
				}
				wantError := test.cancelRecognition || test.modelError != nil ||
					(test.recognitionError != nil && !errors.Is(test.recognitionError, speechio.ErrNoSpeech))
				if (finished.err != nil) != wantError {
					t.Fatalf("error=%v", finished.err)
				}
				if wantError || test.silent {
					if prepared.calls != 0 || speech.streamCalls != 0 || len(output) != 0 {
						t.Fatal("failed or silent turn generated PCM")
					}
				} else {
					wantPCM, wantPrepared, wantOrdinary := []byte{80, 0}, 1, 0
					if test.slowPreparation || test.preparationError != nil {
						wantPCM, wantOrdinary = []byte{90, 0}, 1
					}
					if test.slowPreparation {
						wantPrepared = 0
					}
					wantCaption := decision.SpokenReply
					if errors.Is(test.recognitionError, speechio.ErrNoSpeech) {
						wantCaption = lowConfidencePrompt
					}
					if !bytes.Equal(output, wantPCM) || prepared.calls != wantPrepared || speech.streamCalls != wantOrdinary || finished.result.Caption != wantCaption {
						t.Fatalf("output=%v prepared=%d ordinary=%d result=%+v", output, prepared.calls, speech.streamCalls, finished.result)
					}
				}
				if test.slowPreparation {
					select {
					case <-speech.prepareCanceled:
					default:
						t.Fatal("slow preparation was not canceled")
					}
				} else if !prepared.closed || prepared.ctx.Err() == nil {
					t.Fatal("prepared connection was not reclaimed")
				}
			})
		})
	}
}

func TestHTTPStreamingSkipsIneligiblePreparation(t *testing.T) {
	for _, test := range []struct {
		name     string
		input    httpapi.VoiceTurnInput
		canceled bool
	}{
		{name: "strict", input: httpapi.VoiceTurnInput{StrictCloudMinimization: true, StateToken: "state"}},
		{name: "document", input: httpapi.VoiceTurnInput{Document: &httpapi.VoiceDocument{Data: []byte("private")}}},
		{name: "passive ambient", input: httpapi.VoiceTurnInput{Ambient: true}},
		{name: "already canceled", canceled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if test.canceled {
					cancel()
				}
				speech := &preparingStreamingSpeech{
					fakeStreamingSpeech: fakeStreamingSpeech{fakeSpeech: fakeSpeech{transcript: "こんにちは", confidence: .99}},
					prepared:            &fakePreparedSynthesis{}, prepareStarted: make(chan struct{}),
				}
				pipeline, err := New(speech, &fakeAgent{result: liveTestDecision("", "final-state")})
				if err != nil {
					t.Fatal(err)
				}
				_, _ = pipeline.ProcessStream(ctx, "http-ineligible", test.input, func([]byte) error { t.Error("unexpected PCM"); return nil })
				synctest.Wait()
				select {
				case <-speech.prepareStarted:
					t.Fatal("ineligible turn opened preparatory RPC")
				default:
				}
			})
		})
	}
}
