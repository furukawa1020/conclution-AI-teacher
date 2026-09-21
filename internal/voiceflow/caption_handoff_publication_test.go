package voiceflow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCaptionHandoffCancelWaitsForPublishedPCMAndPreventsRevival(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	handoff := &captionHandoff{
		ctx:             ctx,
		cancel:          cancel,
		committed:       true,
		audioAuthorized: true,
		onAudio: func([]byte) error {
			calls.Add(1)
			close(entered)
			<-release
			return nil
		},
	}

	delivered := make(chan error, 1)
	go func() {
		delivered <- handoff.deliverAudio([]byte{1, 0})
	}()
	<-entered

	canceled := make(chan struct{})
	go func() {
		handoff.Cancel()
		close(canceled)
	}()
	select {
	case <-canceled:
		t.Fatal("Cancel returned while a PCM callback was still running")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	if err := <-delivered; err != nil {
		t.Fatalf("deliverAudio error = %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Cancel did not pass the publication barrier")
	}

	if err := handoff.deliverAudio([]byte{2, 0}); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-cancel delivery error = %v; want context cancellation", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("PCM callbacks after Cancel = %d; want exactly one drained callback", calls.Load())
	}
}

func TestCaptionHandoffConcurrentCancelCallsSharePublicationBarrier(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	handoff := &captionHandoff{
		ctx:             ctx,
		cancel:          cancel,
		committed:       true,
		audioAuthorized: true,
		onAudio: func([]byte) error {
			close(entered)
			<-release
			return nil
		},
	}
	delivered := make(chan error, 1)
	go func() {
		delivered <- handoff.deliverAudio([]byte{1, 0})
	}()
	<-entered

	var cancelers sync.WaitGroup
	cancelers.Add(2)
	done := make(chan struct{})
	for range 2 {
		go func() {
			defer cancelers.Done()
			handoff.Cancel()
		}()
	}
	go func() {
		cancelers.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("concurrent Cancel returned before the PCM callback drained")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-delivered; err != nil {
		t.Fatalf("deliverAudio error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent Cancel calls did not complete")
	}
}

func TestCaptionHandoffNormalFinishClosesPublicationBoundary(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	handoff := &captionHandoff{
		ctx:             ctx,
		cancel:          cancel,
		committed:       true,
		audioAuthorized: true,
		onAudio: func([]byte) error {
			close(entered)
			<-release
			return nil
		},
	}
	delivered := make(chan error, 1)
	go func() {
		delivered <- handoff.deliverAudio([]byte{1, 0})
	}()
	<-entered

	finished := make(chan struct{})
	go func() {
		handoff.finishCommitted()
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("finishCommitted returned before the PCM callback drained")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-delivered; err != nil {
		t.Fatalf("deliverAudio error = %v", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("finishCommitted did not pass the publication barrier")
	}
	if !handoff.finished || handoff.audioAuthorized {
		t.Fatalf("finish state = finished:%v authorized:%v", handoff.finished, handoff.audioAuthorized)
	}
}
