// Package initiativebench defines the content-free, reproducible input
// contract for the mixed-initiative benchmark in Issue #223.
package initiativebench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

const SchemaVersion = "kotae.mixed-initiative-benchmark.v1"

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Variant string

const (
	VariantReactiveBaseline   Variant = "reactive_baseline"
	VariantAlwaysProactive    Variant = "always_proactive"
	VariantProposalControlled Variant = "proposal_controlled"
)

var variants = [...]Variant{
	VariantReactiveBaseline,
	VariantAlwaysProactive,
	VariantProposalControlled,
}

type Situation string

const (
	SituationThinking            Situation = "thinking"
	SituationSelfRepair          Situation = "self_repair"
	SituationQuietWord           Situation = "quiet_word"
	SituationLongSilence         Situation = "long_silence"
	SituationTopicContinuation   Situation = "topic_continuation"
	SituationTopicEnd            Situation = "topic_end"
	SituationExplicitRefusal     Situation = "explicit_refusal"
	SituationUsefulButNotNow     Situation = "useful_but_not_now"
	SituationQuestionRequired    Situation = "question_required"
	SituationMultiTurnContinuity Situation = "multi_turn_continuity"
	SituationStaleGoal           Situation = "stale_goal"
	SituationProviderFault       Situation = "provider_fault"
)

type Action string

const (
	ActionWait            Action = "wait"
	ActionAskOne          Action = "ask_one"
	ActionReflectUserGoal Action = "reflect_user_goal"
	ActionOfferChoice     Action = "offer_choice"
	ActionStartPractice   Action = "start_practice"
	ActionRelease         Action = "release"
	ActionStop            Action = "stop"
)

type Observation struct {
	FixtureID              string    `json:"fixture_id"`
	Variant                Variant   `json:"variant"`
	Situation              Situation `json:"situation"`
	ExpectedAction         Action    `json:"expected_action"`
	ActualAction           Action    `json:"actual_action"`
	UserSpokeSpontaneously bool      `json:"user_spoke_spontaneously"`
	UserAuthoredAnswer     bool      `json:"user_authored_answer"`
	GoalCompleted          bool      `json:"goal_completed"`
	Dismissed              bool      `json:"dismissed"`
	Corrected              bool      `json:"corrected"`
	RollbackRequired       bool      `json:"rollback_required"`
	RollbackSucceeded      bool      `json:"rollback_succeeded"`
	TimeToUsefulActionMS   *int      `json:"time_to_useful_action_ms"`
	FirstMeaningfulAudioMS *int      `json:"first_meaningful_audio_ms"`
	RaterCount             int       `json:"rater_count"`
	AgreementBPS           int       `json:"agreement_bps"`
	Ambiguous              bool      `json:"ambiguous"`
}

type Suite struct {
	SchemaVersion string        `json:"schema_version"`
	SourceCommit  string        `json:"source_commit"`
	Seed          uint64        `json:"seed"`
	Generated     []Observation `json:"generated_counterexamples"`
	HumanHoldout  []Observation `json:"human_annotated_holdout"`
}

func Decode(reader io.Reader) (Suite, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 64<<20))
	decoder.DisallowUnknownFields()
	var suite Suite
	if err := decoder.Decode(&suite); err != nil {
		return Suite{}, fmt.Errorf("initiative benchmark decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Suite{}, errors.New("initiative benchmark trailing data")
	}
	if err := suite.Validate(); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

func (suite Suite) Validate() error {
	if suite.SchemaVersion != SchemaVersion || !commitPattern.MatchString(suite.SourceCommit) || suite.Seed == 0 {
		return errors.New("initiative benchmark provenance invalid")
	}
	if len(suite.Generated) == 0 || len(suite.HumanHoldout) == 0 {
		return errors.New("initiative benchmark datasets must stay separate and non-empty")
	}
	seen := make(map[string]struct{}, len(suite.Generated)+len(suite.HumanHoldout))
	if err := validateDataset("generated", suite.Generated, false, seen); err != nil {
		return err
	}
	if err := validateDataset("holdout", suite.HumanHoldout, true, seen); err != nil {
		return err
	}
	return nil
}

func validateDataset(name string, observations []Observation, human bool, seen map[string]struct{}) error {
	fixtureVariants := make(map[string]map[Variant]struct{})
	type fixtureContract struct {
		situation    Situation
		expected     Action
		raterCount   int
		agreementBPS int
		ambiguous    bool
	}
	fixtureContracts := make(map[string]fixtureContract)
	for _, observation := range observations {
		if !validID(observation.FixtureID) || !validVariant(observation.Variant) ||
			!validSituation(observation.Situation) || !validAction(observation.ExpectedAction) ||
			!validAction(observation.ActualAction) || !validDuration(observation.TimeToUsefulActionMS) ||
			!validDuration(observation.FirstMeaningfulAudioMS) ||
			(!observation.RollbackRequired && observation.RollbackSucceeded) {
			return fmt.Errorf("initiative benchmark %s observation invalid", name)
		}
		if human {
			if observation.RaterCount < 2 || observation.RaterCount > 20 ||
				observation.AgreementBPS < 0 || observation.AgreementBPS > 10_000 {
				return errors.New("initiative benchmark holdout annotation invalid")
			}
		} else if observation.RaterCount != 0 || observation.AgreementBPS != 0 || observation.Ambiguous {
			return errors.New("initiative benchmark generated data cannot claim human annotation")
		}
		key := name + "\x00" + observation.FixtureID + "\x00" + string(observation.Variant)
		if _, exists := seen[key]; exists {
			return errors.New("initiative benchmark duplicate observation")
		}
		seen[key] = struct{}{}
		if fixtureVariants[observation.FixtureID] == nil {
			fixtureVariants[observation.FixtureID] = make(map[Variant]struct{}, len(variants))
			fixtureContracts[observation.FixtureID] = fixtureContract{
				situation: observation.Situation, expected: observation.ExpectedAction,
				raterCount: observation.RaterCount, agreementBPS: observation.AgreementBPS,
				ambiguous: observation.Ambiguous,
			}
		} else if contract := fixtureContracts[observation.FixtureID]; contract.situation != observation.Situation || contract.expected != observation.ExpectedAction ||
			contract.raterCount != observation.RaterCount || contract.agreementBPS != observation.AgreementBPS ||
			contract.ambiguous != observation.Ambiguous {
			return errors.New("initiative benchmark fixture contract changed between variants")
		}
		fixtureVariants[observation.FixtureID][observation.Variant] = struct{}{}
	}
	for _, present := range fixtureVariants {
		if len(present) != len(variants) {
			return errors.New("initiative benchmark fixture comparison incomplete")
		}
		for _, variant := range variants {
			if _, ok := present[variant]; !ok {
				return errors.New("initiative benchmark fixture comparison incomplete")
			}
		}
	}
	return nil
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validVariant(value Variant) bool {
	for _, variant := range variants {
		if value == variant {
			return true
		}
	}
	return false
}

func validSituation(value Situation) bool {
	switch value {
	case SituationThinking, SituationSelfRepair, SituationQuietWord, SituationLongSilence,
		SituationTopicContinuation, SituationTopicEnd, SituationExplicitRefusal, SituationUsefulButNotNow,
		SituationQuestionRequired, SituationMultiTurnContinuity, SituationStaleGoal, SituationProviderFault:
		return true
	default:
		return false
	}
}

func validAction(value Action) bool {
	switch value {
	case ActionWait, ActionAskOne, ActionReflectUserGoal, ActionOfferChoice, ActionStartPractice, ActionRelease, ActionStop:
		return true
	default:
		return false
	}
}

func validDuration(value *int) bool {
	return value == nil || (*value >= 0 && *value <= 600_000)
}
