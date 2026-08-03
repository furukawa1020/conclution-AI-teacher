package nativeflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
)

type fakePreparer struct {
	token          string
	requiresStaged bool
	err            error
	coachToken     string
	coachErr       error
}

func (f fakePreparer) PrepareNativeState(string, string) (string, bool, error) {
	return f.token, f.requiresStaged, f.err
}

func (f fakePreparer) PrepareNativeCoachState(
	string,
	string,
	string,
) (string, error) {
	return f.coachToken, f.coachErr
}

type fakeOpener struct {
	session nativevoice.Session
	opens   int
}

func (f *fakeOpener) Open(context.Context) (nativevoice.Session, error) {
	f.opens++
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
}

func newScriptedSession(events ...nativevoice.Event) *scriptedSession {
	return &scriptedSession{
		events:    events,
		ended:     make(chan struct{}),
		committed: make(chan struct{}),
	}
}

func (s *scriptedSession) StartActivity(context.Context) error { return nil }

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
	return httpapi.VoiceTurnInput{
		MIMEType:      "audio/L16",
		NativeAudio:   true,
		SchemaVersion: 1,
		TurnMode:      httpapi.VoiceTurnForeground,
		Foreground:    true,
	}
}

func oneFrame() <-chan []byte {
	audio := make(chan []byte, 1)
	audio <- make([]byte, nativevoice.InputFrameBytes)
	close(audio)
	return audio
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
		result.Route != nativevoice.RouteNativeAudio || result.StateToken != "opaque-state" {
		t.Fatalf("result = %+v", result)
	}
	if len(delivered) != 4 || session.commits != 1 || session.frames != 1 {
		t.Fatalf("delivery=%v commits=%d frames=%d", delivered, session.commits, session.frames)
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

func TestNativeFlowRoutesExplicitRespondentCoachRequestDirectlyThroughNativeAudio(t *testing.T) {
	for _, value := range []string{
		"上司に目的を聞かれた。答え方を一問だけ手伝って",
		"上司に「この企画の目的は？」と聞かれました。どう答えればいいですか",
		"面接で強みを質問されました。何て答えたらいいですか",
		"My manager asked me why the change was needed. How should I answer?",
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
					token:      "advanced-native-state",
					coachToken: "generic-signed-coach-state",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()

			input := nativeInput()
			input.StateToken = "original-state"
			order := make([]string, 0, 2)
			result, err := service.ProcessLiveWithControl(
				context.Background(),
				"uid-respondent-"+value,
				input,
				oneFrame(),
				func([]byte) error {
					order = append(order, "audio")
					return nil
				},
				nil,
				func(sessionState string) error {
					if sessionState != "generic-signed-coach-state" {
						t.Errorf("control state = %q", sessionState)
					}
					order = append(order, "coach")
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(order) != 2 || order[0] != "coach" || order[1] != "audio" ||
				session.commits != 1 {
				t.Fatalf("order=%v commits=%d", order, session.commits)
			}
			if result.Route != routeNativeRespondentCoach ||
				result.StateToken != "generic-signed-coach-state" ||
				result.AssistanceTarget != "respondent" ||
				result.RespondentStage != "awaiting_answer" ||
				result.CoachPhase != "awaiting_answer" ||
				result.CoachAction != "elicit" ||
				result.LiveTimings.ConversationMS < 0 ||
				result.LiveTimings.TTSFirstChunkMS < 0 ||
				result.LiveTimings.FinalToFirstAudioMS < 0 {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestNativeRespondentCoachCompletesWithoutConversationPlanner(t *testing.T) {
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
			token:      "advanced-native-state",
			coachToken: "generic-signed-coach-state",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	controlSeen := make(chan struct{})
	audioSeen := make(chan struct{})
	outcome := make(chan struct {
		result httpapi.VoiceTurnResult
		err    error
	}, 1)
	go func() {
		result, processErr := service.ProcessLiveWithControl(
			context.Background(),
			"uid-native-coach-concurrent",
			nativeInput(),
			oneFrame(),
			func([]byte) error {
				select {
				case <-controlSeen:
				default:
					t.Error("audio arrived before coach control")
				}
				close(audioSeen)
				return nil
			},
			nil,
			func(sessionState string) error {
				if sessionState != "generic-signed-coach-state" {
					t.Errorf("control state = %q", sessionState)
				}
				close(controlSeen)
				return nil
			},
		)
		outcome <- struct {
			result httpapi.VoiceTurnResult
			err    error
		}{result: result, err: processErr}
	}()

	select {
	case <-audioSeen:
	case <-time.After(time.Second):
		t.Fatal("first Native audio did not follow the local signed-state gate")
	}
	select {
	case completed := <-outcome:
		if completed.err != nil ||
			completed.result.Route != routeNativeRespondentCoach ||
			completed.result.StateToken != "generic-signed-coach-state" {
			t.Fatalf("completed=%+v err=%v", completed.result, completed.err)
		}
	case <-time.After(time.Second):
		t.Fatal("turn waited for an external conversation plan")
	}
}

func TestNativeRespondentCoachReturnsGenericSignedAuthority(t *testing.T) {
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
			token:      "advanced-native-state",
			coachToken: "generic-signed-coach-state",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	controls := 0
	result, err := service.ProcessLiveWithControl(
		context.Background(),
		"uid-invalid-coach-plan",
		nativeInput(),
		oneFrame(),
		func([]byte) error { return nil },
		nil,
		func(sessionState string) error {
			controls++
			if sessionState != "generic-signed-coach-state" {
				t.Errorf("control state = %q", sessionState)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if controls != 1 || result.Route != routeNativeRespondentCoach ||
		result.StateToken != "generic-signed-coach-state" ||
		result.AssistanceTarget != "respondent" ||
		result.RespondentStage != "awaiting_answer" ||
		result.CoachPhase != "awaiting_answer" || result.CoachAction != "elicit" {
		t.Fatalf("controls=%d result=%+v", controls, result)
	}
}

func TestNativeRespondentCoachControlWriteFailureReleasesNoAudio(t *testing.T) {
	const utterance = "上司に目的を聞かれた。答え方を一問だけ手伝って"
	session := newScriptedSession(nativevoice.Event{
		Kind:         nativevoice.EventInputCaption,
		CaptionUTF8:  []byte(utterance),
		CaptionFinal: true,
	})
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{
			token:      "advanced-native-state",
			coachToken: "generic-signed-coach-state",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	delivered := false
	_, err = service.ProcessLiveWithControl(
		context.Background(),
		"uid-coach-control-failed",
		nativeInput(),
		oneFrame(),
		func([]byte) error { delivered = true; return nil },
		nil,
		func(string) error { return errors.New("control write failed") },
	)
	if err == nil || delivered || session.commits != 0 || session.discards == 0 {
		t.Fatalf(
			"err=%v delivered=%v commits=%d discards=%d",
			err,
			delivered,
			session.commits,
			session.discards,
		)
	}
}

func TestNativeRespondentCoachStatePreparationFailureReleasesNothing(t *testing.T) {
	const utterance = "My manager asked why this change was needed. How should I answer?"
	session := newScriptedSession(nativevoice.Event{
		Kind:         nativevoice.EventInputCaption,
		CaptionUTF8:  []byte(utterance),
		CaptionFinal: true,
	})
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{
			token:    "advanced-native-state",
			coachErr: errors.New("state issuance unavailable"),
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
		"uid-coach-state-failed",
		nativeInput(),
		oneFrame(),
		func([]byte) error { delivered = true; return nil },
		nil,
		func(string) error { controls++; return nil },
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
					token:      "advanced-native-state",
					coachToken: "generic-signed-coach-state",
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
