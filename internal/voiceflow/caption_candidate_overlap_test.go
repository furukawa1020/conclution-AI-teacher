package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type captionCandidateSpeech struct {
	*captionHandoffNoSTTSpeech
	synthesis *committedCandidateLiveSpeech
}

func newCaptionCandidateSpeech() *captionCandidateSpeech {
	return &captionCandidateSpeech{
		captionHandoffNoSTTSpeech: &captionHandoffNoSTTSpeech{scriptedLiveSpeech: &scriptedLiveSpeech{}},
		synthesis:                 newCommittedCandidateLiveSpeech(""),
	}
}

func (speech *captionCandidateSpeech) StreamSynthesize(ctx context.Context, text string, onChunk speechio.StreamChunkHandler) (string, error) {
	return speech.synthesis.StreamSynthesize(ctx, text, onChunk)
}

func openCaptionCandidateHandoff(t *testing.T, agent conversation.Agent, speech *captionCandidateSpeech, input httpapi.VoiceTurnInput, onAudio func([]byte) error, onCheckpoint func(httpapi.VoiceRespondentCheckpoint) error) (*captionHandoff, chan struct{}) {
	t.Helper()
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	committed := make(chan struct{})
	input.NativeAudio = true
	input.MIMEType = speechio.StreamingAudioContentType
	input.ProcessingCommitted = committed
	opened, err := pipeline.OpenCaptionHandoff(context.Background(), "caption-candidate-user", input, onAudio, onCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	return opened.(*captionHandoff), committed
}

func startCaptionCandidateCommit(handoff *captionHandoff, committed chan struct{}) <-chan committedCandidateLiveOutcome {
	close(committed)
	done := make(chan committedCandidateLiveOutcome, 1)
	go func() {
		result, err := handoff.Commit()
		done <- committedCandidateLiveOutcome{result: result, err: err}
	}()
	return done
}

func TestCaptionCandidateOverlapsCommittedAudit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		audit := make(chan struct{})
		final := liveTestDecision("audited ordinary reply", "audited-state")
		final.CoachPhase, final.CoachAction = "none", "none"
		agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
		speech := newCaptionCandidateSpeech()
		var output []byte
		handoff, committed := openCaptionCandidateHandoff(t, agent, speech, httpapi.VoiceTurnInput{}, func(chunk []byte) error {
			output = append(output, chunk...)
			return nil
		}, nil)
		defer handoff.Cancel()
		if err := handoff.Observe([]byte("hello"), true, time.Now()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(agent.recordedCalls()) != 0 || len(speech.synthesis.recordedTexts()) != 0 {
			t.Fatal("final-only caption started work before transport commit")
		}
		done := startCaptionCandidateCommit(handoff, committed)
		synctest.Wait()
		calls, texts := agent.recordedCalls(), speech.synthesis.recordedTexts()
		if len(calls) != 1 || calls[0].callback == nil || len(texts) != 1 || texts[0] != final.SpokenReply {
			t.Fatalf("private TTS did not overlap caption audit: agent calls=%d sealed=%t texts=%v", len(calls), len(calls) == 1 && calls[0].callback != nil, texts)
		}
		if calls[0].turn.Speculative || calls[0].turn.InputOrigin != conversation.InputOriginCommittedVoice || calls[0].turn.FloorEvidence != conversation.FloorEvidenceHybridCommitted || len(output) != 0 {
			t.Fatalf("committed provenance or privacy changed: turn=%+v output=%v", calls[0].turn, output)
		}
		close(audit)
		outcome := completedCommittedCandidateLive(t, done)
		if outcome.err != nil || outcome.result.Caption != final.SpokenReply || outcome.result.StateToken != final.StateToken || !bytes.Equal(output, []byte{1, 1}) || len(speech.synthesis.recordedTexts()) != 1 {
			t.Fatalf("matching candidate was not adopted: result=%+v err=%v output=%v", outcome.result, outcome.err, output)
		}
		if buffered, streaming := speech.sttCalls(); buffered != 0 || streaming != 0 {
			t.Fatalf("caption handoff reran STT: buffered=%d streaming=%d", buffered, streaming)
		}
	})
}

func TestCaptionCandidateRevisionAndExtendedFinalOverlap(t *testing.T) {
	for _, revision := range []bool{false, true} {
		name := "extended final"
		if revision {
			name = "revised final"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				audit := make(chan struct{})
				final := liveTestDecision("audited final reply", "final-state")
				final.CoachPhase, final.CoachAction = "none", "none"
				attempts := []committedCandidateLiveAttempt{}
				if revision {
					attempts = append(attempts, committedCandidateLiveAttempt{candidate: "revoked reply", audit: make(chan struct{})})
				}
				attempts = append(attempts, committedCandidateLiveAttempt{candidate: final.SpokenReply, final: final, audit: audit})
				agent := &committedCandidateLiveAgent{attempts: attempts}
				speech := newCaptionCandidateSpeech()
				var output []byte
				handoff, committed := openCaptionCandidateHandoff(t, agent, speech, httpapi.VoiceTurnInput{}, func(chunk []byte) error { output = append(output, chunk...); return nil }, nil)
				defer handoff.Cancel()
				caption := strings.Repeat("a", extendedSpeechMinRunes)
				wantCalls := 1
				if revision {
					if err := handoff.Observe([]byte("original caption"), false, time.Now()); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					if len(speech.synthesis.recordedTexts()) != 1 {
						t.Fatal("original revision was not staged")
					}
					caption, wantCalls = "revised caption", 2
				}
				if err := handoff.Observe([]byte(caption), true, time.Now()); err != nil {
					t.Fatal(err)
				}
				done := startCaptionCandidateCommit(handoff, committed)
				synctest.Wait()
				calls, texts := agent.recordedCalls(), speech.synthesis.recordedTexts()
				if len(calls) != wantCalls || len(texts) != wantCalls || texts[wantCalls-1] != final.SpokenReply || len(output) != 0 {
					t.Fatalf("committed overlap missing or stale PCM escaped: calls=%d texts=%v output=%v", len(calls), texts, output)
				}
				turn := calls[wantCalls-1].turn
				if turn.Speculative || turn.InputOrigin != conversation.InputOriginCommittedVoice || turn.FloorEvidence != conversation.FloorEvidenceHybridCommitted || turn.ExtendedSpeech != !revision || turn.Utterance != caption {
					t.Fatalf("committed metadata changed: %+v", turn)
				}
				close(audit)
				outcome := completedCommittedCandidateLive(t, done)
				if outcome.err != nil || outcome.result.Caption != final.SpokenReply || !bytes.Equal(output, []byte{byte(wantCalls), 1}) || len(speech.synthesis.recordedTexts()) != wantCalls {
					t.Fatalf("final synthesis reused revoked revision: err=%v output=%v", outcome.err, output)
				}
			})
		})
	}
}

func TestCaptionCandidateExpiredRetryCancelsBeforeNextInvocation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		firstAudit, secondAudit := make(chan struct{}), make(chan struct{})
		final := liveTestDecision("same reply in both attempts", "fresh-state")
		speech := newCaptionCandidateSpeech()
		canceledBeforeRetry := false
		agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{
			{candidate: final.SpokenReply, err: conversation.ErrExpiredStateToken, audit: firstAudit},
			{candidate: final.SpokenReply, final: final, audit: secondAudit, before: func() {
				speech.synthesis.mu.Lock()
				defer speech.synthesis.mu.Unlock()
				canceledBeforeRetry = len(speech.synthesis.contexts) == 1 && errors.Is(speech.synthesis.contexts[0].Err(), context.Canceled)
			}},
		}}
		var output []byte
		handoff, committed := openCaptionCandidateHandoff(t, agent, speech, httpapi.VoiceTurnInput{StateToken: "expired-state"}, func(chunk []byte) error { output = append(output, chunk...); return nil }, nil)
		defer handoff.Cancel()
		if err := handoff.Observe([]byte("hello"), true, time.Now()); err != nil {
			t.Fatal(err)
		}
		done := startCaptionCandidateCommit(handoff, committed)
		synctest.Wait()
		if len(speech.synthesis.recordedTexts()) != 1 || len(output) != 0 {
			t.Fatal("first attempt was not private")
		}
		close(firstAudit)
		synctest.Wait()
		calls := agent.recordedCalls()
		if !canceledBeforeRetry || len(calls) != 2 || calls[0].callback == nil || calls[1].callback == nil || calls[0].turn.StateToken != "expired-state" || calls[1].turn.StateToken != "" || len(speech.synthesis.recordedTexts()) != 2 || len(output) != 0 {
			t.Fatalf("retry ownership changed: canceled-before=%t calls=%d texts=%v output=%v", canceledBeforeRetry, len(calls), speech.synthesis.recordedTexts(), output)
		}
		calls[0].callback(conversation.SealedSpeechCandidate{SpokenReply: "late expired candidate"})
		synctest.Wait()
		if len(speech.synthesis.recordedTexts()) != 2 {
			t.Fatal("expired invocation revived its candidate")
		}
		close(secondAudit)
		outcome := completedCommittedCandidateLive(t, done)
		if outcome.err != nil || outcome.result.StateToken != final.StateToken || !bytes.Equal(output, []byte{2, 1}) || len(speech.synthesis.recordedTexts()) != 2 {
			t.Fatalf("retry released first attempt PCM: result=%+v err=%v output=%v", outcome.result, outcome.err, output)
		}
	})
}

func TestCaptionCandidateMismatchEmptyAndSilentPCM(t *testing.T) {
	for _, mode := range []string{"mismatch", "zero PCM", "digital silence", "silent final"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				audit := make(chan struct{})
				candidate := "early ordinary reply"
				final := liveTestDecision(candidate, "final-state")
				if mode == "mismatch" {
					final.SpokenReply = "different audited reply"
				}
				if mode == "silent final" {
					final.SpokenReply = ""
				}
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: candidate, final: final, audit: audit}}}
				speech := newCaptionCandidateSpeech()
				speech.synthesis.synth = func(_ context.Context, index int, onChunk speechio.StreamChunkHandler) (string, error) {
					if index == 0 && mode == "zero PCM" {
						return speechio.StreamingAudioContentType, nil
					}
					pcm := []byte{byte(index + 1), 1}
					if mode == "digital silence" {
						pcm = []byte{0, 0}
					}
					if err := onChunk(pcm); err != nil {
						return "", err
					}
					return speechio.StreamingAudioContentType, nil
				}
				var output []byte
				handoff, committed := openCaptionCandidateHandoff(t, agent, speech, httpapi.VoiceTurnInput{}, func(chunk []byte) error { output = append(output, chunk...); return nil }, nil)
				defer handoff.Cancel()
				if err := handoff.Observe([]byte("hello"), true, time.Now()); err != nil {
					t.Fatal(err)
				}
				done := startCaptionCandidateCommit(handoff, committed)
				synctest.Wait()
				if len(speech.synthesis.recordedTexts()) != 1 || len(output) != 0 {
					t.Fatal("candidate did not remain private during audit")
				}
				close(audit)
				outcome := completedCommittedCandidateLive(t, done)
				wantCalls, wantPCM := 2, []byte{2, 1}
				if mode == "silent final" {
					speech.synthesis.mu.Lock()
					providerCanceled := len(speech.synthesis.contexts) == 1 && errors.Is(speech.synthesis.contexts[0].Err(), context.Canceled)
					speech.synthesis.mu.Unlock()
					if outcome.err != nil || outcome.result.Caption != "" || outcome.result.StateToken != final.StateToken || len(output) != 0 || len(speech.synthesis.recordedTexts()) != 1 || !providerCanceled {
						t.Fatalf("silent final retained private synthesis or lost state: result=%+v err=%v output=%v canceled=%t", outcome.result, outcome.err, output, providerCanceled)
					}
					return
				}
				if mode == "digital silence" {
					wantCalls, wantPCM = 1, []byte{0, 0}
				}
				texts := speech.synthesis.recordedTexts()
				if outcome.err != nil || len(texts) != wantCalls || texts[wantCalls-1] != final.SpokenReply || !bytes.Equal(output, wantPCM) || len(agent.recordedCalls()) != 1 {
					t.Fatalf("caption candidate acceptance changed: err=%v texts=%v output=%v", outcome.err, texts, output)
				}
				if mode == "digital silence" && (outcome.result.LiveTimings.TTSFirstChunkMS != -1 || outcome.result.LiveTimings.FinalToFirstAudioMS != -1) {
					t.Fatalf("digital silence counted as meaningful audio: %+v", outcome.result.LiveTimings)
				}
			})
		})
	}
}

func TestCaptionCandidateRespondentFinalRequiresCheckpoint(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "checkpoint accepted"
		if reject {
			name = "checkpoint rejected"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				audit := make(chan struct{})
				final := captionHandoffRespondentDecision()
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
				speech := newCaptionCandidateSpeech()
				var output []byte
				checkpointCalls, accepted := 0, false
				handoff, committed := openCaptionCandidateHandoff(t, agent, speech, httpapi.VoiceTurnInput{}, func(chunk []byte) error {
					if !accepted {
						return errors.New("PCM escaped before checkpoint acceptance")
					}
					output = append(output, chunk...)
					return nil
				}, func(checkpoint httpapi.VoiceRespondentCheckpoint) error {
					checkpointCalls++
					if checkpoint.SessionState != final.StateToken || checkpoint.AssistanceTarget != "respondent" || checkpoint.CoachAction != final.CoachAction {
						return errors.New("wrong final checkpoint")
					}
					if reject {
						return errors.New("transport rejected checkpoint")
					}
					accepted = true
					return nil
				})
				defer handoff.Cancel()
				if err := handoff.Observe([]byte("hello"), true, time.Now()); err != nil {
					t.Fatal(err)
				}
				done := startCaptionCandidateCommit(handoff, committed)
				synctest.Wait()
				if len(speech.synthesis.recordedTexts()) != 1 || len(output) != 0 || checkpointCalls != 0 {
					t.Fatal("candidate bypassed final ownership decision")
				}
				close(audit)
				outcome := completedCommittedCandidateLive(t, done)
				if checkpointCalls != 1 || (outcome.err != nil) != reject || len(agent.recordedCalls()) != 1 {
					t.Fatalf("checkpoint result changed: count=%d err=%v", checkpointCalls, outcome.err)
				}
				if reject {
					if len(output) != 0 || len(speech.synthesis.recordedTexts()) != 1 {
						t.Fatal("rejected checkpoint released or restarted synthesis")
					}
				} else if !bytes.Equal(output, []byte{2, 1}) || len(speech.synthesis.recordedTexts()) != 2 || outcome.result.Route != httpapi.VoiceNativeRespondentCoachRoute {
					t.Fatalf("ordinary candidate authorized respondent PCM: output=%v result=%+v", output, outcome.result)
				}
			})
		})
	}
}

func TestCaptionCandidateCancelRejectsNoncooperativeProvider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		audit, providerRelease := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(providerRelease) }) }
		defer release()
		lateCallback := make(chan error, 1)
		final := liveTestDecision("canceled caption reply", "unused-state")
		agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
		speech := newCaptionCandidateSpeech()
		speech.synthesis.synth = func(_ context.Context, _ int, onChunk speechio.StreamChunkHandler) (string, error) {
			if err := onChunk([]byte{1, 1}); err != nil {
				return "", err
			}
			<-providerRelease // Deliberately ignore cancellation until released.
			err := onChunk([]byte{99, 1})
			lateCallback <- err
			return speechio.StreamingAudioContentType, err
		}
		var output []byte
		handoff, committed := openCaptionCandidateHandoff(t, agent, speech, httpapi.VoiceTurnInput{}, func(chunk []byte) error { output = append(output, chunk...); return nil }, nil)
		defer handoff.Cancel()
		if err := handoff.Observe([]byte("hello"), true, time.Now()); err != nil {
			t.Fatal(err)
		}
		done := startCaptionCandidateCommit(handoff, committed)
		synctest.Wait()
		if len(speech.synthesis.recordedTexts()) != 1 || len(output) != 0 {
			t.Fatal("candidate was not privately staged")
		}
		handoff.Cancel()
		outcome := completedCommittedCandidateLive(t, done)
		if outcome.err == nil || len(output) != 0 {
			t.Fatalf("canceled Commit published or waited for provider: err=%v output=%v", outcome.err, output)
		}
		release()
		synctest.Wait()
		select {
		case err := <-lateCallback:
			if err == nil {
				t.Fatal("canceled candidate accepted late PCM")
			}
		default:
			t.Fatal("noncooperative provider did not finish")
		}
		if len(output) != 0 || len(speech.synthesis.recordedTexts()) != 1 || len(agent.recordedCalls()) != 1 {
			t.Fatal("canceled caption candidate published or retried")
		}
	})
}

func TestCaptionCandidateFullBufferAndPostReleaseFailures(t *testing.T) {
	for _, mode := range []string{"success", "late provider error", "callback error"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				audit, providerRelease := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(providerRelease) }) }
				defer release()
				final := liveTestDecision("long ordinary reply", "audited-state")
				agent := &committedCandidateLiveAgent{attempts: []committedCandidateLiveAttempt{{candidate: final.SpokenReply, final: final, audit: audit}}}
				speech := newCaptionCandidateSpeech()
				pcm := bytes.Repeat([]byte{40, 0}, maxSpeculativeTTSBufferBytes/2+1)
				speech.synthesis.synth = func(ctx context.Context, _ int, onChunk speechio.StreamChunkHandler) (string, error) {
					if err := onChunk(pcm); err != nil {
						return "", err
					}
					select {
					case <-providerRelease:
					case <-ctx.Done():
						return "", ctx.Err()
					}
					if mode == "late provider error" {
						return "", errors.New("provider failed after output")
					}
					return speechio.StreamingAudioContentType, nil
				}
				var output []byte
				callbackCalls := 0
				handoff, committed := openCaptionCandidateHandoff(t, agent, speech, httpapi.VoiceTurnInput{}, func(chunk []byte) error {
					callbackCalls++
					if mode == "callback error" {
						return errors.New("output transport failed")
					}
					output = append(output, chunk...)
					return nil
				}, nil)
				defer handoff.Cancel()
				if err := handoff.Observe([]byte("hello"), true, time.Now()); err != nil {
					t.Fatal(err)
				}
				done := startCaptionCandidateCommit(handoff, committed)
				synctest.Wait()
				if len(speech.synthesis.recordedTexts()) != 1 || callbackCalls != 0 {
					t.Fatal("full candidate buffer escaped during audit")
				}
				close(audit)
				synctest.Wait()
				if callbackCalls == 0 || (mode != "callback error" && !bytes.Equal(output, pcm)) {
					t.Fatalf("full buffer blocked committed progress: callbacks=%d bytes=%d", callbackCalls, len(output))
				}
				release()
				outcome := completedCommittedCandidateLive(t, done)
				if (outcome.err != nil) != (mode != "success") || len(speech.synthesis.recordedTexts()) != 1 || len(agent.recordedCalls()) != 1 {
					t.Fatalf("post-release failure retried: err=%v texts=%v", outcome.err, speech.synthesis.recordedTexts())
				}
				if mode == "callback error" && callbackCalls != 1 {
					t.Fatalf("failed callback was invoked %d times", callbackCalls)
				}
			})
		})
	}
}
