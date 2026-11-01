package nativevoice

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInputCancellationDoesNotWaitForSendLock(t *testing.T) {
	for _, action := range []string{"start", "pcm", "end"} {
		t.Run(action, func(t *testing.T) {
			_, opened := openTestSession(t, Config{})
			session := opened.(*liveSession)
			if err := session.sendMu.LockContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				switch action {
				case "start":
					done <- session.StartActivity(ctx)
				case "pcm":
					done <- session.SendPCM20ms(ctx, make([]byte, InputFrameBytes))
				case "end":
					done <- session.EndActivity(ctx)
				}
			}()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, ErrClosed) {
					t.Error(err)
				}
			case <-time.After(time.Second):
				t.Error("cancelled input still waiting for lock")
			}
			session.sendMu.Unlock()
			if err := session.StartActivity(context.Background()); err != nil {
				t.Fatal("subsequent caller lost lock", err)
			}
		})
	}
}

func TestSendBudgetIncludesLockWait(t *testing.T) {
	_, opened := openTestSession(t, Config{SendTimeout: 20 * time.Millisecond})
	session := opened.(*liveSession)
	if err := session.sendMu.LockContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer session.sendMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- session.StartActivity(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrDeadline) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("send budget did not bound lock wait")
	}
}

func TestCommitStopsWaitingWhenSessionCloses(t *testing.T) {
	_, opened := openTestSession(t, Config{})
	session := opened.(*liveSession)
	if err := session.sendMu.LockContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer session.sendMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- session.CommitOutput() }()
	session.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("commit did not observe session shutdown")
	}
}
