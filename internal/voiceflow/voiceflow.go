package voiceflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
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
	transcript, _, err := p.speech.Transcribe(ctx, input.Audio)
	if err != nil {
		if errors.Is(err, speechio.ErrNoSpeech) {
			return httpapi.VoiceTurnResult{}, httpapi.ErrVoiceNotRecognized
		}
		return httpapi.VoiceTurnResult{}, fmt.Errorf("voiceflow: transcribe: %w", err)
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
		return httpapi.VoiceTurnResult{}, fmt.Errorf("voiceflow: reason: %w", err)
	}

	result := httpapi.VoiceTurnResult{
		StateToken:     decision.StateToken,
		DetectedDomain: decision.Domain,
		Route:          decision.Route,
		NeedsPaper: decision.Intervention.Act == "paper_check" &&
			input.Document == nil,
	}
	if decision.SpokenReply == "" {
		return result, nil
	}

	audio, audioMIME, err := p.speech.Synthesize(ctx, decision.SpokenReply)
	if err != nil {
		return httpapi.VoiceTurnResult{}, fmt.Errorf("voiceflow: synthesize: %w", err)
	}
	result.Audio = audio
	result.AudioMIMEType = audioMIME
	return result, nil
}
