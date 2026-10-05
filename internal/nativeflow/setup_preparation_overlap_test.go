package nativeflow

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
	"github.com/furukawa1020/conclution-ai-teacher/internal/voiceflow"
)

type setupOverlapOpener struct {
	session nativevoice.Session
	gate    <-chan struct{}
	err     error
	opens   atomic.Int64
}

func (opener *setupOverlapOpener) Open(ctx context.Context) (nativevoice.Session, error) {
	opener.opens.Add(1)
	if opener.gate != nil {
		select {
		case <-opener.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if opener.err != nil {
		return nil, opener.err
	}
	return opener.session, nil
}

type setupOverlapSession struct {
	*scriptedSession
	gate <-chan struct{}
}

func (session *setupOverlapSession) StartActivity(ctx context.Context) error {
	err := session.scriptedSession.StartActivity(ctx)
	if session.gate != nil {
		select {
		case <-session.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

type setupOverlapSpeech struct {
	earlyPreparedSpeech
	gate     <-chan struct{}
	started  atomic.Int64
	canceled atomic.Int64
}

func (speech *setupOverlapSpeech) PrepareStreamingSynthesis(ctx context.Context) (speechio.PreparedStreamingSynthesis, error) {
	speech.started.Add(1)
	select {
	case <-speech.gate:
	case <-ctx.Done():
		speech.canceled.Add(1)
		return nil, ctx.Err()
	}
	return speech.earlyPreparedSpeech.PrepareStreamingSynthesis(ctx)
}

// Both waits are real boundaries in the normal turn: a cold setup and an
// already-prepared connection's StartActivity write. Neither may serialize
// content-free TTS configuration once the same UID has acquired its slot.
func TestKnownStagedPreparationOverlapsNativeSetupAndStart(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		name := "cold setup"
		if prepared {
			name = "prepared start"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				nativeGate, preparationGate := make(chan struct{}), make(chan struct{})
				session := &setupOverlapSession{scriptedSession: newScriptedSession(nativeCaptionEvent("こんにちは"))}
				opener := &setupOverlapOpener{session: session}
				speech := &setupOverlapSpeech{gate: preparationGate}
				agent := &earlyStagedAgent{}
				pipeline, _ := voiceflow.New(speech, agent)
				service, _ := NewWithCaptionHandoff(opener, fakePreparer{token: "prepared-state", requiresStaged: true}, pipeline)
				defer service.Close()
				if prepared {
					if err := service.PrepareLive(ctx, "overlap", maxPreparedSessionTTL); err != nil {
						t.Fatal(err)
					}
					session.gate = nativeGate
				} else {
					opener.gate = nativeGate
				}
				var ready, pcm, checkpoint atomic.Int64
				input := nativeInput()
				input.OnInputReady = func() { ready.Add(1) }
				done := make(chan error, 1)
				go func() {
					_, err := service.ProcessLiveWithControl(ctx, "overlap", input, oneFrame(),
						func([]byte) error {
							if checkpoint.Load() != 1 {
								return errors.New("PCM preceded checkpoint")
							}
							pcm.Add(1)
							return nil
						}, nil, func(transition httpapi.VoiceRespondentCheckpointTransition) error {
							if transition.PreviousSessionState != "prepared-state" {
								return errors.New("stale state token")
							}
							checkpoint.Add(1)
							return nil
						})
					done <- err
				}()
				synctest.Wait()
				if speech.started.Load() != 1 || opener.opens.Load() != 1 {
					t.Fatalf("native wait serialized preparation: preparation=%d opens=%d", speech.started.Load(), opener.opens.Load())
				}
				if ready.Load() != 0 || agent.calls.Load() != 0 || pcm.Load() != 0 || checkpoint.Load() != 0 || speech.uses.Load() != 0 {
					t.Fatal("preparation published readiness, semantic work, or PCM before Native setup/start")
				}
				close(preparationGate)
				synctest.Wait()
				close(nativeGate)
				synctest.Wait()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if speech.prepares.Load() != 1 || speech.uses.Load() != 1 || speech.closes.Load() != 1 || speech.cold.Load() != 0 || ready.Load() != 1 || pcm.Load() != 1 || checkpoint.Load() != 1 || agent.calls.Load() != 1 {
					t.Fatalf("prepare/use/close/cold=%d/%d/%d/%d ready/PCM/checkpoint/model=%d/%d/%d/%d", speech.prepares.Load(), speech.uses.Load(), speech.closes.Load(), speech.cold.Load(), ready.Load(), pcm.Load(), checkpoint.Load(), agent.calls.Load())
				}
				if session.closes != 1 || session.commits != 0 || len(service.sessions) != 0 {
					t.Fatalf("native closes/commits/leases=%d/%d/%d", session.closes, session.commits, len(service.sessions))
				}
			})
		})
	}
}

func TestKnownStagedSetupPreparationReclaimedOnFailureAndCancellation(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		for _, readyPreparation := range []bool{false, true} {
			for _, action := range []string{"provider failure", "request cancel", "service close"} {
				t.Run(fmt.Sprintf("prepared=%t/ready=%t/%s", prepared, readyPreparation, action), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						nativeGate, preparationGate := make(chan struct{}), make(chan struct{})
						session := &setupOverlapSession{scriptedSession: newScriptedSession()}
						opener := &setupOverlapOpener{session: session}
						speech := &setupOverlapSpeech{gate: preparationGate}
						agent := &earlyStagedAgent{}
						pipeline, _ := voiceflow.New(speech, agent)
						service, _ := NewWithCaptionHandoff(opener, fakePreparer{requiresStaged: true}, pipeline)
						defer service.Close()
						if prepared {
							if err := service.PrepareLive(ctx, "failure", maxPreparedSessionTTL); err != nil {
								t.Fatal(err)
							}
							session.gate = nativeGate
							if action == "provider failure" {
								session.startErr = errors.New("start failed")
							}
						} else {
							opener.gate = nativeGate
							if action == "provider failure" {
								opener.err = errors.New("setup failed")
							}
						}
						var published atomic.Int64
						input := nativeInput()
						input.OnInputReady = func() { published.Add(1) }
						done := make(chan error, 1)
						go func() {
							_, err := service.ProcessLiveWithControl(ctx, "failure", input, oneFrame(),
								func([]byte) error { published.Add(1); return nil }, nil,
								func(httpapi.VoiceRespondentCheckpointTransition) error { published.Add(1); return nil })
							done <- err
						}()
						synctest.Wait()
						if speech.started.Load() != 1 {
							t.Fatal("preparation did not overlap blocked setup/start")
						}
						if readyPreparation {
							close(preparationGate)
							synctest.Wait()
						}
						switch action {
						case "provider failure":
							close(nativeGate)
						case "request cancel":
							cancel()
						case "service close":
							_ = service.Close()
						}
						synctest.Wait()
						select {
						case err := <-done:
							if !errors.Is(err, errNativeFlowUnavailable) || errors.Is(err, httpapi.ErrVoiceNativeFallback) {
								t.Fatalf("premature fallback or success: %v", err)
							}
						default:
							t.Fatal("failure retained native opening or prepared handoff")
						}
						if readyPreparation && speech.closes.Load() != 1 || !readyPreparation && speech.canceled.Load() != 1 {
							t.Fatalf("preparation close/cancel=%d/%d", speech.closes.Load(), speech.canceled.Load())
						}
						if published.Load() != 0 || agent.calls.Load() != 0 || speech.uses.Load() != 0 || speech.cold.Load() != 0 || len(service.sessions) != 0 {
							t.Fatal("failed setup/start retained a lease or crossed a publication boundary")
						}
						wantCloses := 0
						if prepared {
							wantCloses = 1
						}
						if session.closes != wantCloses || action == "service close" && ctx.Err() != nil {
							t.Fatalf("session closes=%d parent cancellation=%v", session.closes, ctx.Err())
						}
					})
				})
			}
		}
	}
}

func TestKnownStagedAdmissionDenialDoesNotStartPreparation(t *testing.T) {
	for _, denial := range []string{"same UID", "capacity", "closed service", "invalid state"} {
		t.Run(denial, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				speech := &setupOverlapSpeech{gate: make(chan struct{})}
				pipeline, _ := voiceflow.New(speech, &earlyStagedAgent{})
				opener := &setupOverlapOpener{}
				preparer := fakePreparer{requiresStaged: true}
				wantErr := errNativeFlowUnavailable
				if denial == "invalid state" {
					preparer.err = conversation.ErrInvalidStateToken
					wantErr = httpapi.ErrVoiceStateInvalid
				}
				service, _ := NewWithCaptionHandoff(opener, preparer, pipeline)
				defer service.Close()
				switch denial {
				case "same UID":
					service.sessions["denied"] = &pooledSession{}
				case "capacity":
					for index := range maxProviderSessions {
						service.sessions[fmt.Sprint(index)] = &pooledSession{}
					}
				case "closed service":
					_ = service.Close()
				}
				leases := len(service.sessions)
				_, err := service.ProcessLiveWithControl(context.Background(), "denied", nativeInput(), oneFrame(),
					func([]byte) error { t.Error("denied PCM"); return nil }, nil,
					func(httpapi.VoiceRespondentCheckpointTransition) error { t.Error("denied checkpoint"); return nil })
				synctest.Wait()
				if !errors.Is(err, wantErr) || speech.started.Load() != 0 || opener.opens.Load() != 0 || len(service.sessions) != leases {
					t.Fatalf("denial err=%v preparations=%d opens=%d leases=%d/%d", err, speech.started.Load(), opener.opens.Load(), len(service.sessions), leases)
				}
			})
		})
	}
}

func TestKnownStagedPreparedClaimHandoffFailureReleasesWithoutReady(t *testing.T) {
	session := newScriptedSession()
	opener := &fakeOpener{session: session}
	handoff := &recordingCaptionHandoffService{openErr: httpapi.ErrVoiceNativeFallback}
	service, _ := NewWithCaptionHandoff(opener, fakePreparer{requiresStaged: true}, handoff)
	defer service.Close()
	if err := service.PrepareLive(context.Background(), "claimed", maxPreparedSessionTTL); err != nil {
		t.Fatal(err)
	}
	input := nativeInput()
	input.OnInputReady = func() { t.Error("failed handoff published readiness") }
	_, err := service.ProcessLiveWithControl(context.Background(), "claimed", input, oneFrame(),
		func([]byte) error { t.Error("failed handoff published PCM"); return nil }, nil,
		func(httpapi.VoiceRespondentCheckpointTransition) error {
			t.Error("failed handoff published checkpoint")
			return nil
		})
	if !errors.Is(err, errNativeFlowUnavailable) || handoff.openCount() != 1 || opener.opens != 1 || session.startCalls != 0 || session.closes != 1 || session.discards != 1 || len(service.sessions) != 0 {
		t.Fatalf("err=%v handoff=%d opens/starts/closes/discards/leases=%d/%d/%d/%d/%d", err, handoff.openCount(), opener.opens, session.startCalls, session.closes, session.discards, len(service.sessions))
	}
}

func TestNativePreparationHookDoesNotLockOrLeakOnPanic(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprint(prepared), func(t *testing.T) {
			session := newScriptedSession()
			opener := &fakeOpener{session: session}
			service, _ := New(opener, fakePreparer{})
			defer service.Close()
			if prepared {
				if err := service.PrepareLive(context.Background(), "hook", maxPreparedSessionTTL); err != nil {
					t.Fatal(err)
				}
			}
			func() {
				defer func() {
					if recover() != "hook panic" {
						t.Error("hook did not execute")
					}
				}()
				_, _ = service.acquire(context.Background(), "hook", "", func() error {
					if !service.mu.TryLock() {
						t.Fatal("external preparation ran under the pool lock")
					}
					owned := service.sessions["hook"]
					service.mu.Unlock()
					if owned == nil || owned.prepared {
						t.Fatal("preparation ran before UID admission")
					}
					panic("hook panic")
				})
			}()
			wantCloses := 0
			if prepared {
				wantCloses = 1
			}
			if len(service.sessions) != 0 || opener.opens != wantCloses || session.closes != wantCloses {
				t.Fatalf("panic retained lease/provider: leases=%d opens=%d closes=%d", len(service.sessions), opener.opens, session.closes)
			}
		})
	}
}

func TestKnownStagedPendingPreparationDoesNotDelayNativeReady(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		speech := &setupOverlapSpeech{gate: make(chan struct{})}
		agent := &earlyStagedAgent{}
		pipeline, _ := voiceflow.New(speech, agent)
		session := &earlyCaptionSession{scriptedSession: newScriptedSession(), captionReady: make(chan struct{})}
		service, _ := NewWithCaptionHandoff(&fakeOpener{session: session}, fakePreparer{requiresStaged: true}, pipeline)
		defer service.Close()
		var ready, published atomic.Int64
		input := nativeInput()
		input.OnInputReady = func() { ready.Add(1) }
		done := make(chan error, 1)
		go func() {
			_, err := service.ProcessLiveWithControl(ctx, "pending-configuration", input, oneFrame(),
				func([]byte) error { published.Add(1); return nil }, nil,
				func(httpapi.VoiceRespondentCheckpointTransition) error { published.Add(1); return nil })
			done <- err
		}()
		synctest.Wait()
		if ready.Load() != 1 || speech.started.Load() != 1 || speech.prepares.Load() != 0 || agent.calls.Load() != 0 || published.Load() != 0 {
			t.Fatal("pending content-free configuration delayed ready or advanced semantic output")
		}
		cancel()
		synctest.Wait()
		if err := <-done; !errors.Is(err, errNativeFlowUnavailable) || speech.canceled.Load() != 1 {
			t.Fatalf("pending configuration survived request: err=%v canceled=%d", err, speech.canceled.Load())
		}
	})
}
