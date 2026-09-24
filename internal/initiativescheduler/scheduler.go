// Package initiativescheduler makes the audible boundary explicit. Preparation
// is never permission to publish PCM; only a revalidated commit is.
package initiativescheduler

import "errors"

type Action string

const (
	ActionAskOne      Action = "ask_one"
	ActionReflectGoal Action = "reflect_user_goal"
	ActionOfferChoice Action = "offer_choice"
	ActionPractice    Action = "start_practice"
	// ActionAcknowledge releases the floor without asking, coaching or adding
	// answer content. It is distinct from reflection so completion receipts do
	// not get misclassified as a new intervention.
	ActionAcknowledge Action = "acknowledge_release"
)

type Floor string

const (
	FloorIdle           Floor = "idle"
	FloorSpeaking       Floor = "speaking"
	FloorThinking       Floor = "thinking"
	FloorSelfRepair     Floor = "self_repair"
	FloorQuietCandidate Floor = "quiet_candidate"
)

type Control string

const (
	ControlNone    Control = "none"
	ControlStop    Control = "stop"
	ControlNotNow  Control = "not_now"
	ControlCorrect Control = "correct"
)

type Stage string

const (
	StageListen  Stage = "listen"
	StagePrepare Stage = "prepare"
	StageCommit  Stage = "commit"
	StageAbort   Stage = "abort"
)

// Lease is content-free and scoped to one user, goal and turn generation.
// A future transport integration must keep the actual audio hidden until Commit.
type Lease struct {
	SessionCapability [16]byte
	UserGeneration    uint64
	GoalGeneration    uint64
	TurnGeneration    uint64
	Action            Action
	ExpiresMS         int64
}

type Input struct {
	NowMS                     int64
	SessionCapability         [16]byte
	FirstAudioDeadlineMS      int64
	EstimatedDeliveryMS       int64
	UserGeneration            uint64
	GoalGeneration            uint64
	TurnGeneration            uint64
	Action                    Action
	Floor                     Floor
	Control                   Control
	GoalActive                bool
	ProviderReady             bool
	WouldSubstituteAnswer     bool
	CooldownActive            bool
	FinalAcousticCommit       bool
	Calibrated                bool
	FloorResumeRiskUpperBPS   int
	MaximumFloorResumeRiskBPS int
	UtilityLowerBoundBPS      int
	Prepared                  *Lease
}

type Decision struct {
	Stage          Stage  `json:"stage"`
	Reason         string `json:"reason"`
	DeadlineMissed bool   `json:"deadline_missed"`
}

// Decide has no clock or I/O dependency, so the same trace can be replayed.
// A missed latency deadline never relaxes the audible safety boundary.
func Decide(input Input) (Decision, error) {
	if input.NowMS < 0 || input.FirstAudioDeadlineMS < 0 || input.EstimatedDeliveryMS < 0 ||
		input.SessionCapability == ([16]byte{}) ||
		input.UserGeneration == 0 || input.GoalGeneration == 0 || input.TurnGeneration == 0 ||
		!validAction(input.Action) || !validFloor(input.Floor) || !validControl(input.Control) ||
		input.FloorResumeRiskUpperBPS < 0 || input.FloorResumeRiskUpperBPS > 10_000 ||
		input.MaximumFloorResumeRiskBPS < 0 || input.MaximumFloorResumeRiskBPS > 10_000 ||
		input.UtilityLowerBoundBPS < -10_000 || input.UtilityLowerBoundBPS > 10_000 {
		return Decision{}, errors.New("initiative_scheduler_input_invalid")
	}
	if input.Prepared != nil && (input.Prepared.SessionCapability == ([16]byte{}) ||
		input.Prepared.UserGeneration == 0 || input.Prepared.GoalGeneration == 0 ||
		input.Prepared.TurnGeneration == 0 || !validAction(input.Prepared.Action) || input.Prepared.ExpiresMS < 0) {
		return Decision{}, errors.New("initiative_scheduler_lease_invalid")
	}
	if input.Control != ControlNone || !input.GoalActive ||
		input.WouldSubstituteAnswer || input.CooldownActive || !input.Calibrated ||
		input.FloorResumeRiskUpperBPS > input.MaximumFloorResumeRiskBPS ||
		input.UtilityLowerBoundBPS <= 0 {
		return inactive(input.Prepared, "hard_veto"), nil
	}
	if input.Prepared != nil && (input.Prepared.SessionCapability != input.SessionCapability ||
		input.Prepared.UserGeneration != input.UserGeneration ||
		input.Prepared.GoalGeneration != input.GoalGeneration || input.Prepared.TurnGeneration != input.TurnGeneration ||
		input.Prepared.Action != input.Action || input.Prepared.ExpiresMS < input.NowMS) {
		return Decision{Stage: StageAbort, Reason: "stale_lease"}, nil
	}
	switch input.Floor {
	case FloorSpeaking, FloorThinking, FloorSelfRepair:
		return inactive(input.Prepared, "user_floor"), nil
	case FloorQuietCandidate:
		if input.Prepared != nil {
			return Decision{Stage: StageListen, Reason: "quiet_candidate_uncommitted"}, nil
		}
		return Decision{Stage: StagePrepare, Reason: "quiet_candidate_prepare_only"}, nil
	}
	if input.Prepared == nil {
		return Decision{Stage: StagePrepare, Reason: "prepare_before_commit"}, nil
	}
	if !input.ProviderReady {
		return Decision{Stage: StageListen, Reason: "provider_not_ready"}, nil
	}
	if !input.FinalAcousticCommit || !input.Calibrated ||
		input.FloorResumeRiskUpperBPS > input.MaximumFloorResumeRiskBPS || input.UtilityLowerBoundBPS <= 0 {
		return Decision{Stage: StageListen, Reason: "commit_evidence_insufficient"}, nil
	}
	// Avoid signed overflow when reporting an unmet deadline; this does not
	// affect the decision to commit once all safety conditions hold.
	missed := input.NowMS > input.FirstAudioDeadlineMS ||
		input.EstimatedDeliveryMS > input.FirstAudioDeadlineMS-input.NowMS
	return Decision{Stage: StageCommit, Reason: "safe_commit", DeadlineMissed: missed}, nil
}

func inactive(prepared *Lease, reason string) Decision {
	if prepared != nil {
		return Decision{Stage: StageAbort, Reason: reason}
	}
	return Decision{Stage: StageListen, Reason: reason}
}

func validAction(action Action) bool {
	return action == ActionAskOne || action == ActionReflectGoal ||
		action == ActionOfferChoice || action == ActionPractice ||
		action == ActionAcknowledge
}

func validFloor(floor Floor) bool {
	return floor == FloorIdle || floor == FloorSpeaking || floor == FloorThinking ||
		floor == FloorSelfRepair || floor == FloorQuietCandidate
}

func validControl(control Control) bool {
	return control == ControlNone || control == ControlStop || control == ControlNotNow || control == ControlCorrect
}
