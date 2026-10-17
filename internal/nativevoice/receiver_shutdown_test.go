package nativevoice

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/genai"
)

type lateReceiveProvider struct {
	*fakeProviderSession
	cancel  context.CancelFunc
	message *genai.LiveServerMessage
	calls   int
}

func (p *lateReceiveProvider) Receive() (*genai.LiveServerMessage, error) {
	p.calls++
	if p.calls > 1 {
		return nil, errors.New("unexpected second receive")
	}
	p.cancel()
	return p.message, nil
}

func TestNativeReceiverStopsBeforeDecodeAfterSessionCancellation(t *testing.T) {
	for _, phase := range []string{"before", "empty", "audio"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pcm := []byte{80, 0}
			message := &genai.LiveServerMessage{}
			if phase == "audio" {
				message.ServerContent = &genai.LiveServerContent{ModelTurn: &genai.Content{Parts: []*genai.Part{{InlineData: &genai.Blob{MIMEType: "audio/pcm;rate=24000", Data: pcm}}}}}
			}
			provider := &lateReceiveProvider{fakeProviderSession: newFakeProviderSession(), cancel: cancel, message: message}
			s := &liveSession{ctx: ctx, cancel: cancel, raw: provider, notify: make(chan struct{}), config: Config{MaxOutputBytes: 1024, MaxOutputChunkBytes: 1024, MaxPendingBytes: 1024}}
			if phase == "before" {
				cancel()
			}
			s.receiverLoop()
			wantCalls := 1
			if phase == "before" {
				wantCalls = 0
			}
			if provider.calls != wantCalls || s.receiveOutputBytes != 0 || s.terminalErr == nil {
				t.Fatalf("calls=%d decoded=%d terminal=%v", provider.calls, s.receiveOutputBytes, s.terminalErr)
			}
			if phase == "audio" && (pcm[0] != 0 || message.ServerContent != nil) {
				t.Fatal("late provider audio was not scrubbed")
			}
			select {
			case <-provider.closed:
			default:
				t.Fatal("provider not closed")
			}
		})
	}
}
