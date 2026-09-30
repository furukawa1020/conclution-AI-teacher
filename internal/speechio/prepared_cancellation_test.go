package speechio

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

type cancellationAwarePreparedStream struct {
	ctx            context.Context
	blockInput     bool
	blocked        chan struct{}
	closeSendCalls atomic.Int32
	closeSendGate  <-chan struct{}
}

func (stream *cancellationAwarePreparedStream) Send(
	request *texttospeechpb.StreamingSynthesizeRequest,
) error {
	if request.GetInput() != nil && stream.blockInput {
		close(stream.blocked)
		<-stream.ctx.Done()
		return stream.ctx.Err()
	}
	return nil
}

func (stream *cancellationAwarePreparedStream) Recv() (
	*texttospeechpb.StreamingSynthesizeResponse,
	error,
) {
	close(stream.blocked)
	<-stream.ctx.Done()
	return nil, stream.ctx.Err()
}

func (stream *cancellationAwarePreparedStream) CloseSend() error {
	stream.closeSendCalls.Add(1)
	if stream.closeSendGate != nil {
		<-stream.closeSendGate
	}
	return nil
}

func prepareCancellationAwareStream(
	t *testing.T,
	ctx context.Context,
	stream *cancellationAwarePreparedStream,
) PreparedStreamingSynthesis {
	t.Helper()
	service := &CloudService{
		voiceName: "ja-JP-Chirp3-HD-Kore",
		streamSynthesizeCall: func(ctx context.Context) (streamingSynthesizeClient, error) {
			stream.ctx = ctx
			return stream, nil
		},
	}
	prepared, err := service.PrepareStreamingSynthesis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestPreparedStreamingSynthesisCloseCancelsActiveProvider(t *testing.T) {
	for _, test := range []struct {
		name           string
		blockInput     bool
		wantCloseSends int32
	}{
		{name: "input send", blockInput: true, wantCloseSends: 0},
		{name: "audio receive", wantCloseSends: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream := &cancellationAwarePreparedStream{
				blockInput: test.blockInput,
				blocked:    make(chan struct{}),
			}
			prepared := prepareCancellationAwareStream(t, ctx, stream)
			done := make(chan error, 1)
			go func() {
				_, err := prepared.StreamSynthesize("cancel this reply", func([]byte) error {
					return errors.New("canceled provider emitted unexpected audio")
				})
				done <- err
			}()
			select {
			case <-stream.blocked:
			case <-time.After(time.Second):
				t.Fatal("prepared provider did not reach blocking operation")
			}

			var closers sync.WaitGroup
			for range 8 {
				closers.Add(1)
				go func() {
					defer closers.Done()
					prepared.Close()
				}()
			}
			closed := make(chan struct{})
			go func() {
				closers.Wait()
				close(closed)
			}()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("concurrent Close calls waited for provider transport")
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("active synthesis error = %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Close did not cancel the active prepared provider")
			}
			if ctx.Err() != nil || stream.ctx.Err() != context.Canceled {
				t.Fatalf("parent error=%v provider error=%v", ctx.Err(), stream.ctx.Err())
			}
			if got := stream.closeSendCalls.Load(); got != test.wantCloseSends {
				t.Fatalf("CloseSend calls=%d, want %d; Close must not race provider Send", got, test.wantCloseSends)
			}
		})
	}
}

func TestPreparedStreamingSynthesisCloseDoesNotWaitForUnusedTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeSendGate := make(chan struct{})
	defer close(closeSendGate)
	stream := &cancellationAwarePreparedStream{closeSendGate: closeSendGate}
	prepared := prepareCancellationAwareStream(t, ctx, stream)
	closed := make(chan struct{})
	go func() {
		prepared.Close()
		prepared.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close waited for the unused provider's CloseSend")
	}
	if ctx.Err() != nil || stream.ctx.Err() != context.Canceled {
		t.Fatalf("parent error=%v provider error=%v", ctx.Err(), stream.ctx.Err())
	}
	if got := stream.closeSendCalls.Load(); got != 0 {
		t.Fatalf("CloseSend calls=%d, want cancellation without transport operations", got)
	}
	if _, err := prepared.StreamSynthesize("revived reply", func([]byte) error { return nil }); err == nil {
		t.Fatal("closed prepared stream was revived")
	}
}
