package voiceflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

// captionHandoffNoSTTSpeech makes any accidental second recognizer pass
// observable while reusing the live synthesis fake used by the speculation
// tests. Caption handoff must call only StreamSynthesize.
type captionHandoffNoSTTSpeech struct {
	*scriptedLiveSpeech

	callsMu         sync.Mutex
	transcribeCalls int
	streamOpenCalls int
}

func (speech *captionHandoffNoSTTSpeech) Transcribe(
	context.Context,
	[]byte,
) (string, float32, error) {
	speech.callsMu.Lock()
	speech.transcribeCalls++
	speech.callsMu.Unlock()
	return "", 0, errors.New("caption handoff unexpectedly ran buffered STT")
}

func (speech *captionHandoffNoSTTSpeech) OpenStreamingTranscription(
	context.Context,
) (speechio.StreamingTranscriptionSession, error) {
	speech.callsMu.Lock()
	speech.streamOpenCalls++
	speech.callsMu.Unlock()
	return nil, errors.New("caption handoff unexpectedly opened streaming STT")
}

func (speech *captionHandoffNoSTTSpeech) sttCalls() (int, int) {
	speech.callsMu.Lock()
	defer speech.callsMu.Unlock()
	return speech.transcribeCalls, speech.streamOpenCalls
}

func captionHandoffRespondentDecision() conversation.VoiceTurnResult {
	return conversation.VoiceTurnResult{
		Domain:           "conversation",
		AssistanceTarget: "respondent",
		RespondentStage:  "awaiting_answer",
		CoachPhase:       "awaiting_answer",
		CoachAction:      "elicit",
		ResearchStatus:   "none",
		ResearchRecords:  []conversation.ResearchRecord{},
		SpokenReply:      "Lead with the purpose.",
		Route:            "caption-handoff-test",
		StateToken:       "signed-question-bound-state",
	}
}

func TestCaptionHandoffCheckpointsEveryLegalRespondentShapeBeforePCM(
	t *testing.T,
) {
	shapes := []struct {
		stage  string
		phase  string
		action string
	}{
		{stage: "awaiting_answer", phase: "awaiting_answer", action: "elicit"},
		{stage: "restructure", phase: "awaiting_restatement", action: "restate"},
		{stage: "awaiting_answer", phase: "expanding", action: "expand"},
		{stage: "restructure", phase: "complete", action: "complete"},
		{stage: "restructure", phase: "blocked", action: "retry"},
		{stage: "restructure", phase: "blocked", action: "release"},
	}
	for _, shape := range shapes {
		name := shape.stage + "/" + shape.phase + "/" + shape.action
		t.Run(name, func(t *testing.T) {
			decision := captionHandoffRespondentDecision()
			decision.RespondentStage = shape.stage
			decision.CoachPhase = shape.phase
			decision.CoachAction = shape.action
			speech := &captionHandoffNoSTTSpeech{
				scriptedLiveSpeech: &scriptedLiveSpeech{
					scripts: []scriptedSynthesis{{chunks: [][]byte{{1, 0}}}},
				},
			}
			pipeline, err := New(speech, &fakeAgent{result: decision})
			if err != nil {
				t.Fatal(err)
			}
			processingCommitted := make(chan struct{})
			checkpointAccepted := false
			audioCalls := 0
			checkpointCalls := 0
			handoff, err := pipeline.OpenCaptionHandoff(
				context.Background(),
				"uid-all-respondent-shapes",
				httpapi.VoiceTurnInput{
					MIMEType:            "audio/L16",
					NativeAudio:         true,
					ProcessingCommitted: processingCommitted,
				},
				func([]byte) error {
					if !checkpointAccepted {
						return errors.New("PCM crossed before checkpoint")
					}
					audioCalls++
					return nil
				},
				func(checkpoint httpapi.VoiceRespondentCheckpoint) error {
					checkpointCalls++
					if checkpoint.SessionState != decision.StateToken ||
						checkpoint.Route != httpapi.VoiceNativeRespondentCoachRoute ||
						checkpoint.AssistanceTarget != "respondent" ||
						checkpoint.RespondentStage != shape.stage ||
						checkpoint.CoachPhase != shape.phase ||
						checkpoint.CoachAction != shape.action {
						return errors.New("unexpected respondent checkpoint")
					}
					checkpointAccepted = true
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := handoff.Observe(
				[]byte("My manager asked why. Help me answer."),
				true,
				time.Now(),
			); err != nil {
				t.Fatal(err)
			}
			close(processingCommitted)
			result, err := handoff.Commit()
			if err != nil {
				t.Fatal(err)
			}
			if checkpointCalls != 1 || audioCalls != 1 ||
				result.Route != httpapi.VoiceNativeRespondentCoachRoute {
				t.Fatalf(
					"checkpoint=%d audio=%d result=%+v",
					checkpointCalls,
					audioCalls,
					result,
				)
			}
		})
	}
}

func TestCaptionHandoffCheckpointsSilentRespondentTransition(t *testing.T) {
	decision := captionHandoffRespondentDecision()
	decision.RespondentStage = "awaiting_answer"
	decision.CoachPhase = "expanding"
	decision.CoachAction = "expand"
	decision.SpokenReply = ""
	speech := &captionHandoffNoSTTSpeech{
		scriptedLiveSpeech: &scriptedLiveSpeech{},
	}
	pipeline, err := New(speech, &fakeAgent{result: decision})
	if err != nil {
		t.Fatal(err)
	}
	processingCommitted := make(chan struct{})
	checkpointCalls := 0
	audioCalls := 0
	handoff, err := pipeline.OpenCaptionHandoff(
		context.Background(),
		"uid-silent-respondent-checkpoint",
		httpapi.VoiceTurnInput{
			MIMEType:            "audio/L16",
			NativeAudio:         true,
			ProcessingCommitted: processingCommitted,
		},
		func([]byte) error { audioCalls++; return nil },
		func(checkpoint httpapi.VoiceRespondentCheckpoint) error {
			checkpointCalls++
			if checkpoint.CoachPhase != "expanding" ||
				checkpoint.CoachAction != "expand" {
				return errors.New("unexpected silent checkpoint")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.Observe(
		[]byte("I want to keep going."),
		true,
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	close(processingCommitted)
	result, err := handoff.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if checkpointCalls != 1 || audioCalls != 0 || result.Caption != "" ||
		len(speech.synthesisTexts()) != 0 {
		t.Fatalf(
			"checkpoint=%d audio=%d result=%+v synthesis=%v",
			checkpointCalls,
			audioCalls,
			result,
			speech.synthesisTexts(),
		)
	}
}

func TestCaptionHandoffDoesNotCountDigitalSilenceAsFirstAudio(t *testing.T) {
	decision := captionHandoffRespondentDecision()
	speech := &captionHandoffNoSTTSpeech{
		scriptedLiveSpeech: &scriptedLiveSpeech{
			scripts: []scriptedSynthesis{{chunks: [][]byte{{0, 0, 0, 0}}}},
		},
	}
	pipeline, err := New(speech, &fakeAgent{result: decision})
	if err != nil {
		t.Fatal(err)
	}
	processingCommitted := make(chan struct{})
	var delivered []byte
	handoff, err := pipeline.OpenCaptionHandoff(
		context.Background(),
		"uid-caption-silent-output-metric",
		httpapi.VoiceTurnInput{
			MIMEType:            "audio/L16",
			NativeAudio:         true,
			ProcessingCommitted: processingCommitted,
		},
		func(chunk []byte) error {
			delivered = append(delivered, chunk...)
			return nil
		},
		func(httpapi.VoiceRespondentCheckpoint) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.Observe(
		[]byte("My manager asked why. Help me answer."),
		true,
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	close(processingCommitted)
	result, err := handoff.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if string(delivered) != string([]byte{0, 0, 0, 0}) ||
		result.LiveTimings.FinalToFirstAudioMS != -1 {
		t.Fatalf("delivered=%v timings=%+v", delivered, result.LiveTimings)
	}
}

func TestCaptionHandoff159RunesMayAdoptSpeculationAfterCheckpoint(
	t *testing.T,
) {
	completed := make(chan int, 1)
	speech := &captionHandoffNoSTTSpeech{scriptedLiveSpeech: &scriptedLiveSpeech{
		scripts:   []scriptedSynthesis{{chunks: [][]byte{{1, 0, 2, 0}}}},
		completed: completed,
	}}
	agent := &speculativeTestAgent{
		speculativeResult: captionHandoffRespondentDecision(),
		normalErr:         errors.New("exact caption should adopt speculative work"),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	var checkpointAccepted atomic.Bool
	var eventMu sync.Mutex
	var events []string
	var delivered []byte
	processingCommitted := make(chan struct{})
	handoff, err := pipeline.OpenCaptionHandoff(
		context.Background(),
		"uid-caption-speculation",
		httpapi.VoiceTurnInput{
			MIMEType:            "audio/L16",
			NativeAudio:         true,
			RequestID:           "caption-speculation",
			ProcessingCommitted: processingCommitted,
		},
		func(chunk []byte) error {
			if !checkpointAccepted.Load() {
				return errors.New("PCM crossed before checkpoint")
			}
			eventMu.Lock()
			events = append(events, "pcm")
			delivered = append(delivered, chunk...)
			eventMu.Unlock()
			return nil
		},
		func(checkpoint httpapi.VoiceRespondentCheckpoint) error {
			if checkpoint.SessionState != "signed-question-bound-state" ||
				checkpoint.Route != httpapi.VoiceNativeRespondentCoachRoute ||
				checkpoint.AssistanceTarget != "respondent" ||
				checkpoint.RespondentStage != "awaiting_answer" ||
				checkpoint.CoachPhase != "awaiting_answer" ||
				checkpoint.CoachAction != "elicit" {
				return errors.New("unexpected checkpoint token")
			}
			eventMu.Lock()
			events = append(events, "checkpoint")
			eventMu.Unlock()
			checkpointAccepted.Store(true)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	caption := strings.Repeat("界", extendedSpeechMinRunes-1)
	started := time.Now()
	if err := handoff.Observe([]byte(caption), false, started); err != nil {
		t.Fatal(err)
	}
	if err := handoff.Observe(
		[]byte(caption),
		false,
		started.Add(minSpeculativeStableDuration),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("speculative synthesis did not finish behind commit buffer")
	}
	eventMu.Lock()
	if len(events) != 0 || len(delivered) != 0 {
		eventMu.Unlock()
		t.Fatalf(
			"speculative PCM crossed before exact final: events=%v audio=%v",
			events,
			delivered,
		)
	}
	eventMu.Unlock()
	if err := handoff.Observe(
		[]byte(caption),
		true,
		started.Add(minSpeculativeStableDuration+time.Millisecond),
	); err != nil {
		t.Fatal(err)
	}
	close(processingCommitted)
	result, err := handoff.Commit()
	if err != nil {
		t.Fatal(err)
	}

	bufferedCalls, streamingCalls := speech.sttCalls()
	if bufferedCalls != 0 || streamingCalls != 0 {
		t.Fatalf(
			"caption donation ran STT: buffered=%d streaming=%d",
			bufferedCalls,
			streamingCalls,
		)
	}
	turns := agent.recordedTurns()
	if len(turns) != 1 || !turns[0].Speculative || turns[0].ExtendedSpeech ||
		turns[0].InputOrigin != conversation.InputOriginProvisionalVoice ||
		!turns[0].OutputCancelable ||
		turns[0].Utterance != caption {
		t.Fatalf("agent turns = %+v", turns)
	}
	eventMu.Lock()
	gotEvents := append([]string(nil), events...)
	gotAudio := append([]byte(nil), delivered...)
	eventMu.Unlock()
	if len(gotEvents) != 2 || gotEvents[0] != "checkpoint" ||
		gotEvents[1] != "pcm" {
		t.Fatalf("release order = %v", gotEvents)
	}
	if string(gotAudio) != string([]byte{1, 0, 2, 0}) {
		t.Fatalf("delivered PCM = %v", gotAudio)
	}
	if result.Caption != agent.speculativeResult.SpokenReply ||
		result.StateToken != "signed-question-bound-state" ||
		result.LiveTimings.NativeCaptionHandoff != 1 ||
		result.LiveTimings.SpecHit != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCaptionHandoff160RunesUsesCommittedExtendedSpeechOnly(
	t *testing.T,
) {
	speech := &captionHandoffNoSTTSpeech{scriptedLiveSpeech: &scriptedLiveSpeech{
		scripts: []scriptedSynthesis{{chunks: [][]byte{{7, 0, 8, 0}}}},
	}}
	agent := &speculativeTestAgent{
		speculativeResult: captionHandoffRespondentDecision(),
		normalResult:      captionHandoffRespondentDecision(),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	processingCommitted := make(chan struct{})
	var eventMu sync.Mutex
	var events []string
	var delivered []byte
	handoff, err := pipeline.OpenCaptionHandoff(
		context.Background(),
		"uid-caption-extended-boundary",
		httpapi.VoiceTurnInput{
			MIMEType:            "audio/L16",
			NativeAudio:         true,
			RequestID:           "caption-extended-boundary",
			ProcessingCommitted: processingCommitted,
		},
		func(chunk []byte) error {
			eventMu.Lock()
			events = append(events, "pcm")
			delivered = append(delivered, chunk...)
			eventMu.Unlock()
			return nil
		},
		func(checkpoint httpapi.VoiceRespondentCheckpoint) error {
			if checkpoint.SessionState != "signed-question-bound-state" {
				return errors.New("unexpected checkpoint token")
			}
			eventMu.Lock()
			events = append(events, "checkpoint")
			eventMu.Unlock()
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	caption := strings.Repeat("界", extendedSpeechMinRunes)
	started := time.Now()
	if err := handoff.Observe([]byte(caption), false, started); err != nil {
		t.Fatal(err)
	}
	if err := handoff.Observe(
		[]byte(caption),
		false,
		started.Add(minSpeculativeStableDuration),
	); err != nil {
		t.Fatal(err)
	}
	concrete := handoff.(*captionHandoff)
	concrete.mu.Lock()
	speculation := concrete.speculation
	concrete.mu.Unlock()
	if speculation != nil {
		speculation.cancel()
		t.Fatal("160-rune caption retained metadata-unsafe speculation")
	}
	if turns := agent.recordedTurns(); len(turns) != 0 {
		t.Fatalf("agent ran before committed processing: %+v", turns)
	}
	if got := speech.synthesisTexts(); len(got) != 0 {
		t.Fatalf("TTS ran before committed processing: %v", got)
	}
	eventMu.Lock()
	beforeCommitEvents := append([]string(nil), events...)
	beforeCommitAudio := append([]byte(nil), delivered...)
	eventMu.Unlock()
	if len(beforeCommitEvents) != 0 || len(beforeCommitAudio) != 0 {
		t.Fatalf(
			"output crossed before committed processing: events=%v audio=%v",
			beforeCommitEvents,
			beforeCommitAudio,
		)
	}

	if err := handoff.Observe(
		[]byte(caption),
		true,
		started.Add(minSpeculativeStableDuration+time.Millisecond),
	); err != nil {
		t.Fatal(err)
	}
	if turns := agent.recordedTurns(); len(turns) != 0 {
		t.Fatalf("final caption ran agent before transport commit: %+v", turns)
	}
	eventMu.Lock()
	beforeCommitEvents = append([]string(nil), events...)
	beforeCommitAudio = append([]byte(nil), delivered...)
	eventMu.Unlock()
	if len(beforeCommitEvents) != 0 || len(beforeCommitAudio) != 0 {
		t.Fatalf(
			"final caption published before transport commit: events=%v audio=%v",
			beforeCommitEvents,
			beforeCommitAudio,
		)
	}

	close(processingCommitted)
	result, err := handoff.Commit()
	if err != nil {
		t.Fatal(err)
	}

	turns := agent.recordedTurns()
	if len(turns) != 1 || turns[0].Speculative || !turns[0].ExtendedSpeech ||
		turns[0].InputOrigin != conversation.InputOriginCommittedVoice ||
		!turns[0].OutputCancelable || turns[0].Utterance != caption {
		t.Fatalf("committed turns = %+v", turns)
	}
	if got := speech.synthesisTexts(); len(got) != 1 ||
		got[0] != agent.normalResult.SpokenReply {
		t.Fatalf("synthesis texts = %v", got)
	}
	eventMu.Lock()
	gotEvents := append([]string(nil), events...)
	gotAudio := append([]byte(nil), delivered...)
	eventMu.Unlock()
	if len(gotEvents) != 2 || gotEvents[0] != "checkpoint" ||
		gotEvents[1] != "pcm" {
		t.Fatalf("release order = %v", gotEvents)
	}
	if string(gotAudio) != string([]byte{7, 0, 8, 0}) {
		t.Fatalf("delivered PCM = %v", gotAudio)
	}
	if result.LiveTimings.SpecHit != 0 || result.LiveTimings.SpecMiss != 1 ||
		result.LiveTimings.SpecCancel != 0 || result.LiveTimings.TTSPrestarted != 0 ||
		result.LiveTimings.NativeCaptionHandoff != 1 {
		t.Fatalf("result = %+v", result)
	}
	bufferedCalls, streamingCalls := speech.sttCalls()
	if bufferedCalls != 0 || streamingCalls != 0 {
		t.Fatalf(
			"caption boundary ran STT: buffered=%d streaming=%d",
			bufferedCalls,
			streamingCalls,
		)
	}
}

func TestCaptionHandoffMismatchCancelsPrivateAudioAndRunsOneCommittedPass(
	t *testing.T,
) {
	completed := make(chan int, 2)
	speech := &captionHandoffNoSTTSpeech{scriptedLiveSpeech: &scriptedLiveSpeech{
		scripts: []scriptedSynthesis{
			{chunks: [][]byte{{1, 0}}},
			{chunks: [][]byte{{2, 0}}},
		},
		completed: completed,
	}}
	speculativeCaption := "My manager asked why the change was needed."
	finalCaption := "My manager asked what the change was for."
	speculativeDecision := captionHandoffRespondentDecision()
	speculativeDecision.SpokenReply = "speculative reply"
	speculativeDecision.StateToken = "spec-state"
	committedDecision := captionHandoffRespondentDecision()
	committedDecision.SpokenReply = "committed reply"
	committedDecision.StateToken = "committed-state"
	agent := &speculativeTestAgent{
		speculativeResult: speculativeDecision,
		normalResult:      committedDecision,
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	var checkpointAccepted atomic.Bool
	checkpointCalls := 0
	var delivered []byte
	processingCommitted := make(chan struct{})
	handoff, err := pipeline.OpenCaptionHandoff(
		context.Background(),
		"uid-caption-mismatch",
		httpapi.VoiceTurnInput{
			MIMEType:            "audio/L16",
			NativeAudio:         true,
			RequestID:           "caption-mismatch",
			ProcessingCommitted: processingCommitted,
		},
		func(chunk []byte) error {
			if !checkpointAccepted.Load() {
				return errors.New("mismatched PCM crossed before checkpoint")
			}
			delivered = append(delivered, chunk...)
			return nil
		},
		func(checkpoint httpapi.VoiceRespondentCheckpoint) error {
			checkpointCalls++
			if checkpoint.SessionState != "committed-state" {
				return errors.New("speculative state escaped mismatch cancellation")
			}
			checkpointAccepted.Store(true)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	if err := handoff.Observe([]byte(speculativeCaption), false, started); err != nil {
		t.Fatal(err)
	}
	if err := handoff.Observe(
		[]byte(speculativeCaption),
		false,
		started.Add(minSpeculativeStableDuration),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("speculative synthesis did not finish")
	}
	if len(delivered) != 0 {
		t.Fatalf("mismatched speculative PCM escaped before final: %v", delivered)
	}
	if err := handoff.Observe(
		[]byte(finalCaption),
		true,
		started.Add(minSpeculativeStableDuration+time.Millisecond),
	); err != nil {
		t.Fatal(err)
	}
	close(processingCommitted)
	result, err := handoff.Commit()
	if err != nil {
		t.Fatal(err)
	}

	turns := agent.recordedTurns()
	if len(turns) != 2 || !turns[0].Speculative || turns[0].Utterance != speculativeCaption ||
		turns[1].Speculative || turns[1].Utterance != finalCaption {
		t.Fatalf("agent turns = %+v", turns)
	}
	if got := speech.synthesisTexts(); len(got) != 2 ||
		got[0] != "speculative reply" || got[1] != "committed reply" {
		t.Fatalf("synthesis texts = %v", got)
	}
	if string(delivered) != string([]byte{2, 0}) {
		t.Fatalf("delivered PCM = %v; want committed audio only", delivered)
	}
	if checkpointCalls != 1 {
		t.Fatalf("checkpoint calls = %d; want one committed checkpoint", checkpointCalls)
	}
	if result.Caption != "committed reply" ||
		result.LiveTimings.SpecHit != 0 || result.LiveTimings.SpecMiss != 1 ||
		result.LiveTimings.SpecCancel != 1 ||
		result.LiveTimings.NativeCaptionHandoff != 1 {
		t.Fatalf("result = %+v", result)
	}
	bufferedCalls, streamingCalls := speech.sttCalls()
	if bufferedCalls != 0 || streamingCalls != 0 {
		t.Fatalf(
			"caption mismatch ran STT: buffered=%d streaming=%d",
			bufferedCalls,
			streamingCalls,
		)
	}
}

type cancelBlockingCaptionHandoffAgent struct {
	started chan struct{}
	once    sync.Once
	result  conversation.VoiceTurnResult
}

func (agent *cancelBlockingCaptionHandoffAgent) Process(
	ctx context.Context,
	_ string,
	_ conversation.VoiceTurn,
) (conversation.VoiceTurnResult, error) {
	agent.once.Do(func() { close(agent.started) })
	<-ctx.Done()
	// Deliberately return a usable decision after cancellation. The handoff
	// boundary, rather than cooperative downstream behavior, must still stop
	// checkpoint and PCM release.
	return agent.result, nil
}

func TestCaptionHandoffCancelDuringCommittedPassFailsClosed(t *testing.T) {
	speech := &captionHandoffNoSTTSpeech{
		scriptedLiveSpeech: &scriptedLiveSpeech{},
	}
	agent := &cancelBlockingCaptionHandoffAgent{
		started: make(chan struct{}),
		result:  captionHandoffRespondentDecision(),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	processingCommitted := make(chan struct{})
	audioCalls := 0
	checkpointCalls := 0
	handoff, err := pipeline.OpenCaptionHandoff(
		context.Background(),
		"uid-caption-cancel-race",
		httpapi.VoiceTurnInput{
			MIMEType:            "audio/L16",
			NativeAudio:         true,
			ProcessingCommitted: processingCommitted,
		},
		func([]byte) error { audioCalls++; return nil },
		func(httpapi.VoiceRespondentCheckpoint) error {
			checkpointCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.Observe(
		[]byte("My manager asked why. Help me answer."),
		true,
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	close(processingCommitted)

	type commitOutcome struct {
		err error
	}
	committed := make(chan commitOutcome, 1)
	go func() {
		_, commitErr := handoff.Commit()
		committed <- commitOutcome{err: commitErr}
	}()
	select {
	case <-agent.started:
	case <-time.After(time.Second):
		t.Fatal("committed agent pass did not start")
	}
	handoff.Cancel()
	select {
	case outcome := <-committed:
		if !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("Commit error = %v; want context cancellation", outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Cancel did not stop the committed pass")
	}
	if audioCalls != 0 || checkpointCalls != 0 ||
		len(speech.synthesisTexts()) != 0 {
		t.Fatalf(
			"cancel leak: audio=%d checkpoint=%d synthesis=%v",
			audioCalls,
			checkpointCalls,
			speech.synthesisTexts(),
		)
	}
}

func TestCaptionHandoffDeadlineBeforeAuthorizationFailsClosed(t *testing.T) {
	speech := &captionHandoffNoSTTSpeech{
		scriptedLiveSpeech: &scriptedLiveSpeech{},
	}
	agent := &cancelBlockingCaptionHandoffAgent{
		started: make(chan struct{}),
		result:  captionHandoffRespondentDecision(),
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	processingCommitted := make(chan struct{})
	audioCalls := 0
	checkpointCalls := 0
	handoff, err := pipeline.OpenCaptionHandoff(
		ctx,
		"uid-caption-deadline",
		httpapi.VoiceTurnInput{
			MIMEType:            "audio/L16",
			NativeAudio:         true,
			ProcessingCommitted: processingCommitted,
		},
		func([]byte) error { audioCalls++; return nil },
		func(httpapi.VoiceRespondentCheckpoint) error {
			checkpointCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.Observe(
		[]byte("My manager asked why. Help me answer."),
		true,
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	close(processingCommitted)
	_, err = handoff.Commit()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Commit error = %v; want deadline exceeded", err)
	}
	if audioCalls != 0 || checkpointCalls != 0 ||
		len(speech.synthesisTexts()) != 0 {
		t.Fatalf(
			"deadline leak: audio=%d checkpoint=%d synthesis=%v",
			audioCalls,
			checkpointCalls,
			speech.synthesisTexts(),
		)
	}
}

func TestCaptionHandoffCancellationDuringCheckpointReleasesNoPCM(t *testing.T) {
	speech := &captionHandoffNoSTTSpeech{
		scriptedLiveSpeech: &scriptedLiveSpeech{
			scripts: []scriptedSynthesis{{chunks: [][]byte{{5, 0}}}},
		},
	}
	pipeline, err := New(
		speech,
		&fakeAgent{result: captionHandoffRespondentDecision()},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processingCommitted := make(chan struct{})
	audioCalls := 0
	checkpointCalls := 0
	handoff, err := pipeline.OpenCaptionHandoff(
		ctx,
		"uid-caption-checkpoint-cancel",
		httpapi.VoiceTurnInput{
			MIMEType:            "audio/L16",
			NativeAudio:         true,
			ProcessingCommitted: processingCommitted,
		},
		func([]byte) error { audioCalls++; return nil },
		func(httpapi.VoiceRespondentCheckpoint) error {
			checkpointCalls++
			cancel()
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.Observe(
		[]byte("My manager asked why. Help me answer."),
		true,
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	close(processingCommitted)
	_, err = handoff.Commit()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit error = %v; want context cancellation", err)
	}
	if checkpointCalls != 1 || audioCalls != 0 ||
		len(speech.synthesisTexts()) != 0 {
		t.Fatalf(
			"checkpoint cancellation leak: checkpoint=%d audio=%d synthesis=%v",
			checkpointCalls,
			audioCalls,
			speech.synthesisTexts(),
		)
	}
}

func TestCaptionHandoffRejectsMissingOrFailedCheckpointBeforeRespondentAudio(
	t *testing.T,
) {
	callbackFailure := errors.New("control checkpoint write failed")
	tests := []struct {
		name       string
		checkpoint func(httpapi.VoiceRespondentCheckpoint) error
		wantErr    error
	}{
		{name: "missing callback", wantErr: errCaptionHandoffState},
		{
			name: "callback failure",
			checkpoint: func(httpapi.VoiceRespondentCheckpoint) error {
				return callbackFailure
			},
			wantErr: callbackFailure,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			speech := &captionHandoffNoSTTSpeech{
				scriptedLiveSpeech: &scriptedLiveSpeech{
					scripts: []scriptedSynthesis{{
						chunks: [][]byte{{9, 0}},
					}},
				},
			}
			agent := &fakeAgent{result: captionHandoffRespondentDecision()}
			pipeline, err := New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			audioCalls := 0
			processingCommitted := make(chan struct{})
			handoff, err := pipeline.OpenCaptionHandoff(
				context.Background(),
				"uid-caption-checkpoint",
				httpapi.VoiceTurnInput{
					MIMEType:            "audio/L16",
					NativeAudio:         true,
					ProcessingCommitted: processingCommitted,
				},
				func([]byte) error {
					audioCalls++
					return nil
				},
				test.checkpoint,
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := handoff.Observe(
				[]byte("My manager asked why. Help me answer."),
				true,
				time.Now(),
			); err != nil {
				t.Fatal(err)
			}
			close(processingCommitted)
			_, err = handoff.Commit()
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Commit error = %v; want %v", err, test.wantErr)
			}
			if audioCalls != 0 || len(speech.synthesisTexts()) != 0 {
				t.Fatalf(
					"respondent audio started before checkpoint: audio=%d synthesis=%v",
					audioCalls,
					speech.synthesisTexts(),
				)
			}
			bufferedCalls, streamingCalls := speech.sttCalls()
			if bufferedCalls != 0 || streamingCalls != 0 {
				t.Fatalf(
					"caption donation ran STT: buffered=%d streaming=%d",
					bufferedCalls,
					streamingCalls,
				)
			}
		})
	}
}
