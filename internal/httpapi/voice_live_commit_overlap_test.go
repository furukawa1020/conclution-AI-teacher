package httpapi

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestVoiceLiveCommitOverlapsInputEndWithAcknowledgement(t *testing.T) {
	for _, mode := range []string{
		"ordinary", "strict", "coach", "ack_failure", "cancelled",
		"strict_ack_failure", "strict_cancelled", "coach_ack_failure", "coach_cancelled",
	} {
		t.Run(mode, func(t *testing.T) {
			strict := strings.HasPrefix(mode, "strict")
			coach := strings.HasPrefix(mode, "coach")
			ackFails := strings.HasSuffix(mode, "ack_failure")
			cancelDuringAck := strings.HasSuffix(mode, "cancelled")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ackStarted := make(chan struct{})
			releaseAck := make(chan struct{})
			var released bool
			defer func() {
				if !released {
					close(releaseAck)
				}
			}()
			writeOrder := make(chan string, 4)
			ackFailure := errors.New("stopped acknowledgement")
			writer := newVoiceLiveOutboundWriterWithWrite(ctx, func(
				writeCtx context.Context, kind websocket.MessageType, payload []byte,
			) error {
				if kind == websocket.MessageBinary {
					writeOrder <- "pcm"
					return nil
				}
				if string(payload) == `{"type":"coach","version":1}` {
					writeOrder <- "coach"
					return nil
				}
				close(ackStarted)
				select {
				case <-releaseAck:
				case <-writeCtx.Done():
					return writeCtx.Err()
				}
				if err := writeCtx.Err(); err != nil {
					return err
				}
				if ackFails {
					return ackFailure
				}
				writeOrder <- "committed"
				return nil
			})
			metrics := &voiceLiveOutputMetrics{writer: writer}
			strictOutput := &strictAudioBuffer{}
			defer strictOutput.clear()
			gate := &voiceLiveCoachOutputGate{}
			audioInput := make(chan []byte)
			inputEnded := make(chan struct{})
			responseDone := make(chan error, 1)
			go func() {
				// This is the provider's existing input-drain goroutine: its next
				// step is ActivityEnd, independent of browser output writes.
				for pcm := range audioInput {
					clear(pcm)
				}
				close(inputEnded)
				if coach {
					if err := gate.beginCheckpoint(); err != nil {
						responseDone <- err
						return
					}
					gate.mu.Lock()
					if gate.rejected {
						gate.mu.Unlock()
						responseDone <- errors.New("checkpoint was rejected")
						return
					}
					err := writer.writeJSON(ctx, voiceLiveOutboundFrame{Type: "coach", Version: 1})
					gate.accepted = err == nil
					gate.mu.Unlock()
					if err != nil {
						responseDone <- err
						return
					}
				}
				responseDone <- gate.deliver(func() error {
					if strict {
						return strictOutput.append([]byte{64, 0})
					}
					return metrics.deliver(ctx, nil, []byte{64, 0})
				})
			}()
			commitDone := make(chan error, 1)
			var inputClosed atomic.Bool
			defer func() {
				if inputClosed.CompareAndSwap(false, true) {
					close(audioInput)
				}
			}()
			go func() {
				commitDone <- gate.commitInput(metrics, strictOutput, func() bool {
					if inputClosed.CompareAndSwap(false, true) {
						close(audioInput)
					}
					return true
				}, func() error {
					return writer.writeJSON(ctx, voiceLiveOutboundFrame{Type: "committed", Version: 1})
				}, cancel)
			}()
			select {
			case <-ackStarted:
			case <-ctx.Done():
				t.Fatal("acknowledgement did not start")
			}
			if !inputClosed.Load() {
				t.Fatal("input close waited for the browser acknowledgement write")
			}
			select {
			case <-inputEnded:
			case <-ctx.Done():
				t.Fatal("provider input drain waited for the browser acknowledgement write")
			}
			if gate.mu.TryLock() {
				gate.mu.Unlock()
				t.Fatal("response boundary was not held during acknowledgement")
			}
			if len(writeOrder) != 0 || len(writer.audio) != 0 || len(writer.control) != 0 {
				t.Fatal("response entered the writer before the acknowledgement completed")
			}
			if _, frames, _ := metrics.snapshot(); frames != 0 {
				t.Fatal("response audio escaped the acknowledgement barrier")
			}
			if cancelDuringAck {
				cancel()
			}
			close(releaseAck)
			released = true
			commitErr := <-commitDone
			responseErr := <-responseDone
			if ackFails || cancelDuringAck {
				if commitErr == nil || responseErr == nil || ctx.Err() == nil {
					t.Fatalf("failed acknowledgement released response: commit=%v response=%v context=%v", commitErr, responseErr, ctx.Err())
				}
				if len(writeOrder) != 0 {
					t.Fatal("failed acknowledgement wrote response output")
				}
				if strictOutput.spoke() {
					t.Fatal("failed acknowledgement retained strict audio")
				}
			} else {
				if commitErr != nil || responseErr != nil {
					t.Fatalf("commit=%v response=%v", commitErr, responseErr)
				}
				if got := <-writeOrder; got != "committed" {
					t.Fatalf("first write=%q", got)
				}
				if coach {
					if got := <-writeOrder; got != "coach" {
						t.Fatalf("checkpoint write=%q", got)
					}
				}
				if strict {
					if len(writeOrder) != 0 || !strictOutput.spoke() {
						t.Fatal("strict audio did not remain behind final validation")
					}
					if err := strictOutput.release(func(pcm []byte) error {
						return metrics.deliver(ctx, nil, pcm)
					}); err != nil {
						t.Fatal(err)
					}
				}
				if got := <-writeOrder; got != "pcm" {
					t.Fatalf("response write=%q", got)
				}
			}
			cancel()
			<-writer.done
		})
	}
}

func TestVoiceLiveCommitInputReaderFailureReleasesNoOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := &voiceLiveCoachOutputGate{}
	metrics := &voiceLiveOutputMetrics{}
	strictOutput := &strictAudioBuffer{}
	ackWrites := 0
	err := gate.commitInput(metrics, strictOutput, func() bool { return false }, func() error {
		ackWrites++
		return nil
	}, cancel)
	if err == nil || ctx.Err() == nil || ackWrites != 0 {
		t.Fatalf("failed reader was acknowledged: error=%v context=%v ack=%d", err, ctx.Err(), ackWrites)
	}
	if err := gate.deliver(func() error { t.Fatal("failed reader released output"); return nil }); err == nil {
		t.Fatal("failed reader did not reject response")
	}
}

func TestVoiceLiveCommitWithoutLatencyProofClosesInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := &voiceLiveCoachOutputGate{}
	metrics := &voiceLiveOutputMetrics{}
	strictOutput := &strictAudioBuffer{}
	closed := 0
	if err := gate.commitInput(metrics, strictOutput, func() bool { closed++; return true }, nil, cancel); err != nil {
		t.Fatal(err)
	}
	if closed != 1 || ctx.Err() != nil || !metrics.committed || !strictOutput.committed {
		t.Fatal("legacy commit did not publish its input and output boundaries")
	}
}
