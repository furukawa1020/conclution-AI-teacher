package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/coder/websocket"
)

const (
	voiceLiveAudioWriteQueueCapacity   = 4
	voiceLiveControlWriteQueueCapacity = 8
	voiceLiveSocketWriteTimeout        = 500 * time.Millisecond
)

type voiceLiveSocketWriteRequest struct {
	ctx         context.Context
	messageType websocket.MessageType
	payload     []byte
	done        chan error
}

// voiceLiveOutboundWriter gives PCM its own bounded lane. Control traffic can
// never fill the audio queue, and an audio frame already waiting is selected
// before the next control frame. Callers remain synchronous so borrowed PCM
// stays valid until websocket.Write has snapshotted it.
type voiceLiveOutboundWriter struct {
	audio   chan voiceLiveSocketWriteRequest
	control chan voiceLiveSocketWriteRequest
	done    chan struct{}
	write   func(context.Context, websocket.MessageType, []byte) error
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
		audio:   make(chan voiceLiveSocketWriteRequest, voiceLiveAudioWriteQueueCapacity),
		control: make(chan voiceLiveSocketWriteRequest, voiceLiveControlWriteQueueCapacity),
		done:    make(chan struct{}),
		write:   write,
	}
	go writer.run(ctx)
	return writer
}

func (writer *voiceLiveOutboundWriter) run(ctx context.Context) {
	defer close(writer.done)
	var pendingControl *voiceLiveSocketWriteRequest
	for {
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
		writeCtx, cancel := context.WithTimeout(
			request.ctx,
			voiceLiveSocketWriteTimeout,
		)
		err := writer.write(writeCtx, request.messageType, request.payload)
		cancel()
		request.done <- err
	}
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
	request := voiceLiveSocketWriteRequest{
		ctx:         ctx,
		messageType: messageType,
		payload:     payload,
		done:        make(chan error, 1),
	}
	select {
	case lane <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-writer.done:
		return errors.New("voice live outbound writer is closed")
	}
	select {
	case err := <-request.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-writer.done:
		return errors.New("voice live outbound writer is closed")
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
