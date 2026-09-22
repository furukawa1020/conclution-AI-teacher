package httpapi

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestWriteVoiceLivePCM20msUsesOrderedZeroCopyFrames(t *testing.T) {
	t.Parallel()

	audio := make([]byte, voiceLiveOutputPCMFrameBytes*2+100)
	for index := range audio {
		audio[index] = byte(index % 251)
	}
	var frames [][]byte
	err := writeVoiceLivePCM20ms(
		context.Background(),
		audio,
		func(frame []byte) error {
			offset := 0
			for _, prior := range frames {
				offset += len(prior)
			}
			if &frame[0] != &audio[offset] {
				t.Fatal("PCM frame was copied before WebSocket publication")
			}
			frames = append(frames, frame)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("frames = %d; want 3", len(frames))
	}
	if len(frames[0]) != voiceLiveOutputPCMFrameBytes ||
		len(frames[1]) != voiceLiveOutputPCMFrameBytes ||
		len(frames[2]) != 100 {
		t.Fatalf("frame lengths = %d, %d, %d",
			len(frames[0]), len(frames[1]), len(frames[2]))
	}
	if !bytes.Equal(bytes.Join(frames, nil), audio) {
		t.Fatal("20 ms framing changed PCM order or content")
	}
}

func TestWriteVoiceLivePCM20msChecksCancellationBetweenFrames(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	audio := make([]byte, voiceLiveOutputPCMFrameBytes*3)
	writes := 0
	err := writeVoiceLivePCM20ms(ctx, audio, func([]byte) error {
		writes++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context cancellation", err)
	}
	if writes != 1 {
		t.Fatalf("writes after cancellation = %d; want 1", writes)
	}
}

func TestWriteVoiceLivePCM20msRejectsInvalidInputAndStopsOnWriteError(t *testing.T) {
	t.Parallel()

	if err := writeVoiceLivePCM20ms(
		context.Background(),
		[]byte{1},
		func([]byte) error { return nil },
	); err == nil {
		t.Fatal("odd PCM input was accepted")
	}

	writeError := errors.New("slow consumer")
	writes := 0
	err := writeVoiceLivePCM20ms(
		context.Background(),
		make([]byte, voiceLiveOutputPCMFrameBytes*2),
		func([]byte) error {
			writes++
			return writeError
		},
	)
	if !errors.Is(err, writeError) {
		t.Fatalf("error = %v; want write error", err)
	}
	if writes != 1 {
		t.Fatalf("writes after failure = %d; want 1", writes)
	}
}
