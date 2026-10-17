package nativeflow

import (
	"bytes"
	"testing"
)

func TestRepeatedCaptionKeepsOwnedBufferWithoutAllocation(t *testing.T) {
	current := []byte("same finalized caption")
	next := bytes.Clone(current)
	original := &current[0]
	allocs := testing.AllocsPerRun(1000, func() { current = mergeCaption(current, next) })
	if allocs != 0 || &current[0] != original || !bytes.Equal(current, next) {
		t.Fatalf("repeated caption allocated or replaced ownership: allocs=%v", allocs)
	}
	clear(next)
	if string(current) != "same finalized caption" {
		t.Fatal("caller input aliased retained caption")
	}
}

func TestCaptionMergeStillClearsReplacedBuffer(t *testing.T) {
	for _, next := range []string{"first extended", " second"} {
		current := []byte("first")
		incoming := []byte(next)
		merged := mergeCaption(current, incoming)
		want := next
		if next == " second" {
			want = "first second"
		}
		if string(merged) != want || string(incoming) != next {
			t.Fatal("merge content changed")
		}
		for _, value := range current {
			if value != 0 {
				t.Fatal("replaced buffer not cleared")
			}
		}
		clear(incoming)
		if string(merged) != want {
			t.Fatal("merge borrowed caller buffer")
		}
	}
}
