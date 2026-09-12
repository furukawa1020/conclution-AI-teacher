package initiativebench

import "testing"

func benchmarkFixture(id string, human bool, actual Action, ambiguous bool, latency int) []Observation {
	items := fixture(id, human)
	for index := range items {
		items[index].Situation = SituationQuestionRequired
		items[index].ExpectedAction = ActionAskOne
		items[index].ActualAction = actual
		items[index].TimeToUsefulActionMS = duration(latency)
		items[index].FirstMeaningfulAudioMS = duration(latency)
		items[index].GoalCompleted = actual == ActionAskOne
		items[index].Ambiguous = human && ambiguous
	}
	return items
}

func TestEvaluateSeparatesVariantsAndExcludesAmbiguousHoldout(t *testing.T) {
	suite := validSuite()
	suite.Generated = nil
	suite.HumanHoldout = nil
	for index := 0; index < 100; index++ {
		id := stringID("generated", index)
		suite.Generated = append(suite.Generated, benchmarkFixture(id, false, ActionAskOne, false, index+1)...)
		holdout := benchmarkFixture(stringID("holdout", index), true, ActionAskOne, index == 99, index+1)
		suite.HumanHoldout = append(suite.HumanHoldout, holdout...)
	}
	result, err := Evaluate(suite)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	generated := result.Generated.Variants[0]
	if generated.Variant != VariantReactiveBaseline || generated.Observations != 100 ||
		generated.ExactAction.BPS == nil || *generated.ExactAction.BPS != 10_000 ||
		generated.FirstMeaningfulAudio.P50MS == nil || *generated.FirstMeaningfulAudio.P50MS != 50 ||
		generated.FirstMeaningfulAudio.P95MS == nil || *generated.FirstMeaningfulAudio.P95MS != 95 {
		t.Fatalf("unexpected generated summary: %#v", generated)
	}
	human := result.HumanHoldout.Variants[0]
	if human.Observations != 100 || human.ScoredObservations != 99 || human.AmbiguousObservations != 1 {
		t.Fatalf("ambiguous holdout was not separated: %#v", human)
	}
	if human.FirstMeaningfulAudio.P50MS != nil || human.FirstMeaningfulAudio.P95MS != nil || human.FirstMeaningfulAudio.Maximum == nil {
		t.Fatalf("percentiles below 100 samples must stay absent: %#v", human.FirstMeaningfulAudio)
	}
}

func TestEvaluateReportsWaitAndPrematureInterruptionFromSameDenominator(t *testing.T) {
	suite := validSuite()
	for index := range suite.Generated {
		if suite.Generated[index].Variant == VariantAlwaysProactive {
			suite.Generated[index].ActualAction = ActionAskOne
			suite.Generated[index].FirstMeaningfulAudioMS = duration(300)
		}
	}
	result, err := Evaluate(suite)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	always := result.Generated.Variants[1]
	if always.AppropriateWait.BPS == nil || *always.AppropriateWait.BPS != 0 ||
		always.PrematureInterruption.BPS == nil || *always.PrematureInterruption.BPS != 10_000 {
		t.Fatalf("wait metrics disagree: %#v", always)
	}
	if result.Generated.MeanAgreementBPS != nil || result.HumanHoldout.MeanAgreementBPS == nil ||
		*result.HumanHoldout.MeanAgreementBPS != 10_000 {
		t.Fatal("human agreement boundary was not retained")
	}
}

func stringID(prefix string, index int) string {
	const digits = "0123456789"
	if index < 10 {
		return prefix + "-00" + string(digits[index])
	}
	if index < 100 {
		return prefix + "-0" + string(digits[index/10]) + string(digits[index%10])
	}
	return prefix + "-100"
}
