package respondent

import (
	"math"
	"reflect"
	"testing"
)

func TestAnswerControllerInputHasNoContentChannelOrCallerEvidence(t *testing.T) {
	typeOf := reflect.TypeOf(AnswerControllerInput{})
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		if field.Name == "Evidence" {
			t.Fatal("caller can supply derived evidence")
		}
		assertFinitePolicyType(t, field.Type, field.Name)
	}
}

func assertFinitePolicyType(t *testing.T, value reflect.Type, path string) {
	t.Helper()
	switch value.Kind() {
	case reflect.String, reflect.Slice, reflect.Array, reflect.Map,
		reflect.Interface, reflect.Pointer, reflect.Func, reflect.Chan:
		t.Fatalf("policy input content channel %s has kind %s", path, value.Kind())
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			field := value.Field(index)
			assertFinitePolicyType(t, field.Type, path+"."+field.Name)
		}
	}
}

func TestPlanAnswerSupportCompletesOnlyJointVerifiedFirst(t *testing.T) {
	decision := PlanAnswerSupport(controllerInput(
		VerifierSignalFirst,
		VerifierSignalFirst,
	))
	if decision.Action != AnswerSupportComplete || !decision.VerifiedFirst ||
		decision.ReasonCode != AnswerReasonVerifiedFirst {
		t.Fatalf("joint first decision = %#v", decision)
	}
	if decision.Posterior.CommittedFirst <= 0.90 {
		t.Fatalf("first evidence did not dominate posterior: %#v", decision.Posterior)
	}
}

func TestPlanAnswerSupportConflictingVerifiersNeverComplete(t *testing.T) {
	conflicts := [][2]VerifierSignal{
		{VerifierSignalFirst, VerifierSignalLater},
		{VerifierSignalLater, VerifierSignalFirst},
		{VerifierSignalFirst, VerifierSignalAvailable},
		{VerifierSignalAvailable, VerifierSignalLater},
	}
	for _, conflict := range conflicts {
		for _, phase := range []AnswerPhase{
			AnswerPhaseAwaitingAnswer,
			AnswerPhaseComplete,
		} {
			input := controllerInput(conflict[0], conflict[1])
			input.Phase = phase
			decision := PlanAnswerSupport(input)
			if decision.Action == AnswerSupportComplete || decision.VerifiedFirst {
				t.Fatalf("conflict %v in phase %d completed: %#v", conflict, phase, decision)
			}
			if decision.Action != AnswerSupportWait || decision.NextAttempts != 0 {
				t.Fatalf("conflict %v in phase %d did not wait: %#v", conflict, phase, decision)
			}
		}
	}
}

func TestPlanAnswerSupportJointLateReasksOnceWithoutMintingFirst(t *testing.T) {
	input := controllerInput(
		VerifierSignalLater,
		VerifierSignalLater,
	)
	first := PlanAnswerSupport(input)
	if first.Action != AnswerSupportRestate || first.VerifiedFirst ||
		first.ReasonCode != AnswerReasonLateRestatement ||
		first.NextAttempts != 1 || !first.KeepPending {
		t.Fatalf("first joint late decision = %#v", first)
	}

	input.Prior = first.Posterior
	input.Phase = AnswerPhaseAwaitingRestatement
	input.Attempts = AnswerAttemptOne
	second := PlanAnswerSupport(input)
	if second.Action != AnswerSupportRelease || second.VerifiedFirst ||
		second.NextAttempts != MaxCoachAttempts || second.KeepPending {
		t.Fatalf("second joint late decision = %#v", second)
	}
}

func TestPlanAnswerSupportRejectedScopePrecedesCompletePhase(t *testing.T) {
	input := controllerInput(VerifierSignalRejected, VerifierSignalFirst)
	input.Phase = AnswerPhaseComplete
	decision := PlanAnswerSupport(input)
	if decision.Action != AnswerSupportRelease || decision.KeepPending ||
		decision.VerifiedFirst || decision.ReasonCode != AnswerReasonScopeRejected {
		t.Fatalf("rejected complete scope decision = %#v", decision)
	}
}

func TestPlanAnswerSupportHesitationWaitsWithoutAttempt(t *testing.T) {
	input := controllerInput(VerifierSignalMissing, VerifierSignalMissing)
	input.Hesitation = true
	decision := PlanAnswerSupport(input)
	if decision.Action != AnswerSupportWait || decision.NextAttempts != 0 ||
		decision.ReasonCode != AnswerReasonHesitation || !decision.KeepPending {
		t.Fatalf("hesitation decision = %#v", decision)
	}
}

func TestPlanAnswerSupportMalformedFiniteValuesFailClosed(t *testing.T) {
	input := controllerInput(VerifierSignal(255), VerifierSignalFirst)
	input.Phase = AnswerPhase(255)
	decision := PlanAnswerSupport(input)
	if decision.Action != AnswerSupportWait || decision.VerifiedFirst ||
		decision.NextAttempts != 0 {
		t.Fatalf("malformed finite input = %#v", decision)
	}
}

func TestPlanAnswerSupportMissingIsBounded(t *testing.T) {
	input := controllerInput(VerifierSignalMissing, VerifierSignalMissing)
	first := PlanAnswerSupport(input)
	if first.Action != AnswerSupportElicit || first.NextAttempts != 1 {
		t.Fatalf("first missing decision = %#v", first)
	}
	input.Prior = first.Posterior
	input.Attempts = AnswerAttemptOne
	second := PlanAnswerSupport(input)
	if second.Action != AnswerSupportRelease || second.NextAttempts != MaxCoachAttempts {
		t.Fatalf("second missing decision = %#v", second)
	}
}

func TestPlanAnswerSupportRepairsNonFinitePrior(t *testing.T) {
	input := controllerInput(VerifierSignalMissing, VerifierSignalMissing)
	input.Prior = VerifierProgressPosterior{
		TargetMissing:        math.NaN(),
		AvailableUncommitted: math.Inf(1),
		CommittedLate:        -1,
	}
	decision := PlanAnswerSupport(input)
	if !decision.PriorRepaired {
		t.Fatal("non-finite prior was not marked repaired")
	}
	assertNormalizedPosterior(t, decision.Posterior)
}

func controllerInput(gate, critic VerifierSignal) AnswerControllerInput {
	return AnswerControllerInput{
		Prior:                 DefaultVerifierProgressPosterior(),
		GateSignal:            gate,
		CriticSignal:          critic,
		Phase:                 AnswerPhaseAwaitingAnswer,
		Attempts:              AnswerAttemptNone,
		VerificationAvailable: true,
	}
}

func assertNormalizedPosterior(t *testing.T, posterior VerifierProgressPosterior) {
	t.Helper()
	sum := 0.0
	for state := LatentAnswerState(0); state < latentAnswerStateCount; state++ {
		probability := posterior.Probability(state)
		if math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 {
			t.Fatalf("invalid posterior probability %d = %f", state, probability)
		}
		sum += probability
	}
	if math.Abs(sum-1) > 1e-12 {
		t.Fatalf("posterior sum = %.17f", sum)
	}
}
