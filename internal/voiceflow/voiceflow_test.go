package voiceflow

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type fakeSpeech struct {
	transcript      string
	confidence      float32
	transcribeErr   error
	synthesizeErr   error
	synthesizeCalls int
	synthesizedText string
}

func (s *fakeSpeech) Transcribe(_ context.Context, _ []byte) (string, float32, error) {
	return s.transcript, s.confidence, s.transcribeErr
}

func (s *fakeSpeech) Synthesize(_ context.Context, text string) ([]byte, string, error) {
	s.synthesizeCalls++
	s.synthesizedText = text
	if s.synthesizeErr != nil {
		return nil, "", s.synthesizeErr
	}
	return []byte("speech"), "audio/mpeg", nil
}

type fakeAgent struct {
	calls  int
	turn   conversation.VoiceTurn
	result conversation.VoiceTurnResult
	err    error
}

func (a *fakeAgent) Process(
	_ context.Context,
	_ string,
	turn conversation.VoiceTurn,
) (conversation.VoiceTurnResult, error) {
	a.calls++
	a.turn = turn
	return a.result, a.err
}

func TestPipelineFailsClosedOnMeasuredLowSTTConfidence(t *testing.T) {
	t.Parallel()

	for _, ambient := range []bool{false, true} {
		name := "intentional"
		if ambient {
			name = "ambient"
		}
		t.Run(name, func(t *testing.T) {
			speech := &fakeSpeech{
				transcript: "誤認したかもしれない高リスク発話",
				confidence: 0.40,
			}
			agent := &fakeAgent{}
			pipeline, err := New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			result, err := pipeline.Process(
				context.Background(),
				"uid",
				httpapi.VoiceTurnInput{
					Audio:      []byte("audio"),
					Ambient:    ambient,
					StateToken: "existing-state",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if agent.calls != 0 {
				t.Fatalf("low-confidence transcript reached the model: %d", agent.calls)
			}
			if result.StateToken != "existing-state" ||
				result.DetectedDomain != "unknown" ||
				result.ResearchStatus != "none" ||
				len(result.ResearchRecords) != 0 {
				t.Fatalf("recognition fallback metadata = %+v", result)
			}
			if ambient {
				if result.Route != routeSilentLowConfidence ||
					len(result.Audio) != 0 ||
					result.Caption != "" ||
					speech.synthesizeCalls != 0 {
					t.Fatalf("ambient low-confidence turn spoke: %+v", result)
				}
				return
			}
			if result.Route != routeClarifyLowConfidence ||
				result.Caption != lowConfidencePrompt ||
				speech.synthesizedText != lowConfidencePrompt ||
				len(result.Audio) == 0 ||
				speech.synthesizeCalls != 1 {
				t.Fatalf("intentional low-confidence turn did not clarify: %+v", result)
			}
		})
	}
}

func TestPipelineRecoversNoSpeechWithoutEndingAnIntentionalSession(t *testing.T) {
	t.Parallel()

	noSpeechErrors := []struct {
		name string
		err  error
	}{
		{name: "exact", err: speechio.ErrNoSpeech},
		{
			name: "wrapped",
			err:  errors.Join(errors.New("provider detail"), speechio.ErrNoSpeech),
		},
	}
	for _, noSpeech := range noSpeechErrors {
		for _, ambient := range []bool{false, true} {
			mode := "intentional"
			if ambient {
				mode = "ambient"
			}
			t.Run(noSpeech.name+"/"+mode, func(t *testing.T) {
				speech := &fakeSpeech{transcribeErr: noSpeech.err}
				agent := &fakeAgent{}
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				result, err := pipeline.Process(
					context.Background(),
					"uid",
					httpapi.VoiceTurnInput{
						Audio:      []byte("audio"),
						Ambient:    ambient,
						StateToken: "existing-state",
					},
				)
				if ambient {
					if err != nil {
						t.Fatal(err)
					}
					if result.Route != routeSilentNoSpeech ||
						result.StateToken != "existing-state" ||
						result.DetectedDomain != "unknown" ||
						result.ResearchStatus != "none" ||
						len(result.ResearchRecords) != 0 ||
						len(result.Audio) != 0 ||
						result.Caption != "" {
						t.Fatalf("ambient no-speech result = %+v", result)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if result.Route != routeClarifyNoSpeech ||
						result.StateToken != "existing-state" ||
						result.Caption != lowConfidencePrompt ||
						len(result.Audio) == 0 {
						t.Fatalf("intentional no-speech result = %+v", result)
					}
				}
				if agent.calls != 0 {
					t.Fatalf("no-speech turn reached the model: %d", agent.calls)
				}
				wantSynthesisCalls := 1
				if ambient {
					wantSynthesisCalls = 0
				}
				if speech.synthesizeCalls != wantSynthesisCalls {
					t.Fatalf(
						"no-speech synthesis calls = %d; want %d",
						speech.synthesizeCalls,
						wantSynthesisCalls,
					)
				}
			})
		}
	}
}

func TestPipelineTreatsZeroSTTConfidenceAsUnavailableNotLow(t *testing.T) {
	t.Parallel()

	speech := &fakeSpeech{transcript: "confidenceが提供されない認識結果"}
	agent := &fakeAgent{result: conversation.VoiceTurnResult{
		Domain:           "general",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		ResearchStatus:   "none",
		ResearchRecords:  []conversation.ResearchRecord{},
		Route:            "fast",
		StateToken:       "state",
		SpokenReply:      "続けます。",
	}}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pipeline.Process(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{Audio: []byte("audio")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if agent.calls != 1 {
		t.Fatalf("zero/unavailable confidence suppressed transcript: %d", agent.calls)
	}
}

func TestTranscriptConfidenceBoundary(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		confidence float32
		tooLow     bool
	}{
		{name: "unavailable zero", confidence: 0, tooLow: false},
		{name: "below threshold", confidence: 0.649, tooLow: true},
		{name: "at threshold", confidence: 0.65, tooLow: false},
		{name: "high", confidence: 0.95, tooLow: false},
		{name: "negative invalid", confidence: -0.1, tooLow: true},
		{name: "above one invalid", confidence: 1.1, tooLow: true},
		{name: "NaN invalid", confidence: float32(math.NaN()), tooLow: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if got := transcriptConfidenceTooLow(test.confidence); got != test.tooLow {
				t.Fatalf("tooLow(%v) = %v; want %v", test.confidence, got, test.tooLow)
			}
		})
	}
}

func TestPipelinePreservesDeliberateSilence(t *testing.T) {
	t.Parallel()

	speech := &fakeSpeech{transcript: "いや、なんか締切が不安で"}
	agent := &fakeAgent{
		result: conversation.VoiceTurnResult{
			Domain:           "daily",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			ResearchStatus:   "none",
			ResearchRecords:  []conversation.ResearchRecord{},
			Route:            "fast",
			StateToken:       "encrypted-state",
			Intervention: conversation.ArbiterDecision{
				Act: "silent",
			},
		},
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
		Audio:      []byte("audio"),
		MIMEType:   "audio/webm",
		RequestID:  "0123456789abcdef01234567",
		Ambient:    true,
		STTLocale:  "ja-JP",
		StateToken: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Audio) != 0 || result.AudioMIMEType != "" || result.Caption != "" {
		t.Fatalf("silent result contains audio: %+v", result)
	}
	if speech.synthesizeCalls != 0 {
		t.Fatalf("synthesis calls = %d; want 0", speech.synthesizeCalls)
	}
	if !agent.turn.Ambient ||
		agent.turn.Utterance == "" ||
		agent.turn.RequestID != "0123456789abcdef01234567" {
		t.Fatalf("agent turn = %+v", agent.turn)
	}
}

func TestPipelineKeepsPlannerFallbackConversationalAndRequestsPDFReattach(
	t *testing.T,
) {
	t.Parallel()

	for _, test := range []struct {
		name             string
		route            string
		spokenReply      string
		needsClarify     bool
		ambient          bool
		wantSynthesized  int
		wantIntervention string
	}{
		{
			name:             "intentional",
			route:            "planner-unavailable",
			spokenReply:      "今の話は聞き取れています。もう一度聞かせてください。",
			needsClarify:     true,
			wantSynthesized:  1,
			wantIntervention: "clarify",
		},
		{
			name:             "ambient",
			route:            "planner-unavailable-silent",
			ambient:          true,
			wantSynthesized:  0,
			wantIntervention: "silent",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			speech := &fakeSpeech{
				transcript: "この内容に答えてください",
				confidence: 0.95,
			}
			agent := &fakeAgent{result: conversation.VoiceTurnResult{
				Domain:             "other",
				AssistanceTarget:   "assistant",
				RespondentStage:    "none",
				ResearchStatus:     "none",
				ResearchRecords:    []conversation.ResearchRecord{},
				Route:              test.route,
				StateToken:         "fresh-encrypted-state",
				SpokenReply:        test.spokenReply,
				NeedsClarification: test.needsClarify,
				InterventionPolicy: "clarify",
				Intervention: conversation.ArbiterDecision{
					Act: test.wantIntervention,
				},
			}}
			pipeline, err := New(speech, agent)
			if err != nil {
				t.Fatal(err)
			}
			result, err := pipeline.Process(
				context.Background(),
				"uid",
				httpapi.VoiceTurnInput{
					Audio:    []byte("audio"),
					MIMEType: "audio/webm",
					Ambient:  test.ambient,
					Document: &httpapi.VoiceDocument{
						MIMEType: "application/pdf",
						Data:     []byte("%PDF"),
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Route != test.route ||
				result.StateToken != "fresh-encrypted-state" ||
				result.Caption != test.spokenReply ||
				!result.NeedsPaper ||
				speech.synthesizeCalls != test.wantSynthesized {
				t.Fatalf("planner fallback result = %+v", result)
			}
			if test.wantSynthesized > 0 &&
				speech.synthesizedText != test.spokenReply {
				t.Fatalf("synthesized text = %q", speech.synthesizedText)
			}
		})
	}
}

func TestPipelineSynthesizesOnlySelectedIntervention(t *testing.T) {
	t.Parallel()

	speech := &fakeSpeech{transcript: "この論文の結果は因果関係を示したと思う"}
	agent := &fakeAgent{
		result: conversation.VoiceTurnResult{
			Domain:           "research",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			ResearchStatus:   "none",
			ResearchRecords:  []conversation.ResearchRecord{},
			Route:            "precision",
			StateToken:       "encrypted-state",
			SpokenReply:      "その結果だけでは相関かも。手法を一度見よう。",
			Intervention: conversation.ArbiterDecision{
				Act: "paper_check",
			},
		},
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	result, err := pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
		Audio:    []byte("audio"),
		MIMEType: "audio/webm",
		Ambient:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Audio) != "speech" ||
		result.AudioMIMEType != "audio/mpeg" ||
		result.Caption != agent.result.SpokenReply ||
		result.AssistanceTarget != "assistant" ||
		result.RespondentStage != "none" {
		t.Fatalf("result = %+v", result)
	}
	if !result.NeedsPaper {
		t.Fatal("research intervention should request the explicitly supplied paper")
	}
	if speech.synthesizeCalls != 1 {
		t.Fatalf("synthesis calls = %d; want 1", speech.synthesizeCalls)
	}
}

func TestPipelinePropagatesRespondentAssistanceMetadata(t *testing.T) {
	t.Parallel()

	speech := &fakeSpeech{
		transcript: "音声AIを使っています。目的は答えの核を先に出すことです。",
		confidence: 0.95,
	}
	agent := &fakeAgent{
		result: conversation.VoiceTurnResult{
			Domain:           "conversation",
			AssistanceTarget: "respondent",
			RespondentStage:  "restructure",
			ResearchStatus:   "none",
			ResearchRecords:  []conversation.ResearchRecord{},
			Route:            "respondent-restructure",
			StateToken:       "encrypted-state",
			SpokenReply:      "目的は、答えの核を先に出すことです。音声AIを使っています。",
		},
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	result, err := pipeline.Process(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{Audio: []byte("audio")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.AssistanceTarget != "respondent" ||
		result.RespondentStage != "restructure" ||
		result.Caption != agent.result.SpokenReply ||
		speech.synthesizedText != agent.result.SpokenReply {
		t.Fatalf("respondent result = %+v", result)
	}
}

func TestPipelineMapsResearchRecords(t *testing.T) {
	t.Parallel()

	speech := &fakeSpeech{
		transcript: "このDOIの論文を確認して",
		confidence: 0.95,
	}
	agent := &fakeAgent{
		result: conversation.VoiceTurnResult{
			Domain:           "research",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			ResearchStatus:   "needs_primary_evidence",
			ResearchRecords: []conversation.ResearchRecord{
				{
					Title:     "KOTAEの回答支援に関する研究",
					DOI:       "10.1234/kotae.2026",
					URL:       "https://doi.org/10.1234/kotae.2026",
					Published: "2026-07-29",
					Source:    "Crossref",
				},
			},
			Route:       "research-metadata",
			StateToken:  "encrypted-state",
			SpokenReply: "候補を見つけました。内容の検証には一次資料が必要です。",
		},
	}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	result, err := pipeline.Process(
		context.Background(),
		"uid",
		httpapi.VoiceTurnInput{Audio: []byte("audio")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResearchStatus != "needs_primary_evidence" ||
		len(result.ResearchRecords) != 1 ||
		result.ResearchRecords[0].DOI != "10.1234/kotae.2026" ||
		result.ResearchRecords[0].URL != "https://doi.org/10.1234/kotae.2026" ||
		result.ResearchRecords[0].Source != "Crossref" {
		t.Fatalf("research result = %+v", result)
	}
}

func TestPipelineRejectsInvalidEncryptedState(t *testing.T) {
	t.Parallel()

	speech := &fakeSpeech{transcript: "続きです"}
	agent := &fakeAgent{err: conversation.ErrInvalidStateToken}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}

	_, err = pipeline.Process(context.Background(), "uid", httpapi.VoiceTurnInput{
		Audio: []byte("audio"),
	})
	if !errors.Is(err, httpapi.ErrVoiceStateInvalid) {
		t.Fatalf("error = %v; want invalid state", err)
	}
}

func TestPipelineUnexpectedErrorsExposeOnlyFiniteStage(t *testing.T) {
	t.Parallel()

	const (
		secretProviderText = "provider-response-SECRET"
		secretTranscript   = "田中さんの診療記録"
		secretUID          = "uid-private-123"
	)

	tests := []struct {
		name      string
		speech    *fakeSpeech
		agent     *fakeAgent
		wantStage httpapi.VoicePipelineStage
	}{
		{
			name: "transcribe",
			speech: &fakeSpeech{
				transcribeErr: errors.New(
					secretProviderText + " AUDIO-SECRET",
				),
			},
			agent:     &fakeAgent{},
			wantStage: httpapi.VoicePipelineStageTranscribe,
		},
		{
			name: "conversation",
			speech: &fakeSpeech{
				transcript: secretTranscript,
				confidence: 0.95,
			},
			agent: &fakeAgent{
				err: errors.New(secretProviderText + " " + secretTranscript),
			},
			wantStage: httpapi.VoicePipelineStageConversation,
		},
		{
			name: "synthesize",
			speech: &fakeSpeech{
				transcript:    "回答をまとめて",
				confidence:    0.95,
				synthesizeErr: errors.New(secretProviderText),
			},
			agent: &fakeAgent{
				result: conversation.VoiceTurnResult{
					Domain:           "general",
					AssistanceTarget: "assistant",
					RespondentStage:  "none",
					ResearchStatus:   "none",
					ResearchRecords:  []conversation.ResearchRecord{},
					Route:            "fast",
					StateToken:       "encrypted-state",
					SpokenReply:      secretTranscript,
				},
			},
			wantStage: httpapi.VoicePipelineStageSynthesize,
		},
		{
			name: "synthesize recognition clarification",
			speech: &fakeSpeech{
				transcript:    secretTranscript,
				confidence:    0.40,
				synthesizeErr: errors.New(secretProviderText),
			},
			agent:     &fakeAgent{},
			wantStage: httpapi.VoicePipelineStageSynthesize,
		},
		{
			name: "synthesize no-speech clarification",
			speech: &fakeSpeech{
				transcribeErr: speechio.ErrNoSpeech,
				synthesizeErr: errors.New(secretProviderText),
			},
			agent:     &fakeAgent{},
			wantStage: httpapi.VoicePipelineStageSynthesize,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			pipeline, err := New(test.speech, test.agent)
			if err != nil {
				t.Fatal(err)
			}
			_, err = pipeline.Process(
				context.Background(),
				secretUID,
				httpapi.VoiceTurnInput{Audio: []byte("AUDIO-SECRET")},
			)
			if err == nil {
				t.Fatal("pipeline succeeded; want classified failure")
			}
			stage, classified := httpapi.VoicePipelineStageOf(err)
			if !classified || stage != test.wantStage {
				t.Fatalf(
					"stage = %q, classified = %v; want %q",
					stage,
					classified,
					test.wantStage,
				)
			}
			for _, forbidden := range []string{
				secretProviderText,
				secretTranscript,
				secretUID,
				"AUDIO-SECRET",
			} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("pipeline error exposed private content %q", forbidden)
				}
			}
		})
	}
}
