package voiceflow

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type trialPCMRecognizer func(context.Context, []byte) (string, float32, error)

func (f trialPCMRecognizer) TranscribePCM16(ctx context.Context, audio []byte) (string, float32, error) {
	return f(ctx, audio)
}

type trialVoiceVerifier struct {
	validate func(guesttrial.Owner, guesttrial.Snapshot, string) error
	evaluate func(context.Context, guesttrial.Owner, guesttrial.Snapshot, conversation.VoiceTurn) (guesttrial.Verdict, error)
}

func (v trialVoiceVerifier) ValidateGuestTrialState(o guesttrial.Owner, s guesttrial.Snapshot, token string) error {
	return v.validate(o, s, token)
}
func (v trialVoiceVerifier) EvaluateGuestTrial(ctx context.Context, o guesttrial.Owner, s guesttrial.Snapshot, turn conversation.VoiceTurn) (guesttrial.Verdict, error) {
	return v.evaluate(ctx, o, s, turn)
}
func trialVoiceFixture(t *testing.T) (*guesttrial.Service, guesttrial.Owner, guesttrial.Snapshot) {
	t.Helper()
	store, _ := guesttrial.NewMemoryStore(8)
	service, err := guesttrial.NewService(store, bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	owner := guesttrial.Owner{UID: "guest", AppID: "app"}
	s, err := service.Begin(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	return service, owner, s
}

func TestGuestTrialVoiceCommitsFinalPCMWithoutDialogueOrSynthesis(t *testing.T) {
	service, owner, s := trialVoiceFixture(t)
	var recognized, validated, evaluated int
	verifier := trialVoiceVerifier{
		validate: func(actual guesttrial.Owner, snapshot guesttrial.Snapshot, token string) error {
			validated++
			if actual != owner || snapshot != s || token != "bound" {
				t.Error("wrong state")
			}
			return nil
		},
		evaluate: func(_ context.Context, actual guesttrial.Owner, snapshot guesttrial.Snapshot, turn conversation.VoiceTurn) (guesttrial.Verdict, error) {
			evaluated++
			if actual != owner || snapshot != s || turn.InputOrigin != conversation.InputOriginCommittedVoice || turn.Speculative || !turn.GuestExperience || turn.Utterance != "散歩したい" || turn.StateToken != "bound" || turn.Memory != nil {
				t.Error("wrong voice provenance")
			}
			return guesttrial.VerdictAnswerFirst, nil
		},
	}
	p, err := NewGuestTrialPipeline(trialPCMRecognizer(func(_ context.Context, audio []byte) (string, float32, error) {
		recognized++
		if validated != 1 || len(audio) != 4 {
			t.Error("recognition preceded scope validation")
		}
		return "散歩したい", .95, nil
	}), verifier, service)
	if err != nil {
		t.Fatal(err)
	}
	audio := []byte{1, 2, 3, 4}
	timedCtx, timings := guesttrial.WithTimings(context.Background())
	got, err := p.ProcessPCM(timedCtx, owner, s.ID, 1, "bound", audio)
	for _, stage := range []string{"asr;dur=", "verify;dur=", "pipeline;dur="} {
		if !strings.Contains(timings.ServerTiming(), stage) {
			t.Fatal("missing timing", stage)
		}
	}
	if err != nil || got.Status != guesttrial.AnswerFirst || recognized != 1 || evaluated != 1 || !bytes.Equal(audio, make([]byte, 4)) {
		t.Fatal(got, err, audio)
	}
	if _, err := p.ProcessPCM(context.Background(), owner, s.ID, 1, "bound", []byte{1, 2}); !errors.Is(err, guesttrial.ErrConflict) {
		t.Fatal("replay", err)
	}
	if recognized != 1 {
		t.Fatal("replayed recognition")
	}
}

func TestGuestTrialVoiceFailuresLeavePendingAndNeverRetryEmptyState(t *testing.T) {
	for _, mode := range []string{"expired_state", "no_speech", "provider", "low_confidence", "blank", "cancel", "invalid_pcm"} {
		t.Run(mode, func(t *testing.T) {
			service, owner, s := trialVoiceFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			evaluated := false
			verifier := trialVoiceVerifier{
				validate: func(_ guesttrial.Owner, _ guesttrial.Snapshot, token string) error {
					if token != "bound" {
						t.Error("state erased")
					}
					if mode == "expired_state" {
						return conversation.ErrExpiredStateToken
					}
					return nil
				},
				evaluate: func(context.Context, guesttrial.Owner, guesttrial.Snapshot, conversation.VoiceTurn) (guesttrial.Verdict, error) {
					evaluated = true
					return guesttrial.VerdictAnswerFirst, nil
				},
			}
			p, _ := NewGuestTrialPipeline(trialPCMRecognizer(func(context.Context, []byte) (string, float32, error) {
				calls++
				switch mode {
				case "no_speech":
					return "", 0, speechio.ErrNoSpeech
				case "provider":
					return "", 0, errors.New("private provider error")
				case "low_confidence":
					return "散歩したい", .001, nil
				case "blank":
					return " ", .95, nil
				case "cancel":
					cancel()
				}
				return "散歩したい", .95, nil
			}), verifier, service)
			audio := []byte{1, 2}
			if mode == "invalid_pcm" {
				audio = []byte{1}
			}
			got, err := p.ProcessPCM(ctx, owner, s.ID, 1, "bound", audio)
			if err == nil || got != (guesttrial.Snapshot{}) || evaluated || !bytes.Equal(audio, make([]byte, len(audio))) {
				t.Fatal("failure published result", got, err)
			}
			if mode == "expired_state" && (calls != 0 || !errors.Is(err, conversation.ErrExpiredStateToken)) {
				t.Fatal("expired state retried", calls, err)
			}
			current, err := service.Read(context.Background(), owner, s.ID)
			if err != nil || current.Status != guesttrial.Pending || current.Generation != 1 {
				t.Fatal(current, err)
			}
		})
	}
}

func TestGuestTrialVoiceStopDuringRecognitionCannotPublishSuccess(t *testing.T) {
	service, owner, s := trialVoiceFixture(t)
	verifier := trialVoiceVerifier{validate: func(guesttrial.Owner, guesttrial.Snapshot, string) error { return nil }, evaluate: func(context.Context, guesttrial.Owner, guesttrial.Snapshot, conversation.VoiceTurn) (guesttrial.Verdict, error) {
		return guesttrial.VerdictAnswerFirst, nil
	}}
	p, _ := NewGuestTrialPipeline(trialPCMRecognizer(func(ctx context.Context, _ []byte) (string, float32, error) {
		_, err := service.Update(ctx, owner, s.ID, 1, "stop")
		return "散歩したい", .95, err
	}), verifier, service)
	got, err := p.ProcessPCM(context.Background(), owner, s.ID, 1, "bound", []byte{1, 2})
	if !errors.Is(err, guesttrial.ErrConflict) || got != (guesttrial.Snapshot{}) {
		t.Fatal(got, err)
	}
	current, err := service.Read(context.Background(), owner, s.ID)
	if err != nil || current.Status != guesttrial.Stopped {
		t.Fatal(current, err)
	}
}

func TestGuestTrialVoiceUnrecognizedIsNotACompletedNegativeAnswer(t *testing.T) {
	service, owner, s := trialVoiceFixture(t)
	verifier := trialVoiceVerifier{validate: func(guesttrial.Owner, guesttrial.Snapshot, string) error { return nil }, evaluate: func(context.Context, guesttrial.Owner, guesttrial.Snapshot, conversation.VoiceTurn) (guesttrial.Verdict, error) {
		t.Error("unexpected judgement")
		return 0, nil
	}}
	p, _ := NewGuestTrialPipeline(trialPCMRecognizer(func(context.Context, []byte) (string, float32, error) { return "", 0, speechio.ErrNoSpeech }), verifier, service)
	ctx, timings := guesttrial.WithTimings(context.Background())
	_, err := p.ProcessPCM(ctx, owner, s.ID, 1, "bound", []byte{1, 2})
	if strings.Contains(timings.ServerTiming(), "verify;") || !strings.Contains(timings.ServerTiming(), "asr;") {
		t.Fatal("unrecognized input timing is invalid", timings.ServerTiming())
	}
	if !errors.Is(err, httpapi.ErrVoiceNotRecognized) {
		t.Fatal(err)
	}
}
