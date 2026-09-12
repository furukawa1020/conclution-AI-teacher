package initiativebench

import (
	"encoding/json"
	"strings"
	"testing"
)

func duration(value int) *int { return &value }

func fixture(id string, human bool) []Observation {
	result := make([]Observation, 0, len(variants))
	for _, variant := range variants {
		observation := Observation{
			FixtureID: id, Variant: variant, Situation: SituationThinking,
			ExpectedAction: ActionWait, ActualAction: ActionWait,
			UserSpokeSpontaneously: true, UserAuthoredAnswer: true,
			TimeToUsefulActionMS: duration(800), FirstMeaningfulAudioMS: nil,
		}
		if human {
			observation.RaterCount = 3
			observation.AgreementBPS = 10_000
		}
		result = append(result, observation)
	}
	return result
}

func validSuite() Suite {
	return Suite{
		SchemaVersion: SchemaVersion,
		SourceCommit:  strings.Repeat("a", 40),
		Seed:          20260912,
		Generated:     fixture("generated-1", false),
		HumanHoldout:  fixture("holdout-1", true),
	}
}

func TestValidateKeepsGeneratedAndHumanComparisonsSeparate(t *testing.T) {
	if err := validSuite().Validate(); err != nil {
		t.Fatalf("valid suite rejected: %v", err)
	}
	suite := validSuite()
	suite.HumanHoldout[0].RaterCount = 0
	if err := suite.Validate(); err == nil {
		t.Fatal("unannotated human holdout accepted")
	}
	suite = validSuite()
	suite.Generated[0].AgreementBPS = 10_000
	if err := suite.Validate(); err == nil {
		t.Fatal("generated fixture claimed human agreement")
	}
}

func TestValidateRequiresAllThreeVariantsPerFixture(t *testing.T) {
	suite := validSuite()
	suite.Generated = suite.Generated[:2]
	if err := suite.Validate(); err == nil {
		t.Fatal("incomplete comparison accepted")
	}
	suite = validSuite()
	suite.HumanHoldout[1].ExpectedAction = ActionAskOne
	if err := suite.Validate(); err == nil {
		t.Fatal("variant-specific expected answer accepted")
	}
}

func TestDecodeRejectsUnknownContentAndTrailingJSON(t *testing.T) {
	unknown := `{"schema_version":"kotae.mixed-initiative-benchmark.v1","source_commit":"` +
		strings.Repeat("a", 40) + `","seed":1,"generated_counterexamples":[],"human_annotated_holdout":[],"transcript":"secret"}`
	if _, err := Decode(strings.NewReader(unknown)); err == nil {
		t.Fatal("content-bearing unknown field accepted")
	}
	encoded, err := json.Marshal(validSuite())
	if err != nil {
		t.Fatalf("marshal valid suite: %v", err)
	}
	if _, err := Decode(strings.NewReader(string(encoded) + `{}`)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestValidateRejectsImpossibleMetricsAndFreeFormEnums(t *testing.T) {
	suite := validSuite()
	suite.Generated[0].ActualAction = Action("write_the_answer")
	if err := suite.Validate(); err == nil {
		t.Fatal("free-form action accepted")
	}
	suite = validSuite()
	suite.HumanHoldout[0].FirstMeaningfulAudioMS = duration(600_001)
	if err := suite.Validate(); err == nil {
		t.Fatal("unbounded timing accepted")
	}
	suite = validSuite()
	suite.HumanHoldout[0].RollbackSucceeded = true
	if err := suite.Validate(); err == nil {
		t.Fatal("rollback success without a required rollback accepted")
	}
}
