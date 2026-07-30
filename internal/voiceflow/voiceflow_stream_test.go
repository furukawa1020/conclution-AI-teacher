package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type fakeStreamingSpeech struct {
	fakeSpeech
	chunks      [][]byte
	streamCalls int
	streamErr   error
}

func (speech *fakeStreamingSpeech) StreamSynthesize(
	ctx context.Context,
	text string,
	onChunk speechio.StreamChunkHandler,
) (string, error) {
	speech.streamCalls++
	speech.synthesizedText = text
	for _, chunk := range speech.chunks {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := onChunk(chunk); err != nil {
			return "", err
		}
	}
	if speech.streamErr != nil {
		return "", speech.streamErr
	}
	return speechio.StreamingAudioContentType, nil
}

func TestPipelineStreamsOnlyTheFinalAuditedReply(t *testing.T) {
	t.Parallel()

	speech := &fakeStreamingSpeech{
		fakeSpeech: fakeSpeech{
			transcript: "結論を教えて",
			confidence: 0.98,
		},
		chunks: [][]byte{{0, 0}, {1, 0}},
	}
	agent := &fakeAgent{result: conversation.VoiceTurnResult{
		Domain:             "daily",
		AssistanceTarget:   "assistant",
		RespondentStage:    "none",
		ResearchStatus:     "none",
		ResearchRecords:    []conversation.ResearchRecord{},
		SpokenReply:        "Aです。理由はBです。",
		Route:              "fast",
		StateToken:         "sealed-state",
		InterventionPolicy: "answer",
	}}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	var delivered []byte
	result, err := pipeline.ProcessStream(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{
			Audio:     []byte("audio"),
			RequestID: "request-id",
		},
		func(chunk []byte) error {
			delivered = append(delivered, chunk...)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}
	if !bytes.Equal(delivered, []byte{0, 0, 1, 0}) ||
		speech.streamCalls != 1 ||
		speech.synthesizeCalls != 0 ||
		speech.synthesizedText != agent.result.SpokenReply {
		t.Fatalf(
			"delivered=%v stream=%d buffered=%d text=%q",
			delivered,
			speech.streamCalls,
			speech.synthesizeCalls,
			speech.synthesizedText,
		)
	}
	if len(result.Audio) != 0 ||
		result.AudioMIMEType != "" ||
		result.Caption != agent.result.SpokenReply ||
		result.StateToken != "sealed-state" {
		t.Fatalf("stream result=%+v", result)
	}
}

func TestPipelineStopsStreamingWhenTheTransportRejectsAChunk(t *testing.T) {
	t.Parallel()

	speech := &fakeStreamingSpeech{
		fakeSpeech: fakeSpeech{
			transcript: "答えて",
			confidence: 0.9,
		},
		chunks: [][]byte{{0, 0}, {1, 0}},
	}
	agent := &fakeAgent{result: conversation.VoiceTurnResult{
		Domain:           "daily",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		ResearchStatus:   "none",
		ResearchRecords:  []conversation.ResearchRecord{},
		SpokenReply:      "Aです。",
		Route:            "fast",
		StateToken:       "sealed-state",
	}}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("client disconnected")
	calls := 0
	_, err = pipeline.ProcessStream(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{Audio: []byte("audio")},
		func([]byte) error {
			calls++
			return callbackErr
		},
	)
	if err == nil {
		t.Fatal("callback failure did not fail closed")
	}
	stage, ok := httpapi.VoicePipelineStageOf(err)
	if !ok || stage != httpapi.VoicePipelineStageSynthesize || calls != 1 {
		t.Fatalf("stage=%q classified=%v calls=%d err=%v", stage, ok, calls, err)
	}
}
