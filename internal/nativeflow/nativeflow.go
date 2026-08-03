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
	"time"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
	"golang.org/x/text/unicode/norm"
)

const (
	maxCaptionRunes            = 480
	routeNativeRespondentCoach = "native-respondent-coach"
)

// defaultCompanionSystemPrompt defines the fast companion lane. It
// intentionally has no tools, exercise scoring, diagnosis, or pressure to
// leave home.
const defaultCompanionSystemPrompt = `あなたはKOTAEの日本語音声会話相手です。
最優先は、家にいる時間が長い人や人との会話に不安がある人でも、話題や練習の準備なしに安心して話せることです。
返答は前置きや「考えます」を挟まず、相手の発話の中心に関係する意味のある言葉からすぐ始めてください。
基本は自然な日本語で短い1〜2文、質問は最大1つです。相手が短い、曖昧、沈黙気味なら失敗扱いせず、あなたが具体的で答えやすい話題を一つ持ってください。
訓練、採点、評価、診断として見せず、長くまとまらない発話には中心を先に返してください。
外出、学校、仕事、家族や支援者への相談を本人の依頼なしに目標化しないでください。依存を促さず、治療者や唯一の味方を名乗らないでください。
この経路には検索、PDF、購入、予約などのツールがありません。実行した、調べた、確認したと主張しないでください。
氏名、連絡先、認証情報を復唱しないでください。`

// DefaultSystemPrompt keeps ordinary conversation low-pressure and gives the
// Native Audio model one narrow Respondent Coach behavior. The deterministic
// caption gate and signed generic coach state remain authoritative; this
// prompt controls only the already-gated audio wording.
const DefaultSystemPrompt = defaultCompanionSystemPrompt + `

人から聞かれた質問への答え方を本人が明示的に手伝ってほしいと頼んだ場合だけ、答えを代作したり模範回答を提示したりしないでください。
その場合の返答は「まず、あなた自身が一番伝えたいことは何ですか？」の一文だけにしてください。
評価、採点、訓練という言い方はせず、質問を重ねず、本人の答えを推測しないでください。
明示的な依頼がない通常会話では、この回答支援を始めないでください。`

var errNativeFlowUnavailable = errors.New("native audio flow unavailable")

type pooledSession struct {
	session nativevoice.Session
}

// Service implements only the live service contract. Buffered audio, PDF,
// strict mode, tools, and research continue through the audited legacy flow.
type Service struct {
	opener        nativevoice.Opener
	preparer      conversation.NativeStatePreparer
	coachPreparer conversation.NativeCoachStatePreparer

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
	if opener == nil || preparer == nil {
		return nil, errors.New("native audio dependencies are required")
	}
	coachPreparer, ok := preparer.(conversation.NativeCoachStatePreparer)
	if !ok {
		return nil, errors.New("native coach state boundary is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		opener:        opener,
		preparer:      preparer,
		coachPreparer: coachPreparer,
		ctx:           ctx,
		cancel:        cancel,
		now:           time.Now,
		sessions:      make(map[string]*pooledSession),
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

// ProcessLiveWithControl announces an explicitly requested Respondent Coach
// turn after the provider's final input caption is accepted but before any
// held Native Audio is committed. The transport callback is synchronous so a
// client always observes the control frame before the first binary response.
func (s *Service) ProcessLiveWithControl(
	ctx context.Context,
	uid string,
	input httpapi.VoiceTurnInput,
	audio <-chan []byte,
	onAudio func([]byte) error,
	onEndpoint func(),
	onCoachActive func(sessionState string) error,
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
	onCoachActive func(sessionState string) error,
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
	if requiresStaged {
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
	coachActive := false
	genericCoachStateToken := ""
	coachStateMS := int64(-1)
	sendResultRead := false
	spoke := false
	turnComplete := false
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
			if event.CaptionFinal && inputCaptionFinalAt.IsZero() {
				inputCaptionFinalAt = s.now()
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
				inputCaptionText := string(inputCaption)
				if !nativeAudioEligible(inputCaptionText) {
					pooled.session.DiscardOutput()
					event.Clear()
					clear(inputCaption)
					clear(caption)
					return httpapi.VoiceTurnResult{}, httpapi.ErrVoiceNativeFallback
				}
				if requiresRespondentCoach(inputCaptionText) {
					coachStateStartedAt := s.now()
					genericCoachStateToken, err =
						s.coachPreparer.PrepareNativeCoachState(
							uid,
							input.StateToken,
							inputCaptionText,
						)
					coachStateMS = millisecondsBetween(
						coachStateStartedAt,
						s.now(),
					)
					if err != nil || genericCoachStateToken == "" ||
						len(genericCoachStateToken) > conversation.MaxStateTokenBytes {
						pooled.session.DiscardOutput()
						event.Clear()
						clear(inputCaption)
						clear(caption)
						return httpapi.VoiceTurnResult{}, httpapi.ErrVoiceNativeFallback
					}
					coachActive = true
					if onCoachActive != nil {
						if err := onCoachActive(genericCoachStateToken); err != nil {
							pooled.session.DiscardOutput()
							event.Clear()
							clear(inputCaption)
							clear(caption)
							return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
						}
					}
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
			if firstAudioAt.IsZero() {
				firstAudioAt = s.now()
			}
			if err := onAudio(event.PCM); err != nil {
				event.Clear()
				clear(inputCaption)
				clear(caption)
				return httpapi.VoiceTurnResult{}, errNativeFlowUnavailable
			}
			spoke = true
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
	stateToken := preparedStateToken
	detectedDomain := "unknown"
	assistanceTarget := "assistant"
	respondentStage := "none"
	coachPhase := "none"
	coachAction := "none"
	route := nativevoice.RouteNativeAudio
	conversationMS := int64(-1)
	if coachActive {
		stateToken = genericCoachStateToken
		detectedDomain = "daily"
		assistanceTarget = "respondent"
		respondentStage = "awaiting_answer"
		coachPhase = "awaiting_answer"
		coachAction = "elicit"
		route = routeNativeRespondentCoach
		conversationMS = coachStateMS
	}
	result := httpapi.VoiceTurnResult{
		StateToken:       stateToken,
		DetectedDomain:   detectedDomain,
		AssistanceTarget: assistanceTarget,
		RespondentStage:  respondentStage,
		CoachPhase:       coachPhase,
		CoachAction:      coachAction,
		ResearchStatus:   "none",
		ResearchRecords:  []httpapi.ResearchRecord{},
		Route:            route,
		Caption:          string(caption),
		LiveTimings: httpapi.VoiceLiveTimings{
			STTFirstInterimMS:   -1,
			STTFinalMS:          millisecondsSince(started, inputCaptionFinalAt),
			ConversationMS:      conversationMS,
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
