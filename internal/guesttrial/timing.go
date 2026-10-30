package guesttrial

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// TimingStage is a closed vocabulary: no identifiers or speech can be recorded.
type TimingStage uint8

const (
	TimingASR TimingStage = iota
	TimingVerify
	TimingPipeline
)

type timingContextKey struct{}

// Timings belongs to one request and is never persisted or shared across users.
type Timings struct {
	mu     sync.Mutex
	values [3]int64
	seen   [3]bool
}

func WithTimings(ctx context.Context) (context.Context, *Timings) {
	t := &Timings{}
	return context.WithValue(ctx, timingContextKey{}, t), t
}

func RecordTiming(ctx context.Context, stage TimingStage, elapsed time.Duration) {
	if ctx == nil || stage > TimingPipeline || elapsed < 0 || elapsed > time.Minute {
		return
	}
	t, ok := ctx.Value(timingContextKey{}).(*Timings)
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen[stage] {
		return
	}
	t.values[stage], t.seen[stage] = elapsed.Milliseconds(), true
}

// ServerTiming returns only fixed names and bounded integer milliseconds.
// pipeline includes ledger access; asr and verify are subintervals, not additive.
func (t *Timings) ServerTiming() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var parts []string
	for stage, name := range []string{"asr", "verify", "pipeline"} {
		if t.seen[stage] {
			parts = append(parts, fmt.Sprintf("%s;dur=%d", name, t.values[stage]))
		}
	}
	return strings.Join(parts, ", ")
}
