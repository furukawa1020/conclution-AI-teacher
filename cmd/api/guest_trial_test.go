package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
)

type trialRuntimeSpeech struct{ calls int }

func (s *trialRuntimeSpeech) TranscribePCM16(context.Context, []byte) (string, float32, error) {
	s.calls++
	return "散歩したいです。", 0.95, nil
}

type trialRuntimeAgent struct{}

func (trialRuntimeAgent) IssueGuestTrialState(guesttrial.Owner, guesttrial.Snapshot) (string, error) {
	return "sealed", nil
}
func (trialRuntimeAgent) ValidateGuestTrialState(_ guesttrial.Owner, _ guesttrial.Snapshot, token string) error {
	if token != "sealed" {
		return guesttrial.ErrInvalid
	}
	return nil
}
func (trialRuntimeAgent) EvaluateGuestTrial(_ context.Context, _ guesttrial.Owner, _ guesttrial.Snapshot, turn conversation.VoiceTurn) (guesttrial.Verdict, error) {
	if !turn.GuestExperience || turn.InputOrigin != conversation.InputOriginCommittedVoice || turn.Speculative {
		return 0, guesttrial.ErrInvalid
	}
	return guesttrial.VerdictAnswerFirst, nil
}

func TestGuestTrialRuntimeDisabledHasNoCapabilities(t *testing.T) {
	r, err := connectGuestTrial(false, nil, nil, nil, nil)
	if err != nil || r.control != nil || r.voice != nil || r.state != nil {
		t.Fatal("disabled runtime exposed routes")
	}
}

func TestGuestTrialRuntimeRequiresCompleteCapabilities(t *testing.T) {
	store, _ := guesttrial.NewMemoryStore(4)
	for _, tc := range []struct {
		key    []byte
		store  guesttrial.Store
		speech *trialRuntimeSpeech
		agent  any
	}{
		{nil, store, &trialRuntimeSpeech{}, trialRuntimeAgent{}},
		{bytes.Repeat([]byte{1}, 32), nil, &trialRuntimeSpeech{}, trialRuntimeAgent{}},
		{bytes.Repeat([]byte{1}, 32), store, &trialRuntimeSpeech{}, struct{}{}},
	} {
		r, err := connectGuestTrial(true, tc.key, tc.store, tc.speech, tc.agent)
		if err == nil || r.control != nil || r.voice != nil || r.state != nil {
			t.Fatal("partial runtime exposed")
		}
	}
	if _, err := connectGuestTrial(true, bytes.Repeat([]byte{1}, 32), store, nil, trialRuntimeAgent{}); err == nil {
		t.Fatal("missing ASR accepted")
	}
}

func TestGuestTrialRuntimeSharesLedgerAcrossInstancesAndSeparatesKeys(t *testing.T) {
	ctx := context.Background()
	store, _ := guesttrial.NewMemoryStore(8)
	key := bytes.Repeat([]byte{3}, 32)
	speech := &trialRuntimeSpeech{}
	first, err := connectGuestTrial(true, key, store, speech, trialRuntimeAgent{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := connectGuestTrial(true, key, store, speech, trialRuntimeAgent{})
	if err != nil {
		t.Fatal(err)
	}
	owner := guesttrial.Owner{UID: "guest", AppID: "app"}
	snapshot, err := first.control.Begin(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.control.Read(ctx, owner, snapshot.ID); err != nil {
		t.Fatal("instance-local ledger", err)
	}
	// The root key is not the ledger key. Other consumers cannot reuse it as
	// the document-key derivation input without this purpose separation.
	raw, _ := guesttrial.NewService(store, key)
	if _, err := raw.Read(ctx, owner, snapshot.ID); err == nil {
		t.Fatal("root key reused directly")
	}
	other, _ := connectGuestTrial(true, bytes.Repeat([]byte{4}, 32), store, speech, trialRuntimeAgent{})
	if _, err := other.control.Read(ctx, owner, snapshot.ID); err == nil {
		t.Fatal("different key opened ledger")
	}
	token, err := second.state.IssueGuestTrialState(owner, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	audio := []byte{1, 2}
	result, err := second.voice.ProcessPCM(ctx, owner, snapshot.ID, snapshot.Generation, token, audio)
	if err != nil || result.Status != guesttrial.AnswerFirst || speech.calls != 1 || !bytes.Equal(audio, []byte{0, 0}) {
		t.Fatal("dedicated pipeline not connected", result, err)
	}
	read, err := first.control.Read(ctx, owner, snapshot.ID)
	if err != nil || read.Status != guesttrial.AnswerFirst {
		t.Fatal("completion not shared", err)
	}
	if !bytes.Equal(key, bytes.Repeat([]byte{3}, 32)) {
		t.Fatal("root key modified")
	}
}
