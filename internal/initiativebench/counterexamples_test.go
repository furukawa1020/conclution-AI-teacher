package initiativebench

import "testing"

func TestCounterexampleGeneratorIsDeterministicAndCoversEveryFiniteState(t *testing.T) {
	first, err := GenerateCounterexamples(20260912, 100)
	if err != nil {
		t.Fatalf("generate first: %v", err)
	}
	second, err := GenerateCounterexamples(20260912, 100)
	if err != nil {
		t.Fatalf("generate second: %v", err)
	}
	firstDigest, err := CounterexampleDigest(first)
	if err != nil {
		t.Fatalf("digest first: %v", err)
	}
	secondDigest, err := CounterexampleDigest(second)
	if err != nil {
		t.Fatalf("digest second: %v", err)
	}
	if firstDigest != secondDigest {
		t.Fatal("same seed produced different fixtures")
	}
	seenSituations := make(map[Situation]bool)
	seenFloors := make(map[FloorState]bool)
	seenGoals := make(map[GoalState]bool)
	seenFaults := make(map[Fault]bool)
	for _, fixture := range first.Fixtures {
		seenSituations[fixture.Situation] = true
		seenFloors[fixture.Floor] = true
		seenGoals[fixture.Goal] = true
		seenFaults[fixture.Fault] = true
	}
	if len(seenSituations) != len(allSituations) || len(seenFloors) != len(allFloors) ||
		len(seenGoals) != len(allGoals) || len(seenFaults) != len(allFaults) {
		t.Fatal("finite state coverage is incomplete")
	}
}

func TestCounterexampleOracleProtectsFloorOwnershipAndUserAuthorship(t *testing.T) {
	base := Counterexample{
		FixtureID: "generated-000001", Situation: SituationQuestionRequired,
		Floor: FloorIdle, Goal: GoalActive, Fault: FaultNone, ExplicitControl: ControlNone,
		CandidateAction: ActionAskOne,
	}
	if action := expectedCounterexampleAction(base); action != ActionAskOne {
		t.Fatalf("safe question was not admitted: %s", action)
	}
	base.Floor = FloorSpeaking
	if action := expectedCounterexampleAction(base); action != ActionWait {
		t.Fatalf("speaking floor was interrupted: %s", action)
	}
	base.Floor = FloorIdle
	base.WouldSubstituteAnswer = true
	if action := expectedCounterexampleAction(base); action != ActionWait {
		t.Fatalf("answer substitution was admitted: %s", action)
	}
	base.WouldSubstituteAnswer = false
	base.CrossUserState = true
	if action := expectedCounterexampleAction(base); action != ActionWait {
		t.Fatalf("cross-user state was admitted: %s", action)
	}
}

func TestCounterexampleValidationRejectsContradictoryScenarioState(t *testing.T) {
	corpus, err := GenerateCounterexamples(20260912, 20)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	corpus.Fixtures[0].Floor = FloorIdle
	corpus.Fixtures[0].ExpectedAction = expectedCounterexampleAction(corpus.Fixtures[0])
	if err := corpus.Validate(20); err == nil {
		t.Fatal("thinking situation without thinking floor accepted")
	}
}

func TestCounterexampleCorpusRejectsTamperingAndUnboundedCounts(t *testing.T) {
	corpus, err := GenerateCounterexamples(1, 12)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	corpus.Fixtures[0].ExpectedAction = ActionStartPractice
	if err := corpus.Validate(12); err == nil {
		t.Fatal("tampered oracle output accepted")
	}
	if _, err := GenerateCounterexamples(0, 12); err == nil {
		t.Fatal("zero seed accepted")
	}
	if _, err := GenerateCounterexamples(1, RequiredCounterexampleCount+1); err == nil {
		t.Fatal("unbounded corpus accepted")
	}
}

func TestRequiredOneHundredThousandCounterexamplesHaveStableDigest(t *testing.T) {
	corpus, err := GenerateCounterexamples(20260912, RequiredCounterexampleCount)
	if err != nil {
		t.Fatalf("generate required corpus: %v", err)
	}
	digest, err := CounterexampleDigest(corpus)
	if err != nil {
		t.Fatalf("digest required corpus: %v", err)
	}
	const expected = "0d619f11dfa83ea584065a458ce0c421a6000884231d0bfb877f19b63037c0c2"
	if digest != expected {
		t.Fatalf("counterexample digest changed: got %s", digest)
	}
}
