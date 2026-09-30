package speechio

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestPreparedCachedSynthesisDoesNotWaitForUnusedTransport(t *testing.T) {
	callbackFailure := errors.New("cached playback rejected")
	for _, test := range []struct {
		name          string
		callbackError error
		cancelOnFirst bool
		wantChunks    int
		wantError     error
	}{
		{name: "complete", wantChunks: 2},
		{name: "callback failure", callbackError: callbackFailure, wantChunks: 1, wantError: callbackFailure},
		{name: "cancel between chunks", cancelOnFirst: true, wantChunks: 1, wantError: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			closeSendGate := make(chan struct{})
			defer close(closeSendGate)
			stream := &cancellationAwarePreparedStream{closeSendGate: closeSendGate}
			prepared := prepareCancellationAwareStream(t, ctx, stream)
			defer prepared.Close()
			cloudPrepared := prepared.(*preparedCloudSynthesis)
			cloudPrepared.service.streamingPCMCache = newStreamingPCMCache(2, 1024)
			text := "cached committed reply"
			chunks := [][]byte{{1, 0}, {2, 0}}
			cloudPrepared.service.streamingPCMCache.put(
				newStreamingPCMCacheKey(cloudPrepared.service.voiceName, text),
				cachedStreamingPCM{chunks: chunks, size: 4},
			)
			type result struct {
				mime        string
				err         error
				received    [][]byte
				callbackErr error
			}
			done := make(chan result, 1)
			go func() {
				var outcome result
				outcome.mime, outcome.err = prepared.StreamSynthesize(text, func(chunk []byte) error {
					// The unused RPC must stay live until cached delivery finishes;
					// canceling it first would also cancel this playback context.
					if err := stream.ctx.Err(); err != nil {
						outcome.callbackErr = err
						return err
					}
					outcome.received = append(outcome.received, append([]byte(nil), chunk...))
					if test.cancelOnFirst {
						cancel()
					}
					return test.callbackError
				})
				done <- outcome
			}()
			select {
			case outcome := <-done:
				if !errors.Is(outcome.err, test.wantError) {
					t.Fatalf("synthesis error = %v, want %v", outcome.err, test.wantError)
				}
				if outcome.callbackErr != nil {
					t.Fatalf("cached delivery started with canceled provider context: %v", outcome.callbackErr)
				}
				if outcome.err == nil && outcome.mime != StreamingAudioContentType {
					t.Fatalf("audio type = %q", outcome.mime)
				}
				if len(outcome.received) != test.wantChunks {
					t.Fatalf("cached chunks = %d, want %d", len(outcome.received), test.wantChunks)
				}
				for index, received := range outcome.received {
					if !bytes.Equal(received, chunks[index]) {
						t.Fatalf("cached chunk %d changed: %v", index, received)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("cached synthesis waited for unused provider CloseSend")
			}
			if got := stream.closeSendCalls.Load(); got != 0 {
				t.Fatalf("cache hit called CloseSend %d times", got)
			}
			if stream.ctx.Err() != context.Canceled {
				t.Fatal("completed cached synthesis left its unused provider alive")
			}
			if !test.cancelOnFirst && ctx.Err() != nil {
				t.Fatalf("cached synthesis canceled its parent: %v", ctx.Err())
			}
		})
	}
}
