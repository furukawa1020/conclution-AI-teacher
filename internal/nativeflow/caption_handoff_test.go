package nativeflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
)

type recordedCaptionObservation struct {
	caption string
	final   bool
	at      time.Time
}

type recordingCaptionHandoffService struct {
	mu sync.Mutex

	opens       int
	result      httpapi.VoiceTurnResult
	audio       []byte
	openErr     error
	commitErr   error
	lastHandoff *recordingCaptionHandoff
}

func (service *recordingCaptionHandoffService) OpenCaptionHandoff(
	_ context.Context,
	_ string,
	_ httpapi.VoiceTurnInput,
	onAudio func([]byte) error,
	onCoachActive func(httpapi.VoiceRespondentCheckpoint) error,
) (httpapi.VoiceCaptionHandoff, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.opens++
	if service.openErr != nil {
		return nil, service.openErr
	}
	handoff := &recordingCaptionHandoff{
		service:       service,
		onAudio:       onAudio,
		onCoachActive: onCoachActive,
	}
	service.lastHandoff = handoff
	return handoff, nil
}

func (service *recordingCaptionHandoffService) openCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.opens
}

type recordingCaptionHandoff struct {
	service       *recordingCaptionHandoffService
	onAudio       func([]byte) error
	onCoachActive func(httpapi.VoiceRespondentCheckpoint) error

	mu           sync.Mutex
	observations []recordedCaptionObservation
	commits      int
	cancels      int
}

func (handoff *recordingCaptionHandoff) Observe(
	captionUTF8 []byte,
	final bool,
	observedAt time.Time,
) error {
	defer clear(captionUTF8)
	handoff.mu.Lock()
	handoff.observations = append(
		handoff.observations,
		recordedCaptionObservation{
			caption: string(captionUTF8),
			final:   final,
			at:      observedAt,
		},
	)
	handoff.mu.Unlock()
	return nil
}

func (handoff *recordingCaptionHandoff) Commit() (
	httpapi.VoiceTurnResult,
	error,
) {
	handoff.mu.Lock()
	handoff.commits++
	handoff.mu.Unlock()

	handoff.service.mu.Lock()
	result := handoff.service.result
	audio := append([]byte(nil), handoff.service.audio...)
	commitErr := handoff.service.commitErr
	handoff.service.mu.Unlock()
	if commitErr != nil {
		return result, commitErr
	}
	if result.AssistanceTarget == "respondent" {
		if handoff.onCoachActive == nil {
			return result, errors.New("missing coach checkpoint callback")
		}
		if err := handoff.onCoachActive(httpapi.VoiceRespondentCheckpoint{
			SessionState:     result.StateToken,
			Route:            result.Route,
			AssistanceTarget: result.AssistanceTarget,
			RespondentStage:  result.RespondentStage,
			CoachPhase:       result.CoachPhase,
			CoachAction:      result.CoachAction,
		}); err != nil {
			return result, err
		}
	}
	if len(audio) != 0 {
		if err := handoff.onAudio(audio); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (handoff *recordingCaptionHandoff) Cancel() {
	handoff.mu.Lock()
	handoff.cancels++
	handoff.mu.Unlock()
}

func (handoff *recordingCaptionHandoff) snapshot() (
	[]recordedCaptionObservation,
	int,
	int,
) {
	handoff.mu.Lock()
	defer handoff.mu.Unlock()
	return append([]recordedCaptionObservation(nil), handoff.observations...),
		handoff.commits,
		handoff.cancels
}

func TestNativeExplicitCoachDonatesFinalCaptionWithoutASecondRecognizerPass(
	t *testing.T,
) {
	const caption = "My manager asked me why the change was needed. How should I answer?"
	if !requiresRespondentCoach(caption) || !nativeAudioEligible(caption) {
		t.Fatal("test caption must enter the explicit respondent handoff")
	}
	session := newScriptedSession(nativeCaptionEvent(caption))
	opener := &fakeOpener{session: session}
	handoffService := &recordingCaptionHandoffService{
		result: httpapi.VoiceTurnResult{
			StateToken:       "signed-question-bound-state",
			AssistanceTarget: "respondent",
			RespondentStage:  "awaiting_answer",
			CoachPhase:       "awaiting_answer",
			CoachAction:      "elicit",
			Route:            httpapi.VoiceNativeRespondentCoachRoute,
			Caption:          "Lead with the purpose.",
			LiveTimings: httpapi.VoiceLiveTimings{
				NativeCaptionHandoff: 1,
			},
		},
		audio: []byte{3, 0, 4, 0},
	}
	service, err := NewWithCaptionHandoff(
		opener,
		fakePreparer{token: "advanced-native-state"},
		handoffService,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	checkpointAccepted := false
	checkpointCalls := 0
	endpointCalls := 0
	var delivered []byte
	result, err := service.ProcessLiveWithControl(
		context.Background(),
		"uid-native-caption-donation",
		nativeInput(),
		oneFrame(),
		func(chunk []byte) error {
			if !checkpointAccepted {
				return errors.New("handoff audio crossed before checkpoint")
			}
			delivered = append(delivered, chunk...)
			return nil
		},
		func() { endpointCalls++ },
		func(transition httpapi.VoiceRespondentCheckpointTransition) error {
			checkpointCalls++
			checkpoint := transition.Checkpoint
			if transition.PreviousSessionState != "advanced-native-state" {
				return errors.New("unexpected prepared state")
			}
			if checkpoint.SessionState != "signed-question-bound-state" ||
				checkpoint.AssistanceTarget != "respondent" ||
				checkpoint.RespondentStage != "awaiting_answer" ||
				checkpoint.CoachPhase != "awaiting_answer" ||
				checkpoint.CoachAction != "elicit" {
				return errors.New("unexpected checkpoint")
			}
			checkpointAccepted = true
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Route != httpapi.VoiceNativeRespondentCoachRoute ||
		result.StateToken != "signed-question-bound-state" ||
		result.LiveTimings.NativeCaptionHandoff != 1 {
		t.Fatalf("result = %+v", result)
	}
	if checkpointCalls != 1 || endpointCalls != 1 ||
		string(delivered) != string([]byte{3, 0, 4, 0}) {
		t.Fatalf(
			"checkpoint=%d endpoint=%d delivered=%v",
			checkpointCalls,
			endpointCalls,
			delivered,
		)
	}
	if handoffService.openCount() != 1 || handoffService.lastHandoff == nil {
		t.Fatalf("handoff opens = %d", handoffService.openCount())
	}
	observations, commits, _ := handoffService.lastHandoff.snapshot()
	if len(observations) != 1 || observations[0].caption != caption ||
		!observations[0].final || observations[0].at.IsZero() || commits != 1 {
		t.Fatalf("observations=%+v commits=%d", observations, commits)
	}
	if opener.opens != 1 || session.commits != 0 || session.discards == 0 ||
		session.frames != 1 {
		t.Fatalf(
			"provider opens=%d commits=%d discards=%d frames=%d",
			opener.opens,
			session.commits,
			session.discards,
			session.frames,
		)
	}
}

func TestNativeProxyFinalCaptionDiscardsProviderGhostBeforeStagedHandoff(t *testing.T) {
	for index, caption := range []string{
		"代わりに答えて",
		"回答を作って",
		"この答えをそのまま読んで",
	} {
		t.Run(caption, func(t *testing.T) {
			const providerGhost = "AIが本人の代わりに完成させた回答です。"
			session := newScriptedSession(
				nativevoice.Event{
					Kind:         nativevoice.EventInputCaption,
					CaptionUTF8:  []byte(caption),
					CaptionFinal: true,
				},
				nativevoice.Event{
					Kind:            nativevoice.EventAudioPCM,
					PCM:             []byte{9, 9, 9, 9},
					SampleRateHertz: nativevoice.OutputSampleRateHertz,
				},
				nativevoice.Event{
					Kind:         nativevoice.EventOutputCaption,
					CaptionUTF8:  []byte(providerGhost),
					CaptionFinal: true,
				},
				nativevoice.Event{Kind: nativevoice.EventTurnComplete},
			)
			opener := &fakeOpener{session: session}
			handoffService := &recordingCaptionHandoffService{
				result: httpapi.VoiceTurnResult{
					StateToken:       "proxy-owned-state",
					AssistanceTarget: "respondent",
					RespondentStage:  "awaiting_answer",
					CoachPhase:       "awaiting_answer",
					CoachAction:      "elicit",
					Route:            httpapi.VoiceNativeRespondentCoachRoute,
					Caption:          "今ある自分の答えを、一言だけそのままどうぞ。",
				},
				audio: []byte{3, 0, 4, 0},
			}
			service, err := NewWithCaptionHandoff(
				opener,
				fakePreparer{token: "prepared-proxy-state"},
				handoffService,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()

			checkpointAccepted := false
			var delivered []byte
			result, err := service.ProcessLiveWithControl(
				context.Background(),
				"uid-native-proxy-handoff-"+string(rune('a'+index)),
				nativeInput(),
				oneFrame(),
				func(chunk []byte) error {
					if !checkpointAccepted {
						return errors.New("staged proxy audio crossed before checkpoint")
					}
					delivered = append(delivered, chunk...)
					return nil
				},
				nil,
				func(httpapi.VoiceRespondentCheckpointTransition) error {
					checkpointAccepted = true
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Route != httpapi.VoiceNativeRespondentCoachRoute ||
				result.Caption == providerGhost ||
				string(delivered) != string([]byte{3, 0, 4, 0}) ||
				session.commits != 0 || session.discards == 0 ||
				handoffService.openCount() != 1 || handoffService.lastHandoff == nil {
				t.Fatalf(
					"result=%+v delivered=%v commits=%d discards=%d opens=%d",
					result,
					delivered,
					session.commits,
					session.discards,
					handoffService.openCount(),
				)
			}
			observations, commits, _ := handoffService.lastHandoff.snapshot()
			if len(observations) != 1 || observations[0].caption != caption ||
				!observations[0].final || commits != 1 {
				t.Fatalf("observations=%+v commits=%d", observations, commits)
			}
		})
	}
}

func TestNativeActiveStagedStateUsesFinalCaptionWithoutRecordingReplay(
	t *testing.T,
) {
	const caption = "目的は評価基準をそろえることです"
	opener := &fakeOpener{session: newScriptedSession(nativeCaptionEvent(caption))}
	handoffService := &recordingCaptionHandoffService{
		result: httpapi.VoiceTurnResult{
			StateToken:       "next-question-bound-state",
			AssistanceTarget: "respondent",
			RespondentStage:  "awaiting_answer",
			CoachPhase:       "awaiting_answer",
			CoachAction:      "elicit",
			Route:            "native-respondent-coach",
		},
	}
	service, err := NewWithCaptionHandoff(
		opener,
		fakePreparer{token: "unchanged-state", requiresStaged: true},
		handoffService,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	delivered := false
	checkpointCalls := 0
	result, err := service.ProcessLiveWithControl(
		context.Background(),
		"uid-existing-staged-state",
		nativeInput(),
		oneFrame(),
		func([]byte) error { delivered = true; return nil },
		nil,
		func(transition httpapi.VoiceRespondentCheckpointTransition) error {
			checkpointCalls++
			if transition.PreviousSessionState != "unchanged-state" {
				return errors.New("unexpected pending state")
			}
			return nil
		},
	)
	if err != nil || delivered || result.StateToken != "next-question-bound-state" ||
		checkpointCalls != 1 || opener.opens != 1 ||
		handoffService.openCount() != 1 || handoffService.lastHandoff == nil {
		t.Fatalf(
			"result=%+v err=%v delivered=%v checkpoint=%d provider=%d handoff=%d",
			result,
			err,
			delivered,
			checkpointCalls,
			opener.opens,
			handoffService.openCount(),
		)
	}
	observations, commits, _ := handoffService.lastHandoff.snapshot()
	if len(observations) != 1 || observations[0].caption != caption ||
		!observations[0].final || commits != 1 {
		t.Fatalf("observations=%+v commits=%d", observations, commits)
	}
}

func TestNativeSafetyAndResearchCaptionsRemainStagedFallback(t *testing.T) {
	for _, caption := range []string{
		"I want to kill myself",
		"Search the web for the latest speech recognition benchmark",
	} {
		t.Run(caption, func(t *testing.T) {
			if nativeAudioEligible(caption) {
				t.Fatal("test caption unexpectedly entered Native Audio")
			}
			session := newScriptedSession(nativeCaptionEvent(caption))
			opener := &fakeOpener{session: session}
			handoffService := &recordingCaptionHandoffService{}
			service, err := NewWithCaptionHandoff(
				opener,
				fakePreparer{
					token:          "advanced-native-state",
					requiresStaged: true,
				},
				handoffService,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()

			delivered := false
			checkpointCalls := 0
			_, err = service.ProcessLiveWithControl(
				context.Background(),
				"uid-native-risk-fallback",
				nativeInput(),
				oneFrame(),
				func([]byte) error { delivered = true; return nil },
				nil,
				func(httpapi.VoiceRespondentCheckpointTransition) error {
					checkpointCalls++
					return nil
				},
			)
			var observations []recordedCaptionObservation
			handoffCommits := 0
			handoffCancels := 0
			if handoffService.lastHandoff != nil {
				observations, handoffCommits, handoffCancels =
					handoffService.lastHandoff.snapshot()
			}
			if !errors.Is(err, httpapi.ErrVoiceNativeFallback) || delivered ||
				checkpointCalls != 0 || session.commits != 0 ||
				session.discards == 0 || handoffService.openCount() != 1 ||
				len(observations) != 1 || observations[0].caption != caption ||
				!observations[0].final || handoffCommits != 0 || handoffCancels != 1 {
				t.Fatalf(
					"err=%v delivered=%v checkpoint=%d commits=%d discards=%d handoff_opens=%d observations=%+v handoff_commits=%d handoff_cancels=%d",
					err,
					delivered,
					checkpointCalls,
					session.commits,
					session.discards,
					handoffService.openCount(),
					observations,
					handoffCommits,
					handoffCancels,
				)
			}
		})
	}
}

func nativeCaptionEvent(caption string) nativevoice.Event {
	return nativevoice.Event{
		Kind:         nativevoice.EventInputCaption,
		CaptionUTF8:  []byte(caption),
		CaptionFinal: true,
	}
}
