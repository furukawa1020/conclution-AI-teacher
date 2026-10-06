package speechio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"cloud.google.com/go/speech/apiv2/speechpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type pairedRecognitionResult struct {
	text       string
	confidence float32
	err        error
}

func pairedOwnedTestPCM() [3][]byte {
	return [3][]byte{
		bytes.Repeat([]byte{1, 0}, 320),
		bytes.Repeat([]byte{2, 0}, 320),
		bytes.Repeat([]byte{5, 0}, 320),
	}
}

func TestPairedRecognitionFailureCancelsPeersAndJoinsOwners(t *testing.T) {
	for _, blocked := range []string{"send", "close send", "receive"} {
		for failedIndex := -1; failedIndex < 3; failedIndex++ {
			t.Run(fmt.Sprintf("%s/failure=%d", blocked, failedIndex), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					failureGate, releaseOwners := make(chan struct{}), make(chan struct{})
					ownersReleased := false
					defer func() {
						if !ownersReleased {
							close(releaseOwners)
						}
					}()
					var opened, ownerCanceled, ownerExited, receiverExited atomic.Int64
					providerFailure := status.Error(codes.Unavailable, "private recognition failure")
					service := streamingTestService(nil, "short")
					service.streamRecognizeCall = func(rpcCtx context.Context) (streamingRecognizeClient, error) {
						index := int(opened.Add(1)) - 1
						inputClosed := make(chan struct{})
						waitForCancellation := func(audio []byte) error {
							before := append([]byte(nil), audio...)
							<-rpcCtx.Done()
							ownerCanceled.Add(1)
							<-releaseOwners
							if !bytes.Equal(audio, before) {
								t.Error("caller reclaimed PCM before the sender exited")
							}
							ownerExited.Add(1)
							return rpcCtx.Err()
						}
						return &duplexRecognitionStream{
							send: func(request *speechpb.StreamingRecognizeRequest) error {
								if request.GetStreamingConfig() != nil || index == failedIndex || blocked != "send" {
									return nil
								}
								return waitForCancellation(request.GetAudio())
							},
							closeSend: func() error {
								if index != failedIndex && blocked == "close send" {
									return waitForCancellation(nil)
								}
								close(inputClosed)
								return nil
							},
							recv: func() (*speechpb.StreamingRecognizeResponse, error) {
								defer receiverExited.Add(1)
								if index == failedIndex {
									<-inputClosed
									<-failureGate
									return nil, providerFailure
								}
								<-rpcCtx.Done()
								return nil, rpcCtx.Err()
							},
						}, nil
					}
					audio := pairedOwnedTestPCM()
					done := make(chan pairedRecognitionResult, 1)
					go func() {
						text, confidence, err := service.TranscribePairedPCM16(ctx, audio[0], audio[1], audio[2])
						// This caller is entitled to clear the backing bytes at return.
						for _, view := range audio {
							clear(view)
						}
						done <- pairedRecognitionResult{text, confidence, err}
					}()
					synctest.Wait()
					if opened.Load() != 3 {
						t.Fatal("test did not start all three independent recognizers")
					}
					wantErr := ErrPairedRecognitionUnresolved
					peers := int64(2)
					if failedIndex == -1 {
						cancel()
						wantErr = context.Canceled
						peers = 3
					} else {
						close(failureGate)
					}
					synctest.Wait()
					if receiverExited.Load() != 3 {
						t.Fatalf("terminal failure left sibling recognition running: exited=%d", receiverExited.Load())
					}
					if blocked != "receive" {
						if ownerCanceled.Load() != peers {
							t.Fatalf("terminal failure did not cancel sibling input: canceled=%d", ownerCanceled.Load())
						}
						select {
						case result := <-done:
							t.Fatalf("returned before sender owners joined: %+v", result)
						default:
						}
					}
					close(releaseOwners)
					ownersReleased = true
					synctest.Wait()
					select {
					case result := <-done:
						if !errors.Is(result.err, wantErr) || result.text != "" || result.confidence != 0 {
							t.Fatalf("failed group leaked a result or changed classification: %+v", result)
						}
					default:
						t.Fatal("all canceled owners exited but paired recognition still waits")
					}
					if failedIndex != -1 && ctx.Err() != nil {
						t.Fatal("sibling cancellation escaped to the caller")
					}
					if blocked != "receive" && ownerExited.Load() != peers {
						t.Fatal("an input owner remained live after return")
					}
				})
			})
		}
	}
}

func TestPairedRecognitionKeepsWaitingForNonterminalViews(t *testing.T) {
	for _, mode := range []string{"no speech", "two agree then failure", "two agree then lower confidence"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				finishViews := make(chan struct{})
				var opened, completed, peerCanceled atomic.Int64
				service := streamingTestService(nil, "short")
				service.streamRecognizeCall = func(rpcCtx context.Context) (streamingRecognizeClient, error) {
					index := int(opened.Add(1)) - 1
					inputClosed := make(chan struct{})
					finalSent := false
					return &duplexRecognitionStream{
						send:      func(*speechpb.StreamingRecognizeRequest) error { return nil },
						closeSend: func() error { close(inputClosed); return nil },
						recv: func() (*speechpb.StreamingRecognizeResponse, error) {
							<-inputClosed
							if mode == "no speech" && index == 0 || finalSent {
								completed.Add(1)
								return nil, io.EOF
							}
							if mode == "no speech" || index == 2 {
								select {
								case <-finishViews:
								case <-rpcCtx.Done():
									peerCanceled.Add(1)
									return nil, rpcCtx.Err()
								}
							}
							if mode == "two agree then failure" && index == 2 {
								return nil, status.Error(codes.Unavailable, "third view unavailable")
							}
							finalSent = true
							return &speechpb.StreamingRecognizeResponse{Results: []*speechpb.StreamingRecognitionResult{
								streamingTestResult("一致する本文", true, 0, []float32{.9, .8, .7}[index]),
							}}, nil
						},
					}, nil
				}
				audio := pairedOwnedTestPCM()
				done := make(chan pairedRecognitionResult, 1)
				go func() {
					text, confidence, err := service.TranscribePairedPCM16(ctx, audio[0], audio[1], audio[2])
					done <- pairedRecognitionResult{text, confidence, err}
				}()
				synctest.Wait()
				wantCompleted := int64(2)
				if mode == "no speech" {
					wantCompleted = 1
				}
				if opened.Load() != 3 || completed.Load() != wantCompleted || peerCanceled.Load() != 0 {
					t.Fatalf("nonterminal result canceled independent observations: opened/completed/canceled=%d/%d/%d", opened.Load(), completed.Load(), peerCanceled.Load())
				}
				select {
				case result := <-done:
					t.Fatalf("paired recognition returned before all observations: %+v", result)
				default:
				}
				close(finishViews)
				synctest.Wait()
				result := <-done
				if mode == "two agree then failure" {
					if !errors.Is(result.err, ErrPairedRecognitionUnresolved) || result.text != "" || result.confidence != 0 {
						t.Fatalf("agreement masked a failed third observation: %+v", result)
					}
				} else if result.err != nil || result.text != "一致する本文" || result.confidence != .7 {
					t.Fatalf("exact agreement/minimum confidence changed: %+v", result)
				}
				if ctx.Err() != nil {
					t.Fatal("group canceled the parent")
				}
			})
		})
	}
}
