package initiativebench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const RunSchemaVersion = "kotae.mixed-initiative-run.v1"

type RunObservation struct {
	FixtureID              string `json:"fixture_id"`
	ActualAction           Action `json:"actual_action"`
	UserSpokeSpontaneously bool   `json:"user_spoke_spontaneously"`
	UserAuthoredAnswer     bool   `json:"user_authored_answer"`
	GoalCompleted          bool   `json:"goal_completed"`
	Dismissed              bool   `json:"dismissed"`
	Corrected              bool   `json:"corrected"`
	RollbackRequired       bool   `json:"rollback_required"`
	RollbackSucceeded      bool   `json:"rollback_succeeded"`
	TimeToUsefulActionMS   *int   `json:"time_to_useful_action_ms"`
	FirstMeaningfulAudioMS *int   `json:"first_meaningful_audio_ms"`
}

type SystemRun struct {
	SchemaVersion string           `json:"schema_version"`
	CorpusSHA256  string           `json:"corpus_sha256"`
	SourceCommit  string           `json:"source_commit"`
	Variant       Variant          `json:"variant"`
	Observations  []RunObservation `json:"observations"`
}

func DecodeSystemRun(reader io.Reader) (SystemRun, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 128<<20))
	decoder.DisallowUnknownFields()
	var run SystemRun
	if err := decoder.Decode(&run); err != nil {
		return SystemRun{}, fmt.Errorf("initiative benchmark run decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return SystemRun{}, errors.New("initiative benchmark run trailing data")
	}
	return run, nil
}

func BuildGeneratedObservations(corpus CounterexampleCorpus, runs []SystemRun) ([]Observation, error) {
	if err := corpus.Validate(len(corpus.Fixtures)); err != nil {
		return nil, err
	}
	digest, err := CounterexampleDigest(corpus)
	if err != nil {
		return nil, err
	}
	if len(runs) != len(variants) {
		return nil, errors.New("initiative benchmark requires exactly three system runs")
	}
	var sourceCommit string
	combined := make([]Observation, 0, len(corpus.Fixtures)*len(variants))
	for index, variant := range variants {
		run := runs[index]
		if run.SchemaVersion != RunSchemaVersion || run.CorpusSHA256 != digest ||
			run.Variant != variant || !commitPattern.MatchString(run.SourceCommit) ||
			len(run.Observations) != len(corpus.Fixtures) {
			return nil, fmt.Errorf("initiative benchmark %s run invalid", variant)
		}
		if sourceCommit == "" {
			sourceCommit = run.SourceCommit
		} else if run.SourceCommit != sourceCommit {
			return nil, errors.New("initiative benchmark run commit mismatch")
		}
		for position, outcome := range run.Observations {
			fixture := corpus.Fixtures[position]
			if outcome.FixtureID != fixture.FixtureID || !validRunObservation(outcome) {
				return nil, fmt.Errorf("initiative benchmark %s outcome %d invalid", variant, position+1)
			}
			combined = append(combined, Observation{
				FixtureID: fixture.FixtureID, Variant: variant, Situation: fixture.Situation,
				ExpectedAction: fixture.ExpectedAction, ActualAction: outcome.ActualAction,
				UserSpokeSpontaneously: outcome.UserSpokeSpontaneously,
				UserAuthoredAnswer:     outcome.UserAuthoredAnswer, GoalCompleted: outcome.GoalCompleted,
				Dismissed: outcome.Dismissed, Corrected: outcome.Corrected,
				RollbackRequired: outcome.RollbackRequired, RollbackSucceeded: outcome.RollbackSucceeded,
				TimeToUsefulActionMS:   outcome.TimeToUsefulActionMS,
				FirstMeaningfulAudioMS: outcome.FirstMeaningfulAudioMS,
			})
		}
	}
	return combined, nil
}

func validRunObservation(observation RunObservation) bool {
	return validID(observation.FixtureID) && validAction(observation.ActualAction) &&
		validDuration(observation.TimeToUsefulActionMS) && validDuration(observation.FirstMeaningfulAudioMS) &&
		!(!observation.RollbackRequired && observation.RollbackSucceeded) &&
		(isSpokenAction(observation.ActualAction) == (observation.FirstMeaningfulAudioMS != nil))
}
