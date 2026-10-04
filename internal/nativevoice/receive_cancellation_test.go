package nativevoice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type cancellationReceiveResult struct {
	event Event
	err   error
}

// The provider lifetime intentionally differs from the Receive caller, as it
// does for a claimed prepared session. Canceling this caller must not retire
// that session or consume its next event.
func receiveCancellationFixture(t *testing.T) (*fakeProviderSession, *liveSession) {
	t.Helper()
	provider, session := openTestSession(t, Config{})
	if err := session.StartActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.EndActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	return provider, session.(*liveSession)
}

func enqueueCancellationFixture(t *testing.T, session *liveSession) {
	t.Helper()
	if err := session.enqueueEvents([]Event{
		{Kind: EventInputCaption, CaptionUTF8: []byte("こんにちは"), CaptionFinal: true},
		{Kind: EventAudioPCM, PCM: []byte{64, 0, 65, 0}, SampleRateHertz: OutputSampleRateHertz},
	}, false); err != nil {
		t.Fatal(err)
	}
}

func assertCanceledReceivePreservesSession(
	t *testing.T,
	provider *fakeProviderSession,
	session *liveSession,
	result cancellationReceiveResult,
	want error,
) {
	t.Helper()
	defer result.event.Clear()
	if !errors.Is(result.err, want) || result.event.Kind != "" ||
		result.event.CaptionUTF8 != nil || result.event.PCM != nil {
		t.Fatalf("canceled receive returned kind=%q error=%v, want empty event and %v", result.event.Kind, result.err, want)
	}
	session.queueMu.Lock()
	ready, held, buffered := len(session.ready), len(session.held), session.bufferedBytes
	delivered, committed, terminal := session.inputCaptionDelivered, session.outputCommitted, session.terminalErr
	session.queueMu.Unlock()
	if ready != 1 || held != 1 || buffered != len([]byte("こんにちは"))+4 ||
		delivered || committed || terminal != nil {
		t.Fatalf("canceled receive mutated queue: ready=%d held=%d bytes=%d delivered=%t committed=%t terminal=%v", ready, held, buffered, delivered, committed, terminal)
	}
	select {
	case <-provider.closed:
		t.Fatal("caller cancellation closed the provider session")
	default:
	}
	if err := session.CommitOutput(); !errors.Is(err, ErrInputCaptionPending) {
		t.Fatalf("canceled caller authorized output: %v", err)
	}
	caption, err := session.Receive(context.Background())
	if err != nil || caption.Kind != EventInputCaption || !caption.CaptionFinal || string(caption.CaptionUTF8) != "こんにちは" {
		t.Fatalf("fresh caller lost the final caption: kind=%q error=%v", caption.Kind, err)
	}
	captionBytes := caption.CaptionUTF8
	caption.Clear()
	if !allZero(captionBytes) {
		t.Fatal("received caption was not zeroized")
	}
	if err := session.CommitOutput(); err != nil {
		t.Fatal(err)
	}
	pcm, err := session.Receive(context.Background())
	if err != nil || pcm.Kind != EventAudioPCM || !bytes.Equal(pcm.PCM, []byte{64, 0, 65, 0}) {
		t.Fatalf("fresh caller lost the held PCM: kind=%q error=%v", pcm.Kind, err)
	}
	pcmBytes := pcm.PCM
	pcm.Clear()
	if !allZero(pcmBytes) {
		t.Fatal("received PCM was not zeroized")
	}
}

func TestReceiveCanceledCallerDoesNotConsumeReadyCaption(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline_%t", expired), func(t *testing.T) {
			provider, session := receiveCancellationFixture(t)
			enqueueCancellationFixture(t, session)
			var ctx context.Context
			var cancel context.CancelFunc
			want := ErrClosed
			if expired {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				want = ErrDeadline
			} else {
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			}
			defer cancel()
			event, err := session.Receive(ctx)
			assertCanceledReceivePreservesSession(t, provider, session, cancellationReceiveResult{event, err}, want)
		})
	}
}

func TestReceiveCancellationBeforeQueueLockAcquisitionPreservesReadyCaption(t *testing.T) {
	provider, session := receiveCancellationFixture(t)
	enqueueCancellationFixture(t, session)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	completed := make(chan cancellationReceiveResult, 1)
	session.queueMu.Lock()
	go func() {
		close(started)
		event, err := session.Receive(ctx)
		completed <- cancellationReceiveResult{event, err}
	}()
	<-started
	// Neither a ready-event dequeue nor its caption-delivered mutation can
	// occur until after this cancellation and the subsequent unlock.
	cancel()
	session.queueMu.Unlock()
	select {
	case result := <-completed:
		assertCanceledReceivePreservesSession(t, provider, session, result, ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("Receive did not observe cancellation after acquiring the queue lock")
	}
}

type receiveDoneObservationContext struct {
	context.Context
	once     sync.Once
	observed chan struct{}
	release  chan struct{}
}

func (ctx *receiveDoneObservationContext) Done() <-chan struct{} {
	ctx.once.Do(func() {
		close(ctx.observed)
		<-ctx.release
	})
	return ctx.Context.Done()
}

func TestReceiveNotificationRacingCancellationPreservesReadyCaption(t *testing.T) {
	// Both select cases become ready before Done returns. Exercise their race
	// repeatedly; neither selection may consume a caption after cancellation.
	for attempt := range 24 {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			provider, session := receiveCancellationFixture(t)
			caller, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &receiveDoneObservationContext{
				Context: caller, observed: make(chan struct{}), release: make(chan struct{}),
			}
			completed := make(chan cancellationReceiveResult, 1)
			go func() {
				event, err := session.Receive(ctx)
				completed <- cancellationReceiveResult{event, err}
			}()
			<-ctx.observed
			enqueueCancellationFixture(t, session)
			cancel()
			close(ctx.release)
			select {
			case result := <-completed:
				assertCanceledReceivePreservesSession(t, provider, session, result, ErrClosed)
			case <-time.After(time.Second):
				t.Fatal("Receive remained blocked after notification and cancellation")
			}
		})
	}
}
