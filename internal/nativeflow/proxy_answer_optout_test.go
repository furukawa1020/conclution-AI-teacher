package nativeflow

import (
	"context"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
)

func TestNativeProxyAnswerOptOutDiscardsProviderAndDonatesFinalCaptionOnce(
	t *testing.T,
) {
	for _, caption := range []string{
		"代わりに答えないで",
		"回答を作らないで",
		"この回答を読み上げないで",
		"代わり に 答えないで",
		"回答を、作らないで",
	} {
		t.Run(caption, func(t *testing.T) {
			if requiresRespondentCoach(caption) ||
				!explicitProxyAnswerOptOut(caption) ||
				!nativeAudioEligible(caption) {
				t.Fatal("direct proxy opt-out must enter only the audited assistant handoff")
			}
			const providerGhost = "AIが本人の代わりに完成させた回答です。"
			session := newScriptedSession(
				nativeCaptionEvent(caption),
				nativevoice.Event{
					Kind:            nativevoice.EventAudioPCM,
					PCM:             []byte{9, 0, 9, 0},
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
					StateToken:       "assistant-opt-out-state",
					AssistanceTarget: "assistant",
					RespondentStage:  "none",
					CoachPhase:       "none",
					CoachAction:      "none",
					Route:            "fast",
					Caption:          "わかりました。代わりには答えません。",
				},
				audio: []byte{3, 0, 4, 0},
			}
			service, err := NewWithCaptionHandoff(
				opener,
				fakePreparer{token: "prepared-native-state"},
				handoffService,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()

			var delivered []byte
			checkpointCalls := 0
			endpointCalls := 0
			result, err := service.ProcessLiveWithControl(
				context.Background(),
				"uid-native-proxy-opt-out",
				nativeInput(),
				oneFrame(),
				func(chunk []byte) error {
					delivered = append(delivered, chunk...)
					return nil
				},
				func() { endpointCalls++ },
				func(httpapi.VoiceRespondentCheckpointTransition) error {
					checkpointCalls++
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.AssistanceTarget != "assistant" ||
				result.StateToken != "assistant-opt-out-state" ||
				result.Caption == providerGhost || result.Caption == caption ||
				string(delivered) != string([]byte{3, 0, 4, 0}) ||
				checkpointCalls != 0 || endpointCalls != 1 {
				t.Fatalf(
					"result=%+v audio=%v checkpoints=%d endpoints=%d",
					result,
					delivered,
					checkpointCalls,
					endpointCalls,
				)
			}
			if handoffService.openCount() != 1 || handoffService.lastHandoff == nil {
				t.Fatalf("handoff opens = %d", handoffService.openCount())
			}
			observations, handoffCommits, _ :=
				handoffService.lastHandoff.snapshot()
			if len(observations) != 1 || observations[0].caption != caption ||
				!observations[0].final || handoffCommits != 1 {
				t.Fatalf(
					"observations=%+v commits=%d",
					observations,
					handoffCommits,
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

func TestNativeIncrementalProxyWithdrawalCommitsOnlyFinalOptOut(
	t *testing.T,
) {
	const partialCaption = "代わりに答えて"
	const finalCaption = "代わりに答えて。でも今はやめて"
	if !requiresRespondentCoach(partialCaption) ||
		requiresRespondentCoach(finalCaption) ||
		!explicitProxyAnswerOptOut(finalCaption) {
		t.Fatal("test captions must cross from proxy opt-in to direct opt-out")
	}
	const providerGhost = "AIが本人の代わりに完成させた回答です。"
	session := newScriptedSession(
		nativevoice.Event{
			Kind:         nativevoice.EventInputCaption,
			CaptionUTF8:  []byte(partialCaption),
			CaptionFinal: false,
		},
		nativeCaptionEvent(finalCaption),
		nativevoice.Event{
			Kind:            nativevoice.EventAudioPCM,
			PCM:             []byte{9, 0, 9, 0},
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
			StateToken:       "withdrawn-assistant-state",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			CoachPhase:       "none",
			CoachAction:      "none",
			Route:            "fast",
			Caption:          "わかりました。代わりには答えません。",
		},
		audio: []byte{5, 0, 6, 0},
	}
	service, err := NewWithCaptionHandoff(
		opener,
		fakePreparer{token: "prepared-native-state"},
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
		"uid-native-incremental-proxy-opt-out",
		nativeInput(),
		oneFrame(),
		func(chunk []byte) error {
			delivered = append(delivered, chunk...)
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
	if result.AssistanceTarget != "assistant" || result.Caption == providerGhost ||
		string(delivered) != string([]byte{5, 0, 6, 0}) || checkpointCalls != 0 {
		t.Fatalf(
			"result=%+v audio=%v checkpoints=%d",
			result,
			delivered,
			checkpointCalls,
		)
	}
	if handoffService.openCount() != 1 || handoffService.lastHandoff == nil {
		t.Fatalf("handoff opens = %d", handoffService.openCount())
	}
	observations, handoffCommits, _ := handoffService.lastHandoff.snapshot()
	if len(observations) != 2 ||
		observations[0].caption != partialCaption || observations[0].final ||
		observations[1].caption != finalCaption || !observations[1].final ||
		handoffCommits != 1 {
		t.Fatalf(
			"observations=%+v commits=%d",
			observations,
			handoffCommits,
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

func TestNativeProxyAnswerOptOutWithoutCaptionHandoffFailsClosed(t *testing.T) {
	const caption = "代わりに答えないで"
	session := newScriptedSession(nativeCaptionEvent(caption))
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{token: "prepared-native-state"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	delivered := false
	_, err = service.ProcessLiveWithControl(
		context.Background(),
		"uid-native-opt-out-no-handoff",
		nativeInput(),
		oneFrame(),
		func([]byte) error {
			delivered = true
			return nil
		},
		nil,
		func(httpapi.VoiceRespondentCheckpointTransition) error { return nil },
	)
	if err != httpapi.ErrVoiceNativeFallback || delivered ||
		session.commits != 0 || session.discards == 0 {
		t.Fatalf(
			"err=%v delivered=%v commits=%d discards=%d",
			err,
			delivered,
			session.commits,
			session.discards,
		)
	}
}

func TestNativeProxyAnswerOptOutDonatesWithoutRespondentCallback(t *testing.T) {
	const caption = "回答を作らないで"
	session := newScriptedSession(nativeCaptionEvent(caption))
	handoffService := &recordingCaptionHandoffService{
		result: httpapi.VoiceTurnResult{
			StateToken:       "assistant-opt-out-state",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			CoachPhase:       "none",
			CoachAction:      "none",
			Route:            "fast",
			Caption:          "わかりました。回答は作りません。",
		},
	}
	service, err := NewWithCaptionHandoff(
		&fakeOpener{session: session},
		fakePreparer{token: "prepared-native-state"},
		handoffService,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	delivered := false
	result, err := service.ProcessLive(
		context.Background(),
		"uid-native-opt-out-no-respondent-callback",
		nativeInput(),
		oneFrame(),
		func([]byte) error {
			delivered = true
			return nil
		},
	)
	if err != nil || delivered || result.AssistanceTarget != "assistant" ||
		result.Caption != "わかりました。回答は作りません。" ||
		session.commits != 0 || session.discards == 0 ||
		handoffService.lastHandoff == nil {
		t.Fatalf(
			"result=%+v err=%v delivered=%v commits=%d discards=%d",
			result,
			err,
			delivered,
			session.commits,
			session.discards,
		)
	}
	observations, handoffCommits, _ := handoffService.lastHandoff.snapshot()
	if len(observations) != 1 || observations[0].caption != caption ||
		!observations[0].final || handoffCommits != 1 {
		t.Fatalf("observations=%+v commits=%d", observations, handoffCommits)
	}
}

func TestNativeProxyAnswerOptOutCannotAuthorizeRespondentCheckpoint(t *testing.T) {
	const caption = "代わりに答えないで"
	session := newScriptedSession(nativeCaptionEvent(caption))
	handoffService := &recordingCaptionHandoffService{
		result: httpapi.VoiceTurnResult{
			StateToken:       "must-not-be-published",
			AssistanceTarget: "respondent",
			RespondentStage:  "awaiting_answer",
			CoachPhase:       "awaiting_answer",
			CoachAction:      "elicit",
			Route:            httpapi.VoiceNativeRespondentCoachRoute,
		},
		audio: []byte{9, 0},
	}
	service, err := NewWithCaptionHandoff(
		&fakeOpener{session: session},
		fakePreparer{token: "prepared-native-state"},
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
		"uid-native-opt-out-hostile-handoff",
		nativeInput(),
		oneFrame(),
		func([]byte) error {
			delivered = true
			return nil
		},
		nil,
		func(httpapi.VoiceRespondentCheckpointTransition) error {
			checkpointCalls++
			return nil
		},
	)
	if err == nil || delivered || checkpointCalls != 0 ||
		session.commits != 0 || session.discards == 0 {
		t.Fatalf(
			"err=%v delivered=%v checkpoints=%d commits=%d discards=%d",
			err,
			delivered,
			checkpointCalls,
			session.commits,
			session.discards,
		)
	}
}

func TestNativeInterimProxyRequestTaintsProviderWithoutCaptionHandoff(
	t *testing.T,
) {
	const partialCaption = "代わりに答えて"
	const finalCaption = "代わりに答えて、と友達が言っていた"
	if !requiresRespondentCoach(partialCaption) ||
		requiresRespondentCoach(finalCaption) ||
		explicitProxyAnswerOptOut(finalCaption) {
		t.Fatal("test captions must cross from proxy request to reported speech")
	}
	session := newScriptedSession(
		nativevoice.Event{
			Kind:         nativevoice.EventInputCaption,
			CaptionUTF8:  []byte(partialCaption),
			CaptionFinal: false,
		},
		nativeCaptionEvent(finalCaption),
	)
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{token: "prepared-native-state"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	delivered := false
	_, err = service.ProcessLive(
		context.Background(),
		"uid-native-interim-proxy-no-handoff",
		nativeInput(),
		oneFrame(),
		func([]byte) error {
			delivered = true
			return nil
		},
	)
	if err != httpapi.ErrVoiceNativeFallback || delivered ||
		session.commits != 0 || session.discards == 0 || session.index != 2 {
		t.Fatalf(
			"err=%v delivered=%v commits=%d discards=%d events_read=%d",
			err,
			delivered,
			session.commits,
			session.discards,
			session.index,
		)
	}
}

func TestNativeInterimProxyOptOutTaintsProviderWithoutControlCallback(
	t *testing.T,
) {
	const partialCaption = "代わりに答えないで"
	const finalCaption = "代わりに答えないで、と友達が言っていた"
	if !explicitProxyAnswerOptOut(partialCaption) ||
		requiresRespondentCoach(finalCaption) ||
		explicitProxyAnswerOptOut(finalCaption) {
		t.Fatal("test captions must cross from proxy opt-out to reported speech")
	}
	session := newScriptedSession(
		nativevoice.Event{
			Kind:         nativevoice.EventInputCaption,
			CaptionUTF8:  []byte(partialCaption),
			CaptionFinal: false,
		},
		nativeCaptionEvent(finalCaption),
	)
	service, err := New(
		&fakeOpener{session: session},
		fakePreparer{token: "prepared-native-state"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	delivered := false
	endpointPublished := false
	_, err = service.ProcessLiveWithEndpoint(
		context.Background(),
		"uid-native-interim-opt-out-no-control",
		nativeInput(),
		oneFrame(),
		func([]byte) error {
			delivered = true
			return nil
		},
		func() { endpointPublished = true },
	)
	if err != httpapi.ErrVoiceNativeFallback || delivered || endpointPublished ||
		session.commits != 0 || session.discards == 0 || session.index != 2 {
		t.Fatalf(
			"err=%v delivered=%v endpoint=%v commits=%d discards=%d events_read=%d",
			err,
			delivered,
			endpointPublished,
			session.commits,
			session.discards,
			session.index,
		)
	}
}
