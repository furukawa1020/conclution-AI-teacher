package voiceflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/initiativescheduler"
)

func TestRespondentInitiativeActionIsFiniteAndFailClosed(t *testing.T) {
	tests := []struct {
		coachAction string
		want        initiativescheduler.Action
		valid       bool
	}{
		{"elicit", initiativescheduler.ActionAskOne, true},
		{"retry", initiativescheduler.ActionAskOne, true},
		{"expand", initiativescheduler.ActionAskOne, true},
		{"restate", initiativescheduler.ActionReflectGoal, true},
		{"complete", initiativescheduler.ActionAcknowledge, true},
		{"release", initiativescheduler.ActionAcknowledge, true},
		{"none", "", false},
		{"future_action", "", false},
	}
	for _, test := range tests {
		t.Run(test.coachAction, func(t *testing.T) {
			got, valid := respondentInitiativeAction(test.coachAction)
			if got != test.want || valid != test.valid {
				t.Fatalf("action=%q valid=%t", got, valid)
			}
		})
	}
}

func TestRespondentSpeculationPreparesLeaseBeforeTTS(t *testing.T) {
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{
			fakeSpeech: fakeSpeech{},
			chunks:     [][]byte{{1, 0}},
		},
	}
	decision := liveTestDecision("問いを一つだけ返します", "initiative-state")
	decision.AssistanceTarget = "respondent"
	decision.RespondentStage = "restructure"
	decision.CoachPhase = "awaiting_answer"
	decision.CoachAction = "elicit"
	agent := &speculativeTestAgent{speculativeResult: decision}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	prepareCalls := 0
	orderErr := errors.New("TTS started before the initiative lease was prepared")
	speculation := pipeline.startLiveSpeculation(
		context.Background(),
		"initiative-user",
		httpapi.VoiceTurnInput{RequestID: "initiative-prepare-order"},
		"本人の未完了な発話候補です",
		speech,
		func([]byte) error { return nil },
		func(result conversation.VoiceTurnResult) (*preparedInitiative, error) {
			prepareCalls++
			if speech.streamCalls != 0 {
				return nil, orderErr
			}
			return prepareRespondentInitiative(
				result.AssistanceTarget,
				result.CoachAction,
				result.SpokenReply,
			)
		},
	)
	if speculation == nil {
		t.Fatal("eligible speculation was not started")
	}
	outcome := <-speculation.outcome
	if outcome.synthesis != nil {
		synthesisResult := outcome.synthesis.await(context.Background())
		if synthesisResult.err != nil {
			t.Fatalf("speculative synthesis: %v", synthesisResult.err)
		}
	}
	if outcome.err != nil || outcome.initiative == nil ||
		outcome.synthesis == nil || prepareCalls != 1 || speech.streamCalls != 1 {
		t.Fatalf(
			"outcome err=%v initiative=%t synthesis=%t prepares=%d streams=%d",
			outcome.err,
			outcome.initiative != nil,
			outcome.synthesis != nil,
			prepareCalls,
			speech.streamCalls,
		)
	}
	speculation.cancel()
	if err := outcome.initiative.commit(time.Now().UnixMilli()); err == nil {
		t.Fatal("canceled speculative lease was committed")
	}
}

func TestRespondentSpeculationDoesNotStartTTSWhenPrepareFails(t *testing.T) {
	speech := &fakeLiveSpeech{
		fakeStreamingSpeech: fakeStreamingSpeech{
			fakeSpeech: fakeSpeech{},
			chunks:     [][]byte{{1, 0}},
		},
	}
	decision := liveTestDecision("この音声は生成してはいけません", "initiative-rejected")
	decision.AssistanceTarget = "respondent"
	decision.RespondentStage = "restructure"
	decision.CoachPhase = "awaiting_answer"
	decision.CoachAction = "elicit"
	agent := &speculativeTestAgent{speculativeResult: decision}
	pipeline, err := New(speech, agent)
	if err != nil {
		t.Fatal(err)
	}
	prepareErr := errors.New("initiative prepare rejected")
	speculation := pipeline.startLiveSpeculation(
		context.Background(),
		"initiative-user",
		httpapi.VoiceTurnInput{RequestID: "initiative-prepare-rejected"},
		"本人の未完了な発話候補です",
		speech,
		func([]byte) error { return nil },
		func(conversation.VoiceTurnResult) (*preparedInitiative, error) {
			return nil, prepareErr
		},
	)
	if speculation == nil {
		t.Fatal("eligible speculation was not started")
	}
	outcome := <-speculation.outcome
	if !errors.Is(outcome.err, prepareErr) || outcome.initiative != nil ||
		outcome.synthesis != nil || speech.streamCalls != 0 {
		t.Fatalf(
			"rejected outcome err=%v initiative=%t synthesis=%t streams=%d",
			outcome.err,
			outcome.initiative != nil,
			outcome.synthesis != nil,
			speech.streamCalls,
		)
	}
}
