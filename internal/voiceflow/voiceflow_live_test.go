package voiceflow

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type fakeLiveTranscriptionSession struct {
	mu         sync.Mutex
	ctx        context.Context
	events     []speechio.StreamingTranscriptionEvent
	eventIndex int
	audio      [][]byte
	closeCalls int
	closed     chan struct{}
	closeOnce  sync.Once
}

func newFakeLiveTranscriptionSession(
	events ...speechio.StreamingTranscriptionEvent,
) *fakeLiveTranscriptionSession {
	return &fakeLiveTranscriptionSession{
		events: events,
		closed: make(chan struct{}),
	}
}

func (session *fakeLiveTranscriptionSession) SendPCM(audio []byte) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if err := session.ctx.Err(); err != nil {
		return err
	}
	session.audio = append(session.audio, append([]byte(nil), audio...))
	return nil
}

func (session *fakeLiveTranscriptionSession) CloseSend() error {
	session.mu.Lock()
	session.closeCalls++
	session.mu.Unlock()
	session.closeOnce.Do(func() { close(session.closed) })
	return nil
}

func (session *fakeLiveTranscriptionSession) RecvEvent() (
	speechio.StreamingTranscriptionEvent,
	error,
) {
	select {
	case <-session.ctx.Done():
		return speechio.StreamingTranscriptionEvent{}, session.ctx.Err()
	case <-session.closed:
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.eventIndex >= len(session.events) {
		return speechio.StreamingTranscriptionEvent{}, io.EOF
	}
	event := session.events[session.eventIndex]
	session.eventIndex++
	return event, nil
}

type fakeLiveSpeech struct {
	fakeStreamingSpeech
	session *fakeLiveTranscriptionSession
	opened  chan struct{}
}

func (speech *fakeLiveSpeech) OpenStreamingTranscription(
	ctx context.Context,
) (speechio.StreamingTranscriptionSession, error) {
	speech.session.ctx = ctx
	if speech.opened != nil {
		close(speech.opened)
	}
	return speech.session, nil
}

func TestPipelineLiveAggregatesOnlyFinalsAndIgnoresUncalibratedConfidence(t *testing.T) {
	t.Parallel()
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      "変更される途中",
			Stability: 0.9,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:       speechio.StreamingTranscriptionFinal,
			Text:       "質問の前半",
			Confidence: 0.1,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:       speechio.StreamingTranscriptionFinal,
			Text:       "質問の後半",
			Confidence: 0.1,
		},
	)
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{
			fakeSpeech: fakeSpeech{},
			chunks:     [][]byte{{1, 0}, {2, 0}},
		},
		session: session,
	}
	agent := &fakeAgent{result: conversation.VoiceTurnResult{
		Domain:           "daily",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		ResearchStatus:   "none",
		ResearchRecords:  []conversation.ResearchRecord{},
		SpokenReply:      "Aです。理由はBです。",
		Route:            "fast",
		StateToken:       "sealed-state",
	}}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	audio := make(chan []byte, 2)
	audio <- []byte{1, 0, 2, 0}
	close(audio)
	var output []byte
	result, err := pipeline.ProcessLive(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{RequestID: "request-id"},
		audio,
		func(chunk []byte) error {
			output = append(output, chunk...)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if agent.calls != 1 ||
		agent.turn.Utterance != "質問の前半 質問の後半" {
		t.Fatalf("agent calls=%d turn=%+v", agent.calls, agent.turn)
	}
	if !bytes.Equal(output, []byte{1, 0, 2, 0}) ||
		result.Caption != agent.result.SpokenReply ||
		result.StateToken != "sealed-state" {
		t.Fatalf("output=%v result=%+v", output, result)
	}
	if result.LiveTimings.STTFirstInterimMS < 0 ||
		result.LiveTimings.STTFinalMS < 0 ||
		result.LiveTimings.ConversationMS < 0 ||
		result.LiveTimings.TTSFirstChunkMS < 0 {
		t.Fatalf("timings=%+v", result.LiveTimings)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closeCalls != 1 ||
		len(session.audio) != 1 ||
		!bytes.Equal(session.audio[0], []byte{1, 0, 2, 0}) {
		t.Fatalf("session close=%d audio=%v", session.closeCalls, session.audio)
	}
}

func TestPipelineLiveReusesNoSpeechClarificationAndAmbientSilence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		ambient    bool
		wantRoute  string
		wantStream int
	}{
		{
			name:       "intentional",
			wantRoute:  routeClarifyNoSpeech,
			wantStream: 1,
		},
		{
			name:      "ambient",
			ambient:   true,
			wantRoute: routeSilentNoSpeech,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			session := newFakeLiveTranscriptionSession()
			speech := &fakeLiveSpeech{
				fakeStreamingSpeech: fakeStreamingSpeech{
					fakeSpeech: fakeSpeech{},
					chunks:     [][]byte{{1, 0}},
				},
				session: session,
			}
			agent := &fakeAgent{}
			pipeline, err := New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			audio := make(chan []byte, 1)
			audio <- []byte{0, 0}
			close(audio)
			result, err := pipeline.ProcessLive(
				context.Background(),
				"uid",
				httpapi.VoiceTurnInput{
					Ambient:    test.ambient,
					StateToken: "existing-state",
				},
				audio,
				func([]byte) error { return nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Route != test.wantRoute ||
				result.StateToken != "existing-state" ||
				speech.streamCalls != test.wantStream ||
				agent.calls != 0 {
				t.Fatalf(
					"result=%+v stream=%d agent=%d",
					result,
					speech.streamCalls,
					agent.calls,
				)
			}
		})
	}
}

func TestPipelineLiveNoFrameCloseAndCancellationDoNotLeak(t *testing.T) {
	t.Parallel()
	t.Run("no frame close", func(t *testing.T) {
		t.Parallel()
		session := newFakeLiveTranscriptionSession()
		speech := &fakeLiveSpeech{
			fakeStreamingSpeech: fakeStreamingSpeech{
				fakeSpeech: fakeSpeech{},
				chunks:     [][]byte{{1, 0}},
			},
			session: session,
		}
		pipeline, err := New(speech, &fakeAgent{})
		if err != nil {
			t.Fatal(err)
		}
		audio := make(chan []byte)
		close(audio)
		done := make(chan error, 1)
		go func() {
			_, processErr := pipeline.ProcessLive(
				context.Background(),
				"uid",
				httpapi.VoiceTurnInput{},
				audio,
				func([]byte) error { return nil },
			)
			done <- processErr
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("no-frame close left RecvEvent blocked")
		}
		session.mu.Lock()
		closeCalls := session.closeCalls
		session.mu.Unlock()
		if closeCalls != 1 {
			t.Fatalf("CloseSend calls=%d", closeCalls)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()
		session := newFakeLiveTranscriptionSession()
		opened := make(chan struct{})
		speech := &fakeLiveSpeech{
			fakeStreamingSpeech: fakeStreamingSpeech{
				fakeSpeech: fakeSpeech{},
			},
			session: session,
			opened:  opened,
		}
		pipeline, err := New(speech, &fakeAgent{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		audio := make(chan []byte)
		done := make(chan error, 1)
		go func() {
			_, processErr := pipeline.ProcessLive(
				ctx,
				"uid",
				httpapi.VoiceTurnInput{},
				audio,
				func([]byte) error { return nil },
			)
			done <- processErr
		}()
		<-opened
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("context cancellation left sendDone blocked")
		}
	})
}
