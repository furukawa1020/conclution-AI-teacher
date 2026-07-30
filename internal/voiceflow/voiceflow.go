package voiceflow

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
	"golang.org/x/text/unicode/norm"
)

const (
	minUsableTranscriptConfidence = 0.65
	minSpeculativeStability       = 0.85
	minSpeculativeCandidateRunes  = 8
	minSpeculativeStableDuration  = 160 * time.Millisecond
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
	now    func() time.Time
}

type speculativeCandidateTracker struct {
	candidate   string
	firstSeenAt time.Time
	observed    int
}

type speculativeTurnOutcome struct {
	decision   conversation.VoiceTurnResult
	err        error
	durationMS int64
}

type liveSpeculation struct {
	candidate string
	cancel    context.CancelFunc
	outcome   <-chan speculativeTurnOutcome
}

type liveStreamingSpeech interface {
	speechio.StreamingService
	OpenStreamingTranscription(
		context.Context,
	) (speechio.StreamingTranscriptionSession, error)
}

func New(speech speechio.Service, agent conversation.Agent) (*Pipeline, error) {
	if speech == nil || agent == nil {
		return nil, errors.New("voiceflow: speech and conversation agent are required")
	}
	return &Pipeline{speech: speech, agent: agent, now: time.Now}, nil
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

// ProcessLive consumes bounded PCM chunks as they arrive, receives streaming
// recognition events concurrently, and sends only the final audited reply to
// streaming synthesis. Ownership of each audio slice transfers to this method.
func (p *Pipeline) ProcessLive(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
) (httpapi.VoiceTurnResult, error) {
	if audio == nil || onAudio == nil {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageTranscribe,
		)
	}
	streamingSpeech, ok := p.speech.(liveStreamingSpeech)
	if !ok {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageTranscribe,
		)
	}

	transcriptionStarted := time.Now()
	transcriptionCtx, cancelTranscription := context.WithCancel(ctx)
	defer cancelTranscription()
	session, err := streamingSpeech.OpenStreamingTranscription(transcriptionCtx)
	if err != nil {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageTranscribe,
		)
	}

	sendDone := make(chan error, 1)
	go func() {
		sentAudio := false
		for {
			select {
			case <-transcriptionCtx.Done():
				sendDone <- transcriptionCtx.Err()
				return
			case chunk, open := <-audio:
				if !open {
					if !sentAudio {
						if err := session.CloseSend(); err != nil {
							sendDone <- err
							return
						}
						sendDone <- speechio.ErrNoSpeech
						return
					}
					sendDone <- session.CloseSend()
					return
				}
				err := session.SendPCM(chunk)
				clear(chunk)
				if err != nil {
					sendDone <- err
					return
				}
				sentAudio = true
			}
		}
	}()

	intentional := !input.Ambient && input.TurnMode != httpapi.VoiceTurnAmbient
	// A speculative agent run must never share a PDF byte slice with a normal
	// fallback. The current live protocol has no document frame, but keep this
	// boundary explicit so a future protocol extension cannot accidentally
	// race the agent's mandatory document wipe.
	speculationEligible := intentional && input.Document == nil
	finalFragments := make([]string, 0, 4)
	latestInterim := ""
	candidateTracker := speculativeCandidateTracker{}
	speculationAttempted := false
	var speculation *liveSpeculation
	defer func() {
		if speculation != nil {
			speculation.cancel()
		}
	}()

	receiveErr := error(nil)
	firstInterimMS := int64(-1)
	firstFinalMS := int64(-1)
	finalObservedAt := time.Time{}
	for {
		event, eventErr := session.RecvEvent()
		if errors.Is(eventErr, io.EOF) {
			break
		}
		if eventErr != nil {
			receiveErr = eventErr
			break
		}
		if event.Kind == speechio.StreamingTranscriptionInterim &&
			firstInterimMS < 0 {
			firstInterimMS = time.Since(transcriptionStarted).Milliseconds()
		}

		switch event.Kind {
		case speechio.StreamingTranscriptionInterim:
			latestInterim = event.Text
			if !speculationEligible || speculationAttempted {
				continue
			}
			candidate, ready := candidateTracker.observe(
				joinTranscript(finalFragments, latestInterim),
				event.Stability >= minSpeculativeStability,
				p.currentTime(),
			)
			if ready {
				speculationAttempted = true
				speculation = p.startLiveSpeculation(
					ctx,
					uid,
					input,
					candidate,
				)
			}
			continue
		case speechio.StreamingTranscriptionFinal:
		default:
			continue
		}

		if firstFinalMS < 0 {
			firstFinalMS = time.Since(transcriptionStarted).Milliseconds()
		}
		fragment := strings.TrimSpace(event.Text)
		if fragment == "" {
			latestInterim = ""
			if speculationEligible && !speculationAttempted {
				candidateTracker.observe("", false, p.currentTime())
			}
			continue
		}
		finalFragments = append(finalFragments, fragment)
		latestInterim = ""
		finalObservedAt = time.Now()
		finalTranscript := strings.Join(finalFragments, " ")
		if utf8.RuneCountInString(finalTranscript) >
			conversation.MaxUtteranceRunes {
			receiveErr = speechio.ErrTranscriptLong
			break
		}
		// A newly finalized fragment is itself a stable observation. This is
		// important when the recognizer emits one interim and then the same
		// text as final instead of repeating the interim hypothesis.
		if speculationEligible && !speculationAttempted {
			candidate, ready := candidateTracker.observe(
				finalTranscript,
				true,
				p.currentTime(),
			)
			if ready {
				speculationAttempted = true
				speculation = p.startLiveSpeculation(
					ctx,
					uid,
					input,
					candidate,
				)
			}
		}
	}
	cancelTranscription()
	sendErr := <-sendDone
	if receiveErr != nil ||
		(sendErr != nil &&
			!errors.Is(sendErr, context.Canceled) &&
			!errors.Is(sendErr, speechio.ErrNoSpeech)) {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageTranscribe,
		)
	}

	finalTranscript := strings.TrimSpace(strings.Join(finalFragments, " "))
	slog.InfoContext(ctx, "voice pipeline stage completed",
		"request_id", input.RequestID,
		"stage", "transcribe_stream",
		"duration_ms", time.Since(transcriptionStarted).Milliseconds(),
		"recognized", finalTranscript != "",
		"stt_first_interim_ms", firstInterimMS,
		"stt_final_ms", firstFinalMS,
	)
	conversationMS := int64(-1)
	specHit := int64(0)
	specMiss := int64(0)
	specCancel := int64(0)
	var (
		result      httpapi.VoiceTurnResult
		spokenReply string
	)
	if finalTranscript == "" {
		if intentional {
			specMiss = 1
			if speculation != nil {
				speculation.cancel()
				specCancel = 1
			}
		}
		route := routeClarifyNoSpeech
		if input.Ambient {
			route = routeSilentNoSpeech
		}
		result = silentRecognitionResult(input.StateToken, route)
		if !input.Ambient {
			spokenReply = lowConfidencePrompt
		}
	} else {
		adoptedSpeculation := false
		if intentional {
			if speculation == nil {
				specMiss = 1
			} else if speculationTextsMatch(
				speculation.candidate,
				finalTranscript,
			) {
				var outcome speculativeTurnOutcome
				select {
				case outcome = <-speculation.outcome:
				case <-ctx.Done():
					outcome.err = ctx.Err()
				}
				if outcome.err == nil {
					specHit = 1
					conversationMS = outcome.durationMS
					result = voiceResultFromDecision(input, outcome.decision)
					spokenReply = outcome.decision.SpokenReply
					adoptedSpeculation = true
					slog.InfoContext(
						ctx,
						"voice pipeline stage completed",
						"request_id", input.RequestID,
						"stage", "conversation_speculative_adopted",
						"duration_ms", conversationMS,
						"route", outcome.decision.Route,
					)
				} else {
					speculation.cancel()
					specMiss = 1
					specCancel = 1
				}
			} else {
				speculation.cancel()
				specMiss = 1
				specCancel = 1
			}
		}
		if !adoptedSpeculation {
			// Chirp 3's confidence field is not a calibrated utterance score.
			// Preserve the recognized text bounds and let the audited
			// conversation agent handle semantic ambiguity instead of applying
			// the buffered recognizer's fixed confidence gate.
			conversationStarted := time.Now()
			result, spokenReply, err = p.prepareRecognizedTurn(
				ctx,
				uid,
				input,
				finalTranscript,
				0,
			)
			conversationMS = time.Since(conversationStarted).Milliseconds()
		}
	}
	if err != nil || spokenReply == "" {
		result.LiveTimings = httpapi.VoiceLiveTimings{
			STTFirstInterimMS:   firstInterimMS,
			STTFinalMS:          firstFinalMS,
			ConversationMS:      conversationMS,
			TTSFirstChunkMS:     -1,
			FinalToFirstAudioMS: -1,
			SpecHit:             specHit,
			SpecMiss:            specMiss,
			SpecCancel:          specCancel,
		}
		return result, err
	}

	synthesisStarted := time.Now()
	firstTTSChunkMS := int64(-1)
	finalToFirstAudioMS := int64(-1)
	audioMIME, err := streamingSpeech.StreamSynthesize(
		ctx,
		spokenReply,
		func(chunk []byte) error {
			if firstTTSChunkMS < 0 {
				firstAudioAt := time.Now()
				firstTTSChunkMS = firstAudioAt.Sub(synthesisStarted).Milliseconds()
				if !finalObservedAt.IsZero() {
					finalToFirstAudioMS =
						firstAudioAt.Sub(finalObservedAt).Milliseconds()
					if finalToFirstAudioMS < 0 {
						finalToFirstAudioMS = -1
					}
				}
			}
			return onAudio(chunk)
		},
	)
	if err != nil || audioMIME != speechio.StreamingAudioContentType {
		return httpapi.VoiceTurnResult{}, httpapi.NewVoicePipelineFailure(
			httpapi.VoicePipelineStageSynthesize,
		)
	}
	slog.InfoContext(ctx, "voice pipeline stage completed",
		"request_id", input.RequestID,
		"stage", "synthesize_live",
		"duration_ms", time.Since(synthesisStarted).Milliseconds(),
		"tts_first_chunk_ms", firstTTSChunkMS,
		"final_to_first_audio_ms", finalToFirstAudioMS,
		"spec_hit", specHit,
		"spec_miss", specMiss,
		"spec_cancel", specCancel,
	)
	result.Caption = spokenReply
	result.LiveTimings = httpapi.VoiceLiveTimings{
		STTFirstInterimMS:   firstInterimMS,
		STTFinalMS:          firstFinalMS,
		ConversationMS:      conversationMS,
		TTSFirstChunkMS:     firstTTSChunkMS,
		FinalToFirstAudioMS: finalToFirstAudioMS,
		SpecHit:             specHit,
		SpecMiss:            specMiss,
		SpecCancel:          specCancel,
	}
	return result, nil
}

func (p *Pipeline) currentTime() time.Time {
	if p.now == nil {
		return time.Now()
	}
	return p.now()
}

func (p *Pipeline) startLiveSpeculation(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	candidate string,
) *liveSpeculation {
	if input.Document != nil {
		return nil
	}
	speculationCtx, cancel := context.WithCancel(ctx)
	outcome := make(chan speculativeTurnOutcome, 1)
	turn := conversationTurn(input, candidate, true)
	started := time.Now()
	go func() {
		decision, err := p.agent.Process(speculationCtx, uid, turn)
		outcome <- speculativeTurnOutcome{
			decision:   decision,
			err:        err,
			durationMS: time.Since(started).Milliseconds(),
		}
	}()
	return &liveSpeculation{
		candidate: candidate,
		cancel:    cancel,
		outcome:   outcome,
	}
}

func (tracker *speculativeCandidateTracker) observe(
	rawCandidate string,
	eligible bool,
	observedAt time.Time,
) (string, bool) {
	candidate := canonicalSpeculationText(rawCandidate)
	if !eligible ||
		utf8.RuneCountInString(candidate) < minSpeculativeCandidateRunes {
		tracker.reset()
		return "", false
	}
	if candidate != tracker.candidate {
		tracker.candidate = candidate
		tracker.firstSeenAt = observedAt
		tracker.observed = 1
		return candidate, false
	}
	tracker.observed++
	return candidate,
		tracker.observed >= 2 &&
			!observedAt.Before(tracker.firstSeenAt) &&
			observedAt.Sub(tracker.firstSeenAt) >=
				minSpeculativeStableDuration
}

func (tracker *speculativeCandidateTracker) reset() {
	tracker.candidate = ""
	tracker.firstSeenAt = time.Time{}
	tracker.observed = 0
}

func joinTranscript(finalFragments []string, latestInterim string) string {
	parts := make([]string, 0, len(finalFragments)+1)
	parts = append(parts, finalFragments...)
	if strings.TrimSpace(latestInterim) != "" {
		parts = append(parts, latestInterim)
	}
	return strings.Join(parts, " ")
}

func canonicalSpeculationText(value string) string {
	return strings.Join(strings.Fields(norm.NFC.String(value)), " ")
}

func speculationTextsMatch(candidate string, finalTranscript string) bool {
	return trimSpeculationSuffix(canonicalSpeculationText(candidate)) ==
		trimSpeculationSuffix(canonicalSpeculationText(finalTranscript))
}

func trimSpeculationSuffix(value string) string {
	return strings.TrimRightFunc(value, func(r rune) bool {
		if unicode.IsSpace(r) {
			return true
		}
		switch r {
		case '。', '！', '？', '!', '?':
			return true
		default:
			return false
		}
	})
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
	return p.prepareRecognizedTurn(ctx, uid, input, transcript, confidence)
}

func (p *Pipeline) prepareRecognizedTurn(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	transcript string,
	confidence float32,
) (httpapi.VoiceTurnResult, string, error) {
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

	turn := conversationTurn(input, transcript, false)
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

	result := voiceResultFromDecision(input, decision)
	if decision.SpokenReply == "" {
		return result, "", nil
	}
	return result, decision.SpokenReply, nil
}

func conversationTurn(
	input httpapi.VoiceTurnInput,
	transcript string,
	speculative bool,
) conversation.VoiceTurn {
	turn := conversation.VoiceTurn{
		SchemaVersion: conversation.SchemaVersion,
		Utterance:     transcript,
		StateToken:    input.StateToken,
		RequestID:     input.RequestID,
		Ambient:       input.Ambient,
		Speculative:   speculative,
	}
	if input.Document == nil {
		return turn
	}
	turn.PDF = &conversation.InlinePDF{
		MIMEType: input.Document.MIMEType,
		Data:     input.Document.Data,
	}
	return turn
}

func voiceResultFromDecision(
	input httpapi.VoiceTurnInput,
	decision conversation.VoiceTurnResult,
) httpapi.VoiceTurnResult {
	return httpapi.VoiceTurnResult{
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
