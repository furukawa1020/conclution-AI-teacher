package voiceflow

import (
	"context"
	"errors"
	"math"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

const (
	minUsableTranscriptConfidence = 0.65
	lowConfidencePrompt           = "今の一文だけ、もう一度聞かせてください。"
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
	transcript, confidence, err := p.speech.Transcribe(ctx, input.Audio)
	if err != nil {
		if errors.Is(err, speechio.ErrNoSpeech) {
			if input.Ambient {
				return silentRecognitionResult(input.StateToken), nil
			}
			return p.recognitionClarification(ctx, input.StateToken)
		}
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageTranscribe,
		)
	}
	if transcriptConfidenceTooLow(confidence) {
		result := silentRecognitionResult(input.StateToken)
		if input.Ambient {
			return result, nil
		}
		return p.recognitionClarification(ctx, input.StateToken)
	}

	turn := conversation.VoiceTurn{
		SchemaVersion: conversation.SchemaVersion,
		Utterance:     transcript,
		StateToken:    input.StateToken,
		Ambient:       input.Ambient,
	}
	if input.Document != nil {
		turn.PDF = &conversation.InlinePDF{
			MIMEType: input.Document.MIMEType,
			Data:     input.Document.Data,
		}
	}
	decision, err := p.agent.Process(ctx, uid, turn)
	if err != nil {
		if errors.Is(err, conversation.ErrInvalidStateToken) ||
			errors.Is(err, conversation.ErrInvalidTurn) {
			return httpapi.VoiceTurnResult{}, httpapi.ErrVoiceStateInvalid
		}
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageConversation,
		)
	}

	result := httpapi.VoiceTurnResult{
		StateToken:       decision.StateToken,
		DetectedDomain:   decision.Domain,
		AssistanceTarget: decision.AssistanceTarget,
		RespondentStage:  decision.RespondentStage,
		ResearchStatus:   decision.ResearchStatus,
		ResearchRecords:  researchRecords(decision.ResearchRecords),
		Route:            decision.Route,
		NeedsPaper: decision.Intervention.Act == "paper_check" &&
			input.Document == nil,
	}
	if decision.SpokenReply == "" {
		return result, nil
	}

	audio, audioMIME, err := p.speech.Synthesize(ctx, decision.SpokenReply)
	if err != nil {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageSynthesize,
		)
	}
	result.Audio = audio
	result.AudioMIMEType = audioMIME
	result.Caption = decision.SpokenReply
	return result, nil
}

func (p *Pipeline) recognitionClarification(
	ctx context.Context,
	stateToken string,
) (httpapi.VoiceTurnResult, error) {
	audio, audioMIME, err := p.speech.Synthesize(ctx, lowConfidencePrompt)
	if err != nil {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageSynthesize,
		)
	}
	result := silentRecognitionResult(stateToken)
	result.Audio = audio
	result.AudioMIMEType = audioMIME
	result.Caption = lowConfidencePrompt
	result.Route = "stt-clarify"
	return result, nil
}

func silentRecognitionResult(stateToken string) httpapi.VoiceTurnResult {
	return httpapi.VoiceTurnResult{
		StateToken:       stateToken,
		DetectedDomain:   "unknown",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		ResearchStatus:   "none",
		ResearchRecords:  []httpapi.ResearchRecord{},
		Route:            "stt-silent",
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
