package nativevoice

import (
	"context"
	"errors"
	"google.golang.org/genai"
	"testing"
)

func TestReceiveErrorScrubsRejectedContent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pcm := []byte{1, 2, 3, 4}
	message := &genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{
		ModelTurn:          &genai.Content{Parts: []*genai.Part{{InlineData: &genai.Blob{Data: pcm}}}},
		InputTranscription: &genai.Transcription{Text: "private transcript"},
	}}
	provider := setupResultProvider{ProviderSession: newFakeProviderSession(), receive: func() (*genai.LiveServerMessage, error) { return message, errors.New("private provider detail") }}
	session := &liveSession{ctx: ctx, cancel: cancel, raw: provider, notify: make(chan struct{})}
	session.receiverLoop()
	if message.ServerContent != nil || !allZero(pcm) {
		t.Fatal("failed receive retained content")
	}
	if !errors.Is(session.terminalErr, ErrProvider) {
		t.Fatal("missing terminal provider error", session.terminalErr)
	}
}
