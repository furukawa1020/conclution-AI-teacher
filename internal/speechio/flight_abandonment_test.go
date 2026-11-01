package speechio

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAbandonedFlightRetiresWithoutWaitingForProviderReturn(t *testing.T) {
	var group streamingSynthesisFlightGroup
	key := newStreamingPCMCacheKey("voice", "audited cue")
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := group.stream(ctx, key, func(context.Context, StreamChunkHandler) (string, error) {
			close(started)
			<-release
			return StreamingAudioContentType, nil
		}, func([]byte) error { return nil })
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("producer not started")
	}
	group.mu.Lock()
	abandoned := group.flights[key]
	group.mu.Unlock()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber not released")
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		group.mu.Lock()
		present := group.flights[key] != nil
		group.mu.Unlock()
		if !present {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("abandoned registration retained")
		}
	}
	abandoned.mu.Lock()
	terminal, buffered := abandoned.done, abandoned.buffered
	abandoned.mu.Unlock()
	if !terminal || buffered != 0 {
		t.Fatal("abandoned flight not terminal or retained PCM")
	}
	// New calls cannot inherit the old cancellation result.
	mime, err := group.stream(context.Background(), key, func(context.Context, StreamChunkHandler) (string, error) {
		return StreamingAudioContentType, nil
	}, func([]byte) error { return nil })
	if err != nil || mime != StreamingAudioContentType {
		t.Fatal(mime, err)
	}
}
