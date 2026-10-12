package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestVoiceWriterCompletionCancellation(t *testing.T) {
	for _, mode := range []string{"normal", "request", "writer", "transport error"} {
		t.Run(mode, func(t *testing.T) {
			writerCtx, stopWriter := context.WithCancel(context.Background())
			defer stopWriter()
			requestCtx, stopRequest := context.WithCancel(context.Background())
			defer stopRequest()
			transportErr := errors.New("transport failed")
			calls := 0
			writer := newVoiceLiveOutboundWriterWithWrite(writerCtx, func(_ context.Context, _ websocket.MessageType, pcm []byte) error {
				calls++
				if pcm[0] != 80 {
					t.Error("payload cleared before transport completion")
				}
				switch mode {
				case "request":
					stopRequest()
				case "writer":
					stopWriter()
				case "transport error":
					stopRequest()
					return transportErr
				}
				return nil
			})
			pcm := []byte{80, 0}
			done := make(chan error, 1)
			// Inspect the writer's result, not submit's independent cancellation
			// select, which could mask an incorrectly successful completion.
			writer.audio <- voiceLiveSocketWriteRequest{ctx: requestCtx, audio: true, enqueuedAt: time.Now(), messageType: websocket.MessageBinary, payload: pcm, done: done}
			var err error
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("writer did not complete")
			}
			want := error(nil)
			if mode == "request" || mode == "writer" {
				want = context.Canceled
			}
			if mode == "transport error" {
				want = transportErr
			}
			if !errors.Is(err, want) || calls != 1 || pcm[0] != 0 {
				t.Fatalf("err=%v want=%v calls=%d pcm=%v", err, want, calls, pcm)
			}
			stopWriter()
			<-writer.done
		})
	}
}
