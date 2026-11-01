package nativevoice

import (
	"context"
	"errors"
	"google.golang.org/genai"
	"testing"
	"time"
)

type cancellingWriteProvider struct {
	*fakeProviderSession
	cancel context.CancelFunc
}

func (p *cancellingWriteProvider) SendRealtimeInput(genai.LiveRealtimeInput) error {
	p.cancel()
	return nil
}

func TestWriterCannotPublishSuccessAfterCancellation(t *testing.T) {
	for _, cancelSession := range []bool{false, true} {
		sessionCtx, stopSession := context.WithCancel(context.Background())
		requestCtx, stopRequest := context.WithCancel(context.Background())
		cancel := stopRequest
		if cancelSession {
			cancel = stopSession
		}
		provider := &cancellingWriteProvider{fakeProviderSession: newFakeProviderSession(), cancel: cancel}
		session := &liveSession{ctx: sessionCtx, cancel: stopSession, raw: provider, writeCh: make(chan writeRequest), notify: make(chan struct{})}
		done := make(chan struct{})
		go func() { defer close(done); session.writerLoop() }()
		pcm := []byte{1, 2}
		result := make(chan error, 1)
		session.writeCh <- writeRequest{ctx: requestCtx, input: genai.LiveRealtimeInput{Audio: &genai.Blob{Data: pcm}}, result: result}
		select {
		case err := <-result:
			if !errors.Is(err, ErrClosed) {
				t.Error("cancelled write reported success", err)
			}
		case <-time.After(time.Second):
			t.Error("write did not finish")
		}
		stopRequest()
		stopSession()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("writer did not exit")
		}
		if !allZero(pcm) {
			t.Fatal("write retained PCM")
		}
	}
}
