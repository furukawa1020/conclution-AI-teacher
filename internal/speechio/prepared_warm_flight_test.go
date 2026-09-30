package speechio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

// Every created RPC can produce the same prefix and held tail. Counting text
// inputs, rather than configured RPCs, detects duplicate audible synthesis.
type preparedWarmFlightStream struct {
	ctx         context.Context
	releaseTail <-chan struct{}
	inputCalls  atomic.Int32
	received    int
}

func (stream *preparedWarmFlightStream) Send(request *texttospeechpb.StreamingSynthesizeRequest) error {
	if request.GetInput() != nil {
		stream.inputCalls.Add(1)
	}
	return stream.ctx.Err()
}

func (stream *preparedWarmFlightStream) CloseSend() error { return stream.ctx.Err() }

func (stream *preparedWarmFlightStream) Recv() (*texttospeechpb.StreamingSynthesizeResponse, error) {
	stream.received++
	if stream.received == 1 {
		return &texttospeechpb.StreamingSynthesizeResponse{AudioContent: []byte{40, 0}}, nil
	}
	if stream.received > 2 {
		return nil, io.EOF
	}
	select {
	case <-stream.ctx.Done():
		return nil, stream.ctx.Err()
	case <-stream.releaseTail:
		return &texttospeechpb.StreamingSynthesizeResponse{AudioContent: []byte{41, 0}}, nil
	}
}

func TestPreparedEvictedWarmCueSharesOrdinaryFlight(t *testing.T) {
	for _, mode := range []string{"both complete", "prepared cancels", "all cancel"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				releaseTail := make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(releaseTail) }) }
				defer release()
				var streams []*preparedWarmFlightStream
				const cue = "audited fixed reply"
				service := &CloudService{
					voiceName:         "ja-JP-Chirp3-HD-Kore",
					streamingPCMCache: newStreamingPCMCache(1, 1024),
					streamSynthesizeCall: func(ctx context.Context) (streamingSynthesizeClient, error) {
						stream := &preparedWarmFlightStream{ctx: ctx, releaseTail: releaseTail}
						streams = append(streams, stream)
						return stream, nil
					},
				}
				key := newStreamingPCMCacheKey(service.voiceName, cue)
				service.warmSynthesisKeys = map[streamingPCMCacheKey]struct{}{key: {}}
				service.streamingPCMCache.put(key, cachedStreamingPCM{chunks: [][]byte{{1, 0}}, size: 2})
				service.streamingPCMCache.put(newStreamingPCMCacheKey(service.voiceName, "generated reply"),
					cachedStreamingPCM{chunks: [][]byte{{2, 0}}, size: 2})
				if _, ok := service.streamingPCMCache.get(key); ok {
					t.Fatal("fixture did not evict the fixed reply")
				}
				prepared, err := service.PrepareStreamingSynthesis(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer prepared.Close()
				var preparedAudio, ordinaryAudio []byte
				preparedDone := make(chan error, 1)
				go func() {
					_, err := prepared.StreamSynthesize(cue, func(chunk []byte) error {
						preparedAudio = append(preparedAudio, chunk...)
						return nil
					})
					preparedDone <- err
				}()
				synctest.Wait()
				if len(streams) != 2 || streams[0].inputCalls.Load() != 0 || streams[1].inputCalls.Load() != 1 {
					t.Fatalf("fixed reply used the turn-owned prepared RPC; configured RPCs=%d", len(streams))
				}
				provider := streams[1]
				ordinaryCtx, cancelOrdinary := context.WithCancel(context.Background())
				defer cancelOrdinary()
				ordinaryDone := make(chan error, 1)
				go func() {
					_, err := service.StreamSynthesize(ordinaryCtx, cue, func(chunk []byte) error {
						ordinaryAudio = append(ordinaryAudio, chunk...)
						return nil
					})
					ordinaryDone <- err
				}()
				synctest.Wait()
				if !bytes.Equal(preparedAudio, []byte{40, 0}) || !bytes.Equal(ordinaryAudio, preparedAudio) || len(streams) != 2 {
					t.Fatalf("prefix was not shared: prepared=%v ordinary=%v RPCs=%d", preparedAudio, ordinaryAudio, len(streams))
				}
				if mode != "both complete" {
					prepared.Close()
					synctest.Wait()
					if err := <-preparedDone; !errors.Is(err, context.Canceled) {
						t.Fatalf("prepared error=%v", err)
					}
					if provider.ctx.Err() != nil {
						t.Fatal("prepared subscriber canceled the peer's provider")
					}
					select {
					case err := <-ordinaryDone:
						t.Fatalf("peer ended before its tail: %v", err)
					default:
					}
				}
				if mode == "all cancel" {
					cancelOrdinary()
					synctest.Wait()
					if err := <-ordinaryDone; !errors.Is(err, context.Canceled) {
						t.Fatalf("last subscriber error=%v", err)
					}
					if provider.ctx.Err() != context.Canceled {
						t.Fatal("last subscriber left shared provider alive")
					}
					if _, ok := service.streamingPCMCache.get(key); ok {
						t.Fatal("incomplete fixed reply entered PCM cache")
					}
				} else {
					release()
					synctest.Wait()
					if err := <-ordinaryDone; err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(ordinaryAudio, []byte{40, 0, 41, 0}) {
						t.Fatalf("peer audio=%v", ordinaryAudio)
					}
					if mode == "both complete" {
						if err := <-preparedDone; err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(preparedAudio, ordinaryAudio) {
							t.Fatalf("prepared audio=%v", preparedAudio)
						}
					}
					if _, ok := service.streamingPCMCache.get(key); !ok {
						t.Fatal("completed shared reply was not cached")
					}
				}
				if len(service.synthesisFlights.flights) != 0 {
					t.Fatal("completed flight was not released")
				}
				if streams[0].ctx.Err() != context.Canceled {
					t.Fatal("unused prepared RPC was not reclaimed")
				}
				if len(streams) != 2 || provider.inputCalls.Load() != 1 {
					t.Fatal("fixed reply was synthesized more than once")
				}
			})
		})
	}
}

func TestPreparedGeneratedReplyKeepsReadyConnection(t *testing.T) {
	releaseTail := make(chan struct{})
	close(releaseTail)
	var stream *preparedWarmFlightStream
	providerCalls := 0
	service := &CloudService{
		voiceName:         "ja-JP-Chirp3-HD-Kore",
		streamingPCMCache: newStreamingPCMCache(2, 1024),
		streamSynthesizeCall: func(ctx context.Context) (streamingSynthesizeClient, error) {
			providerCalls++
			stream = &preparedWarmFlightStream{ctx: ctx, releaseTail: releaseTail}
			return stream, nil
		},
	}
	service.warmSynthesisKeys = map[streamingPCMCacheKey]struct{}{
		newStreamingPCMCacheKey(service.voiceName, "audited fixed reply"): {},
	}
	prepared, err := service.PrepareStreamingSynthesis(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	var audio []byte
	mime, err := prepared.StreamSynthesize("generated private reply", func(chunk []byte) error {
		audio = append(audio, chunk...)
		return nil
	})
	if err != nil || mime != StreamingAudioContentType || !bytes.Equal(audio, []byte{40, 0, 41, 0}) {
		t.Fatalf("mime=%q error=%v audio=%v", mime, err, audio)
	}
	if providerCalls != 1 || stream.inputCalls.Load() != 1 || service.synthesisFlights.flights != nil {
		t.Fatalf("generated reply left ready route: RPCs=%d inputs=%d", providerCalls, stream.inputCalls.Load())
	}
	if stream.ctx.Err() != context.Canceled {
		t.Fatal("completed prepared provider remained alive")
	}
}
