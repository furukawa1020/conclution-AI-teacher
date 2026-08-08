package voiceflow

import (
	"context"
	"errors"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type scriptedInspector struct {
	statuses []privacyguard.InspectionStatus
	err      error
	calls    []string
}

func (inspector *scriptedInspector) Inspect(
	_ context.Context,
	text string,
) (privacyguard.Inspection, error) {
	inspector.calls = append(inspector.calls, text)
	if inspector.err != nil {
		return privacyguard.Inspection{}, inspector.err
	}
	if len(inspector.statuses) == 0 {
		return privacyguard.Inspection{}, nil
	}
	status := inspector.statuses[0]
	inspector.statuses = inspector.statuses[1:]
	return privacyguard.Inspection{Status: status}, nil
}

type countedSpeech struct {
	fakeSpeech
	transcribeCalls int
	synthesizeCalls int
}

func (speech *countedSpeech) Transcribe(
	ctx context.Context,
	audio []byte,
) (string, float32, error) {
	speech.transcribeCalls++
	return speech.fakeSpeech.Transcribe(ctx, audio)
}

func (speech *countedSpeech) Synthesize(
	ctx context.Context,
	text string,
) ([]byte, string, error) {
	speech.synthesizeCalls++
	return speech.fakeSpeech.Synthesize(ctx, text)
}

func safePrivacyDecision() conversation.VoiceTurnResult {
	return conversation.VoiceTurnResult{
		Domain:           "general",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		CoachPhase:       "none",
		CoachAction:      "none",
		ResearchStatus:   "none",
		ResearchRecords:  []conversation.ResearchRecord{},
		Route:            "fast",
		StateToken:       "server-readable-state",
		SpokenReply:      "結論から言うと、今日は休みます。",
	}
}

func assertStrictBlocked(t *testing.T, result httpapi.VoiceTurnResult) {
	t.Helper()
	if result.Route != routeStrictPrivacyBlocked ||
		result.PrivacyStatus != privacyStatusBlocked ||
		len(result.Audio) != 0 || result.AudioMIMEType != "" ||
		result.Caption != "" || result.StateToken != "" ||
		len(result.ResearchRecords) != 0 {
		t.Fatalf("strict result leaked output: %+v", result)
	}
}

func TestStrictInputStateAndPDFBlockBeforeSpeech(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		input    httpapi.VoiceTurnInput
		document bool
	}{
		{
			name: "state",
			input: httpapi.VoiceTurnInput{
				Audio:                   []byte("audio"),
				StateToken:              "state",
				StrictCloudMinimization: true,
			},
		},
		{
			name: "document",
			input: httpapi.VoiceTurnInput{
				Audio: []byte("audio"),
				Document: &httpapi.VoiceDocument{
					MIMEType: "application/pdf",
					Data:     []byte("%PDF-private"),
				},
				StrictCloudMinimization: true,
			},
			document: true,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			speech := &countedSpeech{fakeSpeech: fakeSpeech{transcript: "safe", confidence: 0.99}}
			agent := &fakeAgent{result: safePrivacyDecision()}
			pipeline, err := NewWithPrivacy(speech, agent, &scriptedInspector{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := pipeline.Process(context.Background(), "uid", test.input)
			if err != nil {
				t.Fatal(err)
			}
			if test.document {
				assertRuntimeDocumentRejected(t, result)
			} else {
				assertStrictBlocked(t, result)
			}
			if speech.transcribeCalls != 0 || speech.synthesizeCalls != 0 || agent.calls != 0 {
				t.Fatalf("blocked input crossed a service boundary: stt=%d tts=%d agent=%d", speech.transcribeCalls, speech.synthesizeCalls, agent.calls)
			}
			if test.input.Document != nil && len(test.input.Document.Data) != 0 {
				t.Fatal("blocked PDF bytes were not cleared")
			}
		})
	}
}

func TestStrictFindingAndInspectorFailureStopBeforeAgent(t *testing.T) {
	t.Parallel()
	for _, inspector := range []*scriptedInspector{
		{statuses: []privacyguard.InspectionStatus{privacyguard.InspectionFinding}},
		{err: errors.New("provider unavailable")},
	} {
		speech := &countedSpeech{fakeSpeech: fakeSpeech{transcript: "文脈から識別できる内容", confidence: 0.99}}
		agent := &fakeAgent{result: safePrivacyDecision()}
		pipeline, err := NewWithPrivacy(speech, agent, inspector)
		if err != nil {
			t.Fatal(err)
		}
		result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
			Audio:                   []byte("audio"),
			StrictCloudMinimization: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertStrictBlocked(t, result)
		if agent.calls != 0 || speech.synthesizeCalls != 0 {
			t.Fatalf("unsafe transcript continued: agent=%d tts=%d", agent.calls, speech.synthesizeCalls)
		}
	}
}

func TestStrictLowConfidenceStillFailsClosedBeforeTTS(t *testing.T) {
	t.Parallel()
	for _, inspector := range []*scriptedInspector{
		{statuses: []privacyguard.InspectionStatus{privacyguard.InspectionFinding}},
		{err: errors.New("provider unavailable")},
	} {
		speech := &countedSpeech{fakeSpeech: fakeSpeech{
			transcript: "低信頼でも個人情報かもしれない内容",
			confidence: 0.1,
		}}
		agent := &fakeAgent{result: safePrivacyDecision()}
		pipeline, err := NewWithPrivacy(speech, agent, inspector)
		if err != nil {
			t.Fatal(err)
		}
		result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
			Audio:                   []byte("audio"),
			StrictCloudMinimization: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertStrictBlocked(t, result)
		if agent.calls != 0 || speech.synthesizeCalls != 0 {
			t.Fatalf("low-confidence private text continued: agent=%d tts=%d", agent.calls, speech.synthesizeCalls)
		}
	}
}

func TestStrictClearTurnInspectsBothSidesAndDropsState(t *testing.T) {
	t.Parallel()
	inspector := &scriptedInspector{statuses: []privacyguard.InspectionStatus{
		privacyguard.InspectionClear,
		privacyguard.InspectionClear,
	}}
	speech := &countedSpeech{fakeSpeech: fakeSpeech{transcript: "今日は休みます", confidence: 0.99}}
	agent := &fakeAgent{result: safePrivacyDecision()}
	pipeline, err := NewWithPrivacy(speech, agent, inspector)
	if err != nil {
		t.Fatal(err)
	}
	result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
		Audio:                   []byte("audio"),
		StrictCloudMinimization: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inspector.calls) != 2 || agent.calls != 1 ||
		!agent.turn.ResearchDisabled || result.StateToken != "" ||
		result.PrivacyStatus != "clear" || speech.synthesizeCalls != 1 ||
		result.Caption != safePrivacyDecision().SpokenReply {
		t.Fatalf("strict clear path mismatch: result=%+v calls=%#v turn=%+v", result, inspector.calls, agent.turn)
	}
}

func TestOrdinaryPDFRejectsBeforeSpeechAndDoesNotExposeState(t *testing.T) {
	t.Parallel()
	inspector := &scriptedInspector{}
	speech := &countedSpeech{fakeSpeech: fakeSpeech{transcript: "このPDFを説明して", confidence: 0.99}}
	agent := &fakeAgent{result: safePrivacyDecision()}
	pipeline, err := NewWithPrivacy(speech, agent, inspector)
	if err != nil {
		t.Fatal(err)
	}
	document := &httpapi.VoiceDocument{MIMEType: "application/pdf", Data: []byte("%PDF-public")}
	result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
		Audio:      []byte("audio"),
		StateToken: "ordinary-state",
		Document:   document,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRuntimeDocumentRejected(t, result)
	if speech.transcribeCalls != 0 || speech.synthesizeCalls != 0 ||
		agent.calls != 0 || len(inspector.calls) != 0 || len(document.Data) != 0 {
		t.Fatalf(
			"ordinary PDF crossed a service boundary: stt=%d agent=%d tts=%d inspector=%d doc=%d",
			speech.transcribeCalls,
			agent.calls,
			speech.synthesizeCalls,
			len(inspector.calls),
			len(document.Data),
		)
	}
}

func TestStrictLiveChecksFinalTextAndNeverSpeculates(t *testing.T) {
	t.Parallel()
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{Kind: speechio.StreamingTranscriptionInterim, Text: "今日は休みます", Stability: 0.99},
		speechio.StreamingTranscriptionEvent{Kind: speechio.StreamingTranscriptionFinal, Text: "今日は休みます", Confidence: 0.99},
	)
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{fakeSpeech: fakeSpeech{}, chunks: [][]byte{{1, 0}}},
		session:             session,
	}
	agent := &fakeAgent{result: safePrivacyDecision()}
	inspector := &scriptedInspector{statuses: []privacyguard.InspectionStatus{
		privacyguard.InspectionClear,
		privacyguard.InspectionClear,
	}}
	pipeline, err := NewWithPrivacy(speech, agent, inspector)
	if err != nil {
		t.Fatal(err)
	}
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	result, err := pipeline.ProcessLive(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{StrictCloudMinimization: true},
		audio,
		func([]byte) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if agent.calls != 1 || !agent.turn.ResearchDisabled ||
		result.LiveTimings.SpecHit != 0 || result.LiveTimings.TTSPrestarted != 0 ||
		result.StateToken != "" || len(inspector.calls) != 2 {
		t.Fatalf("strict live path mismatch: result=%+v agent=%d calls=%#v", result, agent.calls, inspector.calls)
	}
}
