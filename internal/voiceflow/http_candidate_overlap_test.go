package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

func newHTTPCandidateSpeech() *committedCandidateLiveSpeech {
	speech := newCommittedCandidateLiveSpeech("hello")
	speech.fakeSpeech = fakeSpeech{transcript: "hello", confidence: .99}
	return speech
}

func startHTTPCandidate(pipeline *Pipeline, ctx context.Context, input httpapi.VoiceTurnInput, onAudio func([]byte) error) <-chan committedCandidateLiveOutcome {
	done := make(chan committedCandidateLiveOutcome, 1)
	go func() {
		result, err := pipeline.ProcessStream(ctx, "http-candidate-user", input, onAudio)
		done <- committedCandidateLiveOutcome{result: result, err: err}
	}()
	return done
}

func TestHTTPCandidateOverlapsAuditAndRequiresExactFinalAuthority(t *testing.T) {
	for _, mode := range []string{"match", "mismatch", "research status", "research records", "respondent target", "respondent stage", "coach phase", "coach action", "silent", "audit error", "terminal ownership"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				audit := make(chan struct{})
				candidate := "ordinary sealed reply"
				final := liveTestDecision(candidate, "final-state")
				// Production uses explicit enum values, not just empty test fields.
				final.CoachPhase, final.CoachAction = "none", "none"
				wantAudio, wantErr := true, false
				var agentErr error
				switch mode {
				case "mismatch":
					final.SpokenReply += " " // Equality must not normalize final text.
				case "research status":
					final.ResearchStatus = "complete"
				case "research records":
					final.ResearchRecords = []conversation.ResearchRecord{{}}
				case "respondent target":
					final.AssistanceTarget = "respondent"
				case "respondent stage":
					final.RespondentStage = "restructure"
				case "coach phase":
					final.CoachPhase = "complete"
				case "coach action":
					final.CoachAction = "complete"
				case "silent":
					final.SpokenReply, wantAudio = "", false
				case "audit error":
					agentErr, wantAudio, wantErr = errors.New("audit failed"), false, true
				case "terminal ownership":
					final.CoachPhase, final.CoachAction = "complete", "complete"
					final.AnswerProof = conversation.AnswerProofQuestionBoundInputAnswerFirst
					wantAudio, wantErr = false, true
				}
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: candidate, final: final, err: agentErr, audit: audit}}}
				speech := newHTTPCandidateSpeech()
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				var output []byte
				done := startHTTPCandidate(pipeline, context.Background(), httpapi.VoiceTurnInput{}, func(chunk []byte) error { output = append(output, chunk...); return nil })
				synctest.Wait()
				calls := agent.recordedCalls()
				if len(calls) != 1 || calls[0].callback == nil || len(speech.recordedTexts()) != 1 || len(output) != 0 {
					t.Fatal("candidate did not overlap pending audit privately")
				}
				if turn := calls[0].turn; turn.Speculative || turn.InputOrigin != conversation.InputOriginCommittedVoice || turn.FloorEvidence != conversation.FloorEvidenceUnknown || turn.Utterance != "hello" {
					t.Fatalf("committed recognition provenance changed: %+v", turn)
				}
				select {
				case <-done:
					t.Fatal("result escaped before final audit")
				default:
				}
				close(audit)
				outcome := completedCommittedCandidateLive(t, done)
				if (outcome.err != nil) != wantErr || len(agent.recordedCalls()) != 1 {
					t.Fatalf("err=%v calls=%d", outcome.err, len(agent.recordedCalls()))
				}
				texts := speech.recordedTexts()
				if wantAudio {
					wantCalls, wantPCM := 2, []byte{2, 1}
					if mode == "match" {
						wantCalls, wantPCM = 1, []byte{1, 1}
					}
					if len(texts) != wantCalls || texts[wantCalls-1] != final.SpokenReply || !bytes.Equal(output, wantPCM) || outcome.result.Caption != final.SpokenReply || outcome.result.StateToken != final.StateToken || outcome.result.AssistanceTarget != final.AssistanceTarget || outcome.result.RespondentStage != final.RespondentStage || outcome.result.CoachPhase != final.CoachPhase || outcome.result.CoachAction != final.CoachAction {
						t.Fatalf("final authority changed: texts=%v output=%v result=%+v", texts, output, outcome.result)
					}
				} else if len(texts) != 1 || len(output) != 0 {
					t.Fatalf("rejected candidate published: texts=%v output=%v", texts, output)
				}
				calls[0].callback(conversation.SealedSpeechCandidate{SpokenReply: "late candidate"})
				synctest.Wait()
				if len(speech.recordedTexts()) != len(texts) || speech.contexts[0].Err() == nil {
					t.Fatal("completed attempt retained candidate authority")
				}
			})
		})
	}
}

func TestHTTPCandidateRemovesSynthesisFromAuditedCriticalPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		audit := make(chan struct{})
		final := liveTestDecision("approved reply", "final-state")
		agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
		speech := newHTTPCandidateSpeech()
		speech.synth = func(_ context.Context, _ int, onChunk speechio.StreamChunkHandler) (string, error) {
			time.Sleep(100 * time.Millisecond) // Virtual provider work.
			return speechio.StreamingAudioContentType, onChunk([]byte{1, 1})
		}
		pipeline, err := New(speech, agent)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		go func() {
			time.Sleep(250 * time.Millisecond) // Virtual independent audit.
			close(audit)
		}()
		var firstOutput time.Time
		outcome := <-startHTTPCandidate(pipeline, context.Background(), httpapi.VoiceTurnInput{}, func([]byte) error { firstOutput = time.Now(); return nil })
		// Serial execution would take 350 ms. PCM becomes public at the audit
		// boundary (250 ms), never at the earlier provider completion (100 ms).
		if outcome.err != nil || firstOutput.Sub(started) != 250*time.Millisecond || len(speech.recordedTexts()) != 1 {
			t.Fatalf("audit/TTS remained serial or published early: err=%v first_output=%v", outcome.err, firstOutput.Sub(started))
		}
	})
}

func TestHTTPCandidateExpiredStateRevokesBeforeRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		firstAudit, secondAudit := make(chan struct{}), make(chan struct{})
		final := liveTestDecision("identical reply", "fresh-state")
		speech := newHTTPCandidateSpeech()
		retrySawCanceled := false
		agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{
			{candidate: final.SpokenReply, audit: firstAudit, err: conversation.ErrExpiredStateToken},
			{candidate: final.SpokenReply, audit: secondAudit, final: final, before: func() {
				speech.mu.Lock()
				defer speech.mu.Unlock()
				retrySawCanceled = len(speech.contexts) == 1 && errors.Is(speech.contexts[0].Err(), context.Canceled)
			}},
		}}
		pipeline, err := New(speech, agent)
		if err != nil {
			t.Fatal(err)
		}
		var output []byte
		done := startHTTPCandidate(pipeline, context.Background(), httpapi.VoiceTurnInput{StateToken: "expired-state"}, func(chunk []byte) error { output = append(output, chunk...); return nil })
		synctest.Wait()
		close(firstAudit)
		synctest.Wait()
		calls := agent.recordedCalls()
		if !retrySawCanceled || len(calls) != 2 || calls[0].turn.StateToken != "expired-state" || calls[1].turn.StateToken != "" || len(output) != 0 || len(speech.recordedTexts()) != 2 {
			t.Fatalf("retry reused expired authority: canceled=%t calls=%d output=%v", retrySawCanceled, len(calls), output)
		}
		calls[0].callback(conversation.SealedSpeechCandidate{SpokenReply: "expired late reply"})
		synctest.Wait()
		close(secondAudit)
		outcome := completedCommittedCandidateLive(t, done)
		if outcome.err != nil || outcome.result.StateToken != final.StateToken || !bytes.Equal(output, []byte{2, 1}) || len(speech.recordedTexts()) != 2 {
			t.Fatalf("retry released stale PCM: result=%+v err=%v output=%v", outcome.result, outcome.err, output)
		}
	})
}

func TestHTTPCandidatePreservesIneligibleAndRecognitionMissPaths(t *testing.T) {
	for _, mode := range []string{"strict", "document", "passive", "no speech", "low confidence", "ambient miss"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				input := httpapi.VoiceTurnInput{}
				final := safePrivacyDecision()
				speech := newHTTPCandidateSpeech()
				wantAgent, wantAudio := 0, false
				switch mode {
				case "strict":
					input.StrictCloudMinimization = true
					wantAgent, wantAudio = 1, true
				case "document":
					input.Document = &httpapi.VoiceDocument{Data: []byte("private")}
				case "passive":
					input.Ambient, final.SpokenReply, wantAgent = true, "", 1
				case "no speech":
					speech.transcribeErr, wantAudio = speechio.ErrNoSpeech, true
				case "low confidence":
					speech.confidence, wantAudio = .01, true
				case "ambient miss":
					input.Ambient, input.Foreground = true, true
					speech.transcribeErr = speechio.ErrNoSpeech
				}
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: "uninspected", final: final}}}
				inspector := &scriptedInspector{statuses: []privacyguard.InspectionStatus{privacyguard.InspectionClear, privacyguard.InspectionClear}}
				pipeline, err := NewWithPrivacy(speech, agent, inspector)
				if err != nil {
					t.Fatal(err)
				}
				var output []byte
				outcome := completedCommittedCandidateLive(t, startHTTPCandidate(pipeline, context.Background(), input, func(chunk []byte) error { output = append(output, chunk...); return nil }))
				calls := agent.recordedCalls()
				if outcome.err != nil || len(calls) != wantAgent || (len(calls) > 0 && calls[0].callback != nil) || (len(output) > 0) != wantAudio {
					t.Fatalf("eligibility changed: result=%+v err=%v calls=%d output=%v", outcome.result, outcome.err, len(calls), output)
				}
				if mode == "strict" && (len(inspector.calls) != 2 || outcome.result.PrivacyStatus != "clear" || outcome.result.StateToken != "") {
					t.Fatal("strict inspection or state suppression changed")
				}
				if mode == "document" && (input.Document.Data != nil || outcome.result.Route != routeRuntimeDocumentRejected) {
					t.Fatal("document rejection changed")
				}
				if (mode == "no speech" || mode == "low confidence") && outcome.result.Caption != lowConfidencePrompt {
					t.Fatal("recognition miss prompt changed")
				}
			})
		})
	}
}

func TestHTTPCandidatePrivateFailureRetriesOnlyFinalSynthesis(t *testing.T) {
	for _, mode := range []string{"empty", "odd PCM", "wrong MIME", "provider error", "digital silence"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				audit := make(chan struct{})
				final := liveTestDecision("approved reply", "final-state")
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
				speech := newHTTPCandidateSpeech()
				speech.synth = func(_ context.Context, index int, onChunk speechio.StreamChunkHandler) (string, error) {
					if index == 0 {
						switch mode {
						case "empty":
							return speechio.StreamingAudioContentType, nil
						case "odd PCM":
							return "", onChunk([]byte{1})
						case "digital silence":
							return speechio.StreamingAudioContentType, onChunk([]byte{0, 0})
						}
					}
					if err := onChunk([]byte{byte(index + 1), 1}); err != nil {
						return "", err
					}
					if index == 0 && mode == "wrong MIME" {
						return "audio/wav", nil
					}
					if index == 0 && mode == "provider error" {
						return "", errors.New("private provider failed")
					}
					return speechio.StreamingAudioContentType, nil
				}
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				var output []byte
				done := startHTTPCandidate(pipeline, context.Background(), httpapi.VoiceTurnInput{}, func(chunk []byte) error { output = append(output, chunk...); return nil })
				synctest.Wait()
				close(audit)
				outcome := completedCommittedCandidateLive(t, done)
				wantCalls, wantPCM := 2, []byte{2, 1}
				if mode == "digital silence" {
					wantCalls, wantPCM = 1, []byte{0, 0}
				}
				if outcome.err != nil || len(agent.recordedCalls()) != 1 || len(speech.recordedTexts()) != wantCalls || !bytes.Equal(output, wantPCM) {
					t.Fatalf("private failure repeated model or mixed PCM: err=%v texts=%v output=%v", outcome.err, speech.recordedTexts(), output)
				}
			})
		})
	}
}

func TestHTTPCandidateFullBufferAndPostReleaseFailuresNeverRetry(t *testing.T) {
	for _, mode := range []string{"success", "callback error", "late provider error"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				audit, providerRelease := make(chan struct{}), make(chan struct{})
				final := liveTestDecision("long reply", "final-state")
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
				speech := newHTTPCandidateSpeech()
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
						return "", errors.New("provider failed after release")
					}
					return speechio.StreamingAudioContentType, nil
				}
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				var output []byte
				callbackCalls := 0
				done := startHTTPCandidate(pipeline, context.Background(), httpapi.VoiceTurnInput{}, func(chunk []byte) error {
					callbackCalls++
					if mode == "callback error" {
						return errors.New("transport rejected output")
					}
					output = append(output, chunk...)
					return nil
				})
				synctest.Wait()
				if callbackCalls != 0 || len(speech.recordedTexts()) != 1 {
					t.Fatal("full buffer escaped pending audit")
				}
				close(audit)
				synctest.Wait()
				if callbackCalls == 0 || (mode != "callback error" && !bytes.Equal(output, pcm)) {
					t.Fatal("full buffer did not unblock after final audit")
				}
				close(providerRelease)
				outcome := completedCommittedCandidateLive(t, done)
				if (outcome.err != nil) != (mode != "success") || len(speech.recordedTexts()) != 1 || len(agent.recordedCalls()) != 1 || (mode == "callback error" && callbackCalls != 1) {
					t.Fatalf("post-release failure retried: err=%v texts=%v callbacks=%d", outcome.err, speech.recordedTexts(), callbackCalls)
				}
			})
		})
	}
}

func TestHTTPCandidateCancellationDiscardsNoncooperativeProvider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		audit, providerRelease := make(chan struct{}), make(chan struct{})
		lateChunk := make(chan error, 1)
		final := liveTestDecision("canceled reply", "unused-state")
		agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
		speech := newHTTPCandidateSpeech()
		speech.synth = func(_ context.Context, _ int, onChunk speechio.StreamChunkHandler) (string, error) {
			if err := onChunk([]byte{1, 1}); err != nil {
				return "", err
			}
			<-providerRelease // Intentionally ignore cancellation.
			err := onChunk([]byte{99, 1})
			lateChunk <- err
			return speechio.StreamingAudioContentType, err
		}
		pipeline, err := New(speech, agent)
		if err != nil {
			t.Fatal(err)
		}
		outputCalls := 0
		done := startHTTPCandidate(pipeline, ctx, httpapi.VoiceTurnInput{}, func([]byte) error { outputCalls++; return nil })
		synctest.Wait()
		cancel()
		outcome := completedCommittedCandidateLive(t, done)
		close(providerRelease)
		synctest.Wait()
		if outcome.err == nil || outputCalls != 0 || len(speech.recordedTexts()) != 1 || len(agent.recordedCalls()) != 1 || <-lateChunk == nil {
			t.Fatal("canceled candidate published, retried, or waited for noncooperative provider")
		}
	})
}

func TestHTTPCandidateOwnsPreparedConnectionOnce(t *testing.T) {
	for _, mode := range []string{"match", "mismatch", "slow preparation", "expired preparation"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				recognition, audit := make(chan struct{}), make(chan struct{})
				candidate := "sealed reply"
				final := liveTestDecision(candidate, "final-state")
				if mode == "mismatch" {
					final.SpokenReply = "different final reply"
				}
				prepared := &fakePreparedSynthesis{chunks: [][]byte{{80, 0}}}
				if mode == "expired preparation" {
					prepared.chunks, prepared.err = nil, context.DeadlineExceeded
				}
				speech := &preparingHTTPSpeech{
					preparingStreamingSpeech: preparingStreamingSpeech{
						fakeStreamingSpeech: fakeStreamingSpeech{fakeSpeech: fakeSpeech{transcript: "hello", confidence: .99}, chunks: [][]byte{{90, 0}}},
						prepared:            prepared, prepareStarted: make(chan struct{}), prepareCanceled: make(chan struct{}), blockPrepare: mode == "slow preparation",
					},
					transcriptionGate: recognition,
				}
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: candidate, final: final, audit: audit}}}
				pipeline, err := New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				var output []byte
				done := startHTTPCandidate(pipeline, context.Background(), httpapi.VoiceTurnInput{}, func(chunk []byte) error { output = append(output, chunk...); return nil })
				synctest.Wait()
				if prepared.calls != 0 || speech.streamCalls != 0 || len(agent.recordedCalls()) != 0 {
					t.Fatal("recognition-pending preparation sent content")
				}
				close(recognition)
				synctest.Wait()
				wantPrepared, wantOrdinary, wantPCM := 1, 0, []byte{80, 0}
				if mode == "slow preparation" {
					wantPrepared = 0
				}
				if mode == "slow preparation" || mode == "expired preparation" {
					wantOrdinary, wantPCM = 1, []byte{90, 0}
				}
				if prepared.calls != wantPrepared || speech.streamCalls != wantOrdinary || len(output) != 0 {
					t.Fatal("candidate did not privately consume only a ready connection")
				}
				close(audit)
				outcome := completedCommittedCandidateLive(t, done)
				if mode == "mismatch" {
					wantOrdinary, wantPCM = 1, []byte{90, 0}
				}
				if outcome.err != nil || prepared.calls != wantPrepared || speech.streamCalls != wantOrdinary || !bytes.Equal(output, wantPCM) || len(agent.recordedCalls()) != 1 {
					t.Fatalf("prepared connection reused: err=%v prepared=%d ordinary=%d output=%v", outcome.err, prepared.calls, speech.streamCalls, output)
				}
				if mode == "slow preparation" {
					select {
					case <-speech.prepareCanceled:
					default:
						t.Fatal("unused preparation was not canceled")
					}
				} else if !prepared.closed || prepared.ctx.Err() == nil {
					t.Fatal("transferred prepared connection was not closed and canceled")
				}
			})
		})
	}
}
