package voiceflow

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type fakePreparedSynthesis struct {
	mu sync.Mutex

	ctx    context.Context
	chunks [][]byte
	calls  int
	text   string
	closed bool
}

func (prepared *fakePreparedSynthesis) StreamSynthesize(
	text string,
	onChunk speechio.StreamChunkHandler,
) (string, error) {
	prepared.mu.Lock()
	if prepared.ctx != nil && prepared.ctx.Err() != nil {
		err := prepared.ctx.Err()
		prepared.mu.Unlock()
		return "", err
	}
	prepared.calls++
	prepared.text = text
	chunks := append([][]byte(nil), prepared.chunks...)
	prepared.mu.Unlock()
	for _, chunk := range chunks {
		if err := onChunk(chunk); err != nil {
			return "", err
		}
	}
	return speechio.StreamingAudioContentType, nil
}

func (prepared *fakePreparedSynthesis) Close() {
	prepared.mu.Lock()
	prepared.closed = true
	prepared.mu.Unlock()
}

type preparingStreamingSpeech struct {
	fakeStreamingSpeech

	prepared        *fakePreparedSynthesis
	prepareStarted  chan struct{}
	prepareCanceled chan struct{}
	blockPrepare    bool
	startOnce       sync.Once
	cancelOnce      sync.Once
}

func (speech *preparingStreamingSpeech) PrepareStreamingSynthesis(
	ctx context.Context,
) (speechio.PreparedStreamingSynthesis, error) {
	if speech.prepareStarted != nil {
		speech.startOnce.Do(func() { close(speech.prepareStarted) })
	}
	if speech.blockPrepare {
		<-ctx.Done()
		if speech.prepareCanceled != nil {
			speech.cancelOnce.Do(func() { close(speech.prepareCanceled) })
		}
		return nil, ctx.Err()
	}
	speech.prepared.mu.Lock()
	speech.prepared.ctx = ctx
	speech.prepared.mu.Unlock()
	return speech.prepared, nil
}

func TestPreparedSynthesisUsesAnAlreadyReadyConnection(t *testing.T) {
	prepared := &fakePreparedSynthesis{chunks: [][]byte{{1, 0}, {2, 0}}}
	speech := &preparingStreamingSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{chunks: [][]byte{{9, 0}}},
		prepared:            prepared,
		prepareStarted:      make(chan struct{}),
	}
	preparation := startSpeculativeSynthesisPreparation(context.Background(), speech)
	if preparation == nil {
		t.Fatal("streaming preparer was not detected")
	}
	select {
	case <-speech.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("preparation did not start")
	}
	var ready speechio.PreparedStreamingSynthesis
	deadline := time.Now().Add(time.Second)
	for ready == nil && time.Now().Before(deadline) {
		ready = preparation.takeReady()
		time.Sleep(time.Millisecond)
	}
	if ready == nil {
		t.Fatal("prepared synthesis did not become ready")
	}
	preparation.close()

	var delivered []byte
	synthesis := startSpeculativeSynthesis(
		context.Background(),
		speech,
		ready,
		"監査済みの返答",
		func(chunk []byte) error {
			delivered = append(delivered, chunk...)
			return nil
		},
	)
	result := synthesis.await(context.Background())
	if result.err != nil {
		t.Fatalf("prepared synthesis: %v", result.err)
	}
	if _, err := synthesis.buffer.release(context.Background()); err != nil {
		t.Fatalf("release prepared synthesis: %v", err)
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.calls != 1 || prepared.text != "監査済みの返答" ||
		!prepared.closed || speech.streamCalls != 0 ||
		!bytes.Equal(delivered, []byte{1, 0, 2, 0}) {
		t.Fatalf(
			"prepared calls=%d text=%q closed=%t fallback=%d delivered=%v",
			prepared.calls,
			prepared.text,
			prepared.closed,
			speech.streamCalls,
			delivered,
		)
	}
}

func TestSpeculativeSynthesisCountsOnlyMeaningfulPCMAsFirstChunk(t *testing.T) {
	speech := &fakeStreamingSpeech{chunks: [][]byte{
		{0, 0, 0, 0},
		{32, 0},
		{40, 0},
	}}
	synthesis := startSpeculativeSynthesis(
		context.Background(),
		speech,
		nil,
		"短い確認です",
		func([]byte) error { return nil },
	)
	result := synthesis.await(context.Background())
	if result.err != nil {
		t.Fatalf("speculative synthesis: %v", result.err)
	}
	if got := synthesis.firstChunkMS(); got < 0 {
		t.Fatalf("meaningful first chunk ms=%d", got)
	}

	silent := startSpeculativeSynthesis(
		context.Background(),
		&fakeStreamingSpeech{chunks: [][]byte{{0, 0}, {32, 0}}},
		nil,
		"無音では開始しません",
		func([]byte) error { return nil },
	)
	if result := silent.await(context.Background()); result.err != nil {
		t.Fatalf("silent synthesis: %v", result.err)
	}
	if got := silent.firstChunkMS(); got != -1 {
		t.Fatalf("silent first chunk ms=%d want=-1", got)
	}
}

func TestPreparedSynthesisNeverWaitsForASlowConnection(t *testing.T) {
	speech := &preparingStreamingSpeech{
		prepareStarted:  make(chan struct{}),
		prepareCanceled: make(chan struct{}),
		blockPrepare:    true,
	}
	preparation := startSpeculativeSynthesisPreparation(context.Background(), speech)
	select {
	case <-speech.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("preparation did not start")
	}
	started := time.Now()
	if prepared := preparation.takeReady(); prepared != nil {
		t.Fatal("blocked preparation unexpectedly became ready")
	}
	if elapsed := time.Since(started); elapsed > 20*time.Millisecond {
		t.Fatalf("nonblocking readiness check took %v", elapsed)
	}
	preparation.close()
	select {
	case <-speech.prepareCanceled:
	case <-time.After(time.Second):
		t.Fatal("unused preparation was not canceled")
	}
}
