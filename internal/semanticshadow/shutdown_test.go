package semanticshadow

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
)

type shutdownExporter struct {
	ctx     context.Context
	release chan struct{}
	calls   int
}

func (e *shutdownExporter) Export(ctx context.Context, _ Graph) error {
	e.ctx = ctx
	e.calls++
	<-e.release
	return ctx.Err()
}

func TestShadowCloseCancelsExportAndDrainsRacingAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := &shutdownExporter{release: make(chan struct{})}
		d, err := NewDispatcher(e)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		defer close(e.release)
		digest := strings.Repeat("a", 64)
		if !d.Observe(digest, validSignals()) {
			t.Fatal("first item rejected")
		}
		synctest.Wait()
		if e.ctx == nil {
			t.Fatal("export did not start")
		}
		var wg sync.WaitGroup
		for range 32 {
			wg.Add(1)
			go func() { defer wg.Done(); d.Observe(digest, validSignals()) }()
		}
		d.Close()
		wg.Wait()
		if e.ctx.Err() != context.Canceled || len(d.queue) != 0 || e.calls != 1 {
			t.Fatalf("close left work: error=%v queued=%d calls=%d", e.ctx.Err(), len(d.queue), e.calls)
		}
		if d.Observe(digest, validSignals()) {
			t.Fatal("closed admission reopened")
		}
	})
}
