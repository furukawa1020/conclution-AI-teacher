package voiceflow

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

// Keep both the ordinary synthesis and a late preparation alive independently.
// Cancellation must not join preparation or cancel the selected synthesis.
type pendingCommittedPreparationSpeech struct {
	fakeStreamingSpeech
	prepareCtx                                                      context.Context
	prepareStarted, returnPrepared, fallbackStarted, finishFallback chan struct{}
	provider                                                        *fakePreparedSynthesis
}

func (speech *pendingCommittedPreparationSpeech) PrepareStreamingSynthesis(ctx context.Context) (speechio.PreparedStreamingSynthesis, error) {
	speech.prepareCtx = ctx
	close(speech.prepareStarted)
	<-speech.returnPrepared
	return speech.provider, nil
}

func (speech *pendingCommittedPreparationSpeech) StreamSynthesize(ctx context.Context, text string, onChunk speechio.StreamChunkHandler) (string, error) {
	close(speech.fallbackStarted)
	<-speech.finishFallback
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return speech.fakeStreamingSpeech.StreamSynthesize(ctx, text, onChunk)
}

func TestCommittedFallbackReleasesPendingPreparationBeforePlaybackEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		speech := &pendingCommittedPreparationSpeech{
			fakeStreamingSpeech: fakeStreamingSpeech{chunks: [][]byte{{9, 0}}},
			prepareStarted:      make(chan struct{}), returnPrepared: make(chan struct{}),
			fallbackStarted: make(chan struct{}), finishFallback: make(chan struct{}),
			provider: &fakePreparedSynthesis{},
		}
		preparation := startSpeculativeSynthesisPreparation(parent, speech)
		defer preparation.close()
		<-speech.prepareStarted
		done := make(chan error, 1)
		chunks := 0
		go func() {
			_, err := streamCommittedSynthesis(parent, speech, preparation, "audited reply", func([]byte) error { chunks++; return nil })
			done <- err
		}()
		synctest.Wait()
		select {
		case <-speech.fallbackStarted:
		default:
			t.Error("ordinary synthesis waited for the unused preparation")
		}
		if speech.prepareCtx.Err() != context.Canceled {
			t.Error("unused preparation still running during ordinary synthesis")
		}
		if parent.Err() != nil {
			t.Error("releasing preparation canceled the parent turn")
		}
		close(speech.returnPrepared)
		synctest.Wait()
		if !speech.provider.closed || speech.provider.calls != 0 {
			t.Error("late prepared connection was not closed without synthesis")
		}
		close(speech.finishFallback)
		if err := <-done; err != nil || chunks != 1 || speech.streamCalls != 1 {
			t.Fatalf("fallback err=%v chunks=%d calls=%d", err, chunks, speech.streamCalls)
		}
	})
}
