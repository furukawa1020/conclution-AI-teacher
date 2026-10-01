package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type committedCandidateLiveAttempt struct {
	candidate string
	final     conversation.VoiceTurnResult
	err       error
	audit     <-chan struct{}
	before    func()
}

type committedCandidateLiveCall struct {
	turn     conversation.VoiceTurn
	callback func(conversation.SealedSpeechCandidate)
}

type committedCandidateLiveAgent struct {
	mu       sync.Mutex
	attempts []committedCandidateLiveAttempt
	calls    []committedCandidateLiveCall
}

func (agent *committedCandidateLiveAgent) Process(ctx context.Context, _ string, turn conversation.VoiceTurn) (conversation.VoiceTurnResult, error) {
	return agent.process(ctx, turn, nil)
}

func (agent *committedCandidateLiveAgent) ProcessWithSealedCandidate(ctx context.Context, _ string, turn conversation.VoiceTurn, callback func(conversation.SealedSpeechCandidate)) (conversation.VoiceTurnResult, error) {
	return agent.process(ctx, turn, callback)
}

func (agent *committedCandidateLiveAgent) process(ctx context.Context, turn conversation.VoiceTurn, callback func(conversation.SealedSpeechCandidate)) (conversation.VoiceTurnResult, error) {
	agent.mu.Lock()
	index := len(agent.calls)
	agent.calls = append(agent.calls, committedCandidateLiveCall{turn: turn, callback: callback})
	if index >= len(agent.attempts) {
		agent.mu.Unlock()
		return conversation.VoiceTurnResult{}, errors.New("unexpected conversation attempt")
	}
	attempt := agent.attempts[index]
	agent.mu.Unlock()
	if attempt.before != nil {
		attempt.before()
	}
	if callback != nil {
		callback(conversation.SealedSpeechCandidate{})
		callback(conversation.SealedSpeechCandidate{SpokenReply: attempt.candidate})
		callback(conversation.SealedSpeechCandidate{SpokenReply: "a later candidate must not replace the first"})
	}
	if attempt.audit != nil {
		select {
		case <-attempt.audit:
		case <-ctx.Done():
			return conversation.VoiceTurnResult{}, ctx.Err()
		}
	}
	return attempt.final, attempt.err
}

func (agent *committedCandidateLiveAgent) recordedCalls() []committedCandidateLiveCall {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return append([]committedCandidateLiveCall(nil), agent.calls...)
}

type committedCandidateLiveSpeech struct {
	fakeLiveSpeech
	mu       sync.Mutex
	texts    []string
	contexts []context.Context
	synth    func(context.Context, int, speechio.StreamChunkHandler) (string, error)
}

func newCommittedCandidateLiveSpeech(transcript string) *committedCandidateLiveSpeech {
	return &committedCandidateLiveSpeech{fakeLiveSpeech: fakeLiveSpeech{
		session: newFakeLiveTranscriptionSession(speechio.StreamingTranscriptionEvent{
			Kind: speechio.StreamingTranscriptionFinal, Text: transcript, Confidence: .99,
		}),
	}}
}

func (speech *committedCandidateLiveSpeech) StreamSynthesize(ctx context.Context, text string, onChunk speechio.StreamChunkHandler) (string, error) {
	speech.mu.Lock()
	index := len(speech.texts)
	speech.texts = append(speech.texts, text)
	speech.contexts = append(speech.contexts, ctx)
	speech.mu.Unlock()
	if speech.synth != nil {
		return speech.synth(ctx, index, onChunk)
	}
	if err := onChunk([]byte{byte(index + 1), 1}); err != nil {
		return "", err
	}
	return speechio.StreamingAudioContentType, nil
}

func (speech *committedCandidateLiveSpeech) recordedTexts() []string {
	speech.mu.Lock()
	defer speech.mu.Unlock()
	return append([]string(nil), speech.texts...)
}

type committedCandidateLiveOutcome struct {
	result httpapi.VoiceTurnResult
	err    error
}

func startCommittedCandidateLive(pipeline *Pipeline, ctx context.Context, input httpapi.VoiceTurnInput, onAudio func([]byte) error) <-chan committedCandidateLiveOutcome {
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	done := make(chan committedCandidateLiveOutcome, 1)
	go func() {
		result, err := pipeline.ProcessLive(ctx, "committed-candidate-user", input, audio, onAudio)
		done <- committedCandidateLiveOutcome{result: result, err: err}
	}()
	return done
}

func completedCommittedCandidateLive(t *testing.T, done <-chan committedCandidateLiveOutcome) committedCandidateLiveOutcome {
	t.Helper()
	synctest.Wait()
	select {
	case outcome := <-done:
		return outcome
	default:
		t.Fatal("committed live turn did not finish")
		return committedCandidateLiveOutcome{}
	}
}

func TestCommittedCandidateLiveOverlapsAuditWithoutPublishing(t *testing.T) {
	for _, extended := range []bool{false, true} {
		name := "short final only"
		transcript := "こんにちは"
		if extended {
			name = "extended final preserves metadata"
			transcript = strings.Repeat("あ", extendedSpeechMinRunes)
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				audit := make(chan struct{})
				final := liveTestDecision("監査済みの返答", "final-state")
				final.CoachPhase, final.CoachAction = "none", "none"
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
				speech := newCommittedCandidateLiveSpeech(transcript)
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				var output []byte
				done := startCommittedCandidateLive(pipeline, ctx, httpapi.VoiceTurnInput{}, func(chunk []byte) error {
					output = append(output, chunk...)
					return nil
				})
				synctest.Wait()
				calls, texts := agent.recordedCalls(), speech.recordedTexts()
				if len(calls) != 1 || calls[0].callback == nil || len(texts) != 1 || texts[0] != final.SpokenReply {
					t.Fatalf("private TTS did not overlap audit: agent calls=%d sealed=%t synthesis=%v", len(calls), len(calls) == 1 && calls[0].callback != nil, texts)
				}
				turn := calls[0].turn
				if turn.Speculative || turn.InputOrigin != conversation.InputOriginCommittedVoice || turn.FloorEvidence != conversation.FloorEvidenceUnknown || turn.ExtendedSpeech != extended || turn.Utterance != transcript {
					t.Fatalf("committed provenance changed: %+v", turn)
				}
				if len(output) != 0 {
					t.Fatalf("unaudited audio escaped: %v", output)
				}
				close(audit)
				outcome := completedCommittedCandidateLive(t, done)
				if outcome.err != nil || outcome.result.Caption != final.SpokenReply || outcome.result.StateToken != final.StateToken || !bytes.Equal(output, []byte{1, 1}) || len(agent.recordedCalls()) != 1 || len(speech.recordedTexts()) != 1 {
					t.Fatalf("matching candidate was not adopted: result=%+v err=%v output=%v", outcome.result, outcome.err, output)
				}
			})
		})
	}
}

func TestCommittedCandidateLiveDiscardsIneligibleFinal(t *testing.T) {
	for _, name := range []string{"mismatch", "error", "silent", "research status", "research records", "respondent target", "respondent stage", "coach metadata", "terminal answer ownership"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				audit := make(chan struct{})
				candidate := "early ordinary reply"
				final := liveTestDecision(candidate, "audited-final-state")
				var agentErr error
				wantErr, wantAudio := false, true
				switch name {
				case "mismatch":
					final.SpokenReply = "different final reply"
				case "error":
					agentErr = errors.New("independent audit failed")
					wantErr, wantAudio = true, false
				case "silent":
					final.SpokenReply = ""
					wantAudio = false
				case "research status":
					final.ResearchStatus = "complete"
				case "research records":
					final.ResearchRecords = []conversation.ResearchRecord{{}}
				case "respondent target":
					final.AssistanceTarget = "respondent"
				case "respondent stage":
					final.RespondentStage = "restructure"
				case "coach metadata":
					final.CoachPhase, final.CoachAction = "complete", "complete"
				case "terminal answer ownership":
					final.CoachPhase, final.CoachAction = "complete", "complete"
					final.AnswerProof = conversation.AnswerProofQuestionBoundInputAnswerFirst
					wantErr, wantAudio = true, false
				}
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: candidate, final: final, err: agentErr, audit: audit}}}
				speech := newCommittedCandidateLiveSpeech("hello")
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				var output []byte
				done := startCommittedCandidateLive(pipeline, ctx, httpapi.VoiceTurnInput{}, func(chunk []byte) error {
					output = append(output, chunk...)
					return nil
				})
				synctest.Wait()
				if len(speech.recordedTexts()) != 1 || len(output) != 0 {
					t.Fatal("candidate was not privately staged before the final decision")
				}
				close(audit)
				outcome := completedCommittedCandidateLive(t, done)
				if (outcome.err != nil) != wantErr || len(agent.recordedCalls()) != 1 {
					t.Fatalf("error=%v conversation calls=%d", outcome.err, len(agent.recordedCalls()))
				}
				texts := speech.recordedTexts()
				if wantAudio {
					if len(texts) != 2 || texts[1] != final.SpokenReply || !bytes.Equal(output, []byte{2, 1}) || outcome.result.StateToken != final.StateToken || outcome.result.Caption != final.SpokenReply || outcome.result.AssistanceTarget != final.AssistanceTarget || outcome.result.RespondentStage != final.RespondentStage {
						t.Fatalf("ineligible candidate reused or final metadata lost: texts=%v output=%v result=%+v", texts, output, outcome.result)
					}
				} else if len(texts) != 1 || len(output) != 0 {
					t.Fatalf("rejected decision produced audio: texts=%v output=%v", texts, output)
				}
			})
		})
	}
}

func TestCommittedCandidateLiveExpiredRetryOwnsFreshCandidate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		firstAudit, secondAudit := make(chan struct{}), make(chan struct{})
		final := liveTestDecision("identical reply across attempts", "fresh-state")
		speech := newCommittedCandidateLiveSpeech("hello")
		retrySawCanceledProvider := false
		agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{
			{candidate: final.SpokenReply, err: conversation.ErrExpiredStateToken, audit: firstAudit},
			{candidate: final.SpokenReply, final: final, audit: secondAudit, before: func() {
				speech.mu.Lock()
				defer speech.mu.Unlock()
				retrySawCanceledProvider = len(speech.contexts) == 1 &&
					errors.Is(speech.contexts[0].Err(), context.Canceled)
			}},
		}}
		pipeline, err := New(speech, agent)
		if err != nil {
			t.Fatal(err)
		}
		var output []byte
		done := startCommittedCandidateLive(pipeline, ctx, httpapi.VoiceTurnInput{StateToken: "expired-state"}, func(chunk []byte) error {
			output = append(output, chunk...)
			return nil
		})
		synctest.Wait()
		if len(speech.recordedTexts()) != 1 || len(output) != 0 {
			t.Fatal("first attempt did not privately stage its candidate")
		}
		close(firstAudit)
		synctest.Wait()
		if !retrySawCanceledProvider {
			t.Fatal("expired provider was not canceled before the retry invocation")
		}
		calls := agent.recordedCalls()
		if len(calls) != 2 || calls[0].callback == nil || calls[1].callback == nil || calls[0].turn.StateToken != "expired-state" || calls[1].turn.StateToken != "" || len(speech.recordedTexts()) != 2 || len(output) != 0 {
			t.Fatalf("expired attempt leaked or retry reused it: calls=%d texts=%v output=%v", len(calls), speech.recordedTexts(), output)
		}
		for _, call := range calls {
			if call.turn.Speculative || call.turn.InputOrigin != conversation.InputOriginCommittedVoice || call.turn.FloorEvidence != conversation.FloorEvidenceUnknown {
				t.Fatalf("retry changed committed provenance: %+v", call.turn)
			}
		}
		// A retained callback belongs to the completed invocation, never its retry.
		calls[0].callback(conversation.SealedSpeechCandidate{SpokenReply: "late first-attempt reply"})
		synctest.Wait()
		if len(speech.recordedTexts()) != 2 {
			t.Fatal("expired invocation accepted a late candidate")
		}
		close(secondAudit)
		outcome := completedCommittedCandidateLive(t, done)
		if outcome.err != nil || outcome.result.StateToken != final.StateToken || !bytes.Equal(output, []byte{2, 1}) || len(speech.recordedTexts()) != 2 {
			t.Fatalf("retry released stale PCM: result=%+v err=%v output=%v", outcome.result, outcome.err, output)
		}
	})
}

func TestCommittedCandidateLiveStrictModeNeverCallsSealedAPI(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		final := safePrivacyDecision()
		agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: "uninspected candidate", final: final}}}
		speech := newCommittedCandidateLiveSpeech("The sky is blue.")
		inspector := &scriptedInspector{statuses: []privacyguard.InspectionStatus{privacyguard.InspectionClear, privacyguard.InspectionClear}}
		pipeline, err := NewWithPrivacy(speech, agent, inspector)
		if err != nil {
			t.Fatal(err)
		}
		var output []byte
		done := startCommittedCandidateLive(pipeline, context.Background(), httpapi.VoiceTurnInput{StrictCloudMinimization: true}, func(chunk []byte) error {
			output = append(output, chunk...)
			return nil
		})
		outcome := completedCommittedCandidateLive(t, done)
		calls, texts := agent.recordedCalls(), speech.recordedTexts()
		if outcome.err != nil || len(calls) != 1 || calls[0].callback != nil || !calls[0].turn.ResearchDisabled || len(inspector.calls) != 2 || inspector.calls[1] != final.SpokenReply || len(texts) != 1 || texts[0] != final.SpokenReply || !bytes.Equal(output, []byte{1, 1}) || outcome.result.StateToken != "" || outcome.result.PrivacyStatus != "clear" {
			t.Fatalf("strict privacy gate changed: result=%+v err=%v texts=%v inspections=%v", outcome.result, outcome.err, texts, inspector.calls)
		}
	})
}

func TestCommittedCandidateLiveCancellationRejectsNoncooperativeProvider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		audit, providerRelease := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(providerRelease) }) }
		defer release()
		lateCallback := make(chan error, 1)
		final := liveTestDecision("canceled reply", "unused-state")
		agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
		speech := newCommittedCandidateLiveSpeech("hello")
		speech.synth = func(_ context.Context, _ int, onChunk speechio.StreamChunkHandler) (string, error) {
			if err := onChunk([]byte{1, 1}); err != nil {
				return "", err
			}
			// Ignore context cancellation until explicitly released by the test.
			<-providerRelease
			err := onChunk([]byte{99, 0})
			lateCallback <- err
			return speechio.StreamingAudioContentType, err
		}
		pipeline, err := New(speech, agent)
		if err != nil {
			t.Fatal(err)
		}
		var output []byte
		done := startCommittedCandidateLive(pipeline, ctx, httpapi.VoiceTurnInput{}, func(chunk []byte) error {
			output = append(output, chunk...)
			return nil
		})
		synctest.Wait()
		if len(speech.recordedTexts()) != 1 || len(output) != 0 {
			t.Fatal("candidate was not privately staged")
		}
		cancel()
		outcome := completedCommittedCandidateLive(t, done)
		if outcome.err == nil || len(output) != 0 {
			t.Fatalf("cancellation published or waited for provider: err=%v output=%v", outcome.err, output)
		}
		release()
		synctest.Wait()
		select {
		case err := <-lateCallback:
			if err == nil {
				t.Fatal("canceled buffer accepted late provider PCM")
			}
		default:
			t.Fatal("noncooperative provider did not finish")
		}
		if len(output) != 0 || len(speech.recordedTexts()) != 1 || len(agent.recordedCalls()) != 1 {
			t.Fatalf("canceled candidate leaked or retried: output=%v texts=%v", output, speech.recordedTexts())
		}
	})
}

func TestCommittedCandidateLiveFullBufferAndPostReleaseFailures(t *testing.T) {
	for _, mode := range []string{"success", "late provider error", "callback error"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				audit, providerRelease := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(providerRelease) }) }
				defer release()
				final := liveTestDecision("long spoken reply", "audited-state")
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
				speech := newCommittedCandidateLiveSpeech("hello")
				pcm := bytes.Repeat([]byte{40, 0}, maxSpeculativeTTSBufferBytes/2+1)
				speech.synth = func(ctx context.Context, _ int, onChunk speechio.StreamChunkHandler) (string, error) {
					if err := onChunk(pcm); err != nil {
						return "", err
					}
					select {
					case <-providerRelease:
					case <-ctx.Done():
						return "", ctx.Err()
					}
					if mode == "late provider error" {
						return "", errors.New("provider failed after publication")
					}
					return speechio.StreamingAudioContentType, nil
				}
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				var output []byte
				callbackCalls := 0
				done := startCommittedCandidateLive(pipeline, ctx, httpapi.VoiceTurnInput{}, func(chunk []byte) error {
					callbackCalls++
					if mode == "callback error" {
						return errors.New("transport rejected publication")
					}
					output = append(output, chunk...)
					return nil
				})
				synctest.Wait()
				if len(speech.recordedTexts()) != 1 || callbackCalls != 0 {
					t.Fatal("full buffer was not private during audit")
				}
				close(audit)
				synctest.Wait()
				if callbackCalls == 0 || (mode != "callback error" && !bytes.Equal(output, pcm)) {
					t.Fatalf("full buffer blocked committed progress: callbacks=%d output bytes=%d", callbackCalls, len(output))
				}
				release()
				outcome := completedCommittedCandidateLive(t, done)
				if (outcome.err != nil) != (mode != "success") || len(speech.recordedTexts()) != 1 || len(agent.recordedCalls()) != 1 {
					t.Fatalf("post-release result/retry mismatch: err=%v texts=%v agent=%d", outcome.err, speech.recordedTexts(), len(agent.recordedCalls()))
				}
				if mode == "callback error" && callbackCalls != 1 {
					t.Fatalf("failed callback was retried %d times", callbackCalls)
				}
			})
		})
	}
}

func TestCommittedCandidateLivePrivateFailureRetriesOnlySynthesis(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		audit := make(chan struct{})
		final := liveTestDecision("audited reply after private failure", "audited-state")
		agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
		speech := newCommittedCandidateLiveSpeech("hello")
		speech.synth = func(_ context.Context, index int, onChunk speechio.StreamChunkHandler) (string, error) {
			if err := onChunk([]byte{byte(index + 1), 1}); err != nil {
				return "", err
			}
			if index == 0 {
				return "", errors.New("private provider failure")
			}
			return speechio.StreamingAudioContentType, nil
		}
		pipeline, err := New(speech, agent)
		if err != nil {
			t.Fatal(err)
		}
		var output []byte
		done := startCommittedCandidateLive(pipeline, ctx, httpapi.VoiceTurnInput{}, func(chunk []byte) error {
			output = append(output, chunk...)
			return nil
		})
		synctest.Wait()
		if len(speech.recordedTexts()) != 1 || len(output) != 0 {
			t.Fatal("private failure crossed the output boundary")
		}
		close(audit)
		outcome := completedCommittedCandidateLive(t, done)
		if outcome.err != nil || len(agent.recordedCalls()) != 1 || len(speech.recordedTexts()) != 2 || !bytes.Equal(output, []byte{2, 1}) {
			t.Fatalf("private failure did not retry TTS alone: err=%v texts=%v output=%v", outcome.err, speech.recordedTexts(), output)
		}
	})
}

func TestCommittedCandidateLiveExcludesPassiveAndDocumentTurns(t *testing.T) {
	for _, document := range []bool{false, true} {
		name := "passive ambient"
		if document {
			name = "document"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{
					candidate: "must not be synthesized", final: liveTestDecision("", "silent-state"),
				}}}
				speech := newCommittedCandidateLiveSpeech("hello")
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				input := httpapi.VoiceTurnInput{Ambient: true}
				if document {
					input = httpapi.VoiceTurnInput{Document: &httpapi.VoiceDocument{MIMEType: "application/pdf", Data: []byte("%PDF-test")}}
				}
				callbacks := 0
				done := startCommittedCandidateLive(pipeline, context.Background(), input, func([]byte) error { callbacks++; return nil })
				outcome := completedCommittedCandidateLive(t, done)
				if outcome.err != nil || callbacks != 0 || len(speech.recordedTexts()) != 0 {
					t.Fatalf("excluded turn synthesized: err=%v callbacks=%d texts=%v", outcome.err, callbacks, speech.recordedTexts())
				}
				calls := agent.recordedCalls()
				if document {
					assertRuntimeDocumentRejected(t, outcome.result)
					if len(calls) != 0 || speech.session.ctx != nil || len(input.Document.Data) != 0 {
						t.Fatal("document crossed service boundary or was not cleared")
					}
				} else if len(calls) != 1 || calls[0].callback != nil || !calls[0].turn.Ambient || calls[0].turn.Foreground || outcome.result.StateToken != "silent-state" {
					t.Fatalf("passive turn did not keep ordinary processing: calls=%d result=%+v", len(calls), outcome.result)
				}
			})
		})
	}
}
