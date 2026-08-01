package httpapi

import (
	"errors"
	"sync"
)

// strictAudioBuffer keeps synthesized response audio behind the strict-mode
// result-validation boundary. It never contains microphone input. Every copy
// is zeroed after release or rejection.
type strictAudioBuffer struct {
	mu        sync.Mutex
	committed bool
	chunks    [][]byte
	bytes     int
}

func (buffer *strictAudioBuffer) markCommitted() {
	buffer.mu.Lock()
	buffer.committed = true
	buffer.mu.Unlock()
}

func (buffer *strictAudioBuffer) append(audio []byte) error {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if !buffer.committed ||
		len(audio) == 0 ||
		len(audio)%2 != 0 ||
		len(audio) > voiceStreamMaxChunkBytes ||
		len(buffer.chunks) >= voiceStreamMaxChunks ||
		len(audio) > voiceStreamMaxAudioBytes-buffer.bytes {
		return errors.New("strict synthesized audio is outside bounds")
	}
	chunk := append([]byte(nil), audio...)
	buffer.chunks = append(buffer.chunks, chunk)
	buffer.bytes += len(chunk)
	return nil
}

func (buffer *strictAudioBuffer) spoke() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return len(buffer.chunks) > 0
}

func (buffer *strictAudioBuffer) release(deliver func([]byte) error) error {
	if deliver == nil {
		buffer.clear()
		return errors.New("strict audio deliverer is required")
	}
	buffer.mu.Lock()
	chunks := buffer.chunks
	buffer.chunks = nil
	buffer.bytes = 0
	buffer.mu.Unlock()
	defer clearAudioChunks(chunks)
	for _, chunk := range chunks {
		if err := deliver(chunk); err != nil {
			return err
		}
	}
	return nil
}

func (buffer *strictAudioBuffer) clear() {
	buffer.mu.Lock()
	chunks := buffer.chunks
	buffer.chunks = nil
	buffer.bytes = 0
	buffer.mu.Unlock()
	clearAudioChunks(chunks)
}

func clearAudioChunks(chunks [][]byte) {
	for _, chunk := range chunks {
		clear(chunk)
	}
}
