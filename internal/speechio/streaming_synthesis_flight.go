package speechio

import (
	"context"
	"sync"
	"time"
)

const streamingSynthesisFlightTimeout = 60 * time.Second

type streamingSynthesisFlightResult struct {
	mimeType string
	err      error
}

// streamingSynthesisFlight is a short-lived, in-process PCM publication log.
// It contains no text or identity. Late subscribers replay immutable chunks
// from index zero, then follow new chunks until the shared provider call ends.
type streamingSynthesisFlight struct {
	mu      sync.Mutex
	changed *sync.Cond

	chunks      [][]byte
	buffered    int
	done        bool
	result      streamingSynthesisFlightResult
	subscribers int
	cancel      context.CancelFunc
}

type streamingSynthesisFlightGroup struct {
	mu      sync.Mutex
	flights map[streamingPCMCacheKey]*streamingSynthesisFlight
}

func (group *streamingSynthesisFlightGroup) stream(
	ctx context.Context,
	key streamingPCMCacheKey,
	produce func(context.Context, StreamChunkHandler) (string, error),
	onChunk StreamChunkHandler,
) (string, error) {
	group.mu.Lock()
	if group.flights == nil {
		group.flights = make(map[streamingPCMCacheKey]*streamingSynthesisFlight)
	}
	flight := group.flights[key]
	if flight != nil {
		flight.mu.Lock()
		abandoned := !flight.done && flight.subscribers == 0 && flight.cancel == nil
		flight.mu.Unlock()
		if abandoned {
			flight = nil
		}
	}
	owner := flight == nil
	var providerCtx context.Context
	if owner {
		var cancel context.CancelFunc
		providerCtx, cancel = context.WithTimeout(
			context.WithoutCancel(ctx),
			streamingSynthesisFlightTimeout,
		)
		flight = &streamingSynthesisFlight{cancel: cancel}
		flight.changed = sync.NewCond(&flight.mu)
		group.flights[key] = flight
	}
	flight.mu.Lock()
	flight.subscribers++
	flight.mu.Unlock()
	group.mu.Unlock()

	if owner {
		go func() {
			mimeType, err := produce(providerCtx, flight.publish)
			flight.complete(mimeType, err)
			group.mu.Lock()
			if group.flights[key] == flight {
				delete(group.flights, key)
			}
			group.mu.Unlock()
		}()
	}
	return flight.consume(ctx, onChunk)
}

func (flight *streamingSynthesisFlight) publish(chunk []byte) error {
	cloned := append([]byte(nil), chunk...)
	flight.mu.Lock()
	defer flight.mu.Unlock()
	if flight.done {
		return context.Canceled
	}
	if len(cloned) > maxStreamingPCMCacheEntryBytes-flight.buffered {
		return ErrStreamingAudioTooLarge
	}
	flight.chunks = append(flight.chunks, cloned)
	flight.buffered += len(cloned)
	flight.changed.Broadcast()
	return nil
}

func (flight *streamingSynthesisFlight) complete(mimeType string, err error) {
	flight.mu.Lock()
	if !flight.done {
		flight.done = true
		flight.result = streamingSynthesisFlightResult{mimeType: mimeType, err: err}
		if flight.cancel != nil {
			flight.cancel()
			flight.cancel = nil
		}
		flight.changed.Broadcast()
	}
	flight.mu.Unlock()
}

func (flight *streamingSynthesisFlight) consume(
	ctx context.Context,
	onChunk StreamChunkHandler,
) (string, error) {
	stopWake := context.AfterFunc(ctx, func() {
		flight.mu.Lock()
		flight.changed.Broadcast()
		flight.mu.Unlock()
	})
	defer stopWake()
	defer flight.unsubscribe()

	index := 0
	for {
		flight.mu.Lock()
		for index >= len(flight.chunks) && !flight.done && ctx.Err() == nil {
			flight.changed.Wait()
		}
		if err := ctx.Err(); err != nil {
			flight.mu.Unlock()
			return "", err
		}
		if index < len(flight.chunks) {
			chunk := append([]byte(nil), flight.chunks[index]...)
			index++
			flight.mu.Unlock()
			if err := onChunk(chunk); err != nil {
				return "", err
			}
			continue
		}
		result := flight.result
		flight.mu.Unlock()
		return result.mimeType, result.err
	}
}

func (flight *streamingSynthesisFlight) unsubscribe() {
	flight.mu.Lock()
	if flight.subscribers > 0 {
		flight.subscribers--
	}
	if flight.subscribers == 0 && !flight.done && flight.cancel != nil {
		flight.cancel()
		flight.cancel = nil
	}
	flight.mu.Unlock()
}
