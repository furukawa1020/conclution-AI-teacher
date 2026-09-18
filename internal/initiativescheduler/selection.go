package initiativescheduler

import "errors"

const maximumOptions = 4

// Option is one finite, content-free action proposed by an upstream policy.
// UtilityLowerBoundBPS must already include the cost of interruption, delay,
// repeated intervention and substituting for the user's own answer.
type Option struct {
	Action                Action
	UtilityLowerBoundBPS  int
	EstimatedDeliveryMS   int64
	WouldSubstituteAnswer bool
}

// Selection has no action for Listen or Abort. Only Prepare may start resource
// work; only Commit may pass a separate transport publication boundary.
type Selection struct {
	Action   Action
	Decision Decision
}

// DecideOptions compares at most four actions with an implicit wait of zero
// utility. On-time candidates are preferred, then conservative net utility,
// then delivery time, then a fixed action name. A missed deadline is reported,
// never used to bypass the floor or acoustic commit requirements.
func DecideOptions(base Input, options []Option) (Selection, error) {
	if base.Action != "" || base.EstimatedDeliveryMS != 0 ||
		base.UtilityLowerBoundBPS != 0 || base.WouldSubstituteAnswer ||
		len(options) > maximumOptions {
		return Selection{}, errors.New("initiative_scheduler_options_invalid")
	}
	probe := base
	probe.Action = ActionAskOne
	probe.UtilityLowerBoundBPS = 1
	if _, err := Decide(probe); err != nil {
		return Selection{}, err
	}
	seen := make(map[Action]bool, len(options))
	var best Option
	found := false
	bestOnTime := false
	remaining := base.FirstAudioDeadlineMS - base.NowMS
	for _, option := range options {
		if !validAction(option.Action) || seen[option.Action] ||
			option.EstimatedDeliveryMS < 0 ||
			option.UtilityLowerBoundBPS < -10_000 || option.UtilityLowerBoundBPS > 10_000 {
			return Selection{}, errors.New("initiative_scheduler_option_invalid")
		}
		seen[option.Action] = true
		if option.WouldSubstituteAnswer || option.UtilityLowerBoundBPS <= 0 {
			continue
		}
		onTime := remaining >= 0 && option.EstimatedDeliveryMS <= remaining
		if !found || (onTime && !bestOnTime) ||
			(onTime == bestOnTime && (option.UtilityLowerBoundBPS > best.UtilityLowerBoundBPS ||
				(option.UtilityLowerBoundBPS == best.UtilityLowerBoundBPS &&
					(option.EstimatedDeliveryMS < best.EstimatedDeliveryMS ||
						(option.EstimatedDeliveryMS == best.EstimatedDeliveryMS && option.Action < best.Action))))) {
			best, found, bestOnTime = option, true, onTime
		}
	}
	if !found {
		return Selection{Decision: inactive(base.Prepared, "wait_selected")}, nil
	}
	selected := base
	selected.Action = best.Action
	selected.EstimatedDeliveryMS = best.EstimatedDeliveryMS
	selected.UtilityLowerBoundBPS = best.UtilityLowerBoundBPS
	decision, err := Decide(selected)
	if err != nil {
		return Selection{}, err
	}
	if decision.Stage != StagePrepare && decision.Stage != StageCommit {
		return Selection{Decision: decision}, nil
	}
	return Selection{Action: best.Action, Decision: decision}, nil
}
