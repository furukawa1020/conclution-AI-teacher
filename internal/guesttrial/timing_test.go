package guesttrial

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestTimingsAreBoundedRequestLocalAndFirstObservationOnly(t *testing.T) {
	ctx, timings := WithTimings(context.Background())
	other, empty := WithTimings(context.Background())
	RecordTiming(ctx, TimingASR, 1250*time.Microsecond)
	RecordTiming(ctx, TimingASR, 10*time.Second)
	RecordTiming(ctx, TimingVerify, 2*time.Millisecond)
	RecordTiming(ctx, TimingPipeline, 3*time.Millisecond)
	RecordTiming(other, TimingStage(99), time.Second)
	RecordTiming(other, TimingASR, -time.Second)
	RecordTiming(other, TimingASR, time.Hour)
	RecordTiming(context.Background(), TimingASR, time.Second)
	if got := timings.ServerTiming(); got != "asr;dur=1, verify;dur=2, pipeline;dur=3" {
		t.Fatal(got)
	}
	if empty.ServerTiming() != "" {
		t.Fatal("invalid timing was recorded")
	}
}

func TestTimingsConcurrentObservers(t *testing.T) {
	ctx, timings := WithTimings(context.Background())
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() { defer wg.Done(); RecordTiming(ctx, TimingASR, time.Millisecond); _ = timings.ServerTiming() }()
	}
	wg.Wait()
	if timings.ServerTiming() != "asr;dur=1" {
		t.Fatal(timings.ServerTiming())
	}
}
