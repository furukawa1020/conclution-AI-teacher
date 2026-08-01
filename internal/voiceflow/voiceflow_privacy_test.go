package voiceflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type recordingProtector struct {
	calls   []string
	protect func(string) (privacyguard.Result, error)
}

type deadlineCapturingProtector struct {
	deadlines []time.Time
}

func (protector *deadlineCapturingProtector) Protect(
	ctx context.Context,
	text string,
) (privacyguard.Result, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return privacyguard.Result{}, errors.New("privacy context has no deadline")
	}
	protector.deadlines = append(protector.deadlines, deadline)
	if len(protector.deadlines) == 1 {
		return privacyguard.Result{Text: text}, nil
	}
	return privacyguard.Result{}, errors.New("simulated output privacy failure")
}

func (protector *recordingProtector) Protect(
	_ context.Context,
	text string,
) (privacyguard.Result, error) {
	protector.calls = append(protector.calls, text)
	return protector.protect(text)
}

type countingSpeech struct {
	fakeSpeech
	transcribeCalls int
}

func (speech *countingSpeech) Transcribe(
	ctx context.Context,
	audio []byte,
) (string, float32, error) {
	speech.transcribeCalls++
	return speech.fakeSpeech.Transcribe(ctx, audio)
}

func protectedDecision(reply string, state string) conversation.VoiceTurnResult {
	return conversation.VoiceTurnResult{
		Domain:           "general",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		CoachPhase:       "none",
		CoachAction:      "none",
		ResearchStatus:   "none",
		ResearchRecords:  []conversation.ResearchRecord{},
		Route:            "fast",
		StateToken:       state,
		SpokenReply:      reply,
	}
}

func TestProtectedPipelinePassesOnlyProtectedTextDownstream(t *testing.T) {
	const (
		rawTranscript = "連絡先はalice@example.comです"
		rawReply      = "電話番号は090-1234-5678です"
	)
	speech := &fakeSpeech{transcript: rawTranscript, confidence: 0.99}
	agent := &fakeAgent{result: protectedDecision(rawReply, "unsafe-state")}
	protector := &recordingProtector{protect: func(text string) (privacyguard.Result, error) {
		switch text {
		case rawTranscript:
			return privacyguard.Result{Text: "連絡先は[EMAIL]です", Redacted: true}, nil
		case rawReply:
			return privacyguard.Result{Text: "電話番号は[PHONE]です", Redacted: true}, nil
		default:
			t.Fatalf("unexpected protector input: %q", text)
			return privacyguard.Result{}, errors.New("unexpected input")
		}
	}}
	pipeline, err := NewProtected(speech, agent, protector)
	if err != nil {
		t.Fatal(err)
	}
	result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{Audio: []byte("audio")})
	if err != nil {
		t.Fatal(err)
	}
	if agent.calls != 1 || agent.turn.Utterance != "連絡先は[EMAIL]です" ||
		strings.Contains(agent.turn.Utterance, "alice@example.com") {
		t.Fatalf("agent calls=%d turn=%+v", agent.calls, agent.turn)
	}
	if speech.synthesizedText != "電話番号は[PHONE]です" ||
		strings.Contains(speech.synthesizedText, "090-1234-5678") {
		t.Fatalf("unsafe synthesized text: %q", speech.synthesizedText)
	}
	if result.StateToken != "" || len(protector.calls) != 2 {
		t.Fatalf("state=%q protector calls=%d", result.StateToken, len(protector.calls))
	}
}

func TestProtectedPipelineFailsClosedBeforeAgent(t *testing.T) {
	const rawTranscript = "秘密はalice@example.comです"
	speech := &fakeSpeech{transcript: rawTranscript, confidence: 0.99}
	agent := &fakeAgent{result: protectedDecision("unsafe reply", "unsafe-state")}
	protector := &recordingProtector{protect: func(text string) (privacyguard.Result, error) {
		return privacyguard.Result{}, errors.New("provider echoed " + text)
	}}
	pipeline, err := NewProtected(speech, agent, protector)
	if err != nil {
		t.Fatal(err)
	}
	result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{Audio: []byte("audio")})
	if err != nil {
		t.Fatal(err)
	}
	if agent.calls != 0 || result.Route != routePrivacyProtectionBlocked ||
		result.StateToken != "" || result.Caption != privacyProtectionReply ||
		speech.synthesizedText != privacyProtectionReply ||
		strings.Contains(speech.synthesizedText, rawTranscript) {
		t.Fatalf("result=%+v agent=%d speech=%q", result, agent.calls, speech.synthesizedText)
	}
}

func TestProtectedPipelineReservesPrivacyAndSynthesisWindows(t *testing.T) {
	speech := &fakeSpeech{transcript: "普通の会話", confidence: 0.99}
	agent := &fakeAgent{result: protectedDecision("モデル応答", "state")}
	protector := &deadlineCapturingProtector{}
	pipeline, err := NewProtected(speech, agent, protector)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		pipeline.preInferenceReserve()+500*time.Millisecond,
	)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()

	result, err := pipeline.Process(ctx, "uid", httpapi.VoiceTurnInput{Audio: []byte("audio")})
	if err != nil {
		t.Fatal(err)
	}
	if len(protector.deadlines) != 2 ||
		result.Route != routePrivacyProtectionBlocked ||
		speech.synthesizedText != privacyProtectionReply ||
		ctx.Err() != nil {
		t.Fatalf(
			"result=%+v deadlines=%d speech=%q parent_err=%v",
			result,
			len(protector.deadlines),
			speech.synthesizedText,
			ctx.Err(),
		)
	}
	assertDeadlineReserve := func(name string, deadline time.Time, want time.Duration) {
		t.Helper()
		gap := parentDeadline.Sub(deadline)
		if gap < want-100*time.Millisecond || gap > want+100*time.Millisecond {
			t.Fatalf("%s reserve=%v; want about %v", name, gap, want)
		}
	}
	assertDeadlineReserve("input privacy", protector.deadlines[0], conversation.VoiceResponseReserve)
	assertDeadlineReserve("output privacy", protector.deadlines[1], voiceSynthesisReserve)
}

func TestProtectedPipelineBlocksPDFBeforeSpeechAndAgent(t *testing.T) {
	speech := &countingSpeech{fakeSpeech: fakeSpeech{transcript: "unreachable", confidence: 0.99}}
	agent := &fakeAgent{result: protectedDecision("unreachable", "state")}
	protector := &recordingProtector{protect: func(text string) (privacyguard.Result, error) {
		t.Fatalf("protector unexpectedly received %q", text)
		return privacyguard.Result{}, errors.New("unexpected")
	}}
	pipeline, err := NewProtected(speech, agent, protector)
	if err != nil {
		t.Fatal(err)
	}
	document := &httpapi.VoiceDocument{MIMEType: "application/pdf", Data: []byte("%PDF private bytes")}
	result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
		Audio: []byte("audio"), Document: document,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Route != routeDocumentPrivacyBlocked || agent.calls != 0 ||
		speech.transcribeCalls != 0 || len(protector.calls) != 0 ||
		len(document.Data) != 0 || speech.synthesizedText != documentPrivacyReply {
		t.Fatalf("result=%+v agent=%d stt=%d protect=%d doc=%d reply=%q", result, agent.calls, speech.transcribeCalls, len(protector.calls), len(document.Data), speech.synthesizedText)
	}
}

func TestProtectedPipelineDoesNotSpeakUnprotectedModelOutput(t *testing.T) {
	const rawReply = "個人情報を含む unsafe reply"
	speech := &fakeSpeech{transcript: "普通の会話", confidence: 0.99}
	agent := &fakeAgent{result: protectedDecision(rawReply, "unsafe-state")}
	protector := &recordingProtector{protect: func(text string) (privacyguard.Result, error) {
		if text == "普通の会話" {
			return privacyguard.Result{Text: text}, nil
		}
		return privacyguard.Result{}, errors.New("unsafe output " + text)
	}}
	pipeline, err := NewProtected(speech, agent, protector)
	if err != nil {
		t.Fatal(err)
	}
	result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{Audio: []byte("audio")})
	if err != nil {
		t.Fatal(err)
	}
	if agent.calls != 1 || result.Route != routePrivacyProtectionBlocked ||
		result.StateToken != "" || speech.synthesizedText != privacyProtectionReply ||
		strings.Contains(speech.synthesizedText, rawReply) {
		t.Fatalf("result=%+v agent=%d speech=%q", result, agent.calls, speech.synthesizedText)
	}
}

func TestProtectedLiveDisablesSpeculativeAgentCalls(t *testing.T) {
	const finalText = "確定した秘密を含まない質問"
	session := newFakeLiveTranscriptionSession(
		speechio.StreamingTranscriptionEvent{Kind: speechio.StreamingTranscriptionInterim, Text: "投機へ送ってはいけない途中候補です", Stability: 0.99},
		speechio.StreamingTranscriptionEvent{Kind: speechio.StreamingTranscriptionFinal, Text: finalText, Confidence: 0.99},
	)
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{chunks: [][]byte{{1, 0}}},
		session:             session,
	}
	agent := &fakeAgent{result: protectedDecision("安全な返答", "safe-state")}
	protector := &recordingProtector{protect: func(text string) (privacyguard.Result, error) {
		return privacyguard.Result{Text: text}, nil
	}}
	pipeline, err := NewProtected(speech, agent, protector)
	if err != nil {
		t.Fatal(err)
	}
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	result, err := pipeline.ProcessLive(context.Background(), "uid", httpapi.VoiceTurnInput{}, audio, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if agent.calls != 1 || agent.turn.Utterance != finalText ||
		result.LiveTimings.SpecHit != 0 || result.LiveTimings.TTSPrestarted != 0 ||
		len(protector.calls) != 2 {
		t.Fatalf("agent=%d turn=%q timings=%+v protect=%#v", agent.calls, agent.turn.Utterance, result.LiveTimings, protector.calls)
	}
}

func TestNewProtectedRequiresProtector(t *testing.T) {
	if _, err := NewProtected(&fakeSpeech{}, &fakeAgent{}, nil); err == nil {
		t.Fatal("NewProtected accepted a nil protector")
	}
}
