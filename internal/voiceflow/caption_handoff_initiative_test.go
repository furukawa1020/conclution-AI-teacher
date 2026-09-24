package voiceflow

import (
	"testing"

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
