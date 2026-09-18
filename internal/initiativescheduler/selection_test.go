package initiativescheduler

import "testing"

func selectionBaseline() Input {
	input := baseline()
	input.Action = ""
	input.EstimatedDeliveryMS = 0
	input.UtilityLowerBoundBPS = 0
	return input
}

func TestOnTimeConservativeActionWinsAndWaitIsImplicit(t *testing.T) {
	input := selectionBaseline()
	options := []Option{
		{Action: ActionPractice, UtilityLowerBoundBPS: 900, EstimatedDeliveryMS: 900},
		{Action: ActionAskOne, UtilityLowerBoundBPS: 100, EstimatedDeliveryMS: 300},
	}
	selected, err := DecideOptions(input, options)
	if err != nil || selected.Action != ActionAskOne || selected.Decision.Stage != StagePrepare {
		t.Fatalf("on-time action=%#v err=%v", selected, err)
	}
	input.Prepared = readyLease(input)
	selected, err = DecideOptions(input, options)
	if err != nil || selected.Action != ActionAskOne || selected.Decision.Stage != StageCommit {
		t.Fatalf("committed action=%#v err=%v", selected, err)
	}
	input.Prepared = nil
	selected, err = DecideOptions(input, []Option{{Action: ActionAskOne, UtilityLowerBoundBPS: 0}})
	if err != nil || selected.Action != "" || selected.Decision.Stage != StageListen {
		t.Fatalf("wait=%#v err=%v", selected, err)
	}
}

func TestSelectionIsIndependentOfCandidateOrder(t *testing.T) {
	input := selectionBaseline()
	options := []Option{
		{Action: ActionPractice, UtilityLowerBoundBPS: 100, EstimatedDeliveryMS: 300},
		{Action: ActionAskOne, UtilityLowerBoundBPS: 100, EstimatedDeliveryMS: 300},
		{Action: ActionOfferChoice, UtilityLowerBoundBPS: 900, EstimatedDeliveryMS: 900},
	}
	first, err := DecideOptions(input, options)
	if err != nil || first.Action != ActionAskOne {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	options[0], options[1] = options[1], options[0]
	second, err := DecideOptions(input, options)
	if err != nil || first != second {
		t.Fatalf("order-dependent: first=%#v second=%#v err=%v", first, second, err)
	}
}

func TestExpiredDeadlineNeverBypassesSafety(t *testing.T) {
	input := selectionBaseline()
	input.NowMS = 1_001
	input.Prepared = readyLease(input)
	options := []Option{{Action: ActionAskOne, UtilityLowerBoundBPS: 100, EstimatedDeliveryMS: 300}}
	selected, err := DecideOptions(input, options)
	if err != nil || selected.Decision.Stage != StageCommit || !selected.Decision.DeadlineMissed {
		t.Fatalf("miss not reported: %#v err=%v", selected, err)
	}
	input.Floor = FloorSpeaking
	selected, err = DecideOptions(input, options)
	if err != nil || selected.Decision.Stage != StageAbort {
		t.Fatalf("floor bypassed: %#v err=%v", selected, err)
	}
}

func TestSelectionRejectsMalformedAndProxyCandidates(t *testing.T) {
	input := selectionBaseline()
	tests := [][]Option{
		{{Action: "free_text", UtilityLowerBoundBPS: 1}},
		{{Action: ActionAskOne, EstimatedDeliveryMS: -1}},
		{{Action: ActionAskOne}, {Action: ActionAskOne}},
		{{Action: ActionAskOne, UtilityLowerBoundBPS: 10_001}},
		{{Action: ActionAskOne}, {Action: ActionReflectGoal}, {Action: ActionOfferChoice}, {Action: ActionPractice}, {Action: ActionAskOne}},
	}
	for index, options := range tests {
		if _, err := DecideOptions(input, options); err == nil {
			t.Fatalf("malformed case %d accepted", index)
		}
	}
	selected, err := DecideOptions(input, []Option{{Action: ActionAskOne, UtilityLowerBoundBPS: 900, WouldSubstituteAnswer: true}})
	if err != nil || selected.Decision.Stage != StageListen {
		t.Fatalf("proxy answer accepted: %#v err=%v", selected, err)
	}
}

func TestOneHundredThousandDeterministicSafetyCounterexamples(t *testing.T) {
	var seed uint64 = 0x9e3779b97f4a7c15
	options := []Option{{Action: ActionAskOne, UtilityLowerBoundBPS: 100, EstimatedDeliveryMS: 300}}
	for index := 0; index < 100_000; index++ {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		input := selectionBaseline()
		input.Prepared = readyLease(input)
		if seed&1 != 0 {
			input.Floor = FloorSpeaking
		}
		if seed&2 != 0 {
			input.Control = ControlStop
		}
		if seed&4 != 0 {
			input.Calibrated = false
		}
		if seed&8 != 0 {
			input.GoalGeneration++
		}
		if seed&16 != 0 {
			input.CooldownActive = true
		}
		if seed&32 != 0 {
			input.FinalAcousticCommit = false
		}
		if seed&64 != 0 {
			input.SessionCapability[0]++
		}
		selected, err := DecideOptions(input, options)
		if err != nil {
			t.Fatalf("case %d: %v", index, err)
		}
		unsafe := input.Floor != FloorIdle || input.Control != ControlNone || !input.Calibrated ||
			input.GoalGeneration != input.Prepared.GoalGeneration ||
			input.SessionCapability != input.Prepared.SessionCapability ||
			input.CooldownActive || !input.FinalAcousticCommit
		if unsafe && selected.Decision.Stage == StageCommit {
			t.Fatalf("unsafe case %d committed: %#v", index, selected)
		}
	}
}
