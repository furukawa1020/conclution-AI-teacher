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

func TestSpeculativeCandidateRequiresRepeatedStableCanonicalText(t *testing.T) {
	t.Parallel()
	started := time.Unix(100, 0)
	tracker := speculativeCandidateTracker{}
	decomposed := "Cafe\u0301  について　教えて"
	composed := "Café について 教えて"

	candidate, ready := tracker.observe(decomposed, true, started)
	if ready || candidate != composed {
		t.Fatalf("first observation candidate=%q ready=%v", candidate, ready)
	}
	if _, ready = tracker.observe(
		composed,
		true,
		started.Add(minSpeculativeStableDuration-time.Millisecond),
	); ready {
		t.Fatal("candidate became stable before 160ms")
	}
	if _, ready = tracker.observe(
		composed,
		true,
		started.Add(minSpeculativeStableDuration),
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
		"この質問を説明して？ ",
		"この質問を説明して!",
	) {
		t.Fatal("trailing allowed punctuation should not prevent adoption")
	}
	if speculationTextsMatch(
		"この？質問を説明して",
		"この！質問を説明して",
	) {
		t.Fatal("internal punctuation was incorrectly ignored")
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
			Text: utterance + "。",
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
		outcome.result.LiveTimings.FinalToFirstAudioMS < 0 {
		t.Fatalf("timings=%+v", outcome.result.LiveTimings)
	}
	turns := agent.recordedTurns()
	if len(turns) != 1 ||
		!turns[0].Speculative ||
		turns[0].Utterance != utterance {
		t.Fatalf("turns=%+v", turns)
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

func TestPipelineLiveSpeculationRequiresIntentionalDocumentFreeTurn(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		ambient  bool
		document *httpapi.VoiceDocument
	}{
		{name: "ambient", ambient: true},
		{
			name: "document",
			document: &httpapi.VoiceDocument{
				MIMEType: "application/pdf",
				Data:     []byte("pdf"),
			},
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
					Ambient:  test.ambient,
					Document: test.document,
				},
				audio,
				func([]byte) error { return nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			turns := agent.recordedTurns()
			if len(turns) != 1 || turns[0].Speculative {
				t.Fatalf("turns=%+v", turns)
			}
			wantMiss := int64(1)
			if test.ambient {
				wantMiss = 0
			}
			if result.LiveTimings.SpecHit != 0 ||
				result.LiveTimings.SpecMiss != wantMiss ||
				result.LiveTimings.SpecCancel != 0 {
				t.Fatalf("timings=%+v", result.LiveTimings)
			}
		})
	}
}
