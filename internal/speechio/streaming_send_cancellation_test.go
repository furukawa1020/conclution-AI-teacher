package speechio

import (
	"cloud.google.com/go/speech/apiv2/speechpb"
	"context"
	"errors"
	"testing"
)

type cancellingRecognitionWriter struct {
	*fakeStreamingRecognizeClient
	cancel context.CancelFunc
}

func (f *cancellingRecognitionWriter) Send(*speechpb.StreamingRecognizeRequest) error {
	f.cancel()
	return nil
}
func (f *cancellingRecognitionWriter) CloseSend() error { f.cancel(); return nil }

func TestStreamingRecognitionWriteCannotCompleteAfterCancel(t *testing.T) {
	for _, closing := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		stream := &cancellingRecognitionWriter{fakeStreamingRecognizeClient: &fakeStreamingRecognizeClient{}, cancel: cancel}
		session := &cloudStreamingTranscriptionSession{ctx: ctx, cancel: cancel, stream: stream}
		var err error
		if closing {
			err = session.CloseSend()
		} else {
			err = session.SendPCM([]byte{1, 2})
		}
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatal("late success accepted", err)
		}
		if !errors.Is(session.CloseSend(), context.Canceled) {
			t.Fatal("close lost terminal result")
		}
		if !errors.Is(session.SendPCM([]byte{1, 2}), ErrStreamingTranscriptionClosed) {
			t.Fatal("closed sender accepted audio")
		}
	}
}
