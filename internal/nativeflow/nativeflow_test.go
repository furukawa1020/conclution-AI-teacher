package nativeflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
)

func TestDefaultSystemPromptPrioritizesUserAirtimeAndCannotStartCoach(
	t *testing.T,
) {
	for _, required := range []string{
		"原則1〜2文",
		"およそ25〜70文字",
		"相手の次の言葉を最優先",
		"答えやすい任意の一問まで",
		"考え途中なら失敗扱いせず",
		"短く受け取って話す番を返し",
		"最初の挨拶だけは、低開示の話題を一つ短く添え",
		"すぐ相手へ話す番を返して",
	} {
		if !strings.Contains(DefaultSystemPrompt, required) {
			t.Fatalf("Native system prompt is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"Respondent Coach",
		"人から聞かれた質問への答え方",
		"まず、あなた自身が一番伝えたいことは何ですか？",
	} {
		if strings.Contains(DefaultSystemPrompt, forbidden) {
			t.Fatalf("Native prompt can start answer coaching via %q", forbidden)
		}
	}
	if strings.Contains(
		DefaultSystemPrompt,
		"具体的で答えやすい話題を一つ持ってください",
	) {
		t.Fatal("Native prompt still fills every short or quiet turn with AI speech")
	}
}

type fakePreparer struct {
	token          string
	requiresStaged bool
	err            error
}

func (f fakePreparer) PrepareNativeState(string, string) (string, bool, error) {
	return f.token, f.requiresStaged, f.err
}

type fakeOpener struct {
	session nativevoice.Session
	opens   int
	onOpen  func()
}

type fakeCaptionHandoffService struct {
	opens   int
	handoff *fakeCaptionHandoff
}

func (f *fakeCaptionHandoffService) OpenCaptionHandoff(
	context.Context,
	string,
	httpapi.VoiceTurnInput,
	func([]byte) error,
	func(httpapi.VoiceRespondentCheckpoint) error,
) (httpapi.VoiceCaptionHandoff, error) {
	f.opens++
	if f.handoff == nil {
		return nil, errors.New("missing handoff")
	}
	return f.handoff, nil
}

type fakeCaptionHandoff struct {
	observed []string
	finals   []bool
	commits  int
	cancels  int
	result   httpapi.VoiceTurnResult
	err      error
}

func (f *fakeCaptionHandoff) Observe(value []byte, final bool, _ time.Time) error {
	f.observed = append(f.observed, string(value))
	f.finals = append(f.finals, final)
	clear(value)
	return nil
}

func (f *fakeCaptionHandoff) Commit() (httpapi.VoiceTurnResult, error) {
	f.commits++
	return f.result, f.err
}

func (f *fakeCaptionHandoff) Cancel() { f.cancels++ }

func (f *fakeOpener) Open(context.Context) (nativevoice.Session, error) {
	f.opens++
	if f.onOpen != nil {
		f.onOpen()
	}
	if f.session == nil {
		return nil, errors.New("unavailable")
	}
	return f.session, nil
}

type scriptedSession struct {
	mu         sync.Mutex
	events     []nativevoice.Event
	index      int
	ended      chan struct{}
	committed  chan struct{}
	endOnce    sync.Once
	commitOnce sync.Once
	commits    int
	discards   int
	closes     int
	frames     int
	startCalls int
	startErr   error
	onStart    func()
}

func newScriptedSession(events ...nativevoice.Event) *scriptedSession {
	return &scriptedSession{
		events:    events,
		ended:     make(chan struct{}),
		committed: make(chan struct{}),
	}
}

func (s *scriptedSession) StartActivity(context.Context) error {
	s.mu.Lock()
	s.startCalls++
	startErr := s.startErr
	onStart := s.onStart
	s.mu.Unlock()
	if onStart != nil {
		onStart()
	}
	return startErr
}

func (s *scriptedSession) SendPCM20ms(_ context.Context, frame []byte) error {
	if len(frame) != nativevoice.InputFrameBytes {
		return nativevoice.ErrPCMFrameSize
	}
	s.mu.Lock()
	s.frames++
	s.mu.Unlock()
	return nil
}

func (s *scriptedSession) EndActivity(context.Context) error {
	s.endOnce.Do(func() { close(s.ended) })
	return nil
}

func (s *scriptedSession) CommitOutput() error {
	s.mu.Lock()
	s.commits++
	s.mu.Unlock()
	s.commitOnce.Do(func() { close(s.committed) })
	return nil
}

func (s *scriptedSession) DiscardOutput() {
	s.mu.Lock()
	s.discards++
	s.mu.Unlock()
}

func (s *scriptedSession) Receive(ctx context.Context) (nativevoice.Event, error) {
	select {
	case <-s.ended:
	case <-ctx.Done():
		return nativevoice.Event{}, ctx.Err()
	}
	s.mu.Lock()
	if s.index >= len(s.events) {
		s.mu.Unlock()
		return nativevoice.Event{}, errors.New("no event")
	}
	event := s.events[s.index]
	s.index++
	s.mu.Unlock()
	if event.Kind == nativevoice.EventAudioPCM ||
		event.Kind == nativevoice.EventOutputCaption ||
		event.Kind == nativevoice.EventTurnComplete {
		select {
		case <-s.committed:
		case <-ctx.Done():
			return nativevoice.Event{}, ctx.Err()
		}
	}
	return event, nil
}

func (s *scriptedSession) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	return nil
}

func nativeInput() httpapi.VoiceTurnInput {
	// Scripted input captions are emitted only after EndActivity, which models
	// the WebSocket commit boundary. Mirror the server broadcast so caption
	// handoff cannot accidentally rely on provider-final as commit authority.
	processingCommitted := make(chan struct{})
	close(processingCommitted)
	return httpapi.VoiceTurnInput{
		MIMEType:            "audio/L16",
		NativeAudio:         true,
		SchemaVersion:       1,
		TurnMode:            httpapi.VoiceTurnForeground,
		Foreground:          true,
		ProcessingCommitted: processingCommitted,
	}
}

func oneFrame() <-chan []byte {
	audio := make(chan []byte, 1)
	audio <- make([]byte, nativevoice.InputFrameBytes)
	close(audio)
	return audio
}

func TestNativeInputReadyFollowsSetupAndStartActivity(t *testing.T) {
	var orderMu sync.Mutex
	var order []string
	record := func(step string) {
		orderMu.Lock()
		order = append(order, step)
		orderMu.Unlock()
	}
	session := newScriptedSession(
		nativevoice.Event{
			Kind:         nativevoice.EventInputCaption,
			CaptionUTF8:  []byte("hello"),
			CaptionFinal: true,
		},
		nativevoice.Event{
			Kind:            nativevoice.EventAudioPCM,
			PCM:             []byte{1, 0},
			SampleRateHertz: nativevoice.OutputSampleRateHertz,
		},
		nativevoice.Event{
			Kind:         nativevoice.EventOutputCaption,
			CaptionUTF8:  []byte("hello there"),
			CaptionFinal: true,
		},
		nativevoice.Event{Kind: nativevoice.EventTurnComplete},
	)
	session.onStart = func() { record("start_activity") }
	opener := &fakeOpener{
		session: session,
		onOpen:  func() { record("setup_complete") },
	}
	service, err := New(opener, fakePreparer{token: "opaque-state"})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	readyCalls := 0
	input := nativeInput()
	input.OnInputReady = func() {
		orderMu.Lock()
		readyCalls++
		order = append(order, "input_ready")
		orderMu.Unlock()
	}
	if _, err := service.ProcessLive(
		context.Background(),
		"uid-strong-ready",
		input,
		oneFrame(),
		func([]byte) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	orderMu.Lock()
	defer orderMu.Unlock()
	if strings.Join(order, ",") !=
		"setup_complete,start_activity,input_ready" || readyCalls != 1 {
		t.Fatalf("order=%v ready calls=%d", order, readyCalls)
	}
}

func TestNativeInputReadyPrecedesAuditedNoProviderDrain(t *testing.T) {
	opener := &fakeOpener{session: newScriptedSession()}
	service, err := New(
		opener,
		fakePreparer{token: "pending-state", requiresStaged: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	ready := make(chan struct{})
	input := nativeInput()
	input.OnInputReady = func() { close(ready) }
	audio := make(chan []byte)
	outcome := make(chan error, 1)
	go func() {
		_, processErr := service.ProcessLive(
			context.Background(),
			"uid-no-provider-ready",
			input,
			audio,
			func([]byte) error { return nil },
		)
		outcome <- processErr
	}()

	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("audited fallback did not publish input readiness before drain")
	}
	if opener.opens != 0 {
		t.Fatalf("audited fallback opened provider %d times", opener.opens)
	}
	select {
	case processErr := <-outcome:
		t.Fatalf("fallback returned before commit drain: %v", processErr)
	default:
	}
	close(audio)
	select {
	case processErr := <-outcome:
		if !errors.Is(processErr, httpapi.ErrVoiceNativeFallback) {
			t.Fatalf("err=%v", processErr)
		}
	case <-time.After(time.Second):
		t.Fatal("audited fallback did not finish after commit drain")
	}
}

func TestNativeInputReadyIsNotPublishedWhenStartActivityFails(t *testing.T) {
	session := newScriptedSession()
	session.startErr = errors.New("start failed")
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{token: "opaque-state"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	readyCalls := 0
	input := nativeInput()
	input.OnInputReady = func() { readyCalls++ }
	_, processErr := service.ProcessLive(
		context.Background(),
		"uid-start-failure",
		input,
		oneFrame(),
		func([]byte) error { return nil },
	)
	session.mu.Lock()
	startCalls := session.startCalls
	closes := session.closes
	session.mu.Unlock()
	if !errors.Is(processErr, errNativeFlowUnavailable) || readyCalls != 0 ||
		startCalls != 1 || closes != 1 {
		t.Fatalf(
			"err=%v ready=%d starts=%d closes=%d",
			processErr,
			readyCalls,
			startCalls,
			closes,
		)
	}
}

type lateNativeOpener struct {
	started chan struct{}
	release chan struct{}
	session nativevoice.Session
}

func (opener *lateNativeOpener) Open(context.Context) (nativevoice.Session, error) {
	close(opener.started)
	<-opener.release
	return opener.session, nil
}

func TestNativeCanceledLateOpenClosesSessionWithoutPublishingReady(t *testing.T) {
	session := newScriptedSession()
	opener := &lateNativeOpener{
		started: make(chan struct{}),
		release: make(chan struct{}),
		session: session,
	}
	service, err := New(opener, fakePreparer{token: "opaque-state"})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	ctx, cancel := context.WithCancel(context.Background())
	readyCalls := 0
	input := nativeInput()
	input.OnInputReady = func() { readyCalls++ }
	outcome := make(chan error, 1)
	go func() {
		_, processErr := service.ProcessLive(
			ctx,
			"uid-late-open",
			input,
			oneFrame(),
			func([]byte) error { return nil },
		)
		outcome <- processErr
	}()
	<-opener.started
	cancel()
	close(opener.release)
	select {
	case processErr := <-outcome:
		if !errors.Is(processErr, errNativeFlowUnavailable) {
			t.Fatalf("err=%v", processErr)
		}
	case <-time.After(time.Second):
		t.Fatal("late provider open did not return after cancellation")
	}
	session.mu.Lock()
	closes := session.closes
	session.mu.Unlock()
	service.mu.Lock()
	sessions := len(service.sessions)
	service.mu.Unlock()
	if readyCalls != 0 || closes != 1 || sessions != 0 {
		t.Fatalf(
			"ready=%d closes=%d retained sessions=%d",
			readyCalls,
			closes,
			sessions,
		)
	}
}

type orderedCaptionHandoffService struct {
	mu sync.Mutex

	opens      int
	returnNil  bool
	result     httpapi.VoiceTurnResult
	audio      []byte
	checkpoint string
	latest     *orderedCaptionHandoff
}

type orderedCaptionHandoff struct {
	mu sync.Mutex

	onAudio       func([]byte) error
	onCoachActive func(httpapi.VoiceRespondentCheckpoint) error
	result        httpapi.VoiceTurnResult
	audio         []byte
	checkpoint    string
	observed      []string
	finals        []bool
	commits       int
	cancels       int
	committed     bool
}

func (service *orderedCaptionHandoffService) OpenCaptionHandoff(
	_ context.Context,
	_ string,
	_ httpapi.VoiceTurnInput,
	onAudio func([]byte) error,
	onCoachActive func(httpapi.VoiceRespondentCheckpoint) error,
) (httpapi.VoiceCaptionHandoff, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.opens++
	if service.returnNil {
		return nil, nil
	}
	handoff := &orderedCaptionHandoff{
		onAudio:       onAudio,
		onCoachActive: onCoachActive,
		result:        service.result,
		audio:         append([]byte(nil), service.audio...),
		checkpoint:    service.checkpoint,
	}
	service.latest = handoff
	return handoff, nil
}

func (service *orderedCaptionHandoffService) snapshot() (
	int,
	*orderedCaptionHandoff,
) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.opens, service.latest
}

func (handoff *orderedCaptionHandoff) Observe(
	caption []byte,
	final bool,
	_ time.Time,
) error {
	defer clear(caption)
	handoff.mu.Lock()
	defer handoff.mu.Unlock()
	handoff.observed = append(handoff.observed, string(caption))
	handoff.finals = append(handoff.finals, final)
	return nil
}

func (handoff *orderedCaptionHandoff) Commit() (
	httpapi.VoiceTurnResult,
	error,
) {
	handoff.mu.Lock()
	handoff.commits++
	handoff.committed = true
	onCoachActive := handoff.onCoachActive
	onAudio := handoff.onAudio
	checkpoint := handoff.checkpoint
	audio := append([]byte(nil), handoff.audio...)
	result := handoff.result
	handoff.mu.Unlock()
	if checkpoint != "" {
		if onCoachActive == nil {
			return result, errors.New("missing coach checkpoint callback")
		}
		control := httpapi.VoiceRespondentCheckpoint{
			SessionState:     checkpoint,
			Route:            result.Route,
			AssistanceTarget: result.AssistanceTarget,
			RespondentStage:  result.RespondentStage,
			CoachPhase:       result.CoachPhase,
			CoachAction:      result.CoachAction,
		}
		if err := onCoachActive(control); err != nil {
			return result, err
		}
	}
	if len(audio) > 0 {
		if err := onAudio(audio); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (handoff *orderedCaptionHandoff) Cancel() {
	handoff.mu.Lock()
	defer handoff.mu.Unlock()
	if !handoff.committed {
		handoff.cancels++
	}
}

func (handoff *orderedCaptionHandoff) snapshot() (
	[]string,
	[]bool,
	int,
	int,
) {
	handoff.mu.Lock()
	defer handoff.mu.Unlock()
	return append([]string(nil), handoff.observed...),
		append([]bool(nil), handoff.finals...),
		handoff.commits,
		handoff.cancels
}

func TestNativeFlowDonatesRequiredStagedCaptionWithControl(t *testing.T) {
	const inputCaption = "The purpose is to make the response easier to understand."
	session := newScriptedSession(nativevoice.Event{
		Kind:         nativevoice.EventInputCaption,
		CaptionUTF8:  []byte(inputCaption),
		CaptionFinal: true,
	})
	opener := &fakeOpener{session: session}
	handoffService := &orderedCaptionHandoffService{
		result: httpapi.VoiceTurnResult{
			StateToken:       "signed-staged-state",
			AssistanceTarget: "respondent",
			RespondentStage:  "awaiting_answer",
			CoachPhase:       "awaiting_answer",
			CoachAction:      "elicit",
			ResearchStatus:   "none",
			ResearchRecords:  []httpapi.ResearchRecord{},
			Route:            httpapi.VoiceNativeRespondentCoachRoute,
			Caption:          "answer only the requested slot",
			LiveTimings: httpapi.VoiceLiveTimings{
				NativeCaptionHandoff: 1,
			},
		},
		audio:      []byte{4, 0},
		checkpoint: "signed-staged-state",
	}
	service, err := NewWithCaptionHandoff(
		opener,
		fakePreparer{token: "pending-state", requiresStaged: true},
		handoffService,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	var eventMu sync.Mutex
	var events []string
	endpointCalls := 0
	result, err := service.ProcessLiveWithControl(
		context.Background(),
		"uid-required-staged-handoff",
		nativeInput(),
		oneFrame(),
		func(audio []byte) error {
			eventMu.Lock()
			events = append(events, "pcm")
			eventMu.Unlock()
			if string(audio) != string([]byte{4, 0}) {
				return errors.New("unexpected caption handoff audio")
			}
			return nil
		},
		func() { endpointCalls++ },
		func(transition httpapi.VoiceRespondentCheckpointTransition) error {
			checkpoint := transition.Checkpoint
			eventMu.Lock()
			events = append(events, "checkpoint")
			eventMu.Unlock()
			if checkpoint.SessionState != "signed-staged-state" {
				return errors.New("unexpected staged checkpoint")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.StateToken != "signed-staged-state" ||
		result.LiveTimings.NativeCaptionHandoff != 1 {
		t.Fatalf("result = %+v", result)
	}
	eventMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventMu.Unlock()
	if len(gotEvents) != 2 || gotEvents[0] != "checkpoint" ||
		gotEvents[1] != "pcm" {
		t.Fatalf("handoff release order = %v", gotEvents)
	}
	opens, handoff := handoffService.snapshot()
	if opens != 1 || handoff == nil {
		t.Fatalf("handoff opens=%d handoff=%v", opens, handoff)
	}
	observed, finals, commits, cancels := handoff.snapshot()
	if len(observed) != 1 || observed[0] != inputCaption ||
		len(finals) != 1 || !finals[0] || commits != 1 || cancels != 0 {
		t.Fatalf(
			"observed=%v finals=%v commits=%d cancels=%d",
			observed,
			finals,
			commits,
			cancels,
		)
	}
	if opener.opens != 1 || session.commits != 0 || session.discards == 0 ||
		endpointCalls != 1 {
		t.Fatalf(
			"provider opens=%d commits=%d discards=%d endpoints=%d",
			opener.opens,
			session.commits,
			session.discards,
			endpointCalls,
		)
	}
}

func TestNativeFlowWithHandoffFallsBackWithoutControl(t *testing.T) {
	t.Run("already staged state", func(t *testing.T) {
		opener := &fakeOpener{session: newScriptedSession()}
		handoffService := &orderedCaptionHandoffService{}
		service, err := NewWithCaptionHandoff(
			opener,
			fakePreparer{requiresStaged: true},
			handoffService,
		)
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		_, err = service.ProcessLive(
			context.Background(),
			"uid-staged-without-control",
			nativeInput(),
			oneFrame(),
			func([]byte) error { return errors.New("audio must remain private") },
		)
		opens, _ := handoffService.snapshot()
		if !errors.Is(err, httpapi.ErrVoiceNativeFallback) ||
			opener.opens != 0 || opens != 0 {
			t.Fatalf(
				"err=%v provider opens=%d handoff opens=%d",
				err,
				opener.opens,
				opens,
			)
		}
	})

	t.Run("coach discovered from caption", func(t *testing.T) {
		const coachRequest = "My manager asked why the change was needed. How should I answer?"
		session := newScriptedSession(nativevoice.Event{
			Kind:         nativevoice.EventInputCaption,
			CaptionUTF8:  []byte(coachRequest),
			CaptionFinal: true,
		})
		opener := &fakeOpener{session: session}
		handoffService := &orderedCaptionHandoffService{}
		service, err := NewWithCaptionHandoff(
			opener,
			fakePreparer{},
			handoffService,
		)
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivered := false
		_, err = service.ProcessLiveWithEndpoint(
			context.Background(),
			"uid-coach-without-control",
			nativeInput(),
			oneFrame(),
			func([]byte) error { delivered = true; return nil },
			func() {},
		)
		opens, _ := handoffService.snapshot()
		if !errors.Is(err, httpapi.ErrVoiceNativeFallback) || delivered ||
			opener.opens != 1 || opens != 0 || session.commits != 0 ||
			session.discards == 0 {
			t.Fatalf(
				"err=%v delivered=%v provider=%d handoff=%d commits=%d discards=%d",
				err,
				delivered,
				opener.opens,
				opens,
				session.commits,
				session.discards,
			)
		}
	})
}

func TestNativeFlowFailsClosedWhenCaptionHandoffOpensNil(t *testing.T) {
	session := newScriptedSession(nativevoice.Event{
		Kind:         nativevoice.EventInputCaption,
		CaptionUTF8:  []byte("The answer is still forming."),
		CaptionFinal: true,
	})
	handoffService := &orderedCaptionHandoffService{returnNil: true}
	service, err := NewWithCaptionHandoff(
		&fakeOpener{session: session},
		fakePreparer{requiresStaged: true},
		handoffService,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	audioCalls := 0
	checkpointCalls := 0
	_, err = service.ProcessLiveWithControl(
		context.Background(),
		"uid-nil-handoff",
		nativeInput(),
		oneFrame(),
		func([]byte) error { audioCalls++; return nil },
		nil,
		func(httpapi.VoiceRespondentCheckpointTransition) error {
			checkpointCalls++
			return nil
		},
	)
	opens, _ := handoffService.snapshot()
	if err == nil || errors.Is(err, httpapi.ErrVoiceNativeFallback) ||
		opens != 1 || audioCalls != 0 || checkpointCalls != 0 ||
		session.commits != 0 || session.discards == 0 {
		t.Fatalf(
			"err=%v opens=%d audio=%d checkpoint=%d commits=%d discards=%d",
			err,
			opens,
			audioCalls,
			checkpointCalls,
			session.commits,
			session.discards,
		)
	}
}

func TestNativeFlowRejectsInvalidStateBeforeOpeningProvider(t *testing.T) {
	opener := &fakeOpener{session: newScriptedSession()}
	service, err := New(
		opener,
		fakePreparer{err: conversation.ErrInvalidStateToken},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	_, err = service.ProcessLive(
		context.Background(),
		"uid-invalid-state",
		nativeInput(),
		oneFrame(),
		func([]byte) error { return nil },
	)
	if !errors.Is(err, httpapi.ErrVoiceStateInvalid) || opener.opens != 0 {
		t.Fatalf("err=%v provider opens=%d", err, opener.opens)
	}
}

func TestNativeFlowReleasesMeaningfulAudioOnlyAfterFinalInputGate(t *testing.T) {
	session := newScriptedSession(
		nativevoice.Event{
			Kind:         nativevoice.EventInputCaption,
			CaptionUTF8:  []byte("こんにちは"),
			CaptionFinal: true,
		},
		nativevoice.Event{
			Kind:            nativevoice.EventAudioPCM,
			PCM:             []byte{1, 2, 3, 4},
			SampleRateHertz: nativevoice.OutputSampleRateHertz,
		},
		nativevoice.Event{
			Kind:         nativevoice.EventOutputCaption,
			CaptionUTF8:  []byte("こんにちは。今日は何をして過ごしていましたか。"),
			CaptionFinal: true,
		},
		nativevoice.Event{Kind: nativevoice.EventTurnComplete},
	)
	opener := &fakeOpener{session: session}
	service, err := New(
		opener,
		fakePreparer{token: "opaque-state"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	var delivered []byte
	result, err := service.ProcessLive(
		context.Background(),
		"uid-native",
		nativeInput(),
		oneFrame(),
		func(pcm []byte) error {
			session.mu.Lock()
			committed := session.commits > 0
			session.mu.Unlock()
			if !committed {
				t.Fatal("audio crossed the final input gate before commit")
			}
			delivered = append(delivered, pcm...)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Caption) != "こんにちは。今日は何をして過ごしていましたか。" ||
		result.Route != nativevoice.RouteNativeAudio || result.StateToken != "opaque-state" ||
		result.LiveTimings.FinalToFirstAudioMS < 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(delivered) != 4 || session.commits != 1 || session.frames != 1 {
		t.Fatalf("delivery=%v commits=%d frames=%d", delivered, session.commits, session.frames)
	}
}

func TestNativeFlowDoesNotCountDigitalSilenceAsFirstAudio(t *testing.T) {
	session := newScriptedSession(
		nativevoice.Event{
			Kind:         nativevoice.EventInputCaption,
			CaptionUTF8:  []byte("こんにちは"),
			CaptionFinal: true,
		},
		nativevoice.Event{
			Kind:            nativevoice.EventAudioPCM,
			PCM:             []byte{0, 0, 0, 0},
			SampleRateHertz: nativevoice.OutputSampleRateHertz,
		},
		nativevoice.Event{
			Kind:         nativevoice.EventOutputCaption,
			CaptionUTF8:  []byte("こんにちは。"),
			CaptionFinal: true,
		},
		nativevoice.Event{Kind: nativevoice.EventTurnComplete},
	)
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{token: "opaque-state"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	var delivered []byte
	result, err := service.ProcessLive(
		context.Background(),
		"uid-native-silent-output-metric",
		nativeInput(),
		oneFrame(),
		func(pcm []byte) error {
			delivered = append(delivered, pcm...)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(delivered) != string([]byte{0, 0, 0, 0}) ||
		result.LiveTimings.TTSFirstChunkMS != -1 ||
		result.LiveTimings.FinalToFirstAudioMS != -1 {
		t.Fatalf("delivered=%v timings=%+v", delivered, result.LiveTimings)
	}
}

func TestNativeFlowBlocksHighRiskCaptionBeforeAnyAudio(t *testing.T) {
	session := newScriptedSession(nativevoice.Event{
		Kind:         nativevoice.EventInputCaption,
		CaptionUTF8:  []byte("死にたい。どうすればいい"),
		CaptionFinal: true,
	})
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{token: "unused"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	delivered := false
	_, err = service.ProcessLive(
		context.Background(),
		"uid-risk",
		nativeInput(),
		oneFrame(),
		func([]byte) error { delivered = true; return nil },
	)
	if !errors.Is(err, httpapi.ErrVoiceNativeFallback) || delivered || session.commits != 0 {
		t.Fatalf("err=%v delivered=%v commits=%d", err, delivered, session.commits)
	}
}

func TestNativeFlowKeepsSignedPendingCoachOutOfNativeProvider(t *testing.T) {
	opener := &fakeOpener{session: newScriptedSession()}
	service, err := New(
		opener,
		fakePreparer{token: "unchanged-state", requiresStaged: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	audio := make(chan []byte)
	frame := make([]byte, nativevoice.InputFrameBytes)
	for index := range frame {
		frame[index] = 0x5a
	}
	delivered := false
	outcome := make(chan error, 1)
	go func() {
		_, processErr := service.ProcessLive(
			ctx,
			"uid-pending-state",
			nativeInput(),
			audio,
			func([]byte) error { delivered = true; return nil },
		)
		outcome <- processErr
	}()

	select {
	case audio <- frame:
	case <-ctx.Done():
		t.Fatal("pending-state gate did not drain capture before commit")
	}
	select {
	case processErr := <-outcome:
		t.Fatalf("pipeline returned before commit: %v", processErr)
	default:
	}
	close(audio)
	select {
	case err = <-outcome:
	case <-ctx.Done():
		t.Fatal("pending-state gate did not finish after commit")
	}
	if !errors.Is(err, httpapi.ErrVoiceNativeFallback) || delivered || opener.opens != 0 {
		t.Fatalf("err=%v delivered=%v provider opens=%d", err, delivered, opener.opens)
	}
	for index, value := range frame {
		if value != 0 {
			t.Fatalf("captured PCM was not wiped at byte %d", index)
		}
	}
}

func TestNativeFlowRoutesExplicitRespondentCoachRequestThroughBoundStagedFlow(t *testing.T) {
	for _, value := range []string{
		"上司に目的を聞かれた。答え方を一問だけ手伝って",
		"上司に「この企画の目的は？」と聞かれました。どう答えればいいですか",
		"面接で強みを質問されました。何て答えたらいいですか",
		"My manager asked me why the change was needed. How should I answer?",
		"代わりに答えて",
		"回答を作って",
		"この答えをそのまま読んで",
		"私の代わりに「評価基準をそろえます」と答えて",
		"代わりに、答えて",
		"回答を 作って",
		"この答えを、そのまま読み上げて",
	} {
		t.Run(value, func(t *testing.T) {
			session := newScriptedSession(
				nativevoice.Event{
					Kind:         nativevoice.EventInputCaption,
					CaptionUTF8:  []byte(value),
					CaptionFinal: true,
				},
				nativevoice.Event{
					Kind:            nativevoice.EventAudioPCM,
					PCM:             []byte{1, 2, 3, 4},
					SampleRateHertz: nativevoice.OutputSampleRateHertz,
				},
				nativevoice.Event{
					Kind:         nativevoice.EventOutputCaption,
					CaptionUTF8:  []byte("まず、あなたは何を伝えたいですか？"),
					CaptionFinal: true,
				},
				nativevoice.Event{Kind: nativevoice.EventTurnComplete},
			)
			service, err := New(
				&fakeOpener{session: session},
				fakePreparer{
					token: "advanced-native-state",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()

			input := nativeInput()
			input.StateToken = "original-state"
			audioDelivered := false
			controlPublished := false
			_, err = service.ProcessLiveWithControl(
				context.Background(),
				"uid-respondent-"+value,
				input,
				oneFrame(),
				func([]byte) error {
					audioDelivered = true
					return nil
				},
				nil,
				func(httpapi.VoiceRespondentCheckpointTransition) error {
					controlPublished = true
					return nil
				},
			)
			if !errors.Is(err, httpapi.ErrVoiceNativeFallback) ||
				audioDelivered || controlPublished ||
				session.commits != 0 || session.discards == 0 {
				t.Fatalf(
					"err=%v audio=%v control=%v commits=%d discards=%d",
					err,
					audioDelivered,
					controlPublished,
					session.commits,
					session.discards,
				)
			}
		})
	}
}

func TestNativeFinalCaptionKeepsOrdinaryAndReportedRequestsOnNativeAudio(t *testing.T) {
	for index, value := range []string{
		"問題の答えを教えて",
		"友達が「代わりに答えないで」と言っていた",
		"母は回答を作らないでと言いました",
		"『この回答を読み上げないで』は引用です",
	} {
		t.Run(value, func(t *testing.T) {
			if requiresRespondentCoach(value) || explicitProxyAnswerOptOut(value) {
				t.Fatalf("ordinary or reported final intent entered a proxy control: %q", value)
			}
			const providerReply = "わかりました。今の依頼に沿って続けます。"
			session := newScriptedSession(
				nativevoice.Event{
					Kind:         nativevoice.EventInputCaption,
					CaptionUTF8:  []byte(value),
					CaptionFinal: true,
				},
				nativevoice.Event{
					Kind:            nativevoice.EventAudioPCM,
					PCM:             []byte{1, 2, 3, 4},
					SampleRateHertz: nativevoice.OutputSampleRateHertz,
				},
				nativevoice.Event{
					Kind:         nativevoice.EventOutputCaption,
					CaptionUTF8:  []byte(providerReply),
					CaptionFinal: true,
				},
				nativevoice.Event{Kind: nativevoice.EventTurnComplete},
			)
			service, err := New(
				&fakeOpener{session: session},
				fakePreparer{token: "ordinary-native-state"},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()

			var delivered []byte
			result, err := service.ProcessLive(
				context.Background(),
				"uid-native-proxy-control-"+string(rune('a'+index)),
				nativeInput(),
				oneFrame(),
				func(chunk []byte) error {
					delivered = append(delivered, chunk...)
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Route != nativevoice.RouteNativeAudio ||
				string(result.Caption) != providerReply ||
				len(delivered) != 4 || session.commits != 1 {
				t.Fatalf(
					"result=%+v delivered=%v commits=%d discards=%d",
					result,
					delivered,
					session.commits,
					session.discards,
				)
			}
		})
	}
}

func TestNativeRespondentCoachCannotPublishGenericCheckpoint(t *testing.T) {
	const utterance = "上司に目的を聞かれた。答え方を一問だけ手伝って"
	session := newScriptedSession(
		nativevoice.Event{
			Kind:         nativevoice.EventInputCaption,
			CaptionUTF8:  []byte(utterance),
			CaptionFinal: true,
		},
		nativevoice.Event{
			Kind:            nativevoice.EventAudioPCM,
			PCM:             []byte{1, 2},
			SampleRateHertz: nativevoice.OutputSampleRateHertz,
		},
		nativevoice.Event{
			Kind:         nativevoice.EventOutputCaption,
			CaptionUTF8:  []byte("一番伝えたいことは何ですか？"),
			CaptionFinal: true,
		},
		nativevoice.Event{Kind: nativevoice.EventTurnComplete},
	)
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{
			token: "advanced-native-state",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	audioDelivered := false
	controlPublished := false
	_, err = service.ProcessLiveWithControl(
		context.Background(),
		"uid-native-coach-no-generic-checkpoint",
		nativeInput(),
		oneFrame(),
		func([]byte) error { audioDelivered = true; return nil },
		nil,
		func(httpapi.VoiceRespondentCheckpointTransition) error {
			controlPublished = true
			return nil
		},
	)
	if !errors.Is(err, httpapi.ErrVoiceNativeFallback) ||
		audioDelivered || controlPublished ||
		session.commits != 0 || session.discards == 0 {
		t.Fatalf(
			"err=%v audio=%v control=%v commits=%d discards=%d",
			err,
			audioDelivered,
			controlPublished,
			session.commits,
			session.discards,
		)
	}
}

func TestNativeRespondentCoachReturnsNoNativeAuthority(t *testing.T) {
	const utterance = "上司に目的を聞かれた。答え方を一問だけ手伝って"
	session := newScriptedSession(
		nativevoice.Event{
			Kind:         nativevoice.EventInputCaption,
			CaptionUTF8:  []byte(utterance),
			CaptionFinal: true,
		},
		nativevoice.Event{
			Kind:            nativevoice.EventAudioPCM,
			PCM:             []byte{1, 2},
			SampleRateHertz: nativevoice.OutputSampleRateHertz,
		},
		nativevoice.Event{
			Kind:         nativevoice.EventOutputCaption,
			CaptionUTF8:  []byte("話してください。"),
			CaptionFinal: true,
		},
		nativevoice.Event{Kind: nativevoice.EventTurnComplete},
	)
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{
			token: "advanced-native-state",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	controls := 0
	audioDelivered := false
	result, err := service.ProcessLiveWithControl(
		context.Background(),
		"uid-invalid-coach-plan",
		nativeInput(),
		oneFrame(),
		func([]byte) error { audioDelivered = true; return nil },
		nil,
		func(httpapi.VoiceRespondentCheckpointTransition) error {
			controls++
			return nil
		},
	)
	if !errors.Is(err, httpapi.ErrVoiceNativeFallback) ||
		controls != 0 || audioDelivered || result.StateToken != "" ||
		result.Route != "" || result.Caption != "" ||
		session.commits != 0 || session.discards == 0 {
		t.Fatalf(
			"err=%v controls=%d audio=%v result=%+v commits=%d discards=%d",
			err,
			controls,
			audioDelivered,
			result,
			session.commits,
			session.discards,
		)
	}
}

func TestNativeRespondentCoachFallbackNeverCallsControlWriter(t *testing.T) {
	const utterance = "上司に目的を聞かれた。答え方を一問だけ手伝って"
	session := newScriptedSession(nativevoice.Event{
		Kind:         nativevoice.EventInputCaption,
		CaptionUTF8:  []byte(utterance),
		CaptionFinal: true,
	})
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{
			token: "advanced-native-state",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	delivered := false
	controls := 0
	_, err = service.ProcessLiveWithControl(
		context.Background(),
		"uid-coach-control-failed",
		nativeInput(),
		oneFrame(),
		func([]byte) error { delivered = true; return nil },
		nil,
		func(httpapi.VoiceRespondentCheckpointTransition) error {
			controls++
			return errors.New("control write failed")
		},
	)
	if !errors.Is(err, httpapi.ErrVoiceNativeFallback) || delivered ||
		controls != 0 || session.commits != 0 || session.discards == 0 {
		t.Fatalf(
			"err=%v delivered=%v controls=%d commits=%d discards=%d",
			err,
			delivered,
			controls,
			session.commits,
			session.discards,
		)
	}
}

func TestNativeRespondentCoachWithoutControlBoundaryReleasesNothing(t *testing.T) {
	const utterance = "My manager asked why this change was needed. How should I answer?"
	for _, method := range []string{"ProcessLive", "ProcessLiveWithEndpoint"} {
		t.Run(method, func(t *testing.T) {
			session := newScriptedSession(nativevoice.Event{
				Kind:         nativevoice.EventInputCaption,
				CaptionUTF8:  []byte(utterance),
				CaptionFinal: true,
			})
			service, err := New(
				&fakeOpener{session: session},
				fakePreparer{
					token: "advanced-native-state",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()

			delivered := false
			var processErr error
			switch method {
			case "ProcessLive":
				_, processErr = service.ProcessLive(
					context.Background(),
					"uid-coach-no-control",
					nativeInput(),
					oneFrame(),
					func([]byte) error { delivered = true; return nil },
				)
			case "ProcessLiveWithEndpoint":
				_, processErr = service.ProcessLiveWithEndpoint(
					context.Background(),
					"uid-coach-no-control-endpoint",
					nativeInput(),
					oneFrame(),
					func([]byte) error { delivered = true; return nil },
					func() {},
				)
			}
			if !errors.Is(processErr, httpapi.ErrVoiceNativeFallback) || delivered ||
				session.commits != 0 || session.discards == 0 {
				t.Fatalf(
					"err=%v delivered=%v commits=%d discards=%d",
					processErr,
					delivered,
					session.commits,
					session.discards,
				)
			}
		})
	}
}

func TestNativeRespondentCoachUsesAuditedDirectConsentBoundary(t *testing.T) {
	for _, value := range []string{
		"上司に目的を聞かれた。答え方を一問だけ手伝って",
		"上司から目的を質問されました。どう答えればいいですか",
		"上司に「導入目的は何ですか」と聞かれた。回答を少し整えてください",
		"面談で今後の希望を尋ねられた。なんて返せばいいですか",
		"私ならどう答えればいいですか",
		"自分なら何て答えたらいいですか",
		"My boss asked me what the purpose was. Please help me answer.",
		"答え方を一問だけ手伝って",
		"どう答えればいいですか",
		"代わりに答えて",
		"回答を作って",
		"この答えをそのまま読んで",
		"私の代わりに「評価基準をそろえます」と答えて",
		"面接の回答を作って",
		"母はこう言いました。代わりに答えて",
		"回答を作らないで。でも、代わりに答えて",
	} {
		if !requiresRespondentCoach(value) {
			t.Errorf("explicit respondent request did not fall back: %q", value)
		}
	}

	for _, value := range []string{
		"友達が「上司に目的を聞かれた。答え方を一問だけ手伝って」と言っていた",
		"母に「どう答えればいい？」と聞かれた",
		"上司に目的を聞かれたけど、答え方を手伝ってほしくない",
		"上司に目的を聞かれた。あなたならどう答える？",
		"上司に目的を聞かれた。ChatGPTならどう答えればいいですか",
		"上司に目的を聞かれたけど答えられなかった",
		"答えという言葉について話したい",
		"AIにどう答えればいいか聞かれた",
		"友達が上司に目的を聞かれた。友達の答え方を手伝って",
		"「上司に目的を聞かれた。どう答えればいい？」と友達が言っていた",
		"上司に「目的を聞かれた。どう答えればいいですか",
		"問題の答えを教えて",
		"母の代わりに答えて",
		"母が代わりに答えて",
		"母の回答を作って",
		"母の質問への回答を作って",
		"友達が聞かれた質問への回答を作って",
		"友達が「代わりに答えて」と言っていた",
		"代わりに答えないで",
		"この答えを読んで",
		"代わりに答えて。でも今はやめて",
		"回答を作って。いや、作らないで",
		"この回答を読み上げて。やっぱりやめて",
	} {
		if requiresRespondentCoach(value) {
			t.Errorf("ambiguous or unowned request entered respondent coach: %q", value)
		}
	}
}

func TestNativeFlowRejectsProviderAudioBeforeFinalCaption(t *testing.T) {
	session := newScriptedSession(nativevoice.Event{
		Kind:            nativevoice.EventAudioPCM,
		PCM:             []byte{1, 2},
		SampleRateHertz: nativevoice.OutputSampleRateHertz,
	})
	// This adversarial mock bypasses the nativevoice package's held-output
	// contract. The integration boundary must still fail closed.
	close(session.committed)
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{token: "unused"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	delivered := false
	_, err = service.ProcessLive(
		context.Background(),
		"uid-early-output",
		nativeInput(),
		oneFrame(),
		func([]byte) error { delivered = true; return nil },
	)
	if err == nil || delivered {
		t.Fatalf("err=%v delivered=%v", err, delivered)
	}
}

func TestNativeAudioEligibilityKeepsOrdinaryConversationNarrow(t *testing.T) {
	for _, value := range []string{"こんにちは", "今日はゲームをしてた", "うまく話せないけど聞いて"} {
		if !nativeAudioEligible(value) {
			t.Fatalf("ordinary conversation blocked: %q", value)
		}
	}
	for _, value := range []string{
		"最新情報を検索して", "法律相談をしたい", "株を買うべき？", "死にたい",
	} {
		if nativeAudioEligible(value) {
			t.Fatalf("high-risk route entered native audio: %q", value)
		}
	}
}
