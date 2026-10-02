package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	voiceLiveAudioWriteQueueCapacity   = 4
	voiceLiveControlWriteQueueCapacity = 8
	voiceLiveAudioQueueDeadline        = 100 * time.Millisecond
	voiceLiveSocketWriteTimeout        = 500 * time.Millisecond
)

var errVoiceLiveAudioQueueExpired = errors.New(
	"voice live audio frame exceeded its queue deadline",
)

var errVoiceLiveOutboundWriterClosed = errors.New("voice live outbound writer is closed")

type voiceLiveSocketWriteRequest struct {
	ctx         context.Context
	audio       bool
	enqueuedAt  time.Time
	messageType websocket.MessageType
	payload     []byte
	done        chan error
}

type voiceLiveOutboundWriterSnapshot struct {
	audioFrames             int64
	audioExpired            int64
	firstAudioQueueWait     time.Duration
	maximumAudioQueueWait   time.Duration
	maximumAudioWrite       time.Duration
	controlFrames           int64
	maximumControlQueueWait time.Duration
	maximumControlWrite     time.Duration
}

// voiceLiveOutboundWriter gives PCM its own bounded lane. Control traffic can
// never fill the audio queue, and an audio frame already waiting is selected
// before the next control frame. Each queued request owns its bytes: cancellation
// may return to a caller before an in-progress socket write releases the payload.
type voiceLiveOutboundWriter struct {
	audio              chan voiceLiveSocketWriteRequest
	control            chan voiceLiveSocketWriteRequest
	done               chan struct{}
	enqueueMu          sync.RWMutex
	write              func(context.Context, websocket.MessageType, []byte) error
	audioQueueDeadline time.Duration
	metricsMu          sync.Mutex
	metrics            voiceLiveOutboundWriterSnapshot
}

func newVoiceLiveOutboundWriter(
	ctx context.Context,
	conn *websocket.Conn,
) *voiceLiveOutboundWriter {
	return newVoiceLiveOutboundWriterWithWrite(ctx, conn.Write)
}

func newVoiceLiveOutboundWriterWithWrite(
	ctx context.Context,
	write func(context.Context, websocket.MessageType, []byte) error,
) *voiceLiveOutboundWriter {
	writer := &voiceLiveOutboundWriter{
		audio:              make(chan voiceLiveSocketWriteRequest, voiceLiveAudioWriteQueueCapacity),
		control:            make(chan voiceLiveSocketWriteRequest, voiceLiveControlWriteQueueCapacity),
		done:               make(chan struct{}),
		write:              write,
		audioQueueDeadline: voiceLiveAudioQueueDeadline,
	}
	go writer.run(ctx)
	return writer
}

func (writer *voiceLiveOutboundWriter) run(ctx context.Context) {
	var pendingControl *voiceLiveSocketWriteRequest
	// Cleanup runs only after an active socket write returns. Capture the final
	// pending control, not its initial nil value, before clearing owned copies.
	defer func() { writer.shutdown(pendingControl) }()
	for {
		if ctx.Err() != nil {
			return
		}
		var request voiceLiveSocketWriteRequest
		if pendingControl != nil {
			select {
			case request = <-writer.audio:
			default:
				request = *pendingControl
				pendingControl = nil
			}
		} else {
			select {
			case request = <-writer.audio:
			default:
				select {
				case <-ctx.Done():
					return
				case request = <-writer.audio:
				case control := <-writer.control:
					select {
					case request = <-writer.audio:
						pendingControl = &control
					default:
						request = control
					}
				}
			}
		}
		if err := ctx.Err(); err != nil {
			clear(request.payload)
			request.done <- err
			return
		}
		if err := request.ctx.Err(); err != nil {
			clear(request.payload)
			request.done <- err
			continue
		}
		dequeuedAt := time.Now()
		queueWait := dequeuedAt.Sub(request.enqueuedAt)
		if request.audio && queueWait > writer.audioQueueDeadline {
			writer.observe(request.audio, queueWait, 0, true)
			clear(request.payload)
			request.done <- errVoiceLiveAudioQueueExpired
			continue
		}
		writeCtx, cancel := context.WithTimeout(
			request.ctx,
			voiceLiveSocketWriteTimeout,
		)
		// Requests may have independent contexts. Writer cancellation also
		// cancels an active write, without clearing its bytes until it returns.
		stopWriterCancellation := context.AfterFunc(ctx, cancel)
		writeStartedAt := time.Now()
		err := ctx.Err()
		if err == nil {
			err = writer.write(writeCtx, request.messageType, request.payload)
		}
		writeDuration := time.Since(writeStartedAt)
		stopWriterCancellation()
		cancel()
		clear(request.payload)
		writer.observe(request.audio, queueWait, writeDuration, false)
		request.done <- err
	}
}

func (writer *voiceLiveOutboundWriter) shutdown(pendingControl *voiceLiveSocketWriteRequest) {
	// done signals admission closure, not completed zeroization. Close it
	// before taking the exclusive lock so full-queue submitters can unlock.
	close(writer.done)
	writer.enqueueMu.Lock()
	defer writer.enqueueMu.Unlock()
	discard := func(request voiceLiveSocketWriteRequest) {
		clear(request.payload)
		request.done <- errVoiceLiveOutboundWriterClosed
	}
	if pendingControl != nil {
		discard(*pendingControl)
	}
	for _, lane := range []chan voiceLiveSocketWriteRequest{writer.audio, writer.control} {
		for {
			select {
			case request := <-lane:
				discard(request)
			default:
				goto drained
			}
		}
	drained:
	}
}

func (writer *voiceLiveOutboundWriter) observe(
	audio bool,
	queueWait time.Duration,
	writeDuration time.Duration,
	expired bool,
) {
	writer.metricsMu.Lock()
	defer writer.metricsMu.Unlock()
	if audio {
		if writer.metrics.audioFrames == 0 {
			writer.metrics.firstAudioQueueWait = queueWait
		}
		writer.metrics.audioFrames++
		if expired {
			writer.metrics.audioExpired++
		}
		writer.metrics.maximumAudioQueueWait = max(
			writer.metrics.maximumAudioQueueWait,
			queueWait,
		)
		writer.metrics.maximumAudioWrite = max(
			writer.metrics.maximumAudioWrite,
			writeDuration,
		)
		return
	}
	writer.metrics.controlFrames++
	writer.metrics.maximumControlQueueWait = max(
		writer.metrics.maximumControlQueueWait,
		queueWait,
	)
	writer.metrics.maximumControlWrite = max(
		writer.metrics.maximumControlWrite,
		writeDuration,
	)
}

func (writer *voiceLiveOutboundWriter) snapshot() voiceLiveOutboundWriterSnapshot {
	if writer == nil {
		return voiceLiveOutboundWriterSnapshot{}
	}
	writer.metricsMu.Lock()
	defer writer.metricsMu.Unlock()
	return writer.metrics
}

func (writer *voiceLiveOutboundWriter) submit(
	ctx context.Context,
	lane chan voiceLiveSocketWriteRequest,
	messageType websocket.MessageType,
	payload []byte,
) error {
	if ctx == nil || len(payload) == 0 {
		return errors.New("voice live outbound frame is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Only admission holds the read lock. Shutdown closes done first, joins
	// every racing enqueue, then drains; no submit can leave a copy afterward.
	writer.enqueueMu.RLock()
	select {
	case <-writer.done:
		writer.enqueueMu.RUnlock()
		return errVoiceLiveOutboundWriterClosed
	default:
	}
	request := voiceLiveSocketWriteRequest{
		ctx:         ctx,
		audio:       lane == writer.audio,
		enqueuedAt:  time.Now(),
		messageType: messageType,
		payload:     bytes.Clone(payload),
		done:        make(chan error, 1),
	}
	select {
	case lane <- request:
		writer.enqueueMu.RUnlock()
	case <-ctx.Done():
		clear(request.payload)
		writer.enqueueMu.RUnlock()
		return ctx.Err()
	case <-writer.done:
		clear(request.payload)
		writer.enqueueMu.RUnlock()
		return errVoiceLiveOutboundWriterClosed
	}
	select {
	case err := <-request.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-writer.done:
		return errVoiceLiveOutboundWriterClosed
	}
}

func (writer *voiceLiveOutboundWriter) writeAudio(
	ctx context.Context,
	payload []byte,
) error {
	return writer.submit(ctx, writer.audio, websocket.MessageBinary, payload)
}

func (writer *voiceLiveOutboundWriter) writeJSON(
	ctx context.Context,
	frame voiceLiveOutboundFrame,
) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return writer.submit(ctx, writer.control, websocket.MessageText, payload)
}
