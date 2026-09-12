package initiativebench

import "sort"

const ResultSchemaVersion = "kotae.mixed-initiative-benchmark-result.v1"

type Rate struct {
	Count    int  `json:"count"`
	Eligible int  `json:"eligible"`
	BPS      *int `json:"basis_points"`
}

type LatencyDistribution struct {
	Samples int  `json:"samples"`
	P50MS   *int `json:"p50_ms"`
	P95MS   *int `json:"p95_ms"`
	Maximum *int `json:"maximum_ms"`
}

type VariantSummary struct {
	Variant               Variant             `json:"variant"`
	Observations          int                 `json:"observations"`
	ScoredObservations    int                 `json:"scored_observations"`
	AmbiguousObservations int                 `json:"ambiguous_observations"`
	ExactAction           Rate                `json:"exact_action"`
	AppropriateInitiative Rate                `json:"appropriate_initiative"`
	AppropriateWait       Rate                `json:"appropriate_wait"`
	PrematureInterruption Rate                `json:"premature_interruption"`
	UserSpontaneousSpeech Rate                `json:"user_spontaneous_speech"`
	UserAuthoredAnswer    Rate                `json:"user_authored_answer"`
	GoalCompletion        Rate                `json:"goal_completion"`
	Dismissal             Rate                `json:"dismissal"`
	Correction            Rate                `json:"correction"`
	RollbackSuccess       Rate                `json:"rollback_success"`
	TimeToUsefulAction    LatencyDistribution `json:"time_to_useful_action"`
	FirstMeaningfulAudio  LatencyDistribution `json:"first_meaningful_audio"`
}

type DatasetSummary struct {
	Observations      int              `json:"observations"`
	Fixtures          int              `json:"fixtures"`
	AmbiguousFixtures int              `json:"ambiguous_fixtures"`
	MeanAgreementBPS  *int             `json:"mean_agreement_basis_points"`
	Variants          []VariantSummary `json:"variants"`
}

type Result struct {
	SchemaVersion string         `json:"schema_version"`
	SourceCommit  string         `json:"source_commit"`
	Seed          uint64         `json:"seed"`
	Generated     DatasetSummary `json:"generated_counterexamples"`
	HumanHoldout  DatasetSummary `json:"human_annotated_holdout"`
}

func Evaluate(suite Suite) (Result, error) {
	if err := suite.Validate(); err != nil {
		return Result{}, err
	}
	return Result{
		SchemaVersion: ResultSchemaVersion,
		SourceCommit:  suite.SourceCommit,
		Seed:          suite.Seed,
		Generated:     summarizeDataset(suite.Generated, false),
		HumanHoldout:  summarizeDataset(suite.HumanHoldout, true),
	}, nil
}

func summarizeDataset(observations []Observation, human bool) DatasetSummary {
	fixtureAnnotations := make(map[string]Observation)
	for _, observation := range observations {
		fixtureAnnotations[observation.FixtureID] = observation
	}
	summary := DatasetSummary{
		Observations: len(observations),
		Fixtures:     len(fixtureAnnotations),
		Variants:     make([]VariantSummary, 0, len(variants)),
	}
	if human {
		totalAgreement := 0
		for _, annotation := range fixtureAnnotations {
			totalAgreement += annotation.AgreementBPS
			if annotation.Ambiguous {
				summary.AmbiguousFixtures++
			}
		}
		mean := totalAgreement / len(fixtureAnnotations)
		summary.MeanAgreementBPS = &mean
	}
	for _, variant := range variants {
		selected := make([]Observation, 0, len(fixtureAnnotations))
		for _, observation := range observations {
			if observation.Variant == variant {
				selected = append(selected, observation)
			}
		}
		summary.Variants = append(summary.Variants, summarizeVariant(variant, selected))
	}
	return summary
}

func summarizeVariant(variant Variant, observations []Observation) VariantSummary {
	summary := VariantSummary{Variant: variant, Observations: len(observations)}
	useful := make([]int, 0, len(observations))
	audio := make([]int, 0, len(observations))
	for _, observation := range observations {
		if observation.Ambiguous {
			summary.AmbiguousObservations++
			continue
		}
		summary.ScoredObservations++
		addRate(&summary.ExactAction, observation.ActualAction == observation.ExpectedAction, true)
		initiativeExpected := isSpokenAction(observation.ExpectedAction)
		addRate(&summary.AppropriateInitiative, observation.ActualAction == observation.ExpectedAction, initiativeExpected)
		waitExpected := observation.ExpectedAction == ActionWait
		addRate(&summary.AppropriateWait, observation.ActualAction == ActionWait, waitExpected)
		addRate(&summary.PrematureInterruption, observation.ActualAction != ActionWait, waitExpected)
		addRate(&summary.UserSpontaneousSpeech, observation.UserSpokeSpontaneously, true)
		addRate(&summary.UserAuthoredAnswer, observation.UserAuthoredAnswer, true)
		addRate(&summary.GoalCompletion, observation.GoalCompleted, true)
		addRate(&summary.Dismissal, observation.Dismissed, true)
		addRate(&summary.Correction, observation.Corrected, true)
		addRate(&summary.RollbackSuccess, observation.RollbackSucceeded, observation.RollbackRequired)
		if observation.TimeToUsefulActionMS != nil {
			useful = append(useful, *observation.TimeToUsefulActionMS)
		}
		if observation.FirstMeaningfulAudioMS != nil {
			audio = append(audio, *observation.FirstMeaningfulAudioMS)
		}
	}
	finalizeRate(&summary.ExactAction)
	finalizeRate(&summary.AppropriateInitiative)
	finalizeRate(&summary.AppropriateWait)
	finalizeRate(&summary.PrematureInterruption)
	finalizeRate(&summary.UserSpontaneousSpeech)
	finalizeRate(&summary.UserAuthoredAnswer)
	finalizeRate(&summary.GoalCompletion)
	finalizeRate(&summary.Dismissal)
	finalizeRate(&summary.Correction)
	finalizeRate(&summary.RollbackSuccess)
	summary.TimeToUsefulAction = distribution(useful)
	summary.FirstMeaningfulAudio = distribution(audio)
	return summary
}

func addRate(rate *Rate, success, eligible bool) {
	if !eligible {
		return
	}
	rate.Eligible++
	if success {
		rate.Count++
	}
}

func finalizeRate(rate *Rate) {
	if rate.Eligible == 0 {
		return
	}
	value := (rate.Count*10_000 + rate.Eligible/2) / rate.Eligible
	rate.BPS = &value
}

func distribution(values []int) LatencyDistribution {
	result := LatencyDistribution{Samples: len(values)}
	if len(values) == 0 {
		return result
	}
	sort.Ints(values)
	maximum := values[len(values)-1]
	result.Maximum = &maximum
	if len(values) < 100 {
		return result
	}
	p50 := values[(len(values)*50+99)/100-1]
	p95 := values[(len(values)*95+99)/100-1]
	result.P50MS = &p50
	result.P95MS = &p95
	return result
}
