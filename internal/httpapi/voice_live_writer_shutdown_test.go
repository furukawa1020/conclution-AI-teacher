package httpapi

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
)

func newVoiceLiveShutdownTestWriter(write func(context.Context, websocket.MessageType, []byte) error) *voiceLiveOutboundWriter {
	return &voiceLiveOutboundWriter{
		audio:   make(chan voiceLiveSocketWriteRequest, voiceLiveAudioWriteQueueCapacity),
		control: make(chan voiceLiveSocketWriteRequest, voiceLiveControlWriteQueueCapacity),
		done:    make(chan struct{}), write: write, audioQueueDeadline: voiceLiveAudioQueueDeadline,
	}
}

func queuedVoiceLiveTestRequest(audio bool, value byte) voiceLiveSocketWriteRequest {
	messageType := websocket.MessageText
	if audio {
		messageType = websocket.MessageBinary
	}
	return voiceLiveSocketWriteRequest{
		ctx: context.Background(), audio: audio, messageType: messageType, enqueuedAt: time.Now(),
		payload: []byte{value, 0}, done: make(chan error, 1),
	}
}

func TestVoiceLiveOutboundWriterShutdownDrainsWithoutClearingActiveWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		defer release()
		var active []byte
		writes := 0
		writer := newVoiceLiveShutdownTestWriter(func(_ context.Context, _ websocket.MessageType, payload []byte) error {
			writes++
			if writes == 1 {
				active = payload
				<-gate // Deliberately outlive cancellation and caller buffer reuse.
			}
			return nil
		})
		finished := make(chan struct{})
		go func() { writer.run(ctx); close(finished) }()
		original := []byte{80, 0, 90, 0}
		callerPCM := bytes.Clone(original)
		callerDone := make(chan error, 1)
		go func() { callerDone <- writer.writeAudio(ctx, callerPCM) }()
		synctest.Wait()
		if writes != 1 {
			t.Fatal("active socket write did not start")
		}
		// Independent request contexts make shutdown, rather than per-request
		// cancellation or queue expiry, own rejection of these unsent copies.
		var queued []voiceLiveSocketWriteRequest
		for index := 0; index < cap(writer.audio); index++ {
			request := queuedVoiceLiveTestRequest(true, byte(index+1))
			queued = append(queued, request)
			writer.audio <- request
		}
		for index := 0; index < cap(writer.control); index++ {
			request := queuedVoiceLiveTestRequest(false, byte(index+10))
			queued = append(queued, request)
			writer.control <- request
		}
		cancel()
		synctest.Wait()
		if err := <-callerDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation=%v", err)
		}
		clear(callerPCM)
		if !bytes.Equal(active, original) {
			t.Fatal("shutdown cleared the active write before it returned")
		}
		select {
		case <-finished:
			t.Fatal("writer exited while its socket write still owned PCM")
		default:
		}
		release()
		<-finished // done closes before cleanup; run return is the drain barrier.
		if writes != 1 || len(writer.audio) != 0 || len(writer.control) != 0 || !bytes.Equal(active, make([]byte, len(active))) {
			t.Fatalf("shutdown sent or retained queued copies: writes=%d audio=%d control=%d active=%v", writes, len(writer.audio), len(writer.control), active)
		}
		for _, request := range queued {
			if !bytes.Equal(request.payload, []byte{0, 0}) {
				t.Fatalf("unsent copy not cleared: %v", request.payload)
			}
			select {
			case err := <-request.done:
				if err == nil {
					t.Fatal("unsent request reported successful delivery")
				}
			default:
				t.Fatal("shutdown did not notify queued request")
			}
		}
	})
}

func TestVoiceLiveOutboundWriterShutdownUnblocksFullQueueSubmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		defer release()
		writes := 0
		writer := newVoiceLiveShutdownTestWriter(func(context.Context, websocket.MessageType, []byte) error {
			writes++
			if writes == 1 {
				<-gate
			}
			return nil
		})
		finished := make(chan struct{})
		go func() { writer.run(ctx); close(finished) }()
		firstDone := make(chan error, 1)
		go func() { firstDone <- writer.writeAudio(ctx, []byte{1, 0}) }()
		synctest.Wait()
		for index := 0; index < cap(writer.audio); index++ {
			writer.audio <- queuedVoiceLiveTestRequest(true, byte(index+2))
		}
		const submitters = 32
		blockedDone := make(chan error, submitters)
		original := []byte{99, 0}
		for range submitters {
			go func() { blockedDone <- writer.writeAudio(context.Background(), original) }()
		}
		synctest.Wait()
		select {
		case <-blockedDone:
			t.Fatal("full queue did not block submission")
		default:
		}
		cancel()
		release()
		synctest.Wait()
		select {
		case <-finished:
		default:
			t.Fatal("shutdown deadlocked with a submit waiting on a full queue")
		}
		for range submitters {
			if err := <-blockedDone; err == nil {
				t.Fatal("blocked submit reached the socket after shutdown")
			}
		}
		<-firstDone
		if writes != 1 || len(writer.audio) != 0 || !bytes.Equal(original, []byte{99, 0}) {
			t.Fatalf("shutdown changed source or retained queue: writes=%d queued=%d original=%v", writes, len(writer.audio), original)
		}
	})
}

func TestVoiceLiveOutboundWriterShutdownClearsPendingControl(t *testing.T) {
	writer := newVoiceLiveShutdownTestWriter(nil)
	pending := queuedVoiceLiveTestRequest(false, 42)
	queued := queuedVoiceLiveTestRequest(false, 43)
	writer.control <- queued
	writer.shutdown(&pending)
	for _, request := range []voiceLiveSocketWriteRequest{pending, queued} {
		if !bytes.Equal(request.payload, []byte{0, 0}) {
			t.Fatalf("control copy survived shutdown: %v", request.payload)
		}
		select {
		case err := <-request.done:
			if !errors.Is(err, errVoiceLiveOutboundWriterClosed) {
				t.Fatalf("shutdown notification=%v", err)
			}
		default:
			t.Fatal("pending control caller was not released")
		}
	}
}

func TestVoiceLiveOutboundWriterShutdownClosesAdmissionBeforeDrainLock(t *testing.T) {
	writer := newVoiceLiveShutdownTestWriter(nil)
	// Mutex waits are not durably blocked in synctest. Hold the admission
	// gate explicitly and use done as the deterministic shutdown barrier.
	writer.enqueueMu.Lock()
	unlock := sync.OnceFunc(writer.enqueueMu.Unlock)
	defer unlock()
	submitted := make(chan error, 1)
	go func() { submitted <- writer.writeAudio(context.Background(), []byte{7, 0}) }()
	finished := make(chan struct{})
	go func() { writer.shutdown(nil); close(finished) }()
	select {
	case <-writer.done:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for the drain lock before closing admission")
	}
	unlock()
	<-finished
	if err := <-submitted; !errors.Is(err, errVoiceLiveOutboundWriterClosed) {
		t.Fatalf("racing admission=%v", err)
	}
	if len(writer.audio) != 0 {
		t.Fatalf("admission escaped shutdown gate: queued=%d", len(writer.audio))
	}
}

func TestVoiceLiveOutboundWriterShutdownCancelsIndependentWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var owned []byte
		writer := newVoiceLiveShutdownTestWriter(func(writeCtx context.Context, _ websocket.MessageType, payload []byte) error {
			owned = payload
			<-writeCtx.Done()
			return writeCtx.Err()
		})
		finished := make(chan struct{})
		go func() { writer.run(ctx); close(finished) }()
		original := []byte{50, 0}
		submitted := make(chan error, 1)
		go func() { submitted <- writer.writeAudio(context.Background(), original) }()
		synctest.Wait()
		started := time.Now()
		cancel()
		synctest.Wait()
		select {
		case <-finished:
		default:
			t.Fatal("independent write waited for its socket timeout after writer cancellation")
		}
		if err := <-submitted; err == nil || time.Since(started) != 0 || !bytes.Equal(original, []byte{50, 0}) || !bytes.Equal(owned, []byte{0, 0}) {
			t.Fatalf("writer cancellation lost ownership: err=%v elapsed=%s original=%v owned=%v", err, time.Since(started), original, owned)
		}
	})
}

func TestVoiceLiveOutboundWriterShutdownRejectsLaterSubmissions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		writer := newVoiceLiveShutdownTestWriter(func(context.Context, websocket.MessageType, []byte) error {
			t.Error("closed writer sent a frame")
			return nil
		})
		finished := make(chan struct{})
		go func() { writer.run(ctx); close(finished) }()
		synctest.Wait()
		cancel()
		<-finished
		for index := 0; index < 32; index++ {
			for _, lane := range []chan voiceLiveSocketWriteRequest{writer.audio, writer.control} {
				original := []byte{byte(index + 1), 0}
				if err := writer.submit(context.Background(), lane, websocket.MessageBinary, original); err == nil {
					t.Fatal("closed writer accepted submission")
				}
				if !bytes.Equal(original, []byte{byte(index + 1), 0}) {
					t.Fatal("closed writer cleared caller-owned bytes")
				}
			}
		}
		if len(writer.audio) != 0 || len(writer.control) != 0 {
			t.Fatalf("closed writer retained new copies: audio=%d control=%d", len(writer.audio), len(writer.control))
		}
	})
}
