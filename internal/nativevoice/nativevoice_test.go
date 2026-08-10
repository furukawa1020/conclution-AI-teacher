package nativevoice

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestOpenUsesReviewedNativeAudioConfiguration(t *testing.T) {
	provider := newFakeProviderSession()
	provider.push(&genai.LiveServerMessage{
		SetupComplete: &genai.LiveServerSetupComplete{},
	}, nil)
	dialer := &fakeDialer{session: provider}
	service := newTestService(t, Config{
		ProjectID:    "test-project",
		SystemPrompt: "安心できる短い日本語で答える。",
		VoiceName:    "Aoede",
	}, dialer)

	session, err := service.Open(context.Background())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	dialer.mu.Lock()
	model := dialer.model
	config := dialer.config
	dialer.mu.Unlock()
	if model != DefaultModel {
		t.Fatalf("model = %q, want %q", model, DefaultModel)
	}
	if config == nil {
		t.Fatal("Connect() config is nil")
	}
	if got := config.ResponseModalities; len(got) != 1 ||
		got[0] != genai.ModalityAudio {
		t.Fatalf("ResponseModalities = %#v", got)
	}
	if config.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Fatalf(
			"MaxOutputTokens = %d, want default %d",
			config.MaxOutputTokens,
			DefaultMaxOutputTokens,
		)
	}
	if config.SystemInstruction == nil || len(config.SystemInstruction.Parts) != 1 ||
		config.SystemInstruction.Parts[0].Text != "安心できる短い日本語で答える。" ||
		config.SystemInstruction.Role != "" {
		t.Fatalf("SystemInstruction = %#v", config.SystemInstruction)
	}
	if config.SpeechConfig == nil || config.SpeechConfig.VoiceConfig == nil ||
		config.SpeechConfig.VoiceConfig.PrebuiltVoiceConfig == nil ||
		config.SpeechConfig.VoiceConfig.PrebuiltVoiceConfig.VoiceName != "Aoede" {
		t.Fatalf("SpeechConfig = %#v", config.SpeechConfig)
	}
	if config.InputAudioTranscription == nil ||
		config.OutputAudioTranscription == nil ||
		len(config.InputAudioTranscription.LanguageCodes) != 1 ||
		config.InputAudioTranscription.LanguageCodes[0] != "ja-JP" {
		t.Fatal("Japanese input/output transcription was not enabled")
	}
	if config.RealtimeInputConfig == nil ||
		config.RealtimeInputConfig.AutomaticActivityDetection == nil ||
		!config.RealtimeInputConfig.AutomaticActivityDetection.Disabled ||
		config.RealtimeInputConfig.ActivityHandling != genai.ActivityHandlingStartOfActivityInterrupts ||
		config.RealtimeInputConfig.TurnCoverage != genai.TurnCoverageTurnIncludesOnlyActivity {
		t.Fatalf("RealtimeInputConfig = %#v", config.RealtimeInputConfig)
	}
	if config.EnableAffectiveDialog == nil || *config.EnableAffectiveDialog {
		t.Fatal("affective dialog was not explicitly disabled")
	}
	if config.Proactivity == nil || config.Proactivity.ProactiveAudio == nil ||
		*config.Proactivity.ProactiveAudio {
		t.Fatal("proactive audio was not explicitly disabled")
	}
	if config.Tools != nil || config.SessionResumption != nil ||
		config.ContextWindowCompression != nil || config.StreamTranslationConfig != nil ||
		config.ExplicitVADSignal != nil {
		t.Fatal("a disabled Live feature was configured")
	}
	if len(config.SafetySettings) != 4 {
		t.Fatalf("len(SafetySettings) = %d, want 4", len(config.SafetySettings))
	}
}

func TestOpenPassesExactConfiguredNativeAudioResponseBudget(t *testing.T) {
	provider := newFakeProviderSession()
	provider.push(&genai.LiveServerMessage{
		SetupComplete: &genai.LiveServerSetupComplete{},
	}, nil)
	dialer := &fakeDialer{session: provider}
	service := newTestService(t, Config{MaxOutputTokens: 320}, dialer)

	session, err := service.Open(context.Background())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	dialer.mu.Lock()
	config := dialer.config
	dialer.mu.Unlock()
	if config == nil {
		t.Fatal("Connect() config is nil")
	}
	if config.MaxOutputTokens != 320 {
		t.Fatalf("MaxOutputTokens = %d, want 320", config.MaxOutputTokens)
	}
}

func TestConfigurationBoundsNativeAudioResponseBudget(t *testing.T) {
	dialer := &fakeDialer{session: newFakeProviderSession()}
	for _, budget := range []int32{minMaxOutputTokens, DefaultMaxOutputTokens, hardMaxOutputTokens} {
		service, err := NewWithDialer(Config{
			ProjectID:       "project",
			SystemPrompt:    "prompt",
			MaxOutputTokens: budget,
		}, dialer)
		if err != nil {
			t.Fatalf("MaxOutputTokens %d rejected: %v", budget, err)
		}
		if service.config.MaxOutputTokens != budget {
			t.Fatalf(
				"normalized MaxOutputTokens = %d, want %d",
				service.config.MaxOutputTokens,
				budget,
			)
		}
	}

	for _, budget := range []int32{-1, minMaxOutputTokens - 1, hardMaxOutputTokens + 1} {
		_, err := NewWithDialer(Config{
			ProjectID:       "project",
			SystemPrompt:    "prompt",
			MaxOutputTokens: budget,
		}, dialer)
		if err == nil || !strings.Contains(err.Error(), "response token budget") {
			t.Fatalf("MaxOutputTokens %d error = %v, want budget validation error", budget, err)
		}
	}
}

func TestManualActivitySendsExactTwentyMillisecondPCMFrames(t *testing.T) {
	provider, session := openTestSession(t, Config{})
	frame := make([]byte, InputFrameBytes)
	for index := range frame {
		frame[index] = byte(index % 251)
	}
	wantFrame := append([]byte(nil), frame...)

	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatalf("StartActivity() error = %v", err)
	}
	if err := session.SendPCM20ms(context.Background(), frame); err != nil {
		t.Fatalf("SendPCM20ms() error = %v", err)
	}
	if err := session.EndActivity(context.Background()); err != nil {
		t.Fatalf("EndActivity() error = %v", err)
	}
	if !bytes.Equal(frame, wantFrame) {
		t.Fatal("SendPCM20ms mutated the caller's frame")
	}

	sent := provider.sentInputs()
	if len(sent) != 3 {
		t.Fatalf("sent messages = %d, want 3", len(sent))
	}
	if sent[0].ActivityStart == nil || sent[0].Audio != nil ||
		sent[1].Audio == nil || sent[1].ActivityStart != nil || sent[1].ActivityEnd != nil ||
		sent[2].ActivityEnd == nil || sent[2].Audio != nil {
		t.Fatalf("manual VAD message order is invalid: %#v", sent)
	}
	if sent[1].Audio.MIMEType != InputAudioMIMEType ||
		!bytes.Equal(sent[1].Audio.Data, wantFrame) {
		t.Fatalf("audio input = %#v", sent[1].Audio)
	}

	if err := session.SendPCM20ms(context.Background(), make([]byte, InputFrameBytes-2)); !errors.Is(err, ErrPCMFrameSize) {
		t.Fatalf("short frame error = %v, want ErrPCMFrameSize", err)
	}
}

func TestEndActivityReturnsOnlyAfterProviderWriteCompletes(t *testing.T) {
	provider := newBlockingActivityEndProvider()
	provider.push(&genai.LiveServerMessage{
		SetupComplete: &genai.LiveServerSetupComplete{},
	}, nil)
	service := newTestService(t, Config{}, &fakeDialer{session: provider})
	session, err := service.Open(context.Background())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatalf("StartActivity() error = %v", err)
	}

	returned := make(chan error, 1)
	go func() {
		returned <- session.EndActivity(context.Background())
	}()
	select {
	case <-provider.activityEndObserved:
	case <-time.After(time.Second):
		t.Fatal("provider did not observe ActivityEnd")
	}
	select {
	case err := <-returned:
		t.Fatalf("EndActivity returned before provider write completed: %v", err)
	default:
	}
	close(provider.releaseActivityEnd)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("EndActivity() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("EndActivity did not return after provider write completed")
	}
}

func TestOutputIsBufferedUntilCommit(t *testing.T) {
	provider, session := openTestSession(t, Config{})
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.EndActivity(context.Background()); err != nil {
		t.Fatal(err)
	}

	providerPCM := []byte{1, 0, 2, 0, 3, 0, 4, 0}
	provider.push(&genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{
		ModelTurn: &genai.Content{Parts: []*genai.Part{{InlineData: &genai.Blob{
			Data:     providerPCM,
			MIMEType: "audio/pcm",
		}}}},
		OutputTranscription: &genai.Transcription{Text: "短い返答です。", Finished: true},
		InputTranscription:  &genai.Transcription{Text: "final input", Finished: true},
		TurnComplete:        true,
	}}, nil)

	live := session.(*liveSession)
	waitFor(t, func() bool {
		live.queueMu.Lock()
		defer live.queueMu.Unlock()
		return len(live.held) == 3
	})
	if !allZero(providerPCM) {
		t.Fatal("provider PCM was not zeroized after decoding")
	}
	inputEvent, err := session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inputEvent.Kind != EventInputCaption || string(inputEvent.CaptionUTF8) != "final input" ||
		!inputEvent.CaptionFinal {
		t.Fatalf("input event = %#v", inputEvent)
	}
	inputEvent.Clear()

	receiveCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := session.Receive(receiveCtx); !errors.Is(err, ErrDeadline) {
		t.Fatalf("Receive() before commit error = %v, want ErrDeadline", err)
	}

	if err := session.CommitOutput(); err != nil {
		t.Fatalf("CommitOutput() error = %v", err)
	}
	event, err := session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventOutputCaption || string(event.CaptionUTF8) != "短い返答です。" ||
		!event.CaptionFinal || event.Route != RouteNativeAudio {
		t.Fatalf("caption event = %#v", event)
	}
	event.Clear()

	event, err = session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventAudioPCM || event.SampleRateHertz != OutputSampleRateHertz ||
		!bytes.Equal(event.PCM, []byte{1, 0, 2, 0, 3, 0, 4, 0}) {
		t.Fatalf("audio event = %#v", event)
	}
	event.Clear()

	event, err = session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventTurnComplete {
		t.Fatalf("turn event = %#v", event)
	}
}

func TestInputCaptionBypassesOutputCommitGate(t *testing.T) {
	provider, session := openTestSession(t, Config{})
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.push(&genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{
		InputTranscription: &genai.Transcription{Text: "聞こえています", Finished: true},
	}}, nil)

	event, err := session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventInputCaption || string(event.CaptionUTF8) != "聞こえています" ||
		!event.CaptionFinal {
		t.Fatalf("input caption = %#v", event)
	}
	event.Clear()
}

func TestCommitRequiresFinalInputCaptionAndOneSessionIsOneTurn(t *testing.T) {
	_, session := openTestSession(t, Config{})
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.EndActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.CommitOutput(); !errors.Is(err, ErrInputCaptionPending) {
		t.Fatalf("CommitOutput() error = %v, want ErrInputCaptionPending", err)
	}
	if err := session.StartActivity(context.Background()); !errors.Is(err, ErrActivityState) {
		t.Fatalf("second StartActivity() error = %v, want ErrActivityState", err)
	}
}

func TestAudioArrivingBeforeFinalInputCaptionStaysHeld(t *testing.T) {
	provider, session := openTestSession(t, Config{})
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.EndActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.push(audioMessage([]byte{1, 0, 2, 0}), nil)
	live := session.(*liveSession)
	waitFor(t, func() bool {
		live.queueMu.Lock()
		defer live.queueMu.Unlock()
		return len(live.held) == 1
	})
	if err := session.CommitOutput(); !errors.Is(err, ErrInputCaptionPending) {
		t.Fatalf("CommitOutput() before final input error = %v", err)
	}
	receiveCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if _, err := session.Receive(receiveCtx); !errors.Is(err, ErrDeadline) {
		cancel()
		t.Fatalf("Receive() before final input error = %v, want ErrDeadline", err)
	}
	cancel()

	provider.push(&genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{
		InputTranscription: &genai.Transcription{Text: "safe final input", Finished: true},
	}}, nil)
	waitFor(t, func() bool {
		live.queueMu.Lock()
		defer live.queueMu.Unlock()
		return live.inputCaptionFinal
	})
	if err := session.CommitOutput(); !errors.Is(err, ErrInputCaptionPending) {
		t.Fatalf("CommitOutput() before final caption delivery error = %v", err)
	}
	caption, err := session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caption.Kind != EventInputCaption || !caption.CaptionFinal {
		t.Fatalf("caption event = %#v", caption)
	}
	caption.Clear()
	if err := session.CommitOutput(); err != nil {
		t.Fatal(err)
	}
	audio, err := session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if audio.Kind != EventAudioPCM || !bytes.Equal(audio.PCM, []byte{1, 0, 2, 0}) {
		t.Fatalf("audio event = %#v", audio)
	}
	audio.Clear()
}

func TestModelTurnTextBecomesCommittedOutputCaption(t *testing.T) {
	provider, session := openTestSession(t, Config{})
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.EndActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.push(&genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{
		InputTranscription: &genai.Transcription{Text: "safe input", Finished: true},
		ModelTurn: &genai.Content{Parts: []*genai.Part{
			{Text: "model caption"},
		}},
		TurnComplete: true,
	}}, nil)

	input, err := session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if input.Kind != EventInputCaption || string(input.CaptionUTF8) != "safe input" {
		t.Fatalf("input event = %#v", input)
	}
	input.Clear()
	if err := session.CommitOutput(); err != nil {
		t.Fatal(err)
	}
	caption, err := session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caption.Kind != EventOutputCaption || string(caption.CaptionUTF8) != "model caption" ||
		!caption.CaptionFinal {
		t.Fatalf("caption event = %#v", caption)
	}
	caption.Clear()
}

func TestDiscardDropsAndZeroizesFutureOutput(t *testing.T) {
	provider, session := openTestSession(t, Config{})
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.EndActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	session.DiscardOutput()

	providerBuffers := make([][]byte, 6)
	for index := range providerBuffers {
		providerBuffers[index] = []byte{byte(index + 1), 0, byte(index + 2), 0}
		provider.push(audioMessage(providerBuffers[index]), nil)
	}
	// This ungated event is a FIFO fence proving all earlier audio was decoded.
	provider.push(&genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{
		InputTranscription: &genai.Transcription{Text: "final input", Finished: true},
	}}, nil)
	event, err := session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventInputCaption {
		t.Fatalf("event = %#v, want input caption", event)
	}
	event.Clear()
	for index, providerBuffer := range providerBuffers {
		if !allZero(providerBuffer) {
			t.Fatalf("provider buffer %d was not zeroized", index)
		}
	}
	live := session.(*liveSession)
	live.queueMu.Lock()
	heldCount := len(live.held)
	readyCount := len(live.ready)
	terminalErr := live.terminalErr
	live.queueMu.Unlock()
	if heldCount != 0 || readyCount != 0 || terminalErr != nil {
		t.Fatalf("discarded output retained: held=%d ready=%d err=%v", heldCount, readyCount, terminalErr)
	}
	if err := session.CommitOutput(); !errors.Is(err, ErrActivityState) {
		t.Fatalf("CommitOutput() after discard error = %v, want ErrActivityState", err)
	}
}

func TestOutputMIMETypeIsStrictLittleEndianPCM(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "audio/pcm;rate=24000", want: true},
		{value: "audio/L16;rate=24000", want: false},
		{value: "audio/pcm;rate=16000", want: false},
		{value: "audio/pcm;rate=24000;channels=2", want: false},
		{value: "audio/pcm", want: true},
		{value: "audio/pcm;channels=1", want: false},
	}
	for _, test := range tests {
		if got := validOutputAudioMIMEType(test.value); got != test.want {
			t.Errorf("validOutputAudioMIMEType(%q) = %t, want %t", test.value, got, test.want)
		}
	}
}

func TestInterruptedZeroizesHeldOutputAndIsImmediate(t *testing.T) {
	provider, session := openTestSession(t, Config{})
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.EndActivity(context.Background()); err != nil {
		t.Fatal(err)
	}

	provider.push(audioMessage([]byte{1, 0, 2, 0}), nil)
	live := session.(*liveSession)
	var heldPCM []byte
	waitFor(t, func() bool {
		live.queueMu.Lock()
		defer live.queueMu.Unlock()
		if len(live.held) != 1 {
			return false
		}
		heldPCM = live.held[0].PCM
		return true
	})

	provider.push(&genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{
		Interrupted: true,
	}}, nil)
	event, err := session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventInterrupted {
		t.Fatalf("event = %#v, want interrupted", event)
	}
	if !allZero(heldPCM) {
		t.Fatal("held PCM was not zeroized on interruption")
	}
	if err := session.CommitOutput(); !errors.Is(err, ErrActivityState) {
		t.Fatalf("CommitOutput() after interruption error = %v, want ErrActivityState", err)
	}
}

func TestPendingLimitFailsClosedAndZeroizesBufferedAudio(t *testing.T) {
	provider, session := openTestSession(t, Config{
		MaxOutputChunkBytes: 640,
		MaxPendingBytes:     640,
		MaxPendingEvents:    2,
		MaxTranscriptBytes:  640,
	})
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.EndActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.push(audioMessage(make([]byte, 640)), nil)

	live := session.(*liveSession)
	var heldPCM []byte
	waitFor(t, func() bool {
		live.queueMu.Lock()
		defer live.queueMu.Unlock()
		if len(live.held) != 1 {
			return false
		}
		heldPCM = live.held[0].PCM
		return true
	})
	provider.push(audioMessage(make([]byte, 640)), nil)

	_, err := session.Receive(context.Background())
	if !errors.Is(err, ErrPendingLimit) {
		t.Fatalf("Receive() error = %v, want ErrPendingLimit", err)
	}
	if !allZero(heldPCM) {
		t.Fatal("buffered audio was not zeroized after pending-limit failure")
	}
}

func TestProviderErrorsAreContentFree(t *testing.T) {
	provider, session := openTestSession(t, Config{})
	provider.push(nil, errors.New("SECRET RAW TRANSCRIPT"))

	_, err := session.Receive(context.Background())
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("Receive() error = %v, want ErrProvider", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "TRANSCRIPT") {
		t.Fatalf("provider content leaked through error: %v", err)
	}
}

func TestSessionDeadlineClosesBlockedProviderReceive(t *testing.T) {
	provider, session := openTestSession(t, Config{SessionTimeout: 25 * time.Millisecond})
	_, err := session.Receive(context.Background())
	if !errors.Is(err, ErrDeadline) {
		t.Fatalf("Receive() error = %v, want ErrDeadline", err)
	}
	select {
	case <-provider.closed:
	case <-time.After(time.Second):
		t.Fatal("provider session was not closed on deadline")
	}
}

func TestSendDeadlineUnblocksWriterAndZeroizesOwnedFrame(t *testing.T) {
	provider := newBlockingAudioSendProvider()
	provider.push(&genai.LiveServerMessage{
		SetupComplete: &genai.LiveServerSetupComplete{},
	}, nil)
	service := newTestService(t, Config{SendTimeout: 25 * time.Millisecond}, &fakeDialer{
		session: provider,
	})
	session, err := service.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- session.SendPCM20ms(context.Background(), make([]byte, InputFrameBytes))
	}()
	var owned []byte
	select {
	case owned = <-provider.observed:
	case <-time.After(time.Second):
		t.Fatal("provider did not observe the PCM frame")
	}
	select {
	case err := <-sendDone:
		if !errors.Is(err, ErrDeadline) && !errors.Is(err, ErrProvider) && !errors.Is(err, ErrClosed) {
			t.Fatalf("SendPCM20ms() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SendPCM20ms did not unblock after its deadline")
	}
	waitFor(t, func() bool { return allZero(owned) })
}

func TestInputLimitAndActivityStateFailClosed(t *testing.T) {
	_, session := openTestSession(t, Config{MaxInputBytes: InputFrameBytes})
	frame := make([]byte, InputFrameBytes)
	if err := session.SendPCM20ms(context.Background(), frame); !errors.Is(err, ErrActivityState) {
		t.Fatalf("SendPCM20ms before StartActivity error = %v", err)
	}
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.SendPCM20ms(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	if err := session.SendPCM20ms(context.Background(), frame); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("second SendPCM20ms error = %v, want ErrInputLimit", err)
	}
}

func TestEventClearZeroizesPayloads(t *testing.T) {
	pcm := []byte{1, 2, 3, 4}
	caption := []byte("private")
	event := Event{PCM: pcm, CaptionUTF8: caption, CaptionFinal: true, SampleRateHertz: 24_000}
	event.Clear()
	if !allZero(pcm) || !allZero(caption) || event.PCM != nil || event.CaptionUTF8 != nil {
		t.Fatal("Event.Clear did not zeroize owned payloads")
	}
}

func TestConfigurationRejectsUnreviewedRoutes(t *testing.T) {
	dialer := &fakeDialer{session: newFakeProviderSession()}
	_, err := NewWithDialer(Config{
		ProjectID:    "project",
		SystemPrompt: "prompt",
		Model:        "unreviewed-model",
	}, dialer)
	if err == nil {
		t.Fatal("NewWithDialer accepted an unreviewed model")
	}
	_, err = NewWithDialer(Config{
		ProjectID:    "project",
		SystemPrompt: "prompt",
		Location:     "global",
	}, dialer)
	if err == nil {
		t.Fatal("NewWithDialer accepted an unsupported native-audio location")
	}
}

func newTestService(t *testing.T, config Config, dialer Dialer) *Service {
	t.Helper()
	if config.ProjectID == "" {
		config.ProjectID = "test-project"
	}
	if config.SystemPrompt == "" {
		config.SystemPrompt = "短く安全な日本語で答える。"
	}
	service, err := NewWithDialer(config, dialer)
	if err != nil {
		t.Fatalf("NewWithDialer() error = %v", err)
	}
	return service
}

func openTestSession(t *testing.T, config Config) (*fakeProviderSession, Session) {
	t.Helper()
	provider := newFakeProviderSession()
	provider.push(&genai.LiveServerMessage{
		SetupComplete: &genai.LiveServerSetupComplete{},
	}, nil)
	service := newTestService(t, config, &fakeDialer{session: provider})
	session, err := service.Open(context.Background())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return provider, session
}

func audioMessage(data []byte) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{
		ModelTurn: &genai.Content{Parts: []*genai.Part{{InlineData: &genai.Blob{
			Data:     data,
			MIMEType: OutputAudioMIMEType,
		}}}},
	}}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

type fakeDialer struct {
	mu      sync.Mutex
	session ProviderSession
	err     error
	model   string
	config  *genai.LiveConnectConfig
}

func (d *fakeDialer) Connect(
	_ context.Context,
	model string,
	config *genai.LiveConnectConfig,
) (ProviderSession, error) {
	d.mu.Lock()
	d.model = model
	d.config = config
	d.mu.Unlock()
	return d.session, d.err
}

type fakeProviderSession struct {
	mu        sync.Mutex
	sent      []genai.LiveRealtimeInput
	sendErr   error
	receive   chan receiveResult
	closed    chan struct{}
	closeOnce sync.Once
}

type blockingAudioSendProvider struct {
	*fakeProviderSession
	observed chan []byte
}

type blockingActivityEndProvider struct {
	*fakeProviderSession
	activityEndObserved chan struct{}
	releaseActivityEnd  chan struct{}
	observeOnce         sync.Once
}

func newBlockingActivityEndProvider() *blockingActivityEndProvider {
	return &blockingActivityEndProvider{
		fakeProviderSession: newFakeProviderSession(),
		activityEndObserved: make(chan struct{}),
		releaseActivityEnd:  make(chan struct{}),
	}
}

func (s *blockingActivityEndProvider) SendRealtimeInput(
	input genai.LiveRealtimeInput,
) error {
	if input.ActivityEnd != nil {
		s.observeOnce.Do(func() { close(s.activityEndObserved) })
		select {
		case <-s.releaseActivityEnd:
		case <-s.closed:
			return errors.New("blocked ActivityEnd provider closed")
		}
	}
	return s.fakeProviderSession.SendRealtimeInput(input)
}

func newBlockingAudioSendProvider() *blockingAudioSendProvider {
	return &blockingAudioSendProvider{
		fakeProviderSession: newFakeProviderSession(),
		observed:            make(chan []byte, 1),
	}
}

func (s *blockingAudioSendProvider) SendRealtimeInput(input genai.LiveRealtimeInput) error {
	if input.Audio == nil {
		return s.fakeProviderSession.SendRealtimeInput(input)
	}
	s.observed <- input.Audio.Data
	<-s.closed
	return errors.New("blocked provider send closed")
}

func newFakeProviderSession() *fakeProviderSession {
	return &fakeProviderSession{
		receive: make(chan receiveResult, 16),
		closed:  make(chan struct{}),
	}
}

func (s *fakeProviderSession) SendRealtimeInput(input genai.LiveRealtimeInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, cloneRealtimeInput(input))
	return nil
}

func (s *fakeProviderSession) Receive() (*genai.LiveServerMessage, error) {
	select {
	case result := <-s.receive:
		return result.message, result.err
	case <-s.closed:
		return nil, errors.New("provider closed")
	}
}

func (s *fakeProviderSession) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func (s *fakeProviderSession) push(message *genai.LiveServerMessage, err error) {
	s.receive <- receiveResult{message: message, err: err}
}

func (s *fakeProviderSession) sentInputs() []genai.LiveRealtimeInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]genai.LiveRealtimeInput, len(s.sent))
	for index := range s.sent {
		result[index] = cloneRealtimeInput(s.sent[index])
	}
	return result
}

func cloneRealtimeInput(input genai.LiveRealtimeInput) genai.LiveRealtimeInput {
	cloned := input
	if input.Audio != nil {
		audio := *input.Audio
		audio.Data = append([]byte(nil), input.Audio.Data...)
		cloned.Audio = &audio
	}
	if input.ActivityStart != nil {
		cloned.ActivityStart = &genai.ActivityStart{}
	}
	if input.ActivityEnd != nil {
		cloned.ActivityEnd = &genai.ActivityEnd{}
	}
	return cloned
}
