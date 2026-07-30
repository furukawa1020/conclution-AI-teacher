package voiceflow

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

const (
	minUsableTranscriptConfidence = 0.65
	lowConfidencePrompt           = "今の一文だけ、もう一度聞かせてください。"
	routeClarifyNoSpeech          = "stt-clarify-no-speech"
	routeClarifyLowConfidence     = "stt-clarify-low-confidence"
	routeSilentNoSpeech           = "stt-silent-no-speech"
	routeSilentLowConfidence      = "stt-silent-low-confidence"
)

// Pipeline keeps the three trust boundaries explicit: regional speech
// recognition, semantic reasoning, and regional speech synthesis. It does not
// persist audio, transcripts, model replies, or documents.
type Pipeline struct {
	speech speechio.Service
	agent  conversation.Agent
}

func New(speech speechio.Service, agent conversation.Agent) (*Pipeline, error) {
	if speech == nil || agent == nil {
		return nil, errors.New("voiceflow: speech and conversation agent are required")
	}
	return &Pipeline{speech: speech, agent: agent}, nil
}

func (p *Pipeline) Process(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
) (httpapi.VoiceTurnResult, error) {
	result, spokenReply, err := p.prepareTurn(ctx, uid, input)
	if err != nil || spokenReply == "" {
		return result, err
	}
	synthesisStarted := time.Now()
	audio, audioMIME, err := p.speech.Synthesize(ctx, spokenReply)
	if err != nil {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageSynthesize,
		)
	}
	slog.InfoContext(ctx, "voice pipeline stage completed",
		"request_id", input.RequestID,
		"stage", "synthesize_buffered",
		"duration_ms", time.Since(synthesisStarted).Milliseconds(),
	)
	result.Audio = audio
	result.AudioMIMEType = audioMIME
	result.Caption = spokenReply
	return result, nil
}

func (p *Pipeline) ProcessStream(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	onAudio func([]byte) error,
) (httpapi.VoiceTurnResult, error) {
	if onAudio == nil {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageSynthesize,
		)
	}
	streamingSpeech, ok := p.speech.(speechio.StreamingService)
	if !ok {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageSynthesize,
		)
	}
	result, spokenReply, err := p.prepareTurn(ctx, uid, input)
	if err != nil || spokenReply == "" {
		return result, err
	}

	synthesisStarted := time.Now()
	firstChunkAt := time.Time{}
	chunkCount := 0
	audioMIME, err := streamingSpeech.StreamSynthesize(
		ctx,
		spokenReply,
		func(audio []byte) error {
			if firstChunkAt.IsZero() {
				firstChunkAt = time.Now()
			}
			if err := onAudio(audio); err != nil {
				return err
			}
			chunkCount++
			return nil
		},
	)
	if err != nil || audioMIME != speechio.StreamingAudioContentType {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageSynthesize,
		)
	}
	firstChunkMS := int64(-1)
	if !firstChunkAt.IsZero() {
		firstChunkMS = firstChunkAt.Sub(synthesisStarted).Milliseconds()
	}
	slog.InfoContext(ctx, "voice pipeline stage completed",
		"request_id", input.RequestID,
		"stage", "synthesize_stream",
		"duration_ms", time.Since(synthesisStarted).Milliseconds(),
		"first_chunk_ms", firstChunkMS,
		"chunk_count", chunkCount,
	)
	result.Caption = spokenReply
	return result, nil
}

func (p *Pipeline) prepareTurn(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
) (httpapi.VoiceTurnResult, string, error) {
	transcriptionStarted := time.Now()
	transcript, confidence, err := p.speech.Transcribe(ctx, input.Audio)
	if err != nil {
		slog.InfoContext(ctx, "voice pipeline stage completed",
			"request_id", input.RequestID,
			"stage", "transcribe",
			"duration_ms", time.Since(transcriptionStarted).Milliseconds(),
			"recognized", false,
		)
		if errors.Is(err, speechio.ErrNoSpeech) {
			if input.Ambient {
				return silentRecognitionResult(
					input.StateToken,
					routeSilentNoSpeech,
				), "", nil
			}
			return silentRecognitionResult(
				input.StateToken,
				routeClarifyNoSpeech,
			), lowConfidencePrompt, nil
		}
		return httpapi.VoiceTurnResult{}, "", httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageTranscribe,
		)
	}
	slog.InfoContext(ctx, "voice pipeline stage completed",
		"request_id", input.RequestID,
		"stage", "transcribe",
		"duration_ms", time.Since(transcriptionStarted).Milliseconds(),
		"recognized", true,
	)
	if transcriptConfidenceTooLow(confidence) {
		if input.Ambient {
			return silentRecognitionResult(
				input.StateToken,
				routeSilentLowConfidence,
			), "", nil
		}
		return silentRecognitionResult(
			input.StateToken,
			routeClarifyLowConfidence,
		), lowConfidencePrompt, nil
	}

	turn := conversation.VoiceTurn{
		SchemaVersion: conversation.SchemaVersion,
		Utterance:     transcript,
		StateToken:    input.StateToken,
		RequestID:     input.RequestID,
		Ambient:       input.Ambient,
	}
	if input.Document != nil {
		turn.PDF = &conversation.InlinePDF{
			MIMEType: input.Document.MIMEType,
			Data:     input.Document.Data,
		}
	}
	conversationStarted := time.Now()
	decision, err := p.agent.Process(ctx, uid, turn)
	if err != nil {
		if errors.Is(err, conversation.ErrInvalidStateToken) ||
			errors.Is(err, conversation.ErrInvalidTurn) {
			return httpapi.VoiceTurnResult{}, "", httpapi.ErrVoiceStateInvalid
		}
		return httpapi.VoiceTurnResult{}, "", httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageConversation,
		)
	}
	slog.InfoContext(ctx, "voice pipeline stage completed",
		"request_id", input.RequestID,
		"stage", "conversation",
		"duration_ms", time.Since(conversationStarted).Milliseconds(),
		"route", decision.Route,
	)

	result := httpapi.VoiceTurnResult{
		StateToken:       decision.StateToken,
		DetectedDomain:   decision.Domain,
		AssistanceTarget: decision.AssistanceTarget,
		RespondentStage:  decision.RespondentStage,
		ResearchStatus:   decision.ResearchStatus,
		ResearchRecords:  researchRecords(decision.ResearchRecords),
		Route:            decision.Route,
		NeedsPaper: (decision.Intervention.Act == "paper_check" &&
			input.Document == nil) ||
			((decision.Route == "planner-unavailable" ||
				decision.Route == "planner-unavailable-silent") &&
				input.Document != nil),
	}
	if decision.SpokenReply == "" {
		return result, "", nil
	}
	return result, decision.SpokenReply, nil
}

func silentRecognitionResult(
	stateToken string,
	route string,
) httpapi.VoiceTurnResult {
	return httpapi.VoiceTurnResult{
		StateToken:       stateToken,
		DetectedDomain:   "unknown",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		ResearchStatus:   "none",
		ResearchRecords:  []httpapi.ResearchRecord{},
		Route:            route,
	}
}

func researchRecords(records []conversation.ResearchRecord) []httpapi.ResearchRecord {
	result := make([]httpapi.ResearchRecord, 0, len(records))
	for _, record := range records {
		result = append(result, httpapi.ResearchRecord{
			Title:     record.Title,
			DOI:       record.DOI,
			URL:       record.URL,
			Published: record.Published,
			Source:    record.Source,
		})
	}
	return result
}

func transcriptConfidenceTooLow(confidence float32) bool {
	if math.IsNaN(float64(confidence)) ||
		math.IsInf(float64(confidence), 0) ||
		confidence < 0 ||
		confidence > 1 {
		return true
	}
	// Some recognizers omit utterance confidence and return zero. Treat zero
	// as "not provided", not as a measured zero-confidence transcript.
	return confidence > 0 && confidence < minUsableTranscriptConfidence
}
