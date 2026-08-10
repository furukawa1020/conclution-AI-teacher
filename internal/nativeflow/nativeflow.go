// Package nativeflow adapts bounded native-audio sessions to KOTAE's live
// voice transport. Each browser turn owns one provider connection; there is
// no provider-session resumption, and audio or captions are never persisted
// or logged.
package nativeflow

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
	"golang.org/x/text/unicode/norm"
)

const (
	maxCaptionRunes = 480
)

// defaultCompanionSystemPrompt defines the fast companion lane. It
// intentionally has no tools, exercise scoring, diagnosis, or pressure to
// leave home.
const defaultCompanionSystemPrompt = `あなたはKOTAEの日本語音声会話相手です。
最優先は、家にいる時間が長い人や人との会話に不安がある人でも、話題や練習の準備なしに安心して話せることです。
返答は前置きや「考えます」を挟まず、相手の発話の中心に関係する意味のある言葉からすぐ始めてください。
通常の返答は自然な日本語で原則1〜2文、およそ25〜70文字です。相手の次の言葉を最優先し、質問は会話に本当に必要な時だけ、答えやすい任意の一問までにしてください。
相手が短い、曖昧、沈黙気味、または考え途中なら失敗扱いせず、言葉を埋めたり質問を重ねたりしないでください。短く受け取って話す番を返し、話題を求められた時だけ低開示の話題を一つ出してください。
最初の挨拶だけは、低開示の話題を一つ短く添え、すぐ相手へ話す番を返してください。
訓練、採点、評価、診断として見せず、長くまとまらない発話には中心を先に返してください。
外出、学校、仕事、家族や支援者への相談を本人の依頼なしに目標化しないでください。依存を促さず、治療者や唯一の味方を名乗らないでください。
この経路には検索、PDF、購入、予約などのツールがありません。実行した、調べた、確認したと主張しないでください。
氏名、連絡先、認証情報を復唱しないでください。`

// DefaultSystemPrompt is deliberately limited to ordinary companion
// conversation. Respondent Coach may start only after the deterministic
// caption gate replays the same held utterance through the staged path, where
// the actual external question and signed continuity state can be bound.
const DefaultSystemPrompt = defaultCompanionSystemPrompt

var errNativeFlowUnavailable = errors.New("native audio flow unavailable")

type pooledSession struct {
	session nativevoice.Session
}

// Service implements only the live service contract. Buffered audio, PDF,
// strict mode, tools, and research continue through the audited legacy flow.
type Service struct {
	opener         nativevoice.Opener
	preparer       conversation.NativeStatePreparer
	captionHandoff httpapi.VoiceCaptionHandoffService

	ctx    context.Context
	cancel context.CancelFunc
	now    func() time.Time

	mu       sync.Mutex
	sessions map[string]*pooledSession
	close    sync.Once
}

func New(
	opener nativevoice.Opener,
	preparer conversation.NativeStatePreparer,
) (*Service, error) {
	return newService(opener, preparer, nil)
}

// NewWithCaptionHandoff reuses finalized Native Audio captions in the audited
// staged pipeline. The legacy constructor remains fail-closed for tests and
// deployments that have not wired the bridge yet.
func NewWithCaptionHandoff(
	opener nativevoice.Opener,
	preparer conversation.NativeStatePreparer,
	handoff httpapi.VoiceCaptionHandoffService,
) (*Service, error) {
	if handoff == nil {
		return nil, errors.New("native audio caption handoff is required")
	}
	return newService(opener, preparer, handoff)
}

func newService(
	opener nativevoice.Opener,
	preparer conversation.NativeStatePreparer,
	handoff httpapi.VoiceCaptionHandoffService,
) (*Service, error) {
	if opener == nil || preparer == nil {
		return nil, errors.New("native audio dependencies are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		opener:         opener,
		preparer:       preparer,
		captionHandoff: handoff,
		ctx:            ctx,
		cancel:         cancel,
		now:            time.Now,
		sessions:       make(map[string]*pooledSession),
	}, nil
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.close.Do(func() {
		s.cancel()
		s.mu.Lock()
		for uid, pooled := range s.sessions {
			delete(s.sessions, uid)
			if pooled.session != nil {
				_ = pooled.session.Close()
			}
		}
		s.mu.Unlock()
	})
	return nil
}

func (s *Service) ProcessLive(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
) (httpapi.VoiceTurnResult, error) {
	return s.processLive(ctx, uid, input, audio, onAudio, nil, nil)
}

func (s *Service) ProcessLiveWithEndpoint(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
	onEndpoint func(),
) (httpapi.VoiceTurnResult, error) {
	return s.processLive(ctx, uid, input, audio, onAudio, onEndpoint, nil)
}

// ProcessLiveWithControl preserves the live transport contract. Native Audio
// never substitutes a generic cached reflex for a question-bound Respondent
// Coach turn: finalized captions cross only through the audited staged handoff.
func (s *Service) ProcessLiveWithControl(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
	onEndpoint func(),
	onCoachActive func(httpapi.VoiceRespondentCheckpointTransition) error,
) (httpapi.VoiceTurnResult, error) {
	if onCoachActive == nil {
		return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
	}
	return s.processLive(
		ctx,
		uid,
		input,
		audio,
		onAudio,
		onEndpoint,
		onCoachActive,
	)
}

func (s *Service) processLive(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
	onEndpoint func(),
	onCoachActive func(httpapi.VoiceRespondentCheckpointTransition) error,
) (httpapi.VoiceTurnResult, error) {
	if s == nil || ctx == nil || uid == "" || audio == nil || onAudio == nil ||
		!input.NativeAudio || input.StrictCloudMinimization ||
		input.Document != nil || input.MIMEType != "audio/L16" {
		return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
	}
	preparedStateToken, requiresStaged, err := s.preparer.PrepareNativeState(uid, input.StateToken)
	if err != nil {
		if errors.Is(err, conversation.ErrInvalidStateToken) {
			return httpapi.VoiceTurnResult{}, httpapi.ErrVoiceStateInvalid
		}
		return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
	}
	// The preparer is the authority for the state consumed by this turn. In the
	// ordinary Native lane it advances the empty envelope; for an active answer
	// scope it returns the same question-bound token. The caption handoff must
	// receive that exact prepared token instead of replaying stale caller input.
	input.StateToken = preparedStateToken
	if requiresStaged && (s.captionHandoff == nil || onCoachActive == nil) {
		// The WebSocket handler recognizes native fallback only after its commit
		// frame closes audio. Drain and wipe capture without opening a provider;
		// returning before commit would be treated as an unexpected pipeline
		// failure and the client could not replay its held MediaRecorder turn.
		if err := discardNativeInputUntilCommit(ctx, audio); err != nil {
			return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
		}
		return httpapi.VoiceTurnResult{}, httpapi.ErrVoiceNativeFallback
	}

	pooled, err := s.acquire(ctx, uid)
	if err != nil {
		return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
	}
	healthy := false
	defer func() {
		s.release(uid, pooled, healthy)
	}()

	started := s.now()
	if err := pooled.session.StartActivity(ctx); err != nil {
		return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
	}

	sendDone := make(chan error, 1)
	go streamInput(ctx, pooled.session, audio, sendDone)

	var caption []byte
	var inputCaption []byte
	var firstAudioAt time.Time
	var inputCaptionFinalAt time.Time
	endpointPublished := false
	inputGatePassed := false
	sendResultRead := false
	spoke := false
	turnComplete := false
	providerAnswerTainted := false
	deliverAudio := func(pcm []byte) error {
		meaningful := speechio.PCM16HasMeaningfulSample(pcm)
		if err := onAudio(pcm); err != nil {
			return err
		}
		if meaningful && firstAudioAt.IsZero() {
			firstAudioAt = s.now()
		}
		spoke = true
		return nil
	}
	var stagedHandoff httpapi.VoiceCaptionHandoff
	var respondentCheckpointAuthorized atomic.Bool
	defer func() {
		if stagedHandoff != nil {
			stagedHandoff.Cancel()
		}
	}()
	openStagedHandoff := func() error {
		if stagedHandoff != nil {
			return nil
		}
		if s.captionHandoff == nil {
			return httpapi.ErrVoiceNativeFallback
		}
		var publishCheckpoint func(httpapi.VoiceRespondentCheckpoint) error
		if onCoachActive != nil {
			publishCheckpoint = func(checkpoint httpapi.VoiceRespondentCheckpoint) error {
				if !respondentCheckpointAuthorized.Load() {
					return errNativeFlowUnavailable
				}
				return onCoachActive(httpapi.VoiceRespondentCheckpointTransition{
					PreviousSessionState: preparedStateToken,
					Checkpoint:           checkpoint,
				})
			}
		}
		opened, openErr := s.captionHandoff.OpenCaptionHandoff(
			ctx,
			uid,
			input,
			deliverAudio,
			publishCheckpoint,
		)
		if openErr != nil {
			return openErr
		}
		if opened == nil {
			return errNativeFlowUnavailable
		}
		stagedHandoff = opened
		return nil
	}
	for !turnComplete {
		event, receiveErr := pooled.session.Receive(ctx)
		if receiveErr != nil {
			clear(inputCaption)
			clear(caption)
			return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
		}
		switch event.Kind {
		case nativevoice.EventInputCaption:
			inputCaption = mergeCaption(inputCaption, event.CaptionUTF8)
			if !utf8.Valid(inputCaption) ||
				utf8.RuneCount(inputCaption) > conversation.MaxUtteranceRunes {
				event.Clear()
				clear(inputCaption)
				clear(caption)
				return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
			}
			observedAt := s.now()
			if event.CaptionFinal && inputCaptionFinalAt.IsZero() {
				inputCaptionFinalAt = observedAt
			}
			inputCaptionText := string(inputCaption)
			coachObserved := requiresRespondentCoach(inputCaptionText)
			proxyAnswerOptOutObserved := explicitProxyAnswerOptOut(inputCaptionText)
			if coachObserved || proxyAnswerOptOutObserved {
				// Taint belongs to the provider session, not to the optional staged
				// handoff. ProcessLive and ProcessLiveWithEndpoint have no respondent
				// callback, so an interim proxy instruction may be observed even when
				// no handoff can open. Never release that provider's later output.
				providerAnswerTainted = true
			}
			// A direct proxy-answer refusal is an assistant-only control turn. It
			// may use the audited caption handoff without respondent checkpoint
			// authority, while coach opt-in still requires that authority.
			canDonateCaption := s.captionHandoff != nil &&
				(onCoachActive != nil || proxyAnswerOptOutObserved)
			if canDonateCaption &&
				(requiresStaged || coachObserved || proxyAnswerOptOutObserved ||
					stagedHandoff != nil) {
				if err := openStagedHandoff(); err != nil {
					pooled.session.DiscardOutput()
					event.Clear()
					clear(inputCaption)
					clear(caption)
					if errors.Is(err, httpapi.ErrVoiceNativeFallback) {
						return httpapi.VoiceTurnResult{}, err
					}
					return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
				}
				if err := stagedHandoff.Observe(
					bytes.Clone(inputCaption),
					event.CaptionFinal,
					observedAt,
				); err != nil {
					pooled.session.DiscardOutput()
					event.Clear()
					clear(inputCaption)
					clear(caption)
					return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
				}
			}
			if event.CaptionFinal && !inputGatePassed {
				var sendErr error
				select {
				case sendErr = <-sendDone:
				case <-ctx.Done():
					event.Clear()
					clear(inputCaption)
					clear(caption)
					return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
				}
				sendResultRead = true
				if sendErr != nil {
					pooled.session.DiscardOutput()
					event.Clear()
					clear(inputCaption)
					clear(caption)
					return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
				}
				if !nativeAudioEligible(inputCaptionText) {
					pooled.session.DiscardOutput()
					if stagedHandoff != nil {
						stagedHandoff.Cancel()
						stagedHandoff = nil
					}
					event.Clear()
					clear(inputCaption)
					clear(caption)
					return httpapi.VoiceTurnResult{}, httpapi.ErrVoiceNativeFallback
				}
				if coachObserved && onCoachActive == nil {
					pooled.session.DiscardOutput()
					if stagedHandoff != nil {
						stagedHandoff.Cancel()
						stagedHandoff = nil
					}
					event.Clear()
					clear(inputCaption)
					clear(caption)
					return httpapi.VoiceTurnResult{}, httpapi.ErrVoiceNativeFallback
				}
				if providerAnswerTainted && !requiresStaged && !coachObserved &&
					!proxyAnswerOptOutObserved {
					// A non-final caption already asked the provider to answer for the
					// person, or explicitly refused such an answer. If the cumulative
					// final caption is ordinary, the held provider output is still
					// tainted by that earlier instruction. Never commit it as ordinary
					// Native audio; replay the final turn through the staged fallback.
					pooled.session.DiscardOutput()
					if stagedHandoff != nil {
						stagedHandoff.Cancel()
						stagedHandoff = nil
					}
					event.Clear()
					clear(inputCaption)
					clear(caption)
					return httpapi.VoiceTurnResult{}, httpapi.ErrVoiceNativeFallback
				}
				if requiresStaged || coachObserved || proxyAnswerOptOutObserved {
					// A Native checkpoint can retain only a generic operator and
					// cannot prove which external question the next utterance
					// answers. Release no provider output. Donate the provider's
					// already-final caption to the staged planner, which binds the
					// operator, slots, and question-continuity tag without a second
					// speech-recognition pass.
					pooled.session.DiscardOutput()
					if stagedHandoff == nil {
						if err := openStagedHandoff(); err != nil {
							event.Clear()
							clear(inputCaption)
							clear(caption)
							if errors.Is(err, httpapi.ErrVoiceNativeFallback) {
								return httpapi.VoiceTurnResult{}, err
							}
							return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
						}
						if err := stagedHandoff.Observe(
							bytes.Clone(inputCaption),
							true,
							observedAt,
						); err != nil {
							event.Clear()
							clear(inputCaption)
							clear(caption)
							return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
						}
					}
					if !endpointPublished && onEndpoint != nil {
						endpointPublished = true
						onEndpoint()
					}
					respondentCheckpointAuthorized.Store(
						!proxyAnswerOptOutObserved &&
							(requiresStaged || coachObserved),
					)
					result, handoffErr := stagedHandoff.Commit()
					result.LiveTimings.STTFinalMS = millisecondsSince(
						started,
						inputCaptionFinalAt,
					)
					event.Clear()
					clear(inputCaption)
					clear(caption)
					if handoffErr != nil {
						return result, handoffErr
					}
					return result, nil
				}
				if stagedHandoff != nil {
					stagedHandoff.Cancel()
					stagedHandoff = nil
				}
				if err := pooled.session.CommitOutput(); err != nil {
					event.Clear()
					clear(inputCaption)
					clear(caption)
					return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
				}
				inputGatePassed = true
				clear(inputCaption)
				inputCaption = nil
				if !endpointPublished && onEndpoint != nil {
					endpointPublished = true
					onEndpoint()
				}
			}
		case nativevoice.EventOutputCaption:
			if !inputGatePassed {
				event.Clear()
				clear(inputCaption)
				clear(caption)
				return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
			}
			caption = mergeCaption(caption, event.CaptionUTF8)
			if !utf8.Valid(caption) || utf8.RuneCount(caption) > maxCaptionRunes {
				event.Clear()
				clear(inputCaption)
				clear(caption)
				return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
			}
		case nativevoice.EventAudioPCM:
			if !inputGatePassed ||
				event.SampleRateHertz != nativevoice.OutputSampleRateHertz ||
				len(event.PCM) == 0 || len(event.PCM)%2 != 0 {
				event.Clear()
				clear(inputCaption)
				clear(caption)
				return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
			}
			if err := deliverAudio(event.PCM); err != nil {
				event.Clear()
				clear(inputCaption)
				clear(caption)
				return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
			}
		case nativevoice.EventTurnComplete:
			if !inputGatePassed {
				event.Clear()
				clear(inputCaption)
				clear(caption)
				return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
			}
			turnComplete = true
		case nativevoice.EventInterrupted:
			event.Clear()
			clear(inputCaption)
			clear(caption)
			return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
		default:
			event.Clear()
			clear(inputCaption)
			clear(caption)
			return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
		}
		event.Clear()
	}

	if !sendResultRead {
		var sendErr error
		select {
		case sendErr = <-sendDone:
		case <-ctx.Done():
			clear(inputCaption)
			clear(caption)
			return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
		}
		if sendErr != nil {
			clear(inputCaption)
			clear(caption)
			return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
		}
	}
	if !inputGatePassed || !spoke || len(caption) == 0 {
		clear(inputCaption)
		clear(caption)
		return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
	}
	healthy = true
	result := httpapi.VoiceTurnResult{
		StateToken:       preparedStateToken,
		DetectedDomain:   "unknown",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		CoachPhase:       "none",
		CoachAction:      "none",
		ResearchStatus:   "none",
		ResearchRecords:  []httpapi.ResearchRecord{},
		Route:            nativevoice.RouteNativeAudio,
		Caption:          string(caption),
		LiveTimings: httpapi.VoiceLiveTimings{
			STTFirstInterimMS:   -1,
			STTFinalMS:          millisecondsSince(started, inputCaptionFinalAt),
			ConversationMS:      -1,
			TTSFirstChunkMS:     millisecondsSince(started, firstAudioAt),
			FinalToFirstAudioMS: millisecondsBetween(inputCaptionFinalAt, firstAudioAt),
			TTSReleaseMS:        -1,
		},
	}
	clear(inputCaption)
	clear(caption)
	return result, nil
}

func discardNativeInputUntilCommit(ctx context.Context, audio <-chan []byte) error {
	for {
		select {
		case frame, ok := <-audio:
			if !ok {
				return nil
			}
			clear(frame)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func streamInput(
	ctx context.Context,
	session nativevoice.Session,
	audio <-chan []byte,
	done chan<- error,
) {
	for frame := range audio {
		err := session.SendPCM20ms(ctx, frame)
		clear(frame)
		if err != nil {
			session.DiscardOutput()
			_ = session.Close()
			done <- err
			return
		}
	}
	if err := session.EndActivity(ctx); err != nil {
		session.DiscardOutput()
		_ = session.Close()
		done <- err
		return
	}
	done <- nil
}

func mergeCaption(current []byte, next []byte) []byte {
	if len(next) == 0 {
		return current
	}
	if len(current) <= len(next) && bytes.HasPrefix(next, current) {
		clear(current)
		return append([]byte(nil), next...)
	}
	if len(next) <= len(current) && bytes.HasPrefix(current, next) {
		return current
	}
	merged := make([]byte, 0, len(current)+len(next))
	merged = append(merged, current...)
	merged = append(merged, next...)
	clear(current)
	return merged
}

// requiresRespondentCoach delegates the consent boundary to the staged
// coach's single, audited parser so the native lane cannot drift into granting
// broader respondent authority.
func requiresRespondentCoach(value string) bool {
	return conversation.ExplicitCoachOptIn(value)
}

// explicitProxyAnswerOptOut delegates the refusal boundary to the same
// deterministic parser used by the staged agent. A refusal is not coach
// consent, but its provider output is still unsafe to publish: the provider
// may have prepared the very answer the person explicitly rejected.
func explicitProxyAnswerOptOut(value string) bool {
	return conversation.ExplicitProxyAnswerOptOut(value)
}

func nativeAudioEligible(value string) bool {
	value = strings.TrimSpace(norm.NFKC.String(value))
	if value == "" || privacyguard.HasHighConfidenceFinding(value) ||
		privacyguard.IsResearchRequest(value) {
		return false
	}
	lower := strings.ToLower(value)
	for _, signal := range []string{
		"死にたい", "自殺", "自傷", "殺したい", "救急", "意識がない",
		"胸が痛", "息ができない", "診断して", "薬の量", "服用量",
		"法律相談", "法的助言", "訴訟", "逮捕", "示談",
		"投資助言", "株を買", "暗号資産", "借金", "融資",
		"検索して", "調べて", "最新情報", "予約して", "購入して",
		"メールして", "送信して", "電話して",
		"suicide", "self-harm", "kill myself", "medical advice",
		"legal advice", "investment advice", "buy stock", "search the web",
		"book an appointment", "send an email",
	} {
		if strings.Contains(lower, signal) {
			return false
		}
	}
	return true
}

func millisecondsSince(start, value time.Time) int64 {
	if start.IsZero() || value.IsZero() {
		return -1
	}
	result := value.Sub(start).Milliseconds()
	if result < 0 {
		return -1
	}
	return result
}

func millisecondsBetween(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() {
		return -1
	}
	result := end.Sub(start).Milliseconds()
	if result < 0 {
		return -1
	}
	return result
}

func (s *Service) acquire(ctx context.Context, uid string) (*pooledSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.sessions[uid] != nil {
		s.mu.Unlock()
		return nil, errNativeFlowUnavailable
	}
	pooled := &pooledSession{}
	s.sessions[uid] = pooled
	s.mu.Unlock()

	session, err := s.opener.Open(ctx)
	if err != nil || ctx.Err() != nil {
		if session != nil {
			_ = session.Close()
		}
		s.mu.Lock()
		if s.sessions[uid] == pooled {
			delete(s.sessions, uid)
		}
		s.mu.Unlock()
		return nil, errNativeFlowUnavailable
	}
	s.mu.Lock()
	if s.sessions[uid] != pooled || s.ctx.Err() != nil {
		s.mu.Unlock()
		_ = session.Close()
		return nil, errNativeFlowUnavailable
	}
	pooled.session = session
	s.mu.Unlock()
	return pooled, nil
}

func (s *Service) release(uid string, pooled *pooledSession, healthy bool) {
	if pooled == nil {
		return
	}
	s.mu.Lock()
	if s.sessions[uid] != pooled {
		s.mu.Unlock()
		return
	}
	delete(s.sessions, uid)
	s.mu.Unlock()
	if pooled.session != nil {
		if !healthy {
			pooled.session.DiscardOutput()
		}
		_ = pooled.session.Close()
	}
}
