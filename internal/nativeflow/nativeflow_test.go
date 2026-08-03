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
}

func (f fakePreparer) PrepareNativeState(string, string) (string, bool, error) {
	return f.token, f.requiresStaged, f.err
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
	service, err := New(opener, fakePreparer{token: "opaque-state"})
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

func TestNativeFlowRoutesExplicitRespondentCoachRequestBeforeAnyAudio(t *testing.T) {
	for _, value := range []string{
		"上司に目的を聞かれた。答え方を一問だけ手伝って",
		"上司に「この企画の目的は？」と聞かれました。どう答えればいいですか",
		"面接で強みを質問されました。何て答えたらいいですか",
		"My manager asked me why the change was needed. How should I answer?",
	} {
		t.Run(value, func(t *testing.T) {
			session := newScriptedSession(nativevoice.Event{
				Kind:         nativevoice.EventInputCaption,
				CaptionUTF8:  []byte(value),
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
				"uid-respondent-"+value,
				nativeInput(),
				oneFrame(),
				func([]byte) error { delivered = true; return nil },
			)
			if !errors.Is(err, httpapi.ErrVoiceNativeFallback) || delivered || session.commits != 0 {
				t.Fatalf("err=%v delivered=%v commits=%d", err, delivered, session.commits)
			}
		})
	}
}

func TestRespondentCoachFallbackUsesAuditedDirectConsentBoundary(t *testing.T) {
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
		if !requiresRespondentCoachFallback(value) {
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
		if requiresRespondentCoachFallback(value) {
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
