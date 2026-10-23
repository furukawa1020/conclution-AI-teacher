package voiceflow

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

const maxGuestTrialPCMBytes = 30 * 16000 * 2

type GuestTrialVoiceVerifier interface {
	conversation.GuestTrialVerifier
	ValidateGuestTrialState(guesttrial.Owner, guesttrial.Snapshot, string) error
}

// These capabilities cannot generate ordinary dialogue, synthesize speech,
// open a Native session, or update long-term memory. The production composition
// root must not activate this path until the dedicated UI and API are ready.
type GuestTrialPipeline struct {
	speech   speechio.ExplicitPCM16Transcriber
	verifier GuestTrialVoiceVerifier
	trials   *guesttrial.Service
}

func NewGuestTrialPipeline(speech speechio.ExplicitPCM16Transcriber, verifier GuestTrialVoiceVerifier, trials *guesttrial.Service) (*GuestTrialPipeline, error) {
	if speech == nil || verifier == nil || trials == nil {
		return nil, guesttrial.ErrInvalid
	}
	return &GuestTrialPipeline{speech: speech, verifier: verifier, trials: trials}, nil
}

// ProcessPCM consumes and clears the caller's audio, including on rejection.
// PCM is finalized 16kHz mono signed little-endian input, not a provisional
// caption. owner must come from verified guest identity + App Check, not JSON.
// No caption, audio, or model output crosses the result boundary.
func (p *GuestTrialPipeline) ProcessPCM(ctx context.Context, owner guesttrial.Owner, id string, generation int64, token string, audio []byte) (guesttrial.Snapshot, error) {
	defer clear(audio)
	if p == nil || ctx == nil || len(audio) < 2 || len(audio) > maxGuestTrialPCMBytes || len(audio)%2 != 0 || token == "" {
		return guesttrial.Snapshot{}, guesttrial.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	return p.trials.Evaluate(ctx, owner, id, generation, func(ctx context.Context, snapshot guesttrial.Snapshot) (guesttrial.Verdict, error) {
		// An expired/tampered state must never restart with an empty state.
		if err := p.verifier.ValidateGuestTrialState(owner, snapshot, token); err != nil {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		transcript, confidence, err := p.speech.TranscribePCM16(ctx, audio)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err != nil {
			if errors.Is(err, speechio.ErrNoSpeech) {
				return 0, httpapi.ErrVoiceNotRecognized
			}
			return 0, httpapi.NewVoicePipelineFailure(httpapi.VoicePipelineStageTranscribe)
		}
		if !utf8.ValidString(transcript) || strings.TrimSpace(transcript) == "" ||
			utf8.RuneCountInString(transcript) > conversation.MaxUtteranceRunes || transcriptConfidenceTooLow(confidence) {
			// Preserve pending state. Do not turn ASR uncertainty into a failed
			// answer or synthesize an unsolicited "please speak again" cue.
			return 0, httpapi.ErrVoiceNotRecognized
		}
		return p.verifier.EvaluateGuestTrial(ctx, owner, snapshot, conversation.VoiceTurn{
			SchemaVersion: conversation.SchemaVersion, Utterance: transcript, StateToken: token,
			GuestExperience: true, InputOrigin: conversation.InputOriginCommittedVoice,
		})
	})
}
