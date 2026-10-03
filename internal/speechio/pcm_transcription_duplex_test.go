package speechio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"cloud.google.com/go/speech/apiv2/speechpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A replay fixture may emit intermediate/final results during upload, but a
// successful terminal EOF must wait for the complete input half-close.
type pcmEOFJoinedStream struct {
	streamingRecognizeClient
	ctx    context.Context
	closed chan struct{}
}

func (s *pcmEOFJoinedStream) Recv() (*speechpb.StreamingRecognizeResponse, error) {
	response, err := s.streamingRecognizeClient.Recv()
	if errors.Is(err, io.EOF) {
		select {
		case <-s.closed:
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		}
	}
	return response, err
}

func (s *pcmEOFJoinedStream) CloseSend() error {
	err := s.streamingRecognizeClient.CloseSend()
	close(s.closed)
	return err
}

type duplexRecognitionStream struct {
	send      func(*speechpb.StreamingRecognizeRequest) error
	recv      func() (*speechpb.StreamingRecognizeResponse, error)
	closeSend func() error
}

func (s *duplexRecognitionStream) Send(r *speechpb.StreamingRecognizeRequest) error { return s.send(r) }
func (s *duplexRecognitionStream) Recv() (*speechpb.StreamingRecognizeResponse, error) {
	return s.recv()
}
func (s *duplexRecognitionStream) CloseSend() error { return s.closeSend() }

func TestPCMTranscriptionDrainsResultsWhileSending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		frames := make(chan []byte)
		inputClosed := make(chan struct{})
		var rpcCtx context.Context
		var received []byte
		frameCount, closeCount := 0, 0
		stream := &duplexRecognitionStream{
			send: func(r *speechpb.StreamingRecognizeRequest) error {
				if r.GetStreamingConfig() != nil {
					return nil
				}
				if len(r.GetAudio()) > maxStreamingPCMBytes {
					t.Error("oversized input frame")
				}
				select {
				case frames <- r.GetAudio():
					return nil
				case <-rpcCtx.Done():
					return rpcCtx.Err()
				}
			},
			recv: func() (*speechpb.StreamingRecognizeResponse, error) {
				select {
				case audio := <-frames:
					received = append(received, audio...)
					frameCount++
					text, confidence := "途中", float32(.99)
					if frameCount == 2 {
						text, confidence = "小声", .9
					}
					if frameCount == 3 {
						text, confidence = "届く", .8
					}
					return &speechpb.StreamingRecognizeResponse{Results: []*speechpb.StreamingRecognitionResult{
						streamingTestResult(text, frameCount != 1, 0, confidence),
					}}, nil
				case <-inputClosed:
					return nil, io.EOF
				case <-rpcCtx.Done():
					return nil, rpcCtx.Err()
				}
			},
			closeSend: func() error { closeCount++; close(inputClosed); return nil },
		}
		service := streamingTestService(stream, "short")
		service.streamRecognizeCall = func(callCtx context.Context) (streamingRecognizeClient, error) {
			rpcCtx = callCtx
			return stream, nil
		}
		audio := bytes.Repeat([]byte{10, 0}, maxStreamingPCMBytes+320)
		text, confidence, err := service.TranscribePCM16(ctx, audio)
		if err != nil || ctx.Err() != nil || text != "小声 届く" || confidence != .8 {
			t.Fatalf("duplex recognition: text=%q confidence=%v err=%v parent=%v", text, confidence, err, ctx.Err())
		}
		if frameCount != 3 || closeCount != 1 || !bytes.Equal(received, audio) {
			t.Fatalf("input altered or incomplete: frames=%d close=%d bytes=%d", frameCount, closeCount, len(received))
		}
	})
}

func TestPCMTranscriptionDiscardsPartialResultOnSendFailure(t *testing.T) {
	for _, failClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "send", true: "close send"}[failClose], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				var rpcCtx context.Context
				partialRead := make(chan struct{})
				failure := status.Error(codes.Unavailable, "input transport failed")
				recvCalls, closeCalls := 0, 0
				stream := &duplexRecognitionStream{
					send: func(r *speechpb.StreamingRecognizeRequest) error {
						if r.GetStreamingConfig() != nil {
							return nil
						}
						select {
						case <-partialRead:
						case <-rpcCtx.Done():
							return rpcCtx.Err()
						}
						if failClose {
							return nil
						}
						return failure
					},
					recv: func() (*speechpb.StreamingRecognizeResponse, error) {
						recvCalls++
						if recvCalls == 1 {
							close(partialRead)
							return &speechpb.StreamingRecognizeResponse{Results: []*speechpb.StreamingRecognitionResult{
								streamingTestResult("未完の本文", true, 0, .9),
							}}, nil
						}
						<-rpcCtx.Done()
						return nil, rpcCtx.Err()
					},
					closeSend: func() error { closeCalls++; return failure },
				}
				service := streamingTestService(stream, "short")
				service.streamRecognizeCall = func(callCtx context.Context) (streamingRecognizeClient, error) {
					rpcCtx = callCtx
					return stream, nil
				}
				text, confidence, err := service.TranscribePCM16(ctx, []byte{10, 0})
				if !errors.Is(err, failure) || text != "" || confidence != 0 || ctx.Err() != nil {
					t.Fatalf("partial response escaped: text=%q confidence=%v error=%v parent=%v", text, confidence, err, ctx.Err())
				}
				if RecognitionFailureClass(err) != "grpc_Unavailable" {
					t.Fatalf("provider failure mislabeled: %s", RecognitionFailureClass(err))
				}
				if (failClose && closeCalls != 1) || (!failClose && closeCalls != 0) {
					t.Fatalf("unexpected CloseSend calls=%d", closeCalls)
				}
			})
		})
	}
}

func TestPCMTranscriptionCancelsAndJoinsSenderOnReceiveTermination(t *testing.T) {
	for _, mode := range []string{"receive failure", "receive failure grpc cancellation", "premature EOF", "parent cancellation"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				var rpcCtx context.Context
				sendStarted := make(chan struct{})
				sendCancelled := make(chan struct{})
				releaseSender := make(chan struct{})
				senderExited := make(chan struct{})
				failure := status.Error(codes.Unavailable, "recognition failed")
				receiveCalls := 0
				stream := &duplexRecognitionStream{
					send: func(r *speechpb.StreamingRecognizeRequest) error {
						if r.GetStreamingConfig() != nil {
							return nil
						}
						close(sendStarted)
						<-rpcCtx.Done()
						close(sendCancelled)
						<-releaseSender
						// The provider may still be using the caller's backing bytes
						// during cancellation cleanup; Transcribe must not return yet.
						if !bytes.Equal(r.GetAudio(), []byte{10, 0}) {
							t.Error("input owner returned too early")
						}
						close(senderExited)
						if mode == "receive failure grpc cancellation" {
							return status.FromContextError(rpcCtx.Err()).Err()
						}
						return rpcCtx.Err()
					},
					recv: func() (*speechpb.StreamingRecognizeResponse, error) {
						<-sendStarted
						receiveCalls++
						if receiveCalls == 1 {
							return &speechpb.StreamingRecognizeResponse{Results: []*speechpb.StreamingRecognitionResult{
								streamingTestResult("返してはいけない途中結果", true, 0, .9),
							}}, nil
						}
						switch mode {
						case "premature EOF":
							return nil, io.EOF
						case "parent cancellation":
							<-rpcCtx.Done()
							return nil, rpcCtx.Err()
						default:
							return nil, failure
						}
					},
					closeSend: func() error { t.Error("failed upload was half-closed"); return nil },
				}
				service := streamingTestService(stream, "short")
				service.streamRecognizeCall = func(callCtx context.Context) (streamingRecognizeClient, error) {
					rpcCtx = callCtx
					return stream, nil
				}
				type result struct {
					text       string
					confidence float32
					err        error
				}
				done := make(chan result, 1)
				audio := []byte{10, 0}
				go func() {
					text, confidence, err := service.TranscribePCM16(ctx, audio)
					done <- result{text, confidence, err}
				}()
				<-sendStarted
				if mode == "parent cancellation" {
					cancel()
				}
				<-sendCancelled
				synctest.Wait()
				var completed result
				returnedEarly := false
				select {
				case completed = <-done:
					returnedEarly = true
					t.Error("recognition returned before sender released caller-owned audio")
				default:
				}
				close(releaseSender)
				synctest.Wait()
				if !returnedEarly {
					completed = <-done
				}
				if completed.err == nil || completed.text != "" || completed.confidence != 0 {
					t.Fatalf("termination accepted: %+v", completed)
				}
				if mode == "receive failure" || mode == "receive failure grpc cancellation" {
					if !errors.Is(completed.err, failure) || RecognitionFailureClass(completed.err) != "grpc_Unavailable" {
						t.Fatalf("receive error lost: %v", completed.err)
					}
				}
				if mode == "parent cancellation" && !errors.Is(completed.err, context.Canceled) {
					t.Fatalf("parent cancellation lost: %v", completed.err)
				}
				select {
				case <-senderExited:
				default:
					t.Error("sender still running")
				}
				clear(audio)
			})
		})
	}
}
