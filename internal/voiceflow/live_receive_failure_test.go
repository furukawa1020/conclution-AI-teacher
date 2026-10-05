package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type failingReceiveSession struct {
	*fakeLiveTranscriptionSession
	receiveGate <-chan struct{}
	receiveErr  error
	transcript  string
	earlyEvents []speechio.StreamingTranscriptionEvent
}

func (session *failingReceiveSession) RecvEvent() (speechio.StreamingTranscriptionEvent, error) {
	if len(session.earlyEvents) > 0 {
		event := session.earlyEvents[0]
		session.earlyEvents = session.earlyEvents[1:]
		return event, nil
	}
	select {
	case <-session.ctx.Done():
		return speechio.StreamingTranscriptionEvent{}, session.ctx.Err()
	case <-session.receiveGate:
	}
	if session.transcript != "" {
		return speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal, Text: session.transcript,
		}, nil
	}
	return speechio.StreamingTranscriptionEvent{}, session.receiveErr
}

type failingReceiveSpeech struct {
	preparingStreamingSpeech
	session *failingReceiveSession
}

func (speech *failingReceiveSpeech) OpenStreamingTranscription(ctx context.Context) (speechio.StreamingTranscriptionSession, error) {
	speech.session.ctx = ctx
	return speech.session, nil
}

func TestLiveReceiveFailureCancelsSenderBeforeJoining(t *testing.T) {
	for _, test := range []struct {
		name                                       string
		withPCM, closeAudio, blockSend, blockClose bool
		missingDeadline, longTranscript            bool
	}{
		{name: "open input without another frame"},
		{name: "blocked PCM send", withPCM: true, blockSend: true},
		{name: "blocked input close", withPCM: true, closeAudio: true, blockClose: true},
		{name: "completed send without deadline signal", withPCM: true, closeAudio: true, missingDeadline: true},
		{name: "transcript limit with open input", longTranscript: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				receiveGate := make(chan struct{})
				providerErr := errors.New("private provider diagnostic")
				session := &failingReceiveSession{
					fakeLiveTranscriptionSession: newFakeLiveTranscriptionSession(),
					receiveGate:                  receiveGate, receiveErr: providerErr,
				}
				if test.blockSend {
					session.sendGate = make(chan struct{})
					session.sendSeen = make(chan struct{})
				}
				if test.blockClose {
					session.closeGate = make(chan struct{})
				}
				wantCause := providerErr
				if test.longTranscript {
					session.transcript = strings.Repeat("x", conversation.MaxUtteranceRunes+1)
					wantCause = speechio.ErrTranscriptLong
				}
				speech := &failingReceiveSpeech{
					preparingStreamingSpeech: preparingStreamingSpeech{
						blockPrepare: true, prepareStarted: make(chan struct{}), prepareCanceled: make(chan struct{}),
					},
					session: session,
				}
				agent := &fakeAgent{}
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				input := httpapi.VoiceTurnInput{StateToken: "prior-state"}
				if test.missingDeadline {
					input.ProcessingTimeout = time.Second
					input.ProcessingDeadline = make(chan time.Time)
				}
				audio := make(chan []byte, 1)
				pcm := []byte{40, 0}
				if test.withPCM {
					audio <- pcm
				}
				if test.closeAudio {
					close(audio)
				} else {
					defer close(audio)
				}
				var result httpapi.VoiceTurnResult
				var processErr error
				outputCalls := 0
				done := make(chan struct{})
				go func() {
					result, processErr = pipeline.ProcessLive(ctx, "receive-failure", input, audio,
						func([]byte) error { outputCalls++; return nil })
					close(done)
				}()
				synctest.Wait()
				select {
				case <-speech.prepareStarted:
				default:
					t.Fatal("test did not start content-free TTS preparation")
				}
				if test.blockSend {
					select {
					case <-session.sendSeen:
					default:
						t.Fatal("test did not block the PCM sender")
					}
				}
				if test.closeAudio && session.closeCalls != 1 {
					t.Fatal("test did not reach input close")
				}
				close(receiveGate)
				synctest.Wait()
				select {
				case <-done:
				default:
					t.Fatal("known receive failure still waits for input, send, or processing deadline")
				}
				stage, classified := httpapi.VoicePipelineStageOf(processErr)
				if !classified || stage != httpapi.VoicePipelineStageTranscribe {
					t.Fatalf("failure stage=%q classified=%v err=%v", stage, classified, processErr)
				}
				if strings.Contains(processErr.Error(), providerErr.Error()) {
					t.Fatal("provider diagnostic escaped the classified failure")
				}
				if !errors.Is(context.Cause(session.ctx), wantCause) || ctx.Err() != nil {
					t.Fatalf("transcription cause=%v parent=%v", context.Cause(session.ctx), ctx.Err())
				}
				if test.withPCM && !bytes.Equal(pcm, []byte{0, 0}) {
					t.Fatalf("returned before sender cleared its PCM owner: %v", pcm)
				}
				if result.StateToken != "" || result.Caption != "" || agent.calls != 0 || speech.streamCalls != 0 || outputCalls != 0 {
					t.Fatalf("failed recognition published state/content: result=%+v agent=%d tts=%d output=%d", result, agent.calls, speech.streamCalls, outputCalls)
				}
				select {
				case <-speech.prepareCanceled:
				default:
					t.Fatal("receive failure retained pending TTS preparation")
				}
			})
		})
	}
}

func TestLiveReceiveFailureCancelsPendingSpeculation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		receiveGate := make(chan struct{})
		speech := &failingReceiveSpeech{
			preparingStreamingSpeech: preparingStreamingSpeech{
				blockPrepare: true, prepareCanceled: make(chan struct{}),
			},
			session: &failingReceiveSession{
				fakeLiveTranscriptionSession: newFakeLiveTranscriptionSession(),
				receiveGate:                  receiveGate, receiveErr: errors.New("provider failed after endpoint"),
				earlyEvents: []speechio.StreamingTranscriptionEvent{
					{Kind: speechio.StreamingTranscriptionFinal, Text: "hello", Confidence: .99},
					{Kind: speechio.StreamingTranscriptionSpeechEnd},
				},
			},
		}
		agent := &speculativeTestAgent{
			blockSpeculative: true, started: make(chan struct{}), cancelled: make(chan struct{}),
		}
		pipeline, err := New(speech, agent)
		if err != nil {
			t.Fatal(err)
		}
		audio := make(chan []byte)
		defer close(audio)
		done := make(chan error, 1)
		outputCalls := 0
		go func() {
			_, err := pipeline.ProcessLive(ctx, "receive-failure", httpapi.VoiceTurnInput{}, audio,
				func([]byte) error { outputCalls++; return nil })
			done <- err
		}()
		synctest.Wait()
		select {
		case <-agent.started:
		default:
			t.Fatal("test did not start speculative model work")
		}
		close(receiveGate)
		synctest.Wait()
		select {
		case err := <-done:
			stage, classified := httpapi.VoicePipelineStageOf(err)
			if !classified || stage != httpapi.VoicePipelineStageTranscribe {
				t.Fatalf("failure stage=%q classified=%v err=%v", stage, classified, err)
			}
		default:
			t.Fatal("receive failure waited for the speculative model")
		}
		select {
		case <-agent.cancelled:
		default:
			t.Fatal("receive failure retained speculative model work")
		}
		select {
		case <-speech.prepareCanceled:
		default:
			t.Fatal("receive failure retained pending TTS preparation")
		}
		if turns := agent.recordedTurns(); len(turns) != 1 || !turns[0].Speculative || speech.streamCalls != 0 || outputCalls != 0 {
			t.Fatalf("receive failure crossed commit boundary: turns=%+v tts=%d output=%d", turns, speech.streamCalls, outputCalls)
		}
	})
}
