package nativeflow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
	"github.com/furukawa1020/conclution-ai-teacher/internal/voiceflow"
)

// Hold every provider caption while the real handoff has an opportunity to
// prepare configuration. This separates useful overlap from faster fake TTS.
type earlyCaptionSession struct {
	*scriptedSession
	captionReady chan struct{}
	receives     atomic.Int64
}

func (session *earlyCaptionSession) Receive(ctx context.Context) (nativevoice.Event, error) {
	session.receives.Add(1)
	select {
	case <-session.captionReady:
		return session.scriptedSession.Receive(ctx)
	case <-ctx.Done():
		return nativevoice.Event{}, ctx.Err()
	}
}

type earlyPreparedSpeech struct {
	checkpointIntegrationSpeech
	prepares atomic.Int64
	uses     atomic.Int64
	closes   atomic.Int64
	cold     atomic.Int64
}

func (speech *earlyPreparedSpeech) PrepareStreamingSynthesis(ctx context.Context) (speechio.PreparedStreamingSynthesis, error) {
	speech.prepares.Add(1)
	return &earlyPreparedCapability{speech: speech, ctx: ctx}, nil
}

func (speech *earlyPreparedSpeech) StreamSynthesize(context.Context, string, speechio.StreamChunkHandler) (string, error) {
	speech.cold.Add(1)
	return "", errors.New("ready preparation was not reused")
}

type earlyPreparedCapability struct {
	speech *earlyPreparedSpeech
	ctx    context.Context
	closed atomic.Bool
}

func (prepared *earlyPreparedCapability) StreamSynthesize(text string, onChunk speechio.StreamChunkHandler) (string, error) {
	if err := prepared.ctx.Err(); err != nil {
		return "", err
	}
	prepared.speech.uses.Add(1)
	return speechio.StreamingAudioContentType, onChunk([]byte{1, 0})
}

func (prepared *earlyPreparedCapability) Close() {
	if prepared.closed.CompareAndSwap(false, true) {
		prepared.speech.closes.Add(1)
	}
}

type earlyStagedAgent struct {
	calls atomic.Int64
}

func (agent *earlyStagedAgent) Process(context.Context, string, conversation.VoiceTurn) (conversation.VoiceTurnResult, error) {
	agent.calls.Add(1)
	return conversation.VoiceTurnResult{
		Domain: "conversation", AssistanceTarget: "respondent",
		RespondentStage: "awaiting_answer", CoachPhase: "awaiting_answer", CoachAction: "elicit",
		StateToken: "question-bound-state", ResearchStatus: "none",
		SpokenReply: "今の一言をどうぞ。",
	}, nil
}

func TestNativeKnownStagedPreparesBeforeCaptionAndReusesOrCloses(t *testing.T) {
	for _, cancelBeforeCaption := range []bool{false, true} {
		name := "reuse after final caption"
		if cancelBeforeCaption {
			name = "cancel unused preparation"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				session := &earlyCaptionSession{
					scriptedSession: newScriptedSession(nativeCaptionEvent("こんにちは")),
					captionReady:    make(chan struct{}),
				}
				speech := &earlyPreparedSpeech{}
				agent := &earlyStagedAgent{}
				pipeline, err := voiceflow.New(speech, agent)
				if err != nil {
					t.Fatal(err)
				}
				service, err := NewWithCaptionHandoff(&fakeOpener{session: session}, fakePreparer{token: "prepared-state", requiresStaged: true}, pipeline)
				if err != nil {
					t.Fatal(err)
				}
				defer service.Close()
				var checkpointCalls, pcmCalls atomic.Int64
				input := nativeInput()
				processingCommitted := make(chan struct{})
				input.ProcessingCommitted = processingCommitted
				input.ProcessingCommittedAt = nil
				audio := make(chan []byte, 1)
				audio <- make([]byte, nativevoice.InputFrameBytes)
				var closeInputOnce sync.Once
				closeInput := func() { closeInputOnce.Do(func() { close(audio) }) }
				defer closeInput()
				done := make(chan error, 1)
				go func() {
					_, err := service.ProcessLiveWithControl(ctx, "early-known-staged", input, audio,
						func([]byte) error {
							if checkpointCalls.Load() != 1 {
								return errors.New("PCM preceded checkpoint")
							}
							pcmCalls.Add(1)
							return nil
						}, nil, func(transition httpapi.VoiceRespondentCheckpointTransition) error {
							if transition.PreviousSessionState != "prepared-state" {
								return errors.New("stale state")
							}
							checkpointCalls.Add(1)
							return nil
						})
					done <- err
				}()
				synctest.Wait()
				if speech.prepares.Load() != 1 || session.receives.Load() != 1 {
					t.Fatalf("caption wait did not overlap one preparation: prepares=%d receives=%d", speech.prepares.Load(), session.receives.Load())
				}
				if agent.calls.Load() != 0 || speech.uses.Load() != 0 || speech.cold.Load() != 0 || pcmCalls.Load() != 0 || checkpointCalls.Load() != 0 {
					t.Fatal("configuration preparation started semantic work or published output")
				}
				if cancelBeforeCaption {
					cancel()
				} else {
					close(processingCommitted)
					close(session.captionReady)
				}
				closeInput()
				err = <-done
				synctest.Wait()
				wantUses := int64(1)
				if cancelBeforeCaption {
					wantUses = 0
				}
				if (err != nil) != cancelBeforeCaption || speech.prepares.Load() != 1 || speech.closes.Load() != 1 || speech.uses.Load() != wantUses || speech.cold.Load() != 0 || agent.calls.Load() != wantUses || pcmCalls.Load() != wantUses || checkpointCalls.Load() != wantUses {
					t.Fatalf("lifecycle err=%v prepares=%d closes=%d uses=%d cold=%d model=%d pcm=%d checkpoint=%d", err, speech.prepares.Load(), speech.closes.Load(), speech.uses.Load(), speech.cold.Load(), agent.calls.Load(), pcmCalls.Load(), checkpointCalls.Load())
				}
				session.mu.Lock()
				commits, closes := session.commits, session.closes
				session.mu.Unlock()
				if commits != 0 || closes != 1 || speech.transcriptionCount() != 0 {
					t.Fatalf("native commits=%d closes=%d extra recognition=%d", commits, closes, speech.transcriptionCount())
				}
			})
		})
	}
}

func TestNativeOrdinaryDefersHandoffUntilCoachCaption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		session := &earlyCaptionSession{scriptedSession: newScriptedSession(nativeCaptionEvent("My manager asked why. Help me answer.")), captionReady: make(chan struct{})}
		handoff := &recordingCaptionHandoffService{}
		service, err := NewWithCaptionHandoff(&fakeOpener{session: session}, fakePreparer{token: "ordinary-state"}, handoff)
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		done := make(chan error, 1)
		go func() {
			_, err := service.ProcessLiveWithControl(ctx, "ordinary", nativeInput(), oneFrame(), func([]byte) error { return nil }, nil, func(httpapi.VoiceRespondentCheckpointTransition) error { return nil })
			done <- err
		}()
		synctest.Wait()
		if session.receives.Load() != 1 || handoff.openCount() != 0 {
			t.Fatal("ordinary Native opened handoff before coach detection")
		}
		close(session.captionReady)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if handoff.openCount() != 1 {
			t.Fatalf("coach handoff opens=%d", handoff.openCount())
		}
		observations, commits, cancels := handoff.lastHandoff.snapshot()
		if len(observations) != 1 || commits != 1 || cancels != 1 {
			t.Fatalf("observations=%d commits=%d cancels=%d", len(observations), commits, cancels)
		}
	})
}

func TestNativeKnownStagedOpenFailureDoesNotWaitForCaption(t *testing.T) {
	for _, failure := range []error{errors.New("open unavailable"), httpapi.ErrVoiceNativeFallback} {
		t.Run(failure.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				session := &earlyCaptionSession{scriptedSession: newScriptedSession(), captionReady: make(chan struct{})}
				handoff := &recordingCaptionHandoffService{openErr: failure}
				opener := &fakeOpener{session: session}
				service, err := NewWithCaptionHandoff(opener, fakePreparer{requiresStaged: true}, handoff)
				if err != nil {
					t.Fatal(err)
				}
				defer service.Close()
				var pcm, checkpoints atomic.Int64
				done := make(chan error, 1)
				go func() {
					_, err := service.ProcessLiveWithControl(ctx, "open-failure", nativeInput(), oneFrame(), func([]byte) error { pcm.Add(1); return nil }, nil, func(httpapi.VoiceRespondentCheckpointTransition) error { checkpoints.Add(1); return nil })
					done <- err
				}()
				synctest.Wait()
				select {
				case err := <-done:
					// Early failure must never masquerade as post-commit replay
					// readiness: no caption has been validated yet.
					if !errors.Is(err, errNativeFlowUnavailable) {
						t.Fatalf("early error=%v", err)
					}
				default:
					cancel()
					<-done
					t.Fatal("known handoff failure waited for caption")
				}
				if handoff.openCount() != 1 || session.receives.Load() != 0 || pcm.Load() != 0 || checkpoints.Load() != 0 {
					t.Fatal("early failure proceeded into caption or publication")
				}
				session.mu.Lock()
				closed, committed := session.closes, session.commits
				session.mu.Unlock()
				if opener.opens != 0 || closed != 0 || committed != 0 || len(service.sessions) != 0 {
					t.Fatalf("native opens=%d close=%d commit=%d leases=%d", opener.opens, closed, committed, len(service.sessions))
				}
			})
		})
	}
}
