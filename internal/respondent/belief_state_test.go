package respondent

import (
	"encoding/json"
	"math"
	"testing"
)

func TestStoredVerifierProgressRoundTripVersionAndExactMass(t *testing.T) {
	original := VerifierProgressPosterior{
		TargetMissing:        0.12345,
		AvailableUncommitted: 0.23456,
		CommittedLate:        0.11111,
		CommittedFirst:       0.40001,
		VerificationUnknown:  0.13087,
	}
	stored := StoreVerifierProgress(original)
	if stored.Version != StoredVerifierProgressVersion ||
		stored.PolicyVersion != VerifierProgressPolicyVersion || !stored.Valid() {
		t.Fatalf("stored progress invalid: %#v", stored)
	}
	total := uint32(0)
	for _, mass := range stored.Mass {
		total += uint32(mass)
	}
	if total != 10_000 {
		t.Fatalf("stored mass = %d", total)
	}
	restored, ok := stored.Posterior()
	if !ok {
		t.Fatal("stored progress did not decode")
	}
	for state := LatentAnswerState(0); state < latentAnswerStateCount; state++ {
		if delta := math.Abs(
			restored.Probability(state) - original.Probability(state),
		); delta > 1/float64(StoredVerifierProgressScale) {
			t.Fatalf("state %d quantization delta = %f", state, delta)
		}
	}
}

func TestStoredVerifierProgressRejectsUnknownVersionAndWrongMass(t *testing.T) {
	valid := StoreVerifierProgress(DefaultVerifierProgressPosterior())
	for name, mutate := range map[string]func(*StoredVerifierProgress){
		"schema version": func(value *StoredVerifierProgress) { value.Version++ },
		"policy version": func(value *StoredVerifierProgress) { value.PolicyVersion++ },
		"mass":           func(value *StoredVerifierProgress) { value.Mass[0]++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if candidate.Valid() {
				t.Fatalf("invalid stored progress accepted: %#v", candidate)
			}
			if _, ok := candidate.Posterior(); ok {
				t.Fatal("invalid stored progress decoded")
			}
		})
	}
}

func TestStoredVerifierProgressLegacyShapeDecodesButFailsClosed(t *testing.T) {
	legacy := []byte(`{"version":1,"mass":[3000,2500,1000,1500,2000]}`)
	var stored StoredVerifierProgress
	if err := json.Unmarshal(legacy, &stored); err != nil {
		t.Fatalf("legacy shape no longer decodes compatibly: %v", err)
	}
	if stored.Valid() {
		t.Fatalf("legacy progress without policy version was accepted: %#v", stored)
	}
}

func TestStoreVerifierProgressRepairsNonFiniteInput(t *testing.T) {
	stored := StoreVerifierProgress(VerifierProgressPosterior{
		TargetMissing:        math.NaN(),
		AvailableUncommitted: math.Inf(1),
		CommittedLate:        -1,
	})
	if !stored.Valid() {
		t.Fatalf("repaired stored progress invalid: %#v", stored)
	}
}

func TestGuideAttemptWithVerifierProgressUsesBoundedPolicy(t *testing.T) {
	input := controllerInput(VerifierSignalMissing, VerifierSignalMissing)
	first := GuideAttemptWithVerifierProgress(OperatorPurpose, false, input)
	if first.Action != CoachActionElicit || first.Attempts != 1 ||
		first.ReasonCode != AnswerReasonTargetMissing || !first.KeepPending ||
		!first.VerifierProgressUpdated {
		t.Fatalf("first controlled attempt = %#v", first)
	}
	input.Prior = first.Posterior
	input.Attempts = AnswerAttemptOne
	second := GuideAttemptWithVerifierProgress(OperatorPurpose, false, input)
	if second.Action != CoachActionRelease || second.KeepPending ||
		second.Attempts != MaxCoachAttempts || !second.VerifierProgressUpdated {
		t.Fatalf("bounded release = %#v", second)
	}
}

func TestGuideAttemptWithVerifierProgressWaitIsSilent(t *testing.T) {
	input := controllerInput(VerifierSignalInvalid, VerifierSignalFirst)
	input.Phase = AnswerPhaseAwaitingRestatement
	input.Attempts = AnswerAttemptOne
	decision := GuideAttemptWithVerifierProgress(OperatorPurpose, false, input)
	if decision.Action != CoachActionNone || decision.SpokenReply != "" ||
		decision.Phase != CoachPhaseAwaitingRestatement || decision.Attempts != 1 ||
		!decision.KeepPending || !decision.VerifierProgressUpdated {
		t.Fatalf("WAIT became a speaking action: %#v", decision)
	}
}
