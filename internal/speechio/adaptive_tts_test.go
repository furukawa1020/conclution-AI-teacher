package speechio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

func TestWarmStreamingSynthesisPopulatesCompletedPCMCache(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	directCalls := make(map[string]int)
	service := &CloudService{
		voiceName:         "ja-JP-Chirp3-HD-Kore",
		streamingPCMCache: newStreamingPCMCache(8, 4096),
		streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
			return nil, errors.New("stream must not open")
		},
		synthesizePCMCall: func(
			_ context.Context,
			request *texttospeechpb.SynthesizeSpeechRequest,
		) (*texttospeechpb.SynthesizeSpeechResponse, error) {
			text := request.GetInput().GetText()
			mu.Lock()
			directCalls[text]++
			mu.Unlock()
			return &texttospeechpb.SynthesizeSpeechResponse{
				AudioContent: []byte{40, 0},
			}, nil
		},
	}

	result := service.WarmStreamingSynthesis(
		context.Background(),
		[]string{"first cue", " second cue ", "first cue", ""},
		2,
	)
	if result.Requested != 2 || result.Warmed != 2 || result.Failed != 0 {
		mu.Lock()
		calls := make(map[string]int, len(directCalls))
		for text, count := range directCalls {
			calls[text] = count
		}
		mu.Unlock()
		t.Fatalf("warmup result = %+v provider calls = %#v", result, calls)
	}
	for _, text := range []string{"first cue", "second cue"} {
		if _, err := service.StreamSynthesize(
			context.Background(),
			text,
			func([]byte) error { return nil },
		); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if directCalls["first cue"] != 1 || directCalls["second cue"] != 1 {
		t.Fatalf("provider calls after cache reuse = %#v", directCalls)
	}
}

func TestWarmStreamingSynthesisCountsFailureWithoutStoppingPeers(t *testing.T) {
	t.Parallel()

	service := &CloudService{
		voiceName:         "ja-JP-Chirp3-HD-Kore",
		streamingPCMCache: newStreamingPCMCache(8, 4096),
		synthesizePCMCall: func(
			_ context.Context,
			request *texttospeechpb.SynthesizeSpeechRequest,
		) (*texttospeechpb.SynthesizeSpeechResponse, error) {
			if request.GetInput().GetText() == "failure" {
				return nil, errors.New("provider unavailable")
			}
			return &texttospeechpb.SynthesizeSpeechResponse{
				AudioContent: []byte{40, 0},
			}, nil
		},
		streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
			return nil, errors.New("stream unavailable")
		},
	}
	result := service.WarmStreamingSynthesis(
		context.Background(),
		[]string{"success", "failure"},
		2,
	)
	if result.Requested != 2 || result.Warmed != 1 || result.Failed != 1 {
		t.Fatalf("warmup result = %+v", result)
	}
}

func TestShortReplyUsesDirectPCMWithoutOpeningStream(t *testing.T) {
	t.Parallel()

	directCalls := 0
	streamCalls := 0
	var request *texttospeechpb.SynthesizeSpeechRequest
	service := &CloudService{
		voiceName: "ja-JP-Chirp3-HD-Kore",
		synthesizePCMCall: func(
			_ context.Context,
			candidate *texttospeechpb.SynthesizeSpeechRequest,
		) (*texttospeechpb.SynthesizeSpeechResponse, error) {
			directCalls++
			request = candidate
			return &texttospeechpb.SynthesizeSpeechResponse{
				AudioContent: []byte{1, 2, 3, 4},
			}, nil
		},
		streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
			streamCalls++
			return nil, errors.New("stream must not open")
		},
	}

	var audio []byte
	contentType, err := service.StreamSynthesize(
		context.Background(),
		"short audited reply",
		func(chunk []byte) error {
			audio = append(audio, chunk...)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != StreamingAudioContentType {
		t.Fatalf("content type = %q", contentType)
	}
	if directCalls != 1 || streamCalls != 0 {
		t.Fatalf("direct calls = %d, stream calls = %d", directCalls, streamCalls)
	}
	if !bytes.Equal(audio, []byte{1, 2, 3, 4}) {
		t.Fatalf("audio = %v", audio)
	}
	config := request.GetAudioConfig()
	if config.GetAudioEncoding() != texttospeechpb.AudioEncoding_PCM ||
		config.GetSampleRateHertz() != StreamingSampleRateHertz {
		t.Fatalf("audio config = %+v", config)
	}
}

func TestShortReplyFallsBackToStreamingBeforeOutput(t *testing.T) {
	t.Parallel()

	directCalls := 0
	streamCalls := 0
	service := &CloudService{
		voiceName: "ja-JP-Chirp3-HD-Kore",
		synthesizePCMCall: func(
			context.Context,
			*texttospeechpb.SynthesizeSpeechRequest,
		) (*texttospeechpb.SynthesizeSpeechResponse, error) {
			directCalls++
			return nil, errors.New("direct unavailable")
		},
		streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
			streamCalls++
			return &fakeStreamingSynthesizeClient{
				recvResults: []streamingReceiveResult{
					{response: &texttospeechpb.StreamingSynthesizeResponse{
						AudioContent: []byte{5, 6},
					}},
					{err: io.EOF},
				},
			}, nil
		},
	}

	var audio []byte
	if _, err := service.StreamSynthesize(
		context.Background(),
		"short fallback",
		func(chunk []byte) error {
			audio = append(audio, chunk...)
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if directCalls != 1 || streamCalls != 1 {
		t.Fatalf("direct calls = %d, stream calls = %d", directCalls, streamCalls)
	}
	if !bytes.Equal(audio, []byte{5, 6}) {
		t.Fatalf("audio = %v", audio)
	}
}

func TestDirectPCMCallbackFailureDoesNotCauseDoubleSpeech(t *testing.T) {
	t.Parallel()

	streamCalls := 0
	callbackError := errors.New("playback stopped")
	service := &CloudService{
		voiceName: "ja-JP-Chirp3-HD-Kore",
		synthesizePCMCall: func(
			context.Context,
			*texttospeechpb.SynthesizeSpeechRequest,
		) (*texttospeechpb.SynthesizeSpeechResponse, error) {
			return &texttospeechpb.SynthesizeSpeechResponse{
				AudioContent: []byte{1, 2},
			}, nil
		},
		streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
			streamCalls++
			return nil, errors.New("unexpected stream")
		},
	}

	if _, err := service.StreamSynthesize(
		context.Background(),
		"short callback failure",
		func([]byte) error { return callbackError },
	); !errors.Is(err, callbackError) {
		t.Fatalf("error = %v; want callback error", err)
	}
	if streamCalls != 0 {
		t.Fatalf("stream opened %d times after output callback", streamCalls)
	}
}

func TestPreparedShortReplyKeepsReadyStreamAndDeliversFirstChunk(t *testing.T) {
	t.Parallel()

	directCalls := 0
	stream := &fakeStreamingSynthesizeClient{
		recvResults: []streamingReceiveResult{
			{response: &texttospeechpb.StreamingSynthesizeResponse{
				AudioContent: []byte{9, 10},
			}},
			{response: &texttospeechpb.StreamingSynthesizeResponse{
				AudioContent: []byte{11, 12},
			}},
			{err: io.EOF},
		},
	}
	service := cloudServiceWithStream(stream)
	service.synthesizePCMCall = func(
		context.Context,
		*texttospeechpb.SynthesizeSpeechRequest,
	) (*texttospeechpb.SynthesizeSpeechResponse, error) {
		directCalls++
		return nil, errors.New("prepared short reply must not restart direct synthesis")
	}
	prepared, err := service.PrepareStreamingSynthesis(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("first chunk observed")
	callbacks := 0
	_, err = prepared.StreamSynthesize("短い返答です。", func(chunk []byte) error {
		callbacks++
		if !bytes.Equal(chunk, []byte{9, 10}) {
			t.Fatalf("first chunk = %v", chunk)
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v", err)
	}
	if directCalls != 0 || callbacks != 1 || stream.receiveIndex != 1 {
		t.Fatalf(
			"direct=%d callbacks=%d receives=%d",
			directCalls,
			callbacks,
			stream.receiveIndex,
		)
	}
	if len(stream.sent) != 2 ||
		stream.sent[0].GetStreamingConfig() == nil ||
		stream.sent[1].GetInput().GetText() != "短い返答です。" ||
		!stream.closeCalled {
		t.Fatalf("prepared short stream sequence invalid: sent=%d closed=%t", len(stream.sent), stream.closeCalled)
	}
}

func TestLongReplyKeepsStreamingRoute(t *testing.T) {
	t.Parallel()

	directCalls := 0
	streamCalls := 0
	service := &CloudService{
		voiceName: "ja-JP-Chirp3-HD-Kore",
		synthesizePCMCall: func(
			context.Context,
			*texttospeechpb.SynthesizeSpeechRequest,
		) (*texttospeechpb.SynthesizeSpeechResponse, error) {
			directCalls++
			return nil, errors.New("direct must not run")
		},
		streamSynthesizeCall: func(context.Context) (streamingSynthesizeClient, error) {
			streamCalls++
			return &fakeStreamingSynthesizeClient{
				recvResults: []streamingReceiveResult{
					{response: &texttospeechpb.StreamingSynthesizeResponse{
						AudioContent: []byte{7, 8},
					}},
					{err: io.EOF},
				},
			}, nil
		},
	}

	if _, err := service.StreamSynthesize(
		context.Background(),
		strings.Repeat("a", maxDirectPCMSynthesisRunes+1),
		func([]byte) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	if directCalls != 0 || streamCalls != 1 {
		t.Fatalf("direct calls = %d, stream calls = %d", directCalls, streamCalls)
	}
}
