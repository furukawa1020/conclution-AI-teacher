package voiceflow

import (
	"bytes"
	"context"
	"errors"
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
	closeGate  <-chan struct{}
	sendGate   <-chan struct{}
	sendSeen   chan struct{}
	sendOnce   sync.Once
	eventGates map[int]<-chan struct{}
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
	ctx := session.ctx
	sendGate := session.sendGate
	session.mu.Unlock()
	if session.sendSeen != nil {
		session.sendOnce.Do(func() { close(session.sendSeen) })
	}
	if sendGate != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sendGate:
		}
	}
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
	closeGate := session.closeGate
	ctx := session.ctx
	session.mu.Unlock()
	if closeGate != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closeGate:
		}
	}
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
	if session.eventIndex >= len(session.events) {
		session.mu.Unlock()
		return speechio.StreamingTranscriptionEvent{}, io.EOF
	}
	index := session.eventIndex
	event := session.events[index]
	session.eventIndex++
	gate := session.eventGates[index]
	session.mu.Unlock()
	if gate != nil {
		select {
		case <-session.ctx.Done():
			return speechio.StreamingTranscriptionEvent{}, session.ctx.Err()
		case <-gate:
		}
	}
	return event, nil
}

type fakeLiveSpeech struct {
	fakeStreamingSpeech
	session *fakeLiveTranscriptionSession
	opened  chan struct{}
}

type scriptedSynthesis struct {
	chunks       [][]byte
	mimeType     string
	err          error
	beforeReturn <-chan struct{}
}

type synthesisChunkEvent struct {
	call  int
	chunk int
}

type scriptedLiveSpeech struct {
	fakeSpeech
	session *fakeLiveTranscriptionSession
	scripts []scriptedSynthesis

	mu            sync.Mutex
	texts         []string
	chunkStarted  chan synthesisChunkEvent
	chunkFinished chan synthesisChunkEvent
	completed     chan int
	returned      chan int
}

func (speech *scriptedLiveSpeech) OpenStreamingTranscription(
	ctx context.Context,
) (speechio.StreamingTranscriptionSession, error) {
	speech.session.ctx = ctx
	return speech.session, nil
}

func (speech *scriptedLiveSpeech) StreamSynthesize(
	ctx context.Context,
	text string,
	onChunk speechio.StreamChunkHandler,
) (string, error) {
	speech.mu.Lock()
	call := len(speech.texts)
	speech.texts = append(speech.texts, text)
	if call >= len(speech.scripts) {
		speech.mu.Unlock()
		return "", errors.New("unexpected synthesis call")
	}
	script := speech.scripts[call]
	speech.mu.Unlock()
	if speech.returned != nil {
		defer func() {
			speech.returned <- call
		}()
	}

	for index, chunk := range script.chunks {
		event := synthesisChunkEvent{call: call, chunk: index}
		if speech.chunkStarted != nil {
			speech.chunkStarted <- event
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := onChunk(chunk); err != nil {
			return "", err
		}
		if speech.chunkFinished != nil {
			speech.chunkFinished <- event
		}
	}
	if speech.completed != nil {
		speech.completed <- call
	}
	if script.beforeReturn != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-script.beforeReturn:
		}
	}
	if script.err != nil {
		return "", script.err
	}
	mimeType := script.mimeType
	if mimeType == "" {
		mimeType = speechio.StreamingAudioContentType
	}
	return mimeType, nil
}

func (speech *scriptedLiveSpeech) synthesisTexts() []string {
	speech.mu.Lock()
	defer speech.mu.Unlock()
	return append([]string(nil), speech.texts...)
}

type speculativeTestAgent struct {
	mu                sync.Mutex
	turns             []conversation.VoiceTurn
	speculativeResult conversation.VoiceTurnResult
	speculativeErr    error
	normalResult      conversation.VoiceTurnResult
	normalErr         error
	blockSpeculative  bool
	started           chan struct{}
	cancelled         chan struct{}
	startOnce         sync.Once
	cancelOnce        sync.Once
}

func (agent *speculativeTestAgent) Process(
	ctx context.Context,
	_ string,
	turn conversation.VoiceTurn,
) (conversation.VoiceTurnResult, error) {
	agent.mu.Lock()
	agent.turns = append(agent.turns, turn)
	agent.mu.Unlock()
	if !turn.Speculative {
		return agent.normalResult, agent.normalErr
	}
	if agent.started != nil {
		agent.startOnce.Do(func() { close(agent.started) })
	}
	if agent.blockSpeculative {
		<-ctx.Done()
		if agent.cancelled != nil {
			agent.cancelOnce.Do(func() { close(agent.cancelled) })
		}
		return conversation.VoiceTurnResult{}, ctx.Err()
	}
	return agent.speculativeResult, agent.speculativeErr
}

func (agent *speculativeTestAgent) recordedTurns() []conversation.VoiceTurn {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return append([]conversation.VoiceTurn(nil), agent.turns...)
}

func sequenceClock(times ...time.Time) func() time.Time {
	var mu sync.Mutex
	index := 0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if len(times) == 0 {
			return time.Time{}
		}
		if index >= len(times) {
			return times[len(times)-1]
		}
		value := times[index]
		index++
		return value
	}
}

func liveTestDecision(reply string, state string) conversation.VoiceTurnResult {
	return conversation.VoiceTurnResult{
		Domain:           "daily",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		ResearchStatus:   "none",
		ResearchRecords:  []conversation.ResearchRecord{},
		SpokenReply:      reply,
		Route:            "fast",
		StateToken:       state,
	}
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

func TestPipelineLiveExposesPostCommitDeadlineToAgent(t *testing.T) {
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: "deadline test",
		},
	)
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{
			chunks: [][]byte{{1, 0}},
		},
		session: session,
	}
	agent := &fakeAgent{result: liveTestDecision("bounded reply", "state")}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	processingTimeout := conversation.VoiceResponseReserve +
		500*time.Millisecond
	parentTimeout := conversation.VoiceResponseReserve +
		300*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), parentTimeout)
	defer cancel()
	processingDeadline := make(chan time.Time, 1)
	processingDeadline <- time.Now().Add(processingTimeout)
	close(processingDeadline)

	_, err = pipeline.ProcessLive(
		ctx,
		"uid-live-deadline",
		httpapi.VoiceTurnInput{
			ProcessingTimeout:  processingTimeout,
			ProcessingDeadline: processingDeadline,
		},
		audio,
		func([]byte) error { return nil },
	)
	if err != nil {
		t.Fatalf("ProcessLive: %v", err)
	}
	if agent.processingBudget <= conversation.VoiceResponseReserve ||
		agent.processingBudget > parentTimeout {
		t.Fatalf(
			"agent processing budget = %v; want (%v, %v]",
			agent.processingBudget,
			conversation.VoiceResponseReserve,
			parentTimeout,
		)
	}
}

func TestPipelineLiveDoesNotStartSpeculationAfterCommit(t *testing.T) {
	const utterance = "post commit final"
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.91,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.95,
		},
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: utterance,
		},
	)
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{
			chunks: [][]byte{{1, 0}},
		},
		session: session,
	}
	agent := &speculativeTestAgent{
		speculativeResult: liveTestDecision("must not run", "spec-state"),
		normalResult:      liveTestDecision("normal reply", "normal-state"),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(300, 0)
	pipeline.now = sequenceClock(
		started,
		started.Add(minSpeculativeStableDuration),
	)
	processingTimeout := conversation.VoiceResponseReserve +
		500*time.Millisecond
	processingDeadline := make(chan time.Time, 1)
	processingDeadline <- time.Now().Add(processingTimeout)
	close(processingDeadline)
	processingCommitted := make(chan struct{})
	close(processingCommitted)
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)

	result, err := pipeline.ProcessLive(
		context.Background(),
		"uid-post-commit-speculation",
		httpapi.VoiceTurnInput{
			ProcessingTimeout:   processingTimeout,
			ProcessingDeadline:  processingDeadline,
			ProcessingCommitted: processingCommitted,
		},
		audio,
		func([]byte) error { return nil },
	)
	if err != nil {
		t.Fatalf("ProcessLive: %v", err)
	}
	turns := agent.recordedTurns()
	if len(turns) != 1 ||
		turns[0].Speculative ||
		result.StateToken != "normal-state" ||
		result.LiveTimings.SpecHit != 0 {
		t.Fatalf("post-commit speculation escaped: turns=%+v result=%+v", turns, result)
	}
}

func TestPipelineLiveStopsSTTBeforePostCommitResponseReserve(t *testing.T) {
	gate := make(chan struct{})
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: "must not reach the agent",
		},
	)
	session.closeGate = gate
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{},
		session:             session,
	}
	agent := &fakeAgent{}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	started := time.Now()

	_, err = pipeline.ProcessLive(
		context.Background(),
		"uid-live-stt-budget",
		httpapi.VoiceTurnInput{
			ProcessingTimeout: conversation.VoiceResponseReserve +
				75*time.Millisecond,
		},
		audio,
		func([]byte) error { return nil },
	)
	stage, classified := httpapi.VoicePipelineStageOf(err)
	if !classified ||
		stage != httpapi.VoicePipelineStageTranscribe ||
		agent.calls != 0 ||
		time.Since(started) > 2*time.Second {
		t.Fatalf(
			"live STT reserve failure: err=%v stage=%q calls=%d elapsed=%v",
			err,
			stage,
			agent.calls,
			time.Since(started),
		)
	}
}

func TestPipelineLiveCommitDeadlineCancelsBlockedSendPCM(t *testing.T) {
	sendGate := make(chan struct{})
	session := newFakeLiveTranscriptionSession()
	session.sendGate = sendGate
	session.sendSeen = make(chan struct{})
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{},
		session:             session,
	}
	agent := &fakeAgent{}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	audio := make(chan []byte)
	processingDeadline := make(chan time.Time, 1)
	type pipelineOutcome struct {
		err error
	}
	done := make(chan pipelineOutcome, 1)
	go func() {
		_, processErr := pipeline.ProcessLive(
			context.Background(),
			"uid-live-blocked-send",
			httpapi.VoiceTurnInput{
				ProcessingTimeout: conversation.VoiceResponseReserve +
					75*time.Millisecond,
				ProcessingDeadline: processingDeadline,
			},
			audio,
			func([]byte) error { return nil },
		)
		done <- pipelineOutcome{err: processErr}
	}()
	select {
	case audio <- []byte{1, 0}:
	case <-time.After(time.Second):
		t.Fatal("live pipeline did not accept PCM")
	}
	select {
	case <-session.sendSeen:
	case <-time.After(time.Second):
		t.Fatal("streaming recognizer did not start blocked SendPCM")
	}
	deadline := time.Now().Add(
		conversation.VoiceResponseReserve + 75*time.Millisecond,
	)
	processingDeadline <- deadline
	close(processingDeadline)
	close(audio)

	select {
	case outcome := <-done:
		stage, classified := httpapi.VoicePipelineStageOf(outcome.err)
		if !classified ||
			stage != httpapi.VoicePipelineStageTranscribe ||
			agent.calls != 0 {
			t.Fatalf(
				"blocked SendPCM reserve failure: err=%v stage=%q calls=%d",
				outcome.err,
				stage,
				agent.calls,
			)
		}
	case <-time.After(time.Second):
		t.Fatal("commit deadline did not cancel blocked SendPCM")
	}
}

func TestPipelineLiveReusesNoSpeechClarificationAndAmbientSilence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		ambient    bool
		foreground bool
		wantRoute  string
		wantStream int
	}{
		{
			name:       "intentional",
			wantRoute:  routeClarifyNoSpeech,
			wantStream: 1,
		},
		{
			name:       "foreground",
			ambient:    true,
			foreground: true,
			wantRoute:  routeSilentNoSpeech,
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
					Foreground: test.foreground,
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

func TestSpeculativeCandidateRequiresRepeatedStableExactText(t *testing.T) {
	t.Parallel()
	started := time.Unix(100, 0)
	tracker := speculativeCandidateTracker{}
	decomposed := "Cafe\u0301  について　教えて"
	composed := "Café について 教えて"
	collapsedDecomposed := "Cafe\u0301 について 教えて"

	candidate, ready := tracker.observe(decomposed, true, started)
	if ready || candidate != collapsedDecomposed {
		t.Fatalf("first observation candidate=%q ready=%v", candidate, ready)
	}
	if _, ready = tracker.observe(
		composed,
		true,
		started.Add(minSpeculativeStableDuration-time.Millisecond),
	); ready {
		t.Fatal("canonically distinct candidate became stable immediately")
	}
	if _, ready = tracker.observe(
		composed,
		true,
		started.Add(2*minSpeculativeStableDuration-time.Millisecond),
	); !ready {
		t.Fatal("repeated candidate did not become stable at 160ms")
	}

	tracker.reset()
	tracker.observe(composed, true, started)
	tracker.observe(composed, false, started.Add(time.Second))
	if _, ready = tracker.observe(
		composed,
		true,
		started.Add(2*time.Second),
	); ready {
		t.Fatal("an ineligible interim did not break consecutive stability")
	}

	tracker.reset()
	tracker.observe("1234567", true, started)
	if _, ready = tracker.observe(
		"1234567",
		true,
		started.Add(time.Second),
	); ready {
		t.Fatal("a candidate shorter than eight runes became eligible")
	}

	joined := joinTranscript(
		[]string{"確定した前半", "確定した中盤"},
		"仮の後半",
	)
	if joined != "確定した前半 確定した中盤 仮の後半" {
		t.Fatalf("joined candidate=%q", joined)
	}
	if !speculationTextsMatch(
		"この  質問を説明して ",
		"この 質問を説明して",
	) {
		t.Fatal("conversation whitespace normalization should permit adoption")
	}
	if speculationTextsMatch(
		"この質問を説明して？",
		"この質問を説明して!",
	) {
		t.Fatal("punctuation difference was incorrectly ignored")
	}
	if speculationTextsMatch(decomposed, composed) {
		t.Fatal("Unicode normalization collision was incorrectly adopted")
	}
}

func TestSpeculativeAudioCommitBufferBoundsBlocksAndPreservesOrder(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var delivered []byte
	buffer := newSpeculativeAudioCommitBuffer(
		ctx,
		func(chunk []byte) error {
			delivered = append(delivered, chunk...)
			return nil
		},
	)
	audio := bytes.Repeat(
		[]byte{1, 0},
		maxSpeculativeTTSBufferBytes/2+1,
	)
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- buffer.write(ctx, audio)
	}()

	deadline := time.Now().Add(time.Second)
	for buffer.peakBufferedBytes() != maxSpeculativeTTSBufferBytes {
		if time.Now().After(deadline) {
			t.Fatal("commit buffer never reached its byte bound")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-writeDone:
		t.Fatalf("provider callback did not block at the bound: %v", err)
	default:
	}
	if len(delivered) != 0 {
		t.Fatalf("pending audio escaped: %d bytes", len(delivered))
	}

	releaseMS, err := buffer.release(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if releaseMS < 0 {
		t.Fatalf("release_ms=%d", releaseMS)
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("release did not unblock the provider callback")
	}
	if !bytes.Equal(delivered, audio) {
		t.Fatalf(
			"delivered %d bytes out of order; want %d",
			len(delivered),
			len(audio),
		)
	}
	if buffer.peakBufferedBytes() != maxSpeculativeTTSBufferBytes {
		t.Fatalf("peak bytes=%d", buffer.peakBufferedBytes())
	}
}

func TestSpeculativeAudioCommitBufferCancellationWipesAndWakes(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	delivered := 0
	buffer := newSpeculativeAudioCommitBuffer(
		ctx,
		func(chunk []byte) error {
			delivered += len(chunk)
			return nil
		},
	)
	full := bytes.Repeat([]byte{2, 0}, maxSpeculativeTTSBufferBytes/2)
	if err := buffer.write(ctx, full); err != nil {
		t.Fatal(err)
	}
	buffer.mu.Lock()
	bufferedCopy := buffer.chunks[0]
	buffer.mu.Unlock()
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- buffer.write(ctx, []byte{3, 0})
	}()
	select {
	case err := <-writeDone:
		t.Fatalf("full writer returned before cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("cancelled writer returned nil")
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not wake the full writer")
	}
	if delivered != 0 {
		t.Fatalf("cancelled buffer delivered %d bytes", delivered)
	}
	if !bytes.Equal(bufferedCopy, make([]byte, len(bufferedCopy))) {
		t.Fatal("discarded PCM was not zeroized")
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.state != speculativeAudioDiscarded ||
		buffer.bufferedBytes != 0 ||
		len(buffer.chunks) != 0 {
		t.Fatalf(
			"state=%d bytes=%d chunks=%d",
			buffer.state,
			buffer.bufferedBytes,
			len(buffer.chunks),
		)
	}
}

func TestSpeculativeAudioCommitBufferDiscardDoesNotWaitForDelivery(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deliverStarted := make(chan struct{})
	allowDelivery := make(chan struct{})
	buffer := newSpeculativeAudioCommitBuffer(
		ctx,
		func([]byte) error {
			close(deliverStarted)
			<-allowDelivery
			return nil
		},
	)
	if err := buffer.write(ctx, []byte{4, 0}); err != nil {
		t.Fatal(err)
	}
	releaseDone := make(chan error, 1)
	go func() {
		_, err := buffer.release(ctx)
		releaseDone <- err
	}()
	select {
	case <-deliverStarted:
	case <-time.After(time.Second):
		t.Fatal("release never entered delivery")
	}

	discardDone := make(chan struct{})
	go func() {
		buffer.discard(errSpeculativeAudioDiscarded)
		close(discardDone)
	}()
	select {
	case <-discardDone:
	case <-time.After(time.Second):
		t.Fatal("discard waited on a blocked external delivery")
	}
	close(allowDelivery)
	select {
	case err := <-releaseDone:
		if err == nil {
			t.Fatal("discarded release returned nil")
		}
	case <-time.After(time.Second):
		t.Fatal("release did not finish after delivery unblocked")
	}
}

func TestSpeculativeAudioCommitBufferRejectsUnalignedPCM(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buffer := newSpeculativeAudioCommitBuffer(
		ctx,
		func([]byte) error { return nil },
	)
	if err := buffer.write(ctx, []byte{1}); !errors.Is(
		err,
		errSpeculativeAudioChunk,
	) {
		t.Fatalf("write error=%v", err)
	}
}

func TestPipelineLiveKeepsSpeculativeResultPrivateUntilExactFinal(t *testing.T) {
	t.Parallel()
	const utterance = "この仕組みを詳しく説明して"
	finalGate := make(chan struct{})
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.91,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.94,
		},
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: utterance,
		},
	)
	session.eventGates = map[int]<-chan struct{}{2: finalGate}
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{
			fakeSpeech: fakeSpeech{},
			chunks:     [][]byte{{7, 0}},
		},
		session: session,
	}
	agent := &speculativeTestAgent{
		speculativeResult: liveTestDecision("先読みした回答", "spec-state"),
		normalResult:      liveTestDecision("通常回答", "normal-state"),
		started:           make(chan struct{}),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(200, 0)
	pipeline.now = sequenceClock(
		started,
		started.Add(minSpeculativeStableDuration),
	)
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	delivered := make(chan []byte, 1)
	type liveOutcome struct {
		result httpapi.VoiceTurnResult
		err    error
	}
	done := make(chan liveOutcome, 1)
	go func() {
		result, processErr := pipeline.ProcessLive(
			context.Background(),
			"uid",
			httpapi.VoiceTurnInput{RequestID: "spec-private"},
			audio,
			func(chunk []byte) error {
				delivered <- append([]byte(nil), chunk...)
				return nil
			},
		)
		done <- liveOutcome{result: result, err: processErr}
	}()

	select {
	case <-agent.started:
	case <-time.After(time.Second):
		t.Fatal("speculative agent did not start before the final transcript")
	}
	select {
	case chunk := <-delivered:
		t.Fatalf("provisional audio escaped before final: %v", chunk)
	default:
	}
	select {
	case outcome := <-done:
		t.Fatalf("provisional state escaped before final: %+v", outcome)
	default:
	}

	close(finalGate)
	var outcome liveOutcome
	select {
	case outcome = <-done:
	case <-time.After(time.Second):
		t.Fatal("exact final did not release the adopted result")
	}
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if chunk := <-delivered; !bytes.Equal(chunk, []byte{7, 0}) {
		t.Fatalf("delivered=%v", chunk)
	}
	if outcome.result.StateToken != "spec-state" ||
		outcome.result.Caption != "先読みした回答" ||
		speech.synthesizedText != "先読みした回答" {
		t.Fatalf("result=%+v synthesized=%q", outcome.result, speech.synthesizedText)
	}
	if outcome.result.LiveTimings.SpecHit != 1 ||
		outcome.result.LiveTimings.SpecMiss != 0 ||
		outcome.result.LiveTimings.SpecCancel != 0 ||
		outcome.result.LiveTimings.TTSPrestarted != 1 ||
		outcome.result.LiveTimings.TTSBufferedBytes != 2 ||
		outcome.result.LiveTimings.TTSReleaseMS < 0 ||
		outcome.result.LiveTimings.FinalToFirstAudioMS < 0 {
		t.Fatalf("timings=%+v", outcome.result.LiveTimings)
	}
	if speech.streamCalls != 1 {
		t.Fatalf("synthesis calls=%d want 1", speech.streamCalls)
	}
	turns := agent.recordedTurns()
	if len(turns) != 1 ||
		!turns[0].Speculative ||
		turns[0].Utterance != utterance {
		t.Fatalf("turns=%+v", turns)
	}
}

func TestPipelineLiveReleasesFullPrestartedTTSInOrder(t *testing.T) {
	t.Parallel()
	const utterance = "長い先読み音声の順序を確認する"
	finalGate := make(chan struct{})
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.91,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.95,
		},
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: utterance,
		},
	)
	session.eventGates = map[int]<-chan struct{}{2: finalGate}
	first := bytes.Repeat([]byte{5, 0}, maxSpeculativeTTSBufferBytes/2)
	second := []byte{6, 0}
	speech := &scriptedLiveSpeech{
		session: session,
		scripts: []scriptedSynthesis{{
			chunks: [][]byte{first, second},
		}},
		chunkStarted:  make(chan synthesisChunkEvent, 2),
		chunkFinished: make(chan synthesisChunkEvent, 2),
		completed:     make(chan int, 1),
	}
	agent := &speculativeTestAgent{
		speculativeResult: liveTestDecision("長い先読み回答", "spec-state"),
		normalResult:      liveTestDecision("通常回答", "normal-state"),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(800, 0)
	pipeline.now = sequenceClock(
		started,
		started.Add(minSpeculativeStableDuration),
	)
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	type pipelineOutcome struct {
		result httpapi.VoiceTurnResult
		err    error
	}
	done := make(chan pipelineOutcome, 1)
	var outputMu sync.Mutex
	var outputFrames [][]byte
	go func() {
		result, processErr := pipeline.ProcessLive(
			context.Background(),
			"uid",
			httpapi.VoiceTurnInput{},
			audio,
			func(chunk []byte) error {
				outputMu.Lock()
				outputFrames = append(
					outputFrames,
					append([]byte(nil), chunk...),
				)
				outputMu.Unlock()
				return nil
			},
		)
		done <- pipelineOutcome{result: result, err: processErr}
	}()

	for _, want := range []synthesisChunkEvent{
		{call: 0, chunk: 0},
		{call: 0, chunk: 1},
	} {
		select {
		case got := <-speech.chunkStarted:
			if got != want {
				t.Fatalf("started=%+v want %+v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("chunk %+v never started", want)
		}
		if want.chunk == 0 {
			select {
			case got := <-speech.chunkFinished:
				if got != want {
					t.Fatalf("finished=%+v want %+v", got, want)
				}
			case <-time.After(time.Second):
				t.Fatal("first bounded chunk did not finish")
			}
		}
	}
	select {
	case event := <-speech.chunkFinished:
		t.Fatalf("full-buffer callback was not blocked: %+v", event)
	case <-time.After(20 * time.Millisecond):
	}
	outputMu.Lock()
	preFinalFrames := len(outputFrames)
	outputMu.Unlock()
	if preFinalFrames != 0 {
		t.Fatalf("pre-final frames=%d", preFinalFrames)
	}

	close(finalGate)
	var outcome pipelineOutcome
	select {
	case outcome = <-done:
	case <-time.After(time.Second):
		t.Fatal("full-buffer release did not complete")
	}
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	outputMu.Lock()
	frames := append([][]byte(nil), outputFrames...)
	outputMu.Unlock()
	if len(frames) != 2 ||
		!bytes.Equal(frames[0], first) ||
		!bytes.Equal(frames[1], second) {
		sizes := make([]int, 0, len(frames))
		for _, frame := range frames {
			sizes = append(sizes, len(frame))
		}
		t.Fatalf("output frame sizes=%v", sizes)
	}
	if texts := speech.synthesisTexts(); len(texts) != 1 ||
		texts[0] != "長い先読み回答" {
		t.Fatalf("synthesis texts=%v", texts)
	}
	if outcome.result.LiveTimings.TTSPrestarted != 1 ||
		outcome.result.LiveTimings.TTSBufferedBytes !=
			maxSpeculativeTTSBufferBytes ||
		outcome.result.LiveTimings.TTSReleaseMS < 0 ||
		outcome.result.LiveTimings.SpecHit != 1 {
		t.Fatalf("timings=%+v", outcome.result.LiveTimings)
	}
}

func TestPipelineLiveDiscardsPrestartedTTSOnFinalMismatch(t *testing.T) {
	t.Parallel()
	const interim = "先読み時点の質問内容です"
	finalGate := make(chan struct{})
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      interim,
			Stability: 0.9,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      interim,
			Stability: 0.95,
		},
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: "確定時点では異なる質問です",
		},
	)
	session.eventGates = map[int]<-chan struct{}{2: finalGate}
	speech := &scriptedLiveSpeech{
		session: session,
		scripts: []scriptedSynthesis{
			{chunks: [][]byte{{9, 0}}},
			{chunks: [][]byte{{7, 0}}},
		},
		completed: make(chan int, 2),
	}
	agent := &speculativeTestAgent{
		speculativeResult: liveTestDecision(
			"破棄される先読み回答",
			"provisional-state",
		),
		normalResult: liveTestDecision("確定回答", "final-state"),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(900, 0)
	pipeline.now = sequenceClock(
		started,
		started.Add(minSpeculativeStableDuration),
	)
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	type pipelineOutcome struct {
		result httpapi.VoiceTurnResult
		err    error
	}
	done := make(chan pipelineOutcome, 1)
	var outputMu sync.Mutex
	var output []byte
	go func() {
		result, processErr := pipeline.ProcessLive(
			context.Background(),
			"uid",
			httpapi.VoiceTurnInput{},
			audio,
			func(chunk []byte) error {
				outputMu.Lock()
				output = append(output, chunk...)
				outputMu.Unlock()
				return nil
			},
		)
		done <- pipelineOutcome{result: result, err: processErr}
	}()
	select {
	case call := <-speech.completed:
		if call != 0 {
			t.Fatalf("completed call=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("speculative TTS did not complete before final")
	}
	outputMu.Lock()
	preFinalBytes := len(output)
	outputMu.Unlock()
	if preFinalBytes != 0 {
		t.Fatalf("pre-final output bytes=%d", preFinalBytes)
	}
	close(finalGate)
	var outcome pipelineOutcome
	select {
	case outcome = <-done:
	case <-time.After(time.Second):
		t.Fatal("mismatch fallback did not complete")
	}
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	outputMu.Lock()
	finalOutput := append([]byte(nil), output...)
	outputMu.Unlock()
	if !bytes.Equal(finalOutput, []byte{7, 0}) ||
		outcome.result.StateToken != "final-state" ||
		outcome.result.Caption != "確定回答" {
		t.Fatalf("output=%v result=%+v", finalOutput, outcome.result)
	}
	if texts := speech.synthesisTexts(); len(texts) != 2 ||
		texts[0] != "破棄される先読み回答" ||
		texts[1] != "確定回答" {
		t.Fatalf("synthesis texts=%v", texts)
	}
	if outcome.result.LiveTimings.TTSPrestarted != 1 ||
		outcome.result.LiveTimings.TTSBufferedBytes != 2 ||
		outcome.result.LiveTimings.TTSReleaseMS != -1 ||
		outcome.result.LiveTimings.SpecMiss != 1 ||
		outcome.result.LiveTimings.SpecCancel != 1 {
		t.Fatalf("timings=%+v", outcome.result.LiveTimings)
	}
}

func TestPipelineLivePrestartFailureRetriesOnlyTTSBeforeRelease(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		script scriptedSynthesis
	}{
		{
			name: "provider error",
			script: scriptedSynthesis{
				chunks: [][]byte{{8, 0}},
				err:    errors.New("prestart failed"),
			},
		},
		{
			name: "wrong MIME",
			script: scriptedSynthesis{
				chunks:   [][]byte{{8, 0}},
				mimeType: "audio/mpeg",
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const utterance = "先読み失敗時の安全性を確認する"
			finalGate := make(chan struct{})
			session := newFakeLiveTranscriptionSession(
				speechio.StreamingTranscriptionEvent{
					Kind:      speechio.StreamingTranscriptionInterim,
					Text:      utterance,
					Stability: 0.9,
				},
				speechio.StreamingTranscriptionEvent{
					Kind:      speechio.StreamingTranscriptionInterim,
					Text:      utterance,
					Stability: 0.95,
				},
				speechio.StreamingTranscriptionEvent{
					Kind: speechio.StreamingTranscriptionFinal,
					Text: utterance,
				},
			)
			session.eventGates = map[int]<-chan struct{}{2: finalGate}
			speech := &scriptedLiveSpeech{
				session: session,
				scripts: []scriptedSynthesis{
					test.script,
					{chunks: [][]byte{{3, 0}}},
				},
				completed: make(chan int, 2),
			}
			agent := &speculativeTestAgent{
				speculativeResult: liveTestDecision(
					"監査済み先読み回答",
					"spec-state",
				),
				normalResult: liveTestDecision(
					"再推論してはいけない",
					"normal-state",
				),
			}
			pipeline, err := New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Unix(1000, 0)
			pipeline.now = sequenceClock(
				started,
				started.Add(minSpeculativeStableDuration),
			)
			audio := make(chan []byte, 1)
			audio <- []byte{1, 0}
			close(audio)
			type pipelineOutcome struct {
				result httpapi.VoiceTurnResult
				err    error
			}
			done := make(chan pipelineOutcome, 1)
			var outputMu sync.Mutex
			var output []byte
			go func() {
				result, processErr := pipeline.ProcessLive(
					context.Background(),
					"uid",
					httpapi.VoiceTurnInput{},
					audio,
					func(chunk []byte) error {
						outputMu.Lock()
						output = append(output, chunk...)
						outputMu.Unlock()
						return nil
					},
				)
				done <- pipelineOutcome{result: result, err: processErr}
			}()
			select {
			case call := <-speech.completed:
				if call != 0 {
					t.Fatalf("completed call=%d", call)
				}
			case <-time.After(time.Second):
				t.Fatal("prestart did not finish before final")
			}
			outputMu.Lock()
			preFinalBytes := len(output)
			outputMu.Unlock()
			if preFinalBytes != 0 {
				t.Fatalf("invalid prestart output bytes=%d", preFinalBytes)
			}
			close(finalGate)
			var outcome pipelineOutcome
			select {
			case outcome = <-done:
			case <-time.After(time.Second):
				t.Fatal("prestart fallback did not finish")
			}
			if outcome.err != nil {
				t.Fatal(outcome.err)
			}
			outputMu.Lock()
			finalOutput := append([]byte(nil), output...)
			outputMu.Unlock()
			if !bytes.Equal(finalOutput, []byte{3, 0}) ||
				outcome.result.StateToken != "spec-state" ||
				outcome.result.Caption != "監査済み先読み回答" {
				t.Fatalf("output=%v result=%+v", finalOutput, outcome.result)
			}
			if texts := speech.synthesisTexts(); len(texts) != 2 ||
				texts[0] != "監査済み先読み回答" ||
				texts[1] != "監査済み先読み回答" {
				t.Fatalf("synthesis texts=%v", texts)
			}
			turns := agent.recordedTurns()
			if len(turns) != 1 || !turns[0].Speculative {
				t.Fatalf("prestart TTS failure reran agent: %+v", turns)
			}
			if outcome.result.LiveTimings.TTSPrestarted != 1 ||
				outcome.result.LiveTimings.TTSBufferedBytes != 2 ||
				outcome.result.LiveTimings.TTSReleaseMS != -1 ||
				outcome.result.LiveTimings.SpecHit != 1 ||
				outcome.result.LiveTimings.SpecMiss != 0 ||
				outcome.result.LiveTimings.SpecCancel != 0 {
				t.Fatalf("timings=%+v", outcome.result.LiveTimings)
			}
		})
	}
}

func TestPipelineLiveLatePrestartFailureDoesNotDoubleSpeak(t *testing.T) {
	t.Parallel()
	const utterance = "後段失敗でも二重発話を防止する"
	finalGate := make(chan struct{})
	returnGate := make(chan struct{})
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.9,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.95,
		},
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: utterance,
		},
	)
	session.eventGates = map[int]<-chan struct{}{2: finalGate}
	first := bytes.Repeat([]byte{4, 0}, maxSpeculativeTTSBufferBytes/2)
	speech := &scriptedLiveSpeech{
		session: session,
		scripts: []scriptedSynthesis{{
			chunks:       [][]byte{first},
			err:          errors.New("late synthesis failure"),
			beforeReturn: returnGate,
		}},
		completed: make(chan int, 1),
	}
	agent := &speculativeTestAgent{
		speculativeResult: liveTestDecision("先読み回答", "spec-state"),
		normalResult:      liveTestDecision("再送してはいけない", "normal-state"),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1100, 0)
	pipeline.now = sequenceClock(
		started,
		started.Add(minSpeculativeStableDuration),
	)
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	type pipelineOutcome struct {
		result httpapi.VoiceTurnResult
		err    error
	}
	done := make(chan pipelineOutcome, 1)
	delivered := make(chan []byte, 1)
	go func() {
		result, processErr := pipeline.ProcessLive(
			context.Background(),
			"uid",
			httpapi.VoiceTurnInput{},
			audio,
			func(chunk []byte) error {
				delivered <- append([]byte(nil), chunk...)
				return nil
			},
		)
		done <- pipelineOutcome{result: result, err: processErr}
	}()
	select {
	case call := <-speech.completed:
		if call != 0 {
			t.Fatalf("completed call=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("full speculative buffer was not produced")
	}
	close(finalGate)
	select {
	case chunk := <-delivered:
		if !bytes.Equal(chunk, first) {
			t.Fatalf("delivered %d bytes", len(chunk))
		}
	case <-time.After(time.Second):
		t.Fatal("committed prefix was not released")
	}
	close(returnGate)
	var outcome pipelineOutcome
	select {
	case outcome = <-done:
	case <-time.After(time.Second):
		t.Fatal("late synthesis error did not terminate")
	}
	if outcome.err == nil {
		t.Fatal("late synthesis error returned success")
	}
	if outcome.result.StateToken != "" ||
		outcome.result.Caption != "" {
		t.Fatalf("late failure exposed final state: %+v", outcome.result)
	}
	if texts := speech.synthesisTexts(); len(texts) != 1 {
		t.Fatalf("late failure triggered duplicate TTS: %v", texts)
	}
	turns := agent.recordedTurns()
	if len(turns) != 1 || !turns[0].Speculative {
		t.Fatalf("late failure reran the agent: %+v", turns)
	}
	if outcome.result.LiveTimings.TTSPrestarted != 1 ||
		outcome.result.LiveTimings.TTSBufferedBytes !=
			maxSpeculativeTTSBufferBytes ||
		outcome.result.LiveTimings.TTSReleaseMS < 0 ||
		outcome.result.LiveTimings.SpecHit != 0 ||
		outcome.result.LiveTimings.SpecMiss != 1 ||
		outcome.result.LiveTimings.SpecCancel != 1 {
		t.Fatalf("timings=%+v", outcome.result.LiveTimings)
	}
}

func TestPipelineLiveCancellationWakesFullPrestartedTTS(t *testing.T) {
	t.Parallel()
	const utterance = "接続中断時の先読みを停止する"
	finalGate := make(chan struct{})
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.9,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.95,
		},
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: utterance,
		},
	)
	session.eventGates = map[int]<-chan struct{}{2: finalGate}
	full := bytes.Repeat([]byte{2, 0}, maxSpeculativeTTSBufferBytes/2)
	speech := &scriptedLiveSpeech{
		session: session,
		scripts: []scriptedSynthesis{{
			chunks: [][]byte{full, {3, 0}},
		}},
		chunkStarted: make(chan synthesisChunkEvent, 2),
		returned:     make(chan int, 1),
	}
	agent := &speculativeTestAgent{
		speculativeResult: liveTestDecision("先読み回答", "spec-state"),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1200, 0)
	pipeline.now = sequenceClock(
		started,
		started.Add(minSpeculativeStableDuration),
	)
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	delivered := make(chan struct{}, 1)
	go func() {
		_, processErr := pipeline.ProcessLive(
			ctx,
			"uid",
			httpapi.VoiceTurnInput{},
			audio,
			func([]byte) error {
				delivered <- struct{}{}
				return nil
			},
		)
		done <- processErr
	}()
	for index := 0; index < 2; index++ {
		select {
		case event := <-speech.chunkStarted:
			if event != (synthesisChunkEvent{call: 0, chunk: index}) {
				t.Fatalf("chunk event=%+v", event)
			}
		case <-time.After(time.Second):
			t.Fatalf("chunk %d did not start", index)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pipeline cancellation did not return")
	}
	select {
	case call := <-speech.returned:
		if call != 0 {
			t.Fatalf("returned call=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("full speculative TTS goroutine did not return")
	}
	select {
	case <-delivered:
		t.Fatal("cancelled speculative PCM escaped")
	default:
	}
}

func TestPipelineLiveSpeculationUsesFinalsPlusLatestInterim(t *testing.T) {
	t.Parallel()
	const (
		first    = "研究の前提を整理して"
		second   = "次の手順を説明して"
		expected = first + " " + second
	)
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: first,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      second,
			Stability: 0.9,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      second,
			Stability: 0.92,
		},
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: second,
		},
	)
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{
			fakeSpeech: fakeSpeech{},
			chunks:     [][]byte{{8, 0}},
		},
		session: session,
	}
	agent := &speculativeTestAgent{
		speculativeResult: liveTestDecision("統合した回答", "spec-state"),
		normalResult:      liveTestDecision("通常回答", "normal-state"),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(300, 0)
	pipeline.now = sequenceClock(
		started,
		started.Add(10*time.Millisecond),
		started.Add(200*time.Millisecond),
	)
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	result, err := pipeline.ProcessLive(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{},
		audio,
		func([]byte) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	turns := agent.recordedTurns()
	if len(turns) != 1 ||
		!turns[0].Speculative ||
		turns[0].Utterance != expected ||
		result.LiveTimings.SpecHit != 1 {
		t.Fatalf("turns=%+v result=%+v", turns, result)
	}
}

func TestPipelineLiveTreatsFinalAsStableSpeculativeObservation(t *testing.T) {
	t.Parallel()
	const utterance = "確定結果でも安定性を判定する"
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      utterance,
			Stability: 0.9,
		},
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: utterance,
		},
	)
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{
			fakeSpeech: fakeSpeech{},
			chunks:     [][]byte{{9, 0}},
		},
		session: session,
	}
	agent := &speculativeTestAgent{
		speculativeResult: liveTestDecision("先読み回答", "spec-state"),
		normalResult:      liveTestDecision("通常回答", "normal-state"),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(400, 0)
	pipeline.now = sequenceClock(
		started,
		started.Add(minSpeculativeStableDuration),
	)
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	result, err := pipeline.ProcessLive(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{},
		audio,
		func([]byte) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	turns := agent.recordedTurns()
	if len(turns) != 1 ||
		!turns[0].Speculative ||
		result.LiveTimings.SpecHit != 1 {
		t.Fatalf("turns=%+v timings=%+v", turns, result.LiveTimings)
	}
}

func TestPipelineLiveDiscardsUnsafeSpeculationAndRerunsFinal(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name           string
		interim        string
		final          string
		speculativeErr error
	}{
		{
			name:    "final mismatch",
			interim: "最初に推測した質問です",
			final:   "最後は全く異なる質問です",
		},
		{
			name:           "external action",
			interim:        "最新論文を調べてください",
			final:          "最新論文を調べてください",
			speculativeErr: conversation.ErrSpeculativeExternalAction,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			session := newFakeLiveTranscriptionSession(
				speechio.StreamingTranscriptionEvent{
					Kind:      speechio.StreamingTranscriptionInterim,
					Text:      test.interim,
					Stability: 0.9,
				},
				speechio.StreamingTranscriptionEvent{
					Kind:      speechio.StreamingTranscriptionInterim,
					Text:      test.interim,
					Stability: 0.95,
				},
				speechio.StreamingTranscriptionEvent{
					Kind: speechio.StreamingTranscriptionFinal,
					Text: test.final,
				},
			)
			speech := &fakeLiveSpeech{
				fakeStreamingSpeech: fakeStreamingSpeech{
					fakeSpeech: fakeSpeech{},
					chunks:     [][]byte{{10, 0}},
				},
				session: session,
			}
			agent := &speculativeTestAgent{
				speculativeResult: liveTestDecision(
					"外に出してはいけない仮回答",
					"provisional-state",
				),
				speculativeErr: test.speculativeErr,
				normalResult:   liveTestDecision("確定後の回答", "final-state"),
				started:        make(chan struct{}),
			}
			pipeline, err := New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Unix(500, 0)
			pipeline.now = sequenceClock(
				started,
				started.Add(minSpeculativeStableDuration),
			)
			audio := make(chan []byte, 1)
			audio <- []byte{1, 0}
			close(audio)
			var output []byte
			result, err := pipeline.ProcessLive(
				context.Background(),
				"uid",
				httpapi.VoiceTurnInput{},
				audio,
				func(chunk []byte) error {
					output = append(output, chunk...)
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-agent.started:
			case <-time.After(time.Second):
				t.Fatal("speculative call never started")
			}
			turns := agent.recordedTurns()
			if len(turns) != 2 {
				t.Fatalf("turns=%+v", turns)
			}
			var speculativeTurns, finalTurns int
			for _, turn := range turns {
				if turn.Speculative {
					speculativeTurns++
					if turn.Utterance != test.interim {
						t.Fatalf("speculative utterance=%q", turn.Utterance)
					}
					continue
				}
				finalTurns++
				if turn.Utterance != test.final {
					t.Fatalf("final utterance=%q", turn.Utterance)
				}
			}
			if speculativeTurns != 1 || finalTurns != 1 ||
				result.StateToken != "final-state" ||
				result.Caption != "確定後の回答" ||
				speech.synthesizedText != "確定後の回答" ||
				!bytes.Equal(output, []byte{10, 0}) {
				t.Fatalf(
					"spec=%d final=%d output=%v result=%+v synthesized=%q",
					speculativeTurns,
					finalTurns,
					output,
					result,
					speech.synthesizedText,
				)
			}
			if result.LiveTimings.SpecHit != 0 ||
				result.LiveTimings.SpecMiss != 1 ||
				result.LiveTimings.SpecCancel != 1 {
				t.Fatalf("timings=%+v", result.LiveTimings)
			}
		})
	}
}

func TestPipelineLiveSpeculativeCancellationDoesNotLeak(t *testing.T) {
	t.Parallel()
	const interim = "キャンセルされる先読み候補です"
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      interim,
			Stability: 0.9,
		},
		speechio.StreamingTranscriptionEvent{
			Kind:      speechio.StreamingTranscriptionInterim,
			Text:      interim,
			Stability: 0.95,
		},
		speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: "確定文は異なる内容です",
		},
	)
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{
			fakeSpeech: fakeSpeech{},
			chunks:     [][]byte{{11, 0}},
		},
		session: session,
	}
	agent := &speculativeTestAgent{
		normalResult:     liveTestDecision("確定回答", "final-state"),
		blockSpeculative: true,
		started:          make(chan struct{}),
		cancelled:        make(chan struct{}),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(600, 0)
	pipeline.now = sequenceClock(
		started,
		started.Add(minSpeculativeStableDuration),
	)
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	result, err := pipeline.ProcessLive(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{},
		audio,
		func([]byte) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-agent.started:
	case <-time.After(time.Second):
		t.Fatal("speculative call did not start")
	}
	select {
	case <-agent.cancelled:
	case <-time.After(time.Second):
		t.Fatal("discarded speculative call did not observe cancellation")
	}
	if result.StateToken != "final-state" ||
		result.LiveTimings.SpecMiss != 1 ||
		result.LiveTimings.SpecCancel != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestPipelineLiveSpeculationRequiresReplyExpectedDocumentFreeTurn(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		ambient    bool
		foreground bool
		document   *httpapi.VoiceDocument
		wantSpec   bool
		wantHit    int64
		wantMiss   int64
	}{
		{name: "ambient", ambient: true},
		{
			name:       "foreground",
			ambient:    true,
			foreground: true,
			wantSpec:   true,
			wantHit:    1,
		},
		{
			name: "document",
			document: &httpapi.VoiceDocument{
				MIMEType: "application/pdf",
				Data:     []byte("pdf"),
			},
			wantMiss: 1,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const utterance = "先読み条件を満たす長い発話です"
			session := newFakeLiveTranscriptionSession(
				speechio.StreamingTranscriptionEvent{
					Kind:      speechio.StreamingTranscriptionInterim,
					Text:      utterance,
					Stability: 0.9,
				},
				speechio.StreamingTranscriptionEvent{
					Kind:      speechio.StreamingTranscriptionInterim,
					Text:      utterance,
					Stability: 0.95,
				},
				speechio.StreamingTranscriptionEvent{
					Kind: speechio.StreamingTranscriptionFinal,
					Text: utterance,
				},
			)
			speech := &fakeLiveSpeech{
				fakeStreamingSpeech: fakeStreamingSpeech{
					fakeSpeech: fakeSpeech{},
					chunks:     [][]byte{{12, 0}},
				},
				session: session,
			}
			agent := &speculativeTestAgent{
				speculativeResult: liveTestDecision(
					"呼ばれてはいけない",
					"spec-state",
				),
				normalResult: liveTestDecision("通常回答", "normal-state"),
			}
			pipeline, err := New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Unix(700, 0)
			pipeline.now = sequenceClock(
				started,
				started.Add(minSpeculativeStableDuration),
			)
			audio := make(chan []byte, 1)
			audio <- []byte{1, 0}
			close(audio)
			result, err := pipeline.ProcessLive(
				context.Background(),
				"uid",
				httpapi.VoiceTurnInput{
					Ambient:    test.ambient,
					Foreground: test.foreground,
					Document:   test.document,
				},
				audio,
				func([]byte) error { return nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			turns := agent.recordedTurns()
			if len(turns) != 1 ||
				turns[0].Speculative != test.wantSpec ||
				turns[0].Foreground != test.foreground {
				t.Fatalf("turns=%+v", turns)
			}
			if result.LiveTimings.SpecHit != test.wantHit ||
				result.LiveTimings.SpecMiss != test.wantMiss ||
				result.LiveTimings.SpecCancel != 0 {
				t.Fatalf("timings=%+v", result.LiveTimings)
			}
		})
	}
}

type earlyEOFTranscriptionSession struct {
	events       []speechio.StreamingTranscriptionEvent
	eventIndex   int
	sendObserved chan struct{}
	sendOnce     sync.Once
}

func (session *earlyEOFTranscriptionSession) SendPCM([]byte) error {
	session.sendOnce.Do(func() {
		close(session.sendObserved)
	})
	return nil
}

func (*earlyEOFTranscriptionSession) CloseSend() error {
	return nil
}

func (session *earlyEOFTranscriptionSession) RecvEvent() (
	speechio.StreamingTranscriptionEvent,
	error,
) {
	if session.eventIndex >= len(session.events) {
		return speechio.StreamingTranscriptionEvent{}, io.EOF
	}
	event := session.events[session.eventIndex]
	session.eventIndex++
	return event, nil
}

type earlyEOFStreamingSpeech struct {
	fakeSpeech
	session   *earlyEOFTranscriptionSession
	ttsCalled chan struct{}
	ttsOnce   sync.Once
}

func (speech *earlyEOFStreamingSpeech) OpenStreamingTranscription(
	context.Context,
) (speechio.StreamingTranscriptionSession, error) {
	return speech.session, nil
}

func (speech *earlyEOFStreamingSpeech) StreamSynthesize(
	context.Context,
	string,
	speechio.StreamChunkHandler,
) (string, error) {
	speech.ttsOnce.Do(func() {
		close(speech.ttsCalled)
	})
	return speechio.StreamingAudioContentType, nil
}

type commitBoundaryAgent struct {
	called chan struct{}
	once   sync.Once
}

func (agent *commitBoundaryAgent) Process(
	context.Context,
	string,
	conversation.VoiceTurn,
) (conversation.VoiceTurnResult, error) {
	agent.once.Do(func() {
		close(agent.called)
	})
	return liveTestDecision("must not be spoken", "must-not-commit"), nil
}

func TestPipelineLiveProviderEOFFailsClosedBeforeCommit(t *testing.T) {
	t.Parallel()
	session := &earlyEOFTranscriptionSession{
		events: []speechio.StreamingTranscriptionEvent{{
			Kind: speechio.StreamingTranscriptionFinal,
			Text: "確定前にプロバイダーが終了した",
		}},
		sendObserved: make(chan struct{}),
	}
	speech := &earlyEOFStreamingSpeech{
		session:   session,
		ttsCalled: make(chan struct{}),
	}
	agent := &commitBoundaryAgent{called: make(chan struct{})}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	audio := make(chan []byte)
	defer close(audio)
	outputCalled := make(chan struct{}, 1)
	type pipelineOutcome struct {
		result httpapi.VoiceTurnResult
		err    error
	}
	done := make(chan pipelineOutcome, 1)
	go func() {
		result, processErr := pipeline.ProcessLive(
			ctx,
			"uid",
			httpapi.VoiceTurnInput{
				TurnMode: httpapi.VoiceTurnIntentional,
			},
			audio,
			func([]byte) error {
				outputCalled <- struct{}{}
				return nil
			},
		)
		done <- pipelineOutcome{result: result, err: processErr}
	}()

	select {
	case audio <- []byte{1, 0}:
	case <-time.After(time.Second):
		t.Fatal("live sender did not accept PCM")
	}
	select {
	case <-session.sendObserved:
	case <-time.After(time.Second):
		t.Fatal("streaming recognizer did not receive PCM")
	}
	select {
	case <-agent.called:
		t.Fatal("agent ran after provider EOF but before commit")
	case <-speech.ttsCalled:
		t.Fatal("TTS ran after provider EOF but before commit")
	case <-outputCalled:
		t.Fatal("audio escaped after provider EOF but before commit")
	case outcome := <-done:
		t.Fatalf("pipeline returned before commit: result=%+v err=%v",
			outcome.result, outcome.err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case outcome := <-done:
		stage, classified := httpapi.VoicePipelineStageOf(outcome.err)
		if !classified || stage != httpapi.VoicePipelineStageTranscribe {
			t.Fatalf(
				"provider EOF cancellation stage=%q classified=%v err=%v",
				stage,
				classified,
				outcome.err,
			)
		}
		if outcome.result.StateToken != "" ||
			outcome.result.Caption != "" {
			t.Fatalf("uncommitted result escaped: %+v", outcome.result)
		}
	case <-time.After(time.Second):
		t.Fatal("provider EOF cancellation left pipeline goroutine running")
	}
	select {
	case <-agent.called:
		t.Fatal("agent ran while canceling uncommitted provider EOF")
	case <-speech.ttsCalled:
		t.Fatal("TTS ran while canceling uncommitted provider EOF")
	case <-outputCalled:
		t.Fatal("audio escaped while canceling uncommitted provider EOF")
	default:
	}
}
