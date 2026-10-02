package httpapi

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/coder/websocket"
)

func TestVoiceLiveOutboundWriterOwnsPCMUntilCanceledWriteReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writerCtx, cancelWriter := context.WithCancel(context.Background())
		defer cancelWriter()
		requestCtx, cancelRequest := context.WithCancel(writerCtx)
		defer cancelRequest()
		entered := make(chan struct{})
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		defer release()
		observed := make(chan []byte, 1)
		writer := newVoiceLiveOutboundWriterWithWrite(writerCtx,
			func(ctx context.Context, _ websocket.MessageType, payload []byte) error {
				close(entered)
				// A write may still be reading PCM when cancellation lets its
				// caller return and immediately recycle the original buffer.
				<-gate
				observed <- bytes.Clone(payload)
				return ctx.Err()
			})
		pcm := []byte{80, 0, 90, 0}
		original := bytes.Clone(pcm)
		done := make(chan error, 1)
		go func() { done <- writer.writeAudio(requestCtx, pcm) }()
		synctest.Wait()
		select {
		case <-entered:
		default:
			t.Fatal("socket write did not start")
		}
		cancelRequest()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error=%v", err)
			}
		default:
			t.Fatal("caller waited for canceled socket write to return")
		}
		clear(pcm)
		copy(pcm, []byte{1, 2, 3, 4})
		release()
		synctest.Wait()
		if got := <-observed; !bytes.Equal(got, original) {
			t.Fatalf("caller buffer reuse corrupted pending write: got=%v want=%v", got, original)
		}
	})
}

func TestVoiceLiveOutboundWriterSkipsCanceledQueuedAudio(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writerCtx, cancelWriter := context.WithCancel(context.Background())
		defer cancelWriter()
		requestCtx, cancelRequest := context.WithCancel(writerCtx)
		defer cancelRequest()
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		defer release()
		var writes []string
		writer := newVoiceLiveOutboundWriterWithWrite(writerCtx,
			func(_ context.Context, kind websocket.MessageType, payload []byte) error {
				writes = append(writes, string(payload))
				if kind == websocket.MessageText {
					<-gate
				}
				return nil
			})
		controlDone := make(chan error, 1)
		go func() {
			controlDone <- writer.submit(writerCtx, writer.control, websocket.MessageText, []byte("control"))
		}()
		synctest.Wait()
		audioDone := make(chan error, 1)
		go func() { audioDone <- writer.writeAudio(requestCtx, []byte("canceled audio")) }()
		synctest.Wait()
		if len(writer.audio) != 1 {
			t.Fatal("audio did not enter bounded queue")
		}
		cancelRequest()
		synctest.Wait()
		if err := <-audioDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation error=%v", err)
		}
		release()
		synctest.Wait()
		if err := <-controlDone; err != nil {
			t.Fatal(err)
		}
		if len(writes) != 1 || writes[0] != "control" {
			t.Fatalf("canceled queued audio reached socket: %v", writes)
		}
	})
}
