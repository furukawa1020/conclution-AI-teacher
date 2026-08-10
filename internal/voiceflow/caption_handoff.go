package voiceflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

var (
	errCaptionHandoffState = errors.New("voiceflow: caption handoff state is invalid")
	errCaptionHandoffInput = errors.New("voiceflow: caption handoff input is invalid")
)

// captionHandoff lets Native Audio donate its already-finalized transcript to
// the audited staged agent. A stable interim may start model and TTS work, but
// startLiveSpeculation keeps every PCM byte behind its commit buffer until the
// exact final caption is observed and Commit is called by the Native service.
type captionHandoff struct {
	mu sync.Mutex

	p               *Pipeline
	ctx             context.Context
	cancel          context.CancelFunc
	uid             string
	input           httpapi.VoiceTurnInput
	streamingSpeech speechio.StreamingService
	onAudio         func([]byte) error
	onCoachActive   func(httpapi.VoiceRespondentCheckpoint) error

	tracker              speculativeCandidateTracker
	speculation          *liveSpeculation
	speculationAttempted bool
	latestCaption        string
	finalObserved        bool
	finalObservedAt      time.Time
	committed            bool
	canceled             bool
	finished             bool
	audioAuthorized      bool
	firstOutputAt        time.Time
	outputDelivered      bool
	firstOutputMu        sync.Mutex
}

// OpenCaptionHandoff opens no recognizer. It accepts only a Native Audio turn
// without a document or strict-mode state, because those routes have separate
// regional and minimization boundaries.
func (p *Pipeline) OpenCaptionHandoff(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	onAudio func([]byte) error,
	onCoachActive func(httpapi.VoiceRespondentCheckpoint) error,
) (httpapi.VoiceCaptionHandoff, error) {
	if p == nil || p.agent == nil || p.speech == nil || ctx == nil || uid == "" ||
		onAudio == nil || !input.NativeAudio || input.StrictCloudMinimization ||
		input.Document != nil || input.MIMEType != speechio.StreamingAudioContentType ||
		input.ProcessingCommitted == nil {
		return nil, errCaptionHandoffInput
	}
	streamingSpeech, ok := p.speech.(speechio.StreamingService)
	if !ok {
		return nil, errCaptionHandoffInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handoffCtx, cancel := context.WithCancel(ctx)
	return &captionHandoff{
		p:               p,
		ctx:             handoffCtx,
		cancel:          cancel,
		uid:             uid,
		input:           input,
		streamingSpeech: streamingSpeech,
		onAudio:         onAudio,
		onCoachActive:   onCoachActive,
	}, nil
}

// Observe takes ownership of captionUTF8 and clears it before returning. A
// Native partial has no calibrated stability score, so readiness requires the
// identical canonical caption at least twice across the existing 160 ms lease.
func (handoff *captionHandoff) Observe(
	captionUTF8 []byte,
	final bool,
	observedAt time.Time,
) error {
	defer clear(captionUTF8)
	if handoff == nil || observedAt.IsZero() || !utf8.Valid(captionUTF8) ||
		utf8.RuneCount(captionUTF8) > conversation.MaxUtteranceRunes {
		return errCaptionHandoffInput
	}
	caption := strings.TrimSpace(string(captionUTF8))
	if final && caption == "" {
		return errCaptionHandoffInput
	}
	if err := handoff.ctx.Err(); err != nil {
		return err
	}

	handoff.mu.Lock()
	defer handoff.mu.Unlock()
	if handoff.canceled || handoff.committed || handoff.finished ||
		handoff.finalObserved {
		return errCaptionHandoffState
	}
	if err := handoff.ctx.Err(); err != nil {
		return err
	}
	handoff.latestCaption = caption
	if final {
		handoff.finalObserved = true
		handoff.finalObservedAt = observedAt
	}
	if handoff.speculationAttempted || caption == "" ||
		!voiceResponseExpected(handoff.input) {
		return nil
	}
	candidate, ready := handoff.tracker.observe(caption, true, observedAt)
	if !ready {
		return nil
	}
	handoff.speculationAttempted = true
	handoff.speculation = handoff.p.startLiveSpeculation(
		handoff.ctx,
		handoff.uid,
		handoff.input,
		candidate,
		handoff.streamingSpeech,
		handoff.deliverAudio,
	)
	return nil
}

func (handoff *captionHandoff) deliverAudio(chunk []byte) error {
	if len(chunk) == 0 || len(chunk)%2 != 0 {
		return errSpeculativeAudioChunk
	}
	if err := handoff.ctx.Err(); err != nil {
		return err
	}
	handoff.mu.Lock()
	authorized := handoff.committed && !handoff.canceled &&
		!handoff.finished && handoff.audioAuthorized
	handoff.mu.Unlock()
	if !authorized {
		return errCaptionHandoffState
	}
	if err := handoff.ctx.Err(); err != nil {
		return err
	}
	if err := handoff.onAudio(chunk); err != nil {
		return err
	}
	handoff.firstOutputMu.Lock()
	handoff.outputDelivered = true
	if speechio.PCM16HasMeaningfulSample(chunk) &&
		handoff.firstOutputAt.IsZero() {
		handoff.firstOutputAt = time.Now()
	}
	handoff.firstOutputMu.Unlock()
	return nil
}

// Commit is the only operation that may release staged PCM. It first compares
// the speculative caption with the exact final caption using the same
// byte-preserving canonicalization as the ordinary live pipeline. A mismatch
// cancels all speculative work and runs one committed agent pass; it never
// adopts a semantic prefix or fuzzy match.
func (handoff *captionHandoff) Commit() (httpapi.VoiceTurnResult, error) {
	if handoff == nil {
		return httpapi.VoiceTurnResult{}, errCaptionHandoffState
	}
	if err := handoff.ctx.Err(); err != nil {
		handoff.Cancel()
		return httpapi.VoiceTurnResult{}, err
	}
	if handoff.input.ProcessingCommitted == nil {
		return httpapi.VoiceTurnResult{}, errCaptionHandoffState
	}
	select {
	case <-handoff.input.ProcessingCommitted:
	default:
		// A provider-final caption is not transport authority. No decision,
		// checkpoint, or PCM may be released before the browser's commit frame.
		return httpapi.VoiceTurnResult{}, errCaptionHandoffState
	}

	handoff.mu.Lock()
	contextErr := handoff.ctx.Err()
	if handoff.canceled || handoff.committed || !handoff.finalObserved ||
		handoff.latestCaption == "" || contextErr != nil {
		handoff.mu.Unlock()
		if contextErr != nil {
			handoff.Cancel()
			return httpapi.VoiceTurnResult{}, contextErr
		}
		return httpapi.VoiceTurnResult{}, errCaptionHandoffState
	}
	handoff.committed = true
	finalCaption := handoff.latestCaption
	finalObservedAt := handoff.finalObservedAt
	speculation := handoff.speculation
	handoff.speculation = nil
	handoff.latestCaption = ""
	handoff.mu.Unlock()
	defer handoff.finishCommitted()

	conversationMS := int64(-1)
	firstTTSChunkMS := int64(-1)
	ttsReleaseMS := int64(-1)
	ttsBufferedBytes := int64(0)
	specHit := int64(0)
	specMiss := int64(0)
	specCancel := int64(0)
	ttsPrestarted := int64(0)
	prestartedTTSDone := false
	adoptedDecision := false
	var result httpapi.VoiceTurnResult
	var spokenReply string
	outputAuthorized := false
	authorizeOutput := func() error {
		if outputAuthorized {
			return nil
		}
		if err := handoff.ctx.Err(); err != nil {
			return err
		}
		if result.AssistanceTarget == "respondent" {
			// Respondent audio is a state transition, not an ordinary caption. The
			// transport authenticates and accepts this checkpoint synchronously
			// before any PCM byte can cross the commit boundary. Missing control or
			// malformed empty-state decisions therefore fail closed.
			if handoff.onCoachActive == nil {
				return errCaptionHandoffState
			}
			// Every legal respondent phase is checkpointed, including silent
			// continuation states. The callback carries only finite control metadata;
			// no user caption or answer text crosses this channel.
			result.Route = httpapi.VoiceNativeRespondentCoachRoute
			checkpoint, err := httpapi.NewVoiceRespondentCheckpoint(result)
			if err != nil {
				return errCaptionHandoffState
			}
			if err := handoff.onCoachActive(checkpoint); err != nil {
				return err
			}
			if err := handoff.ctx.Err(); err != nil {
				return err
			}
		}
		handoff.mu.Lock()
		defer handoff.mu.Unlock()
		if handoff.canceled || handoff.finished || !handoff.committed {
			return errCaptionHandoffState
		}
		if err := handoff.ctx.Err(); err != nil {
			return err
		}
		handoff.audioAuthorized = true
		outputAuthorized = true
		return nil
	}

	if speculation != nil && speculationPreservesFinalMetadata(finalCaption) &&
		speculationTextsMatch(
			speculation.candidate,
			finalCaption,
		) {
		var outcome speculativeTurnOutcome
		select {
		case outcome = <-speculation.outcome:
		case <-handoff.ctx.Done():
			outcome.err = handoff.ctx.Err()
		}
		if outcome.err == nil {
			conversationMS = outcome.durationMS
			spokenReply = outcome.decision.SpokenReply
			outcome.decision.AnswerProof = committedSpeculativeAnswerProof(
				handoff.input,
				outcome.decision,
			)
			outcome.decision.AnswerTransitionProof =
				committedSpeculativeAnswerTransitionProof(
					handoff.input,
					outcome.decision,
				)
			result = voiceResultFromDecision(handoff.input, outcome.decision)
			if spokenReply == "" {
				adoptedDecision = true
			} else if outcome.synthesis != nil {
				ttsPrestarted = 1
				if err := authorizeOutput(); err != nil {
					outcome.synthesis.abort(err)
					return result, err
				}
				synthesisResult, completed := outcome.synthesis.commitBoundary(
					handoff.ctx,
				)
				synthesisErr := synthesisResult.err
				if completed && synthesisErr == nil &&
					synthesisResult.mimeType != speechio.StreamingAudioContentType {
					synthesisErr = errSpeculativeAudioMIME
				}
				if synthesisErr == nil {
					ttsReleaseMS, synthesisErr = outcome.synthesis.buffer.release(
						handoff.ctx,
					)
				}
				if synthesisErr == nil && !completed {
					synthesisResult = outcome.synthesis.await(handoff.ctx)
					synthesisErr = synthesisResult.err
					if synthesisErr == nil &&
						synthesisResult.mimeType != speechio.StreamingAudioContentType {
						synthesisErr = errSpeculativeAudioMIME
					}
				}
				firstTTSChunkMS = outcome.synthesis.firstChunkMS()
				ttsBufferedBytes = outcome.synthesis.buffer.peakBufferedBytes()
				if synthesisErr == nil && firstTTSChunkMS >= 0 {
					prestartedTTSDone = true
					adoptedDecision = true
				} else {
					outcome.synthesis.abort(synthesisErr)
					handoff.firstOutputMu.Lock()
					outputCrossedBoundary := handoff.outputDelivered
					handoff.firstOutputMu.Unlock()
					if outputCrossedBoundary {
						return httpapi.VoiceTurnResult{},
							httpapi.NewVoicePipelineFailure(
								httpapi.VoicePipelineStageSynthesize,
							)
					}
					// The decision was audited and exactly matched. Retry only TTS;
					// never spend another model pass after a private synthesis miss.
					adoptedDecision = true
				}
			}
		}
	}

	if !adoptedDecision {
		if speculation != nil {
			synthesis := speculation.cancel()
			if synthesis != nil {
				synthesis.await(handoff.ctx)
			}
			specCancel = 1
		}
		specMiss = 1
		started := time.Now()
		var err error
		result, spokenReply, err = handoff.p.prepareRecognizedTurn(
			handoff.ctx,
			handoff.uid,
			handoff.input,
			finalCaption,
			0,
			conversation.FloorEvidenceHybridCommitted,
		)
		conversationMS = time.Since(started).Milliseconds()
		if err != nil {
			return result, err
		}
	} else {
		specHit = 1
	}

	if spokenReply != "" && !prestartedTTSDone {
		if err := authorizeOutput(); err != nil {
			return result, err
		}
		started := time.Now()
		audioMIME, err := handoff.streamingSpeech.StreamSynthesize(
			handoff.ctx,
			spokenReply,
			func(chunk []byte) error {
				if firstTTSChunkMS < 0 {
					firstTTSChunkMS = time.Since(started).Milliseconds()
				}
				return handoff.deliverAudio(chunk)
			},
		)
		if err != nil || audioMIME != speechio.StreamingAudioContentType ||
			firstTTSChunkMS < 0 {
			return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
				httpapi.VoicePipelineStageSynthesize,
			)
		}
	}
	if err := authorizeOutput(); err != nil {
		return result, err
	}
	result.Caption = spokenReply
	handoff.firstOutputMu.Lock()
	firstOutputAt := handoff.firstOutputAt
	handoff.firstOutputMu.Unlock()
	finalToFirstAudioMS := int64(-1)
	if !finalObservedAt.IsZero() && !firstOutputAt.IsZero() {
		finalToFirstAudioMS = firstOutputAt.Sub(finalObservedAt).Milliseconds()
		if finalToFirstAudioMS < 0 {
			finalToFirstAudioMS = -1
		}
	}
	result.LiveTimings = httpapi.VoiceLiveTimings{
		STTFirstInterimMS:           -1,
		STTFinalMS:                  -1,
		ConversationMS:              conversationMS,
		TTSFirstChunkMS:             firstTTSChunkMS,
		FinalToFirstAudioMS:         finalToFirstAudioMS,
		CommitToServerDrainMS:       -1,
		ServerDrainToActivityEndMS:  -1,
		ActivityEndToFinalCaptionMS: -1,
		FinalToRiskRouteGateMS:      -1,
		OutputCommitToFirstAudioMS:  -1,
		SpecHit:                     specHit,
		SpecMiss:                    specMiss,
		SpecCancel:                  specCancel,
		TTSPrestarted:               ttsPrestarted,
		TTSBufferedBytes:            ttsBufferedBytes,
		TTSReleaseMS:                ttsReleaseMS,
		NativeCaptionHandoff:        1,
	}
	return result, nil
}

func (handoff *captionHandoff) Cancel() {
	if handoff == nil {
		return
	}
	handoff.mu.Lock()
	if handoff.canceled || handoff.finished {
		handoff.mu.Unlock()
		return
	}
	handoff.canceled = true
	handoff.audioAuthorized = false
	handoff.latestCaption = ""
	speculation := handoff.speculation
	handoff.speculation = nil
	cancel := handoff.cancel
	handoff.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if speculation != nil {
		speculation.cancel()
	}
}

func (handoff *captionHandoff) finishCommitted() {
	handoff.mu.Lock()
	if handoff.finished {
		handoff.mu.Unlock()
		return
	}
	handoff.finished = true
	handoff.audioAuthorized = false
	cancel := handoff.cancel
	handoff.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

var _ httpapi.VoiceCaptionHandoffService = (*Pipeline)(nil)
var _ httpapi.VoiceCaptionHandoff = (*captionHandoff)(nil)
