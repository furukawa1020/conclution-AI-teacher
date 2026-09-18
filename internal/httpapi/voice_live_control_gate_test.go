package httpapi

import (
	"errors"
	"testing"
	"time"
)

func TestVoiceLiveCoachOutputGateSerializesPCMAndCheckpoint(t *testing.T) {
	gate := &voiceLiveCoachOutputGate{}
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- gate.deliver(func() error {
			close(writeStarted)
			<-releaseWrite
			return nil
		})
	}()
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("PCM write did not start")
	}
	checkpointDone := make(chan error, 1)
	go func() { checkpointDone <- gate.beginCheckpoint() }()
	select {
	case err := <-checkpointDone:
		t.Fatalf("checkpoint crossed in-flight PCM write: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseWrite)
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("PCM write did not complete")
	}
	select {
	case err := <-checkpointDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("checkpoint remained blocked after PCM write")
	}
	called := false
	if err := gate.deliver(func() error {
		called = true
		return nil
	}); err == nil || called {
		t.Fatalf("PCM crossed an unaccepted checkpoint: err=%v called=%v", err, called)
	}
}

func TestVoiceLiveCoachOutputGateWriteFailureUnlocks(t *testing.T) {
	gate := &voiceLiveCoachOutputGate{}
	writeErr := errors.New("socket write failed")
	if err := gate.deliver(func() error { return writeErr }); !errors.Is(err, writeErr) {
		t.Fatalf("write error = %v", err)
	}
	if err := gate.beginCheckpoint(); err != nil {
		t.Fatalf("failed write kept checkpoint locked: %v", err)
	}
}
