package longmemory

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDispatcherRejectsEnqueueAfterClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := dispatcherManager(t, NewMemoryStore())
		source := &dispatcherSource{}
		var observed atomic.Int32
		d, err := NewDispatcher(manager, source, DispatcherOptions{
			Observer: func(Outcome, time.Duration) { observed.Add(1) },
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		var clockCalls int
		d.now = func() time.Time {
			clockCalls++
			return time.Now()
		}
		if d.Enqueue("user", "must-not-outlive-worker") {
			t.Error("closed dispatcher accepted a state token after its workers exited")
		}
		if clockCalls != 0 || len(d.jobs) != 0 {
			t.Error("closed enqueue reached copy preparation or retained a queued token")
		}
		if observed.Load() != 0 || len(d.queueFullWake) != 0 || source.calls.Load() != 0 {
			t.Error("closed rejection invoked memory work or an overflow observation")
		}
	})
}

func TestDispatcherCloseDoesNotWaitForUnadmittedClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := dispatcherManager(t, NewMemoryStore())
		source := &dispatcherSource{}
		var observed atomic.Int32
		d, err := NewDispatcher(manager, source, DispatcherOptions{
			Observer: func(Outcome, time.Duration) { observed.Add(1) },
		})
		if err != nil {
			t.Fatal(err)
		}
		clockStarted := make(chan struct{})
		clockGate := make(chan struct{})
		releaseClock := sync.OnceFunc(func() { close(clockGate) })
		d.now = func() time.Time {
			close(clockStarted)
			<-clockGate
			return time.Now()
		}
		defer func() {
			releaseClock()
			if err := d.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}()
		accepted := make(chan bool, 1)
		go func() { accepted <- d.Enqueue("user", "prepared-before-close") }()
		<-clockStarted
		synctest.Wait()

		closeCtx, cancelClose := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancelClose()
		if err := d.Close(closeCtx); err != nil {
			t.Fatalf("close waited for the unadmitted job's clock: %v", err)
		}
		select {
		case <-accepted:
			t.Fatal("clock gate did not retain the unadmitted enqueue")
		default:
		}
		releaseClock()
		synctest.Wait()
		if <-accepted {
			t.Error("late enqueue transferred its copy after the workers exited")
		}
		if len(d.jobs) != 0 || len(d.queueFullWake) != 0 || observed.Load() != 0 || source.calls.Load() != 0 {
			t.Error("late closed rejection retained state or generated memory work")
		}
	})
}

func TestDispatcherClosedAdmissionWipesPreparedCopyWithoutOverflow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := dispatcherManager(t, NewMemoryStore())
		source := &dispatcherSource{}
		var observed atomic.Int32
		d, err := NewDispatcher(manager, source, DispatcherOptions{
			Observer: func(Outcome, time.Duration) { observed.Add(1) },
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		token := []byte("prepared-but-never-admitted")
		if d.admit(memoryJob{uid: "user", stateToken: token, createdAt: time.Now()}) {
			t.Fatal("closed admission accepted a prepared copy")
		}
		if !bytes.Equal(token, make([]byte, len(token))) {
			t.Fatal("closed admission did not wipe the caller-owned token")
		}
		if len(d.jobs) != 0 || len(d.queueFullWake) != 0 || observed.Load() != 0 || source.calls.Load() != 0 {
			t.Fatal("closed rejection retained a job or was reported as queue overflow")
		}
	})
}

func TestDispatcherCloseWipesAdmittedCopyAndRejectsLaterCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := dispatcherManager(t, NewMemoryStore())
		if _, err := manager.Enable(context.Background(), "user"); err != nil {
			t.Fatal(err)
		}
		source := &dispatcherSource{started: make(chan struct{}), release: make(chan struct{})}
		releaseSource := sync.OnceFunc(func() { close(source.release) })
		d, err := NewDispatcher(manager, source, DispatcherOptions{QueueCapacity: 1})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			releaseSource()
			if err := d.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}()
		if !d.Enqueue("user", "held-by-source") {
			t.Fatal("first job rejected")
		}
		<-source.started
		queuedToken := []byte("admitted-before-close")
		if !d.admit(memoryJob{uid: "user", stateToken: queuedToken, createdAt: time.Now()}) {
			t.Fatal("prepared job did not transfer to the bounded queue")
		}
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancelClose()
		if err := d.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stopped source changed the close deadline: %v", err)
		}
		lateToken := []byte("rejected-during-shutdown")
		if d.admit(memoryJob{uid: "user", stateToken: lateToken, createdAt: time.Now()}) {
			t.Fatal("shutdown still admitted a state token")
		}
		if !bytes.Equal(lateToken, make([]byte, len(lateToken))) {
			t.Fatal("rejected token survived a timed-out close")
		}
		releaseSource()
		synctest.Wait()
		if err := d.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(d.jobs) != 0 || !bytes.Equal(queuedToken, make([]byte, len(queuedToken))) {
			t.Fatal("worker shutdown did not drain and wipe the admitted token")
		}
	})
}
