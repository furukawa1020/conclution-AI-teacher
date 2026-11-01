package nativevoice

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/genai"
)

type setupResultProvider struct {
	ProviderSession
	receive func() (*genai.LiveServerMessage, error)
}

func (p setupResultProvider) Receive() (*genai.LiveServerMessage, error) { return p.receive() }

func TestReceiveSetupAlwaysScrubsProviderContent(t *testing.T) {
	for _, providerError := range []error{nil, errors.New("private provider detail")} {
		pcm := []byte{1, 2, 3, 4}
		message := &genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{
			ModelTurn: &genai.Content{Parts: []*genai.Part{{InlineData: &genai.Blob{Data: pcm}}}},
		}}
		err := receiveSetup(setupResultProvider{receive: func() (*genai.LiveServerMessage, error) { return message, providerError }})
		expected := ErrProtocol
		if providerError != nil {
			expected = ErrProvider
		}
		if !errors.Is(err, expected) || message.ServerContent != nil || !allZero(pcm) {
			t.Fatal("setup content retained", err)
		}
	}
}

func TestAwaitSetupDoesNotReceiveAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := awaitSetupComplete(ctx, setupResultProvider{receive: func() (*genai.LiveServerMessage, error) {
		t.Error("cancelled setup received from provider")
		return nil, nil
	}})
	if !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestAwaitSetupCannotAcceptCompletionAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := setupResultProvider{ProviderSession: newFakeProviderSession(), receive: func() (*genai.LiveServerMessage, error) {
		cancel()
		return &genai.LiveServerMessage{SetupComplete: &genai.LiveServerSetupComplete{}}, nil
	}}
	if err := awaitSetupComplete(ctx, provider); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
