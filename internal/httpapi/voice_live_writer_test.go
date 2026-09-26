package httpapi

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func waitForVoiceLiveQueueDepth(
	t *testing.T,
	queue chan voiceLiveSocketWriteRequest,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(queue) != want {
		if time.Now().After(deadline) {
			t.Fatalf("queue depth=%d want=%d", len(queue), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestVoiceLiveOutboundWriterPrioritizesQueuedAudio(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var order []string
	writer := newVoiceLiveOutboundWriterWithWrite(
		ctx,
		func(
			writeCtx context.Context,
			_ websocket.MessageType,
			payload []byte,
		) error {
			value := string(payload)
			mu.Lock()
			order = append(order, value)
			mu.Unlock()
			if value == "control-1" {
				once.Do(func() { close(firstStarted) })
				select {
				case <-releaseFirst:
				case <-writeCtx.Done():
					return writeCtx.Err()
				}
			}
			return nil
		},
	)

	results := make(chan error, 3)
	go func() {
		results <- writer.submit(
			ctx, writer.control, websocket.MessageText, []byte("control-1"),
		)
	}()
	<-firstStarted
	go func() {
		results <- writer.submit(
			ctx, writer.control, websocket.MessageText, []byte("control-2"),
		)
	}()
	go func() {
		results <- writer.submit(
			ctx, writer.audio, websocket.MessageBinary, []byte("audio-1"),
		)
	}()
	waitForVoiceLiveQueueDepth(t, writer.control, 1)
	waitForVoiceLiveQueueDepth(t, writer.audio, 1)
	close(releaseFirst)
	for range 3 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if want := []string{"control-1", "audio-1", "control-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("write order=%v want=%v", got, want)
	}
}

func TestVoiceLiveOutboundWriterBoundsSlowConsumer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := newVoiceLiveOutboundWriterWithWrite(
		ctx,
		func(
			writeCtx context.Context,
			_ websocket.MessageType,
			_ []byte,
		) error {
			<-writeCtx.Done()
			return writeCtx.Err()
		},
	)
	requestCtx, cancelRequest := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancelRequest()
	started := time.Now()
	err := writer.writeAudio(requestCtx, []byte{1, 2})
	if err == nil || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("slow writer error=%v elapsed=%s", err, time.Since(started))
	}
}
