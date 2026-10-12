package speechio

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestEmptyPCMDoesNotConsumeMetadata(t *testing.T) {
	collector := newStreamingPCMCollector(1024)
	flight := &streamingSynthesisFlight{subscribers: 1}
	flight.changed = sync.NewCond(&flight.mu)
	for range 10000 {
		collector.add(nil)
		if err := flight.publish(nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(collector.chunks) != 0 || len(flight.chunks) != 0 {
		t.Fatal("empty frames retained metadata")
	}
	if _, ok := collector.complete(); ok {
		t.Fatal("empty collector completed")
	}
	input := []byte{80, 0}
	collector.add(input)
	if err := flight.publish(input); err != nil {
		t.Fatal(err)
	}
	input[0] = 0
	if collector.chunks[0][0] != 80 || flight.chunks[0][0] != 80 {
		t.Fatal("PCM ownership lost")
	}
}

func TestRejectedFlightPCMDoesNotAllocateCopies(t *testing.T) {
	pcm := make([]byte, maxStreamingPCMCacheEntryBytes)
	for _, completed := range []bool{false, true} {
		flight := &streamingSynthesisFlight{done: completed, subscribers: 1, buffered: maxStreamingPCMCacheEntryBytes}
		want := ErrStreamingAudioTooLarge
		if completed {
			want = context.Canceled
		}
		allocs := testing.AllocsPerRun(100, func() {
			if err := flight.publish(pcm); !errors.Is(err, want) {
				t.Fatalf("error=%v", err)
			}
		})
		if allocs != 0 {
			t.Fatalf("rejected PCM allocated %v copies", allocs)
		}
	}
}
