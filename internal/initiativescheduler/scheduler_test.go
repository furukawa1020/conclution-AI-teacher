package initiativescheduler

import "testing"

func baseline() Input {
	return Input{
		NowMS: 200, FirstAudioDeadlineMS: 1000, EstimatedDeliveryMS: 300,
		SessionCapability: [16]byte{1},
		UserGeneration:    1, GoalGeneration: 2, TurnGeneration: 3,
		Action: ActionAskOne, Floor: FloorIdle, Control: ControlNone,
		GoalActive: true, ProviderReady: true, FinalAcousticCommit: true,
		Calibrated: true, FloorResumeRiskUpperBPS: 100,
		MaximumFloorResumeRiskBPS: 500, UtilityLowerBoundBPS: 100,
	}
}

func readyLease(input Input) *Lease {
	action := input.Action
	if len(action) == 0 {
		action = ActionAskOne
	}
	return &Lease{
		SessionCapability: input.SessionCapability,
		UserGeneration:    input.UserGeneration, GoalGeneration: input.GoalGeneration,
		TurnGeneration: input.TurnGeneration, Action: action, ExpiresMS: 2000,
	}
}

func TestPrepareThenCommitRequiresRevalidatedLease(t *testing.T) {
	input := baseline()
	decision, err := Decide(input)
	if err != nil || decision.Stage != StagePrepare {
		t.Fatalf("unprepared=%#v err=%v", decision, err)
	}
	input.Prepared = readyLease(input)
	decision, err = Decide(input)
	if err != nil || decision.Stage != StageCommit || decision.DeadlineMissed {
		t.Fatalf("prepared=%#v err=%v", decision, err)
	}
}

func TestQuietWordCanPrepareButNeverCommit(t *testing.T) {
	input := baseline()
	input.Floor = FloorQuietCandidate
	if decision, _ := Decide(input); decision.Stage != StagePrepare {
		t.Fatalf("quiet preparation=%#v", decision)
	}
	input.Prepared = readyLease(input)
	if decision, _ := Decide(input); decision.Stage != StageListen {
		t.Fatalf("quiet candidate committed=%#v", decision)
	}
}

func TestSafetyVetoesAbortPreparedAudio(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Input)
	}{
		{"stop", func(in *Input) { in.Control = ControlStop }},
		{"not-now", func(in *Input) { in.Control = ControlNotNow }},
		{"correct", func(in *Input) { in.Control = ControlCorrect }},
		{"no-goal", func(in *Input) { in.GoalActive = false }},
		{"proxy-answer", func(in *Input) { in.WouldSubstituteAnswer = true }},
		{"cooldown", func(in *Input) { in.CooldownActive = true }},
		{"speaking", func(in *Input) { in.Floor = FloorSpeaking }},
		{"thinking", func(in *Input) { in.Floor = FloorThinking }},
		{"self-repair", func(in *Input) { in.Floor = FloorSelfRepair }},
		{"user-changed", func(in *Input) { in.UserGeneration++ }},
		{"session-changed", func(in *Input) { in.SessionCapability[0]++ }},
		{"goal-changed", func(in *Input) { in.GoalGeneration++ }},
		{"turn-changed", func(in *Input) { in.TurnGeneration++ }},
		{"action-changed", func(in *Input) { in.Action = ActionOfferChoice }},
		{"expired", func(in *Input) { in.NowMS = 2001 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baseline()
			input.Prepared = readyLease(input)
			test.edit(&input)
			decision, err := Decide(input)
			if err != nil || decision.Stage != StageAbort {
				t.Fatalf("unsafe prepared audio not aborted: %#v err=%v", decision, err)
			}
		})
	}
}

func TestUncalibratedOrLowUtilityNeverCommits(t *testing.T) {
	for _, edit := range []func(*Input){
		func(in *Input) { in.FinalAcousticCommit = false },
		func(in *Input) { in.Calibrated = false },
		func(in *Input) { in.FloorResumeRiskUpperBPS = 501 },
		func(in *Input) { in.UtilityLowerBoundBPS = 0 },
	} {
		input := baseline()
		input.Prepared = readyLease(input)
		edit(&input)
		decision, err := Decide(input)
		if err != nil || (decision.Stage != StageListen && decision.Stage != StageAbort) {
			t.Fatalf("insufficient evidence committed: %#v err=%v", decision, err)
		}
	}
}

func TestProviderCanBePreparedBeforeItIsReadyButNeverCommitted(t *testing.T) {
	input := baseline()
	input.ProviderReady = false
	decision, err := Decide(input)
	if err != nil || decision.Stage != StagePrepare {
		t.Fatalf("cold provider did not prepare: %#v err=%v", decision, err)
	}
	input.Prepared = readyLease(input)
	decision, err = Decide(input)
	if err != nil || decision.Stage != StageListen {
		t.Fatalf("cold provider committed: %#v err=%v", decision, err)
	}
	input.ProviderReady = true
	decision, err = Decide(input)
	if err != nil || decision.Stage != StageCommit {
		t.Fatalf("ready provider not committed: %#v err=%v", decision, err)
	}
}

func TestMissedDeadlineDoesNotBypassSafety(t *testing.T) {
	input := baseline()
	input.Prepared = readyLease(input)
	input.EstimatedDeliveryMS = 900
	decision, err := Decide(input)
	if err != nil || decision.Stage != StageCommit || !decision.DeadlineMissed {
		t.Fatalf("missed deadline not reported: %#v err=%v", decision, err)
	}
	input.Floor = FloorSpeaking
	decision, err = Decide(input)
	if err != nil || decision.Stage != StageAbort {
		t.Fatalf("deadline bypassed floor: %#v err=%v", decision, err)
	}
}

func TestInvalidFiniteStateRejected(t *testing.T) {
	input := baseline()
	input.Action = "unbounded_free_text"
	if _, err := Decide(input); err == nil {
		t.Fatal("free-text action accepted")
	}
	input = baseline()
	input.FloorResumeRiskUpperBPS = -1
	if _, err := Decide(input); err == nil {
		t.Fatal("invalid risk accepted")
	}
	input = baseline()
	input.SessionCapability = [16]byte{}
	if _, err := Decide(input); err == nil {
		t.Fatal("empty session capability accepted")
	}
}
