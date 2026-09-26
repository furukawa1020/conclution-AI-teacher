package speechio

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamingSynthesisFlightReplaysPrefixAndFansOutLiveChunks(t *testing.T) {
	t.Parallel()

	var group streamingSynthesisFlightGroup
	key := newStreamingPCMCacheKey("voice", "audited cue")
	firstPublished := make(chan struct{})
	releaseTail := make(chan struct{})
	var providerCalls atomic.Int32
	produce := func(
		ctx context.Context,
		publish StreamChunkHandler,
	) (string, error) {
		providerCalls.Add(1)
		if err := publish([]byte{40, 0}); err != nil {
			return "", err
		}
		close(firstPublished)
		select {
		case <-releaseTail:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if err := publish([]byte{41, 0}); err != nil {
			return "", err
		}
		return StreamingAudioContentType, nil
	}

	type outcome struct {
		audio []byte
		err   error
	}
	results := make(chan outcome, 2)
	secondJoined := make(chan struct{})
	var deliveries atomic.Int32
	consume := func(result chan<- outcome) {
		var audio []byte
		_, err := group.stream(
			context.Background(),
			key,
			produce,
			func(chunk []byte) error {
				audio = append(audio, chunk...)
				if deliveries.Add(1) == 2 {
					close(secondJoined)
				}
				return nil
			},
		)
		result <- outcome{audio: audio, err: err}
	}
	go consume(results)
	select {
	case <-firstPublished:
	case <-time.After(time.Second):
		t.Fatal("provider did not publish first chunk")
	}
	go consume(results)
	// Only the first chunk exists here. Two callback deliveries therefore prove
	// that the late subscriber joined and replayed the prefix before completion.
	for deliveries.Load() < 2 {
		select {
		case <-secondJoined:
		case <-time.After(time.Second):
			t.Fatal("late subscriber did not join the active flight")
		}
	}
	close(releaseTail)

	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if !bytes.Equal(result.audio, []byte{40, 0, 41, 0}) {
			t.Fatalf("subscriber audio = %v", result.audio)
		}
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls = %d", providerCalls.Load())
	}
}

func TestStreamingSynthesisFlightSubscriberFailureDoesNotStopPeer(t *testing.T) {
	t.Parallel()

	var group streamingSynthesisFlightGroup
	key := newStreamingPCMCacheKey("voice", "audited cue")
	firstPublished := make(chan struct{})
	releaseTail := make(chan struct{})
	produce := func(
		ctx context.Context,
		publish StreamChunkHandler,
	) (string, error) {
		if err := publish([]byte{40, 0}); err != nil {
			return "", err
		}
		close(firstPublished)
		select {
		case <-releaseTail:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if err := publish([]byte{41, 0}); err != nil {
			return "", err
		}
		return StreamingAudioContentType, nil
	}

	callbackErr := errors.New("subscriber stopped")
	peerStarted := make(chan struct{})
	failed := make(chan error, 1)
	go func() {
		_, err := group.stream(
			context.Background(), key, produce,
			func([]byte) error {
				<-peerStarted
				return callbackErr
			},
		)
		failed <- err
	}()
	select {
	case <-firstPublished:
	case <-time.After(time.Second):
		t.Fatal("provider did not publish first chunk")
	}

	var peerAudio []byte
	var peerMu sync.Mutex
	var peerStartOnce sync.Once
	peer := make(chan error, 1)
	go func() {
		_, err := group.stream(
			context.Background(), key, produce,
			func(chunk []byte) error {
				peerStartOnce.Do(func() { close(peerStarted) })
				peerMu.Lock()
				peerAudio = append(peerAudio, chunk...)
				peerMu.Unlock()
				return nil
			},
		)
		peer <- err
	}()
	select {
	case <-peerStarted:
	case <-time.After(time.Second):
		t.Fatal("peer did not join the active flight")
	}
	close(releaseTail)

	if err := <-failed; !errors.Is(err, callbackErr) {
		t.Fatalf("failed subscriber error = %v", err)
	}
	if err := <-peer; err != nil {
		t.Fatal(err)
	}
	peerMu.Lock()
	defer peerMu.Unlock()
	if !bytes.Equal(peerAudio, []byte{40, 0, 41, 0}) {
		t.Fatalf("peer audio = %v", peerAudio)
	}
}
