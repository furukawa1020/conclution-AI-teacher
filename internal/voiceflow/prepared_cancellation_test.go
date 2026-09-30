package voiceflow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

// Close deliberately waits for the provider context to be canceled. This
// detects both the missing synthesis cancellation bridge and cleanup ordering.
type contextBlockedPreparedSpeech struct {
	ctx       context.Context
	started   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	calls     atomic.Int32
}

func (prepared *contextBlockedPreparedSpeech) StreamSynthesize(
	_ string, _ speechio.StreamChunkHandler,
) (string, error) {
	prepared.calls.Add(1)
	close(prepared.started)
	<-prepared.ctx.Done()
	return "", prepared.ctx.Err()
}

func (prepared *contextBlockedPreparedSpeech) Close() {
	<-prepared.ctx.Done()
	prepared.closeOnce.Do(func() { close(prepared.closed) })
}

func TestPreparedSynthesisCancellationStopsOnlyItsOwnedProvider(t *testing.T) {
	for _, alreadyCanceled := range []bool{false, true} {
		name := "active synthesis"
		if alreadyCanceled {
			name = "already canceled synthesis"
		}
		t.Run(name, func(t *testing.T) {
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()
			providerCtx, cancelProvider := context.WithCancel(parent)
			defer cancelProvider()
			provider := &contextBlockedPreparedSpeech{
				ctx: providerCtx, started: make(chan struct{}), closed: make(chan struct{}),
			}
			preparation := &speculativeSynthesisPreparation{prepared: provider, cancel: cancelProvider}
			owned := preparation.takeReady()
			preparation.close()
			if providerCtx.Err() != nil {
				t.Fatal("transfer canceled provider before use")
			}
			synthesisCtx, cancelSynthesis := context.WithCancel(parent)
			defer cancelSynthesis()
			if alreadyCanceled {
				cancelSynthesis()
			}
			speech := &fakeStreamingSpeech{}
			var published atomic.Int32
			synthesis := startSpeculativeSynthesis(synthesisCtx, speech, owned, "reply",
				func([]byte) error { published.Add(1); return nil })
			if !alreadyCanceled {
				select {
				case <-provider.started:
				case <-time.After(time.Second):
					t.Fatal("prepared synthesis did not start")
				}
				// Cancel the candidate, keeping the enclosing conversation alive.
				synthesis.abort(context.Canceled)
			}
			select {
			case <-synthesis.done:
			case <-time.After(time.Second):
				t.Fatal("candidate cancellation left the prepared provider running")
			}
			result := synthesis.resultSnapshot()
			if !errors.Is(result.err, context.Canceled) || parent.Err() != nil || providerCtx.Err() != context.Canceled {
				t.Fatalf("result=%v parent=%v provider=%v", result.err, parent.Err(), providerCtx.Err())
			}
			wantCalls := int32(1)
			if alreadyCanceled {
				wantCalls = 0
			}
			if provider.calls.Load() != wantCalls || speech.streamCalls != 0 || published.Load() != 0 {
				t.Fatalf("prepared=%d fallback=%d published=%d", provider.calls.Load(), speech.streamCalls, published.Load())
			}
			select {
			case <-provider.closed:
			default:
				t.Fatal("owned provider cleanup did not finish")
			}
		})
	}
}
