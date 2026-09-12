package initiativebench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	CounterexampleSchemaVersion = "kotae.mixed-initiative-counterexamples.v1"
	RequiredCounterexampleCount = 100_000
)

type FloorState string

const (
	FloorIdle           FloorState = "idle"
	FloorThinking       FloorState = "thinking"
	FloorSpeaking       FloorState = "speaking"
	FloorSelfRepair     FloorState = "self_repair"
	FloorQuietCandidate FloorState = "quiet_candidate"
)

type GoalState string

const (
	GoalNone       GoalState = "none"
	GoalActive     GoalState = "active"
	GoalCompleted  GoalState = "completed"
	GoalSuperseded GoalState = "superseded"
)

type Fault string

const (
	FaultNone            Fault = "none"
	FaultProvider429     Fault = "provider_429"
	FaultProvider500     Fault = "provider_500"
	FaultProviderTimeout Fault = "provider_timeout"
	FaultDisconnect      Fault = "disconnect"
	FaultStaleCompletion Fault = "stale_completion"
)

type ExplicitControl string

const (
	ControlNone    ExplicitControl = "none"
	ControlStop    ExplicitControl = "stop"
	ControlNotNow  ExplicitControl = "not_now"
	ControlCorrect ExplicitControl = "correct"
)

var (
	allSituations = [...]Situation{
		SituationThinking, SituationSelfRepair, SituationQuietWord, SituationLongSilence,
		SituationTopicContinuation, SituationTopicEnd, SituationExplicitRefusal,
		SituationUsefulButNotNow, SituationQuestionRequired, SituationMultiTurnContinuity,
		SituationStaleGoal, SituationProviderFault,
	}
	allFloors        = [...]FloorState{FloorIdle, FloorThinking, FloorSpeaking, FloorSelfRepair, FloorQuietCandidate}
	allGoals         = [...]GoalState{GoalNone, GoalActive, GoalCompleted, GoalSuperseded}
	allFaults        = [...]Fault{FaultNone, FaultProvider429, FaultProvider500, FaultProviderTimeout, FaultDisconnect, FaultStaleCompletion}
	allControls      = [...]ExplicitControl{ControlNone, ControlStop, ControlNotNow, ControlCorrect}
	candidateActions = [...]Action{ActionAskOne, ActionReflectUserGoal, ActionOfferChoice, ActionStartPractice}
)

type Counterexample struct {
	FixtureID             string          `json:"fixture_id"`
	Situation             Situation       `json:"situation"`
	Floor                 FloorState      `json:"floor"`
	Goal                  GoalState       `json:"goal"`
	Fault                 Fault           `json:"fault"`
	ExplicitControl       ExplicitControl `json:"explicit_control"`
	CandidateAction       Action          `json:"candidate_action"`
	PriorInterventions    uint8           `json:"prior_interventions"`
	WouldSubstituteAnswer bool            `json:"would_substitute_answer"`
	CrossUserState        bool            `json:"cross_user_state"`
	ExpectedAction        Action          `json:"expected_action"`
}

type CounterexampleCorpus struct {
	SchemaVersion string           `json:"schema_version"`
	Seed          uint64           `json:"seed"`
	Fixtures      []Counterexample `json:"fixtures"`
}

func GenerateCounterexamples(seed uint64, count int) (CounterexampleCorpus, error) {
	if seed == 0 || count < 1 || count > RequiredCounterexampleCount {
		return CounterexampleCorpus{}, errors.New("initiative counterexample generation parameters invalid")
	}
	random := splitMix64{state: seed}
	fixtures := make([]Counterexample, count)
	for index := range fixtures {
		situation := coveredChoice(index, allSituations[:], random.next())
		floor, goal, fault, control := coherentCounterexampleState(situation, &random)
		fixture := Counterexample{
			FixtureID:             fmt.Sprintf("generated-%06d", index+1),
			Situation:             situation,
			Floor:                 floor,
			Goal:                  goal,
			Fault:                 fault,
			ExplicitControl:       control,
			CandidateAction:       coveredChoice(index, candidateActions[:], random.next()),
			PriorInterventions:    uint8(random.next() % 4),
			WouldSubstituteAnswer: random.next()%11 == 0,
			CrossUserState:        random.next()%97 == 0,
		}
		fixture.ExpectedAction = expectedCounterexampleAction(fixture)
		fixtures[index] = fixture
	}
	corpus := CounterexampleCorpus{SchemaVersion: CounterexampleSchemaVersion, Seed: seed, Fixtures: fixtures}
	if err := corpus.Validate(count); err != nil {
		return CounterexampleCorpus{}, err
	}
	return corpus, nil
}

func coherentCounterexampleState(situation Situation, random *splitMix64) (FloorState, GoalState, Fault, ExplicitControl) {
	floor, goal, fault, control := FloorIdle, GoalActive, FaultNone, ControlNone
	switch situation {
	case SituationThinking:
		floor = FloorThinking
	case SituationSelfRepair:
		floor = FloorSelfRepair
	case SituationQuietWord:
		floor = FloorQuietCandidate
	case SituationTopicContinuation:
		floor = FloorSpeaking
	case SituationTopicEnd:
		goal = GoalCompleted
		if random.next()%2 == 0 {
			goal = GoalNone
		}
	case SituationExplicitRefusal:
		control = ControlStop
	case SituationUsefulButNotNow:
		control = ControlNotNow
	case SituationStaleGoal:
		goal = GoalSuperseded
	case SituationProviderFault:
		fault = allFaults[1+random.next()%uint64(len(allFaults)-1)]
	}
	// Orthogonal races remain semantically possible: a user may take the floor,
	// issue a control, or encounter a provider fault during another situation.
	if floor == FloorIdle && random.next()%13 == 0 {
		floor = allFloors[1+random.next()%uint64(len(allFloors)-1)]
	}
	if control == ControlNone && random.next()%17 == 0 {
		control = allControls[1+random.next()%uint64(len(allControls)-1)]
	}
	if fault == FaultNone && random.next()%19 == 0 {
		fault = allFaults[1+random.next()%uint64(len(allFaults)-1)]
	}
	return floor, goal, fault, control
}

func (corpus CounterexampleCorpus) Validate(requiredCount int) error {
	if corpus.SchemaVersion != CounterexampleSchemaVersion || corpus.Seed == 0 ||
		requiredCount < 1 || requiredCount > RequiredCounterexampleCount || len(corpus.Fixtures) != requiredCount {
		return errors.New("initiative counterexample corpus invalid")
	}
	for index, fixture := range corpus.Fixtures {
		if fixture.FixtureID != fmt.Sprintf("generated-%06d", index+1) ||
			!contains(allSituations[:], fixture.Situation) || !contains(allFloors[:], fixture.Floor) ||
			!contains(allGoals[:], fixture.Goal) || !contains(allFaults[:], fixture.Fault) ||
			!contains(allControls[:], fixture.ExplicitControl) ||
			!contains(candidateActions[:], fixture.CandidateAction) || fixture.PriorInterventions > 3 ||
			!coherentCounterexampleFixture(fixture) ||
			fixture.ExpectedAction != expectedCounterexampleAction(fixture) {
			return fmt.Errorf("initiative counterexample fixture %d invalid", index+1)
		}
	}
	return nil
}

func coherentCounterexampleFixture(fixture Counterexample) bool {
	switch fixture.Situation {
	case SituationThinking:
		return fixture.Floor == FloorThinking
	case SituationSelfRepair:
		return fixture.Floor == FloorSelfRepair
	case SituationQuietWord:
		return fixture.Floor == FloorQuietCandidate
	case SituationTopicContinuation:
		return fixture.Floor == FloorSpeaking
	case SituationTopicEnd:
		return fixture.Goal == GoalCompleted || fixture.Goal == GoalNone
	case SituationExplicitRefusal:
		return fixture.ExplicitControl == ControlStop
	case SituationUsefulButNotNow:
		return fixture.ExplicitControl == ControlNotNow
	case SituationStaleGoal:
		return fixture.Goal == GoalSuperseded
	case SituationProviderFault:
		return fixture.Fault != FaultNone
	default:
		return true
	}
}

func CounterexampleDigest(corpus CounterexampleCorpus) (string, error) {
	encoded, err := json.Marshal(corpus)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func expectedCounterexampleAction(fixture Counterexample) Action {
	if fixture.CrossUserState || fixture.WouldSubstituteAnswer {
		return ActionWait
	}
	switch fixture.ExplicitControl {
	case ControlStop:
		return ActionStop
	case ControlNotNow, ControlCorrect:
		return ActionWait
	}
	switch fixture.Floor {
	case FloorThinking, FloorSpeaking, FloorSelfRepair, FloorQuietCandidate:
		return ActionWait
	}
	if fixture.Fault != FaultNone {
		return ActionWait
	}
	switch fixture.Situation {
	case SituationThinking, SituationSelfRepair, SituationQuietWord, SituationTopicContinuation, SituationUsefulButNotNow:
		return ActionWait
	case SituationTopicEnd, SituationStaleGoal:
		return ActionRelease
	case SituationExplicitRefusal:
		return ActionStop
	case SituationQuestionRequired:
		return ActionAskOne
	case SituationMultiTurnContinuity:
		return ActionReflectUserGoal
	case SituationLongSilence:
		return ActionOfferChoice
	case SituationProviderFault:
		return ActionWait
	}
	if fixture.Goal != GoalActive || fixture.PriorInterventions > 0 {
		return ActionWait
	}
	return fixture.CandidateAction
}

type splitMix64 struct{ state uint64 }

func (random *splitMix64) next() uint64 {
	random.state += 0x9e3779b97f4a7c15
	value := random.state
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func coveredChoice[T comparable](index int, values []T, random uint64) T {
	if index < len(values) {
		return values[index]
	}
	return values[random%uint64(len(values))]
}

func contains[T comparable](values []T, candidate T) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
