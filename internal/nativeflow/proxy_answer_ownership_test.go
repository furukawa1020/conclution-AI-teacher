package nativeflow

import (
	"context"
	"errors"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
)

func TestNativeProxyAnswerRequestsDiscardProviderAndDonateFinalCaptionOnce(
	t *testing.T,
) {
	for _, caption := range []string{
		"代わりに答えて",
		"面接の回答を作って",
		"私の代わりに「評価基準をそろえるためです」と答えて",
		"この答えをそのまま読み上げて",
	} {
		t.Run(caption, func(t *testing.T) {
			if !requiresRespondentCoach(caption) || !nativeAudioEligible(caption) {
				t.Fatal("proxy request must enter the owned staged handoff")
			}
			session := newScriptedSession(
				nativeCaptionEvent(caption),
				nativevoice.Event{
					Kind:            nativevoice.EventAudioPCM,
					PCM:             []byte{1, 2, 3, 4},
					SampleRateHertz: nativevoice.OutputSampleRateHertz,
				},
				nativevoice.Event{
					Kind:         nativevoice.EventOutputCaption,
					CaptionUTF8:  []byte("評価基準をそろえるためです。"),
					CaptionFinal: true,
				},
				nativevoice.Event{Kind: nativevoice.EventTurnComplete},
			)
			opener := &fakeOpener{session: session}
			handoffService := &recordingCaptionHandoffService{
				result: httpapi.VoiceTurnResult{
					StateToken:       "signed-proxy-answer-state",
					AssistanceTarget: "respondent",
					RespondentStage:  "awaiting_answer",
					CoachPhase:       "awaiting_answer",
					CoachAction:      "elicit",
					Route:            httpapi.VoiceNativeRespondentCoachRoute,
					Caption:          "まず一言で言うと？",
				},
				// A proxy request must not release either the provider's proposed
				// answer or staged TTS. The handoff opens only the person's A slot.
				audio: nil,
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

			audioCalls := 0
			checkpointCalls := 0
			result, err := service.ProcessLiveWithControl(
				context.Background(),
				"uid-native-proxy-answer",
				nativeInput(),
				oneFrame(),
				func([]byte) error {
					audioCalls++
					return nil
				},
				nil,
				func(httpapi.VoiceRespondentCheckpointTransition) error {
					checkpointCalls++
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Route != httpapi.VoiceNativeRespondentCoachRoute ||
				result.StateToken != "signed-proxy-answer-state" ||
				audioCalls != 0 || checkpointCalls != 1 {
				t.Fatalf(
					"result=%+v audio=%d checkpoints=%d",
					result,
					audioCalls,
					checkpointCalls,
				)
			}
			if handoffService.openCount() != 1 || handoffService.lastHandoff == nil {
				t.Fatalf("handoff opens = %d", handoffService.openCount())
			}
			observations, handoffCommits, handoffCancels :=
				handoffService.lastHandoff.snapshot()
			if len(observations) != 1 || observations[0].caption != caption ||
				!observations[0].final || handoffCommits != 1 || handoffCancels != 1 {
				t.Fatalf(
					"observations=%+v commits=%d cancels=%d",
					observations,
					handoffCommits,
					handoffCancels,
				)
			}
			session.mu.Lock()
			providerCommits := session.commits
			providerDiscards := session.discards
			providerEventsRead := session.index
			session.mu.Unlock()
			if opener.opens != 1 || providerCommits != 0 ||
				providerDiscards == 0 || providerEventsRead != 1 {
				t.Fatalf(
					"provider opens=%d commits=%d discards=%d events_read=%d",
					opener.opens,
					providerCommits,
					providerDiscards,
					providerEventsRead,
				)
			}
		})
	}
}

func TestNativeProxyAnswerNonConsentKeepsOrdinaryProviderRoute(t *testing.T) {
	for _, caption := range []string{
		"問題の答えを教えて",
		"母の回答を作って",
		"母の質問への回答を作って",
		"友達が「代わりに答えて」と言っていた",
		"代わりに答えないで",
		"代わりに答えてください。でも今はやめて",
	} {
		t.Run(caption, func(t *testing.T) {
			if requiresRespondentCoach(caption) {
				t.Fatal("non-consent caption must not enter the respondent handoff")
			}
			session := newScriptedSession(
				nativeCaptionEvent(caption),
				nativevoice.Event{
					Kind:            nativevoice.EventAudioPCM,
					PCM:             []byte{5, 0, 6, 0},
					SampleRateHertz: nativevoice.OutputSampleRateHertz,
				},
				nativevoice.Event{
					Kind:         nativevoice.EventOutputCaption,
					CaptionUTF8:  []byte("通常の会話として応答します。"),
					CaptionFinal: true,
				},
				nativevoice.Event{Kind: nativevoice.EventTurnComplete},
			)
			opener := &fakeOpener{session: session}
			handoffService := &recordingCaptionHandoffService{}
			service, err := NewWithCaptionHandoff(
				opener,
				fakePreparer{token: "ordinary-native-state"},
				handoffService,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()

			var delivered []byte
			checkpointCalls := 0
			result, err := service.ProcessLiveWithControl(
				context.Background(),
				"uid-native-non-proxy-answer",
				nativeInput(),
				oneFrame(),
				func(pcm []byte) error {
					delivered = append(delivered, pcm...)
					return nil
				},
				nil,
				func(httpapi.VoiceRespondentCheckpointTransition) error {
					checkpointCalls++
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Route != nativevoice.RouteNativeAudio ||
				result.Caption != "通常の会話として応答します。" ||
				result.StateToken != "ordinary-native-state" ||
				string(delivered) != string([]byte{5, 0, 6, 0}) ||
				checkpointCalls != 0 || handoffService.openCount() != 0 ||
				session.commits != 1 || session.discards != 0 {
				t.Fatalf(
					"result=%+v audio=%v checkpoints=%d handoff=%d provider_commits=%d provider_discards=%d",
					result,
					delivered,
					checkpointCalls,
					handoffService.openCount(),
					session.commits,
					session.discards,
				)
			}
		})
	}
}

func TestNativeRetractedIncrementalProxyCaptionNeverReleasesHeldProviderAnswer(
	t *testing.T,
) {
	const partialCaption = "代わりに答えて"
	const finalCaption = "代わりに答えて。でも今はやめて"
	if !requiresRespondentCoach(partialCaption) ||
		requiresRespondentCoach(finalCaption) {
		t.Fatal("test captions must cross then retract the proxy boundary")
	}
	session := newScriptedSession(
		nativevoice.Event{
			Kind:         nativevoice.EventInputCaption,
			CaptionUTF8:  []byte(partialCaption),
			CaptionFinal: false,
		},
		nativeCaptionEvent(finalCaption),
		nativevoice.Event{
			Kind:            nativevoice.EventAudioPCM,
			PCM:             []byte{7, 0, 8, 0},
			SampleRateHertz: nativevoice.OutputSampleRateHertz,
		},
		nativevoice.Event{
			Kind:         nativevoice.EventOutputCaption,
			CaptionUTF8:  []byte("評価基準をそろえるためです。"),
			CaptionFinal: true,
		},
		nativevoice.Event{Kind: nativevoice.EventTurnComplete},
	)
	opener := &fakeOpener{session: session}
	handoffService := &recordingCaptionHandoffService{
		result: httpapi.VoiceTurnResult{
			StateToken:       "must-not-be-committed",
			AssistanceTarget: "respondent",
			Route:            httpapi.VoiceNativeRespondentCoachRoute,
		},
		audio: []byte{9, 0},
	}
	service, err := NewWithCaptionHandoff(
		opener,
		fakePreparer{token: "ordinary-native-state"},
		handoffService,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	audioCalls := 0
	checkpointCalls := 0
	result, err := service.ProcessLiveWithControl(
		context.Background(),
		"uid-native-retracted-incremental-proxy",
		nativeInput(),
		oneFrame(),
		func([]byte) error {
			audioCalls++
			return nil
		},
		nil,
		func(httpapi.VoiceRespondentCheckpointTransition) error {
			checkpointCalls++
			return nil
		},
	)
	if !errors.Is(err, httpapi.ErrVoiceNativeFallback) ||
		result.Route != "" || result.StateToken != "" || result.Caption != "" ||
		audioCalls != 0 ||
		checkpointCalls != 0 {
		t.Fatalf(
			"result=%+v err=%v audio=%d checkpoints=%d",
			result,
			err,
			audioCalls,
			checkpointCalls,
		)
	}
	if handoffService.openCount() != 1 || handoffService.lastHandoff == nil {
		t.Fatalf("handoff opens = %d", handoffService.openCount())
	}
	observations, handoffCommits, handoffCancels :=
		handoffService.lastHandoff.snapshot()
	if len(observations) != 2 ||
		observations[0].caption != partialCaption || observations[0].final ||
		observations[1].caption != finalCaption || !observations[1].final ||
		handoffCommits != 0 || handoffCancels != 1 {
		t.Fatalf(
			"observations=%+v commits=%d cancels=%d",
			observations,
			handoffCommits,
			handoffCancels,
		)
	}
	session.mu.Lock()
	providerCommits := session.commits
	providerDiscards := session.discards
	providerEventsRead := session.index
	session.mu.Unlock()
	if opener.opens != 1 || providerCommits != 0 || providerDiscards == 0 ||
		providerEventsRead != 2 {
		t.Fatalf(
			"provider opens=%d commits=%d discards=%d events_read=%d",
			opener.opens,
			providerCommits,
			providerDiscards,
			providerEventsRead,
		)
	}
}
