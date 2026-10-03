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

func TestDispatcherOverflowDoesNotWaitForObserver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := dispatcherManager(t, NewMemoryStore())
		if _, err := manager.Enable(context.Background(), "user"); err != nil {
			t.Fatal(err)
		}
		source := &dispatcherSource{started: make(chan struct{}), release: make(chan struct{})}
		releaseSource := sync.OnceFunc(func() { close(source.release) })
		observerGate := make(chan struct{})
		releaseObserver := sync.OnceFunc(func() { close(observerGate) })
		var observed atomic.Int32
		d, err := NewDispatcher(manager, source, DispatcherOptions{
			QueueCapacity: 1,
			Observer: func(outcome Outcome, latency time.Duration) {
				if outcome == OutcomeQueueFull {
					observed.Add(1)
					if latency != 0 {
						t.Error("overflow latency must retain its diagnostic zero value")
					}
					<-observerGate
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			releaseSource()
			releaseObserver()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := d.Close(ctx); err != nil {
				t.Error(err)
			}
		}()
		if !d.Enqueue("user", "first-state") {
			t.Fatal("first job rejected")
		}
		<-source.started
		if !d.Enqueue("user", "queued-state") {
			t.Fatal("queue capacity not available")
		}
		rejected := make(chan bool, 1)
		go func() { rejected <- d.Enqueue("user", "discard-state") }()
		synctest.Wait()
		select {
		case accepted := <-rejected:
			if accepted {
				t.Fatal("overflow was accepted")
			}
		default:
			t.Fatal("overflow waited for the stopped observer on the response path")
		}
		if observed.Load() != 0 {
			t.Fatal("response path invoked observer while the only worker was busy")
		}
		if cap(d.queueFullWake) != 1 || len(d.queueFullWake) != 1 {
			t.Fatal("overflow wake must have one content-free pending slot")
		}
		releaseSource()
		synctest.Wait()
		if observed.Load() != 1 {
			t.Fatal("worker did not deliver the overflow notification")
		}
		// Hold the worker inside the observer, then refill its one job slot.
		if len(d.jobs) == 0 && !d.Enqueue("user", "refill-state") {
			t.Fatal("refill failed")
		}
		for range 100 {
			if d.Enqueue("user", "discard-another-state") {
				t.Fatal("overflow accepted while observer was stopped")
			}
		}
		if len(d.jobs) != 1 || observed.Load() != 1 || len(d.queueFullWake) != 1 {
			t.Fatal("overflow changed job capacity or started another callback")
		}
		queued := <-d.jobs
		d.jobs <- queued
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancelClose()
		if err := d.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stopped observer must preserve Close deadline: %v", err)
		}
		releaseObserver()
		synctest.Wait()
		if err := d.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(queued.stateToken, make([]byte, len(queued.stateToken))) {
			t.Fatal("queued token survived completed shutdown")
		}
	})
}

func TestDispatcherOverflowObserverPanicKeepsWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := dispatcherManager(t, NewMemoryStore())
		if _, err := manager.Enable(context.Background(), "user"); err != nil {
			t.Fatal(err)
		}
		source := &dispatcherSource{started: make(chan struct{}), release: make(chan struct{})}
		releaseSource := sync.OnceFunc(func() { close(source.release) })
		var panics atomic.Int32
		d, err := NewDispatcher(manager, source, DispatcherOptions{
			QueueCapacity: 1,
			Observer: func(outcome Outcome, _ time.Duration) {
				if outcome == OutcomeQueueFull {
					panics.Add(1)
					panic("private observer diagnostic")
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { releaseSource(); _ = d.Close(context.Background()) }()
		if !d.Enqueue("user", "first") {
			t.Fatal("first rejected")
		}
		<-source.started
		if !d.Enqueue("user", "queued") || d.Enqueue("user", "overflow") {
			t.Fatal("invalid bounded admission")
		}
		releaseSource()
		synctest.Wait()
		if panics.Load() != 1 || source.calls.Load() != 2 {
			t.Fatal("worker lost its overflow notification or queued job")
		}
		if !d.Enqueue("user", "after-panic") {
			t.Fatal("follow-up job rejected")
		}
		synctest.Wait()
		if source.calls.Load() != 3 {
			t.Fatal("observer panic stopped the worker")
		}
	})
}

func TestDispatcherCancelledOverflowDoesNotStartObserver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observed := 0
	d := &Dispatcher{
		jobs: make(chan memoryJob, 1), queueFullWake: make(chan struct{}, 1),
		observe: func(Outcome, time.Duration) { observed++ },
	}
	// Exercise the selected-wake guard directly, independently of select
	// choosing ctx.Done or the queued marker in the worker loop.
	if d.notifyQueueFull(ctx) {
		t.Fatal("cancelled overflow notification was accepted")
	}
	d.queueFullWake <- struct{}{}
	d.worker(ctx)
	if observed != 0 {
		t.Fatal("cancelled worker started a new overflow callback")
	}
}
