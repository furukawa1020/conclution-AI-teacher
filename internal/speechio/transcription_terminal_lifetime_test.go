package speechio

import (
	"context"
	"errors"
	"io"
	"testing"

	"cloud.google.com/go/speech/apiv2/speechpb"
)

type cancelEOFRecognitionStream struct {
	fakeStreamingRecognizeClient
	cancel context.CancelFunc
}

func (s *cancelEOFRecognitionStream) Recv() (*speechpb.StreamingRecognizeResponse, error) {
	r, err := s.fakeStreamingRecognizeClient.Recv()
	if errors.Is(err, io.EOF) && s.cancel != nil {
		s.cancel()
	}
	return r, err
}

func TestTranscriptionTerminalEOFHonorsCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal EOF", true: "canceled during EOF"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream := &cancelEOFRecognitionStream{}
			want := error(io.EOF)
			if canceled {
				stream.cancel = cancel
				want = context.Canceled
			}
			session, err := streamingTestService(stream, "short").OpenStreamingTranscription(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				event, err := session.RecvEvent()
				if !errors.Is(err, want) || event != (StreamingTranscriptionEvent{}) {
					t.Errorf("terminal event=%+v error=%v want=%v", event, err, want)
				}
			}
			if stream.recvCalls != 1 {
				t.Fatalf("terminal stream read again: %d", stream.recvCalls)
			}
		})
	}
}

func TestTranscriptionPendingReleasesConsumedAndCanceledReferences(t *testing.T) {
	for _, cancelTail := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain", true: "cancel tail"}[cancelTail], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream := &fakeStreamingRecognizeClient{recv: []streamingRecognizeReceive{{
				response: &speechpb.StreamingRecognizeResponse{Results: []*speechpb.StreamingRecognitionResult{
					streamingTestResult("first", true, 0, .9),
					streamingTestResult("second", true, 0, .8),
					streamingTestResult("third", true, 0, .7),
				}},
			}}}
			opened, err := streamingTestService(stream, "short").OpenStreamingTranscription(ctx)
			if err != nil {
				t.Fatal(err)
			}
			session := opened.(*cloudStreamingTranscriptionSession)
			first, err := session.RecvEvent()
			if err != nil || first.Text != "first" || len(session.pending) != 2 {
				t.Fatalf("initial event=%+v err=%v pending=%d", first, err, len(session.pending))
			}
			backing := session.pending
			second, err := session.RecvEvent()
			if err != nil || second.Text != "second" {
				t.Fatalf("second event=%+v err=%v", second, err)
			}
			if backing[0] != (StreamingTranscriptionEvent{}) {
				t.Error("delivered event still referenced by pending backing array")
			}
			if cancelTail {
				cancel()
				if _, err := session.RecvEvent(); !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled tail returned %v", err)
				}
			} else {
				third, err := session.RecvEvent()
				if err != nil || third.Text != "third" {
					t.Fatalf("third event=%+v err=%v", third, err)
				}
			}
			for _, slot := range backing {
				if slot != (StreamingTranscriptionEvent{}) {
					t.Error("drained or canceled event reference retained")
				}
			}
			if session.pending != nil || first.Text != "first" || second.Text != "second" {
				t.Error("queue retained or returned copies changed")
			}
		})
	}
}
