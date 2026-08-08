package respondent

import "math"

const (
	StoredVerifierProgressVersion uint8  = 1
	StoredVerifierProgressScale   uint16 = 10_000
)

// StoredVerifierProgress is the fixed-coordinate, content-free representation
// allowed in an encrypted question scope. Indices follow LatentAnswerState.
// PolicyVersion prevents posterior mass learned under a different controller
// from silently entering the current policy. It stores no question, answer,
// transcript, diagnosis, model text, or user trait.
type StoredVerifierProgress struct {
	Version       uint8     `json:"version"`
	PolicyVersion uint16    `json:"policy_version"`
	Mass          [5]uint16 `json:"mass"`
}

func (stored StoredVerifierProgress) Valid() bool {
	if stored.Version != StoredVerifierProgressVersion ||
		stored.PolicyVersion != VerifierProgressPolicyVersion {
		return false
	}
	total := uint32(0)
	for _, mass := range stored.Mass {
		total += uint32(mass)
	}
	return total == uint32(StoredVerifierProgressScale)
}

func (stored StoredVerifierProgress) Posterior() (VerifierProgressPosterior, bool) {
	if !stored.Valid() {
		return VerifierProgressPosterior{}, false
	}
	scale := float64(StoredVerifierProgressScale)
	return VerifierProgressPosterior{
		TargetMissing:        float64(stored.Mass[LatentTargetMissing]) / scale,
		AvailableUncommitted: float64(stored.Mass[LatentAvailableUncommitted]) / scale,
		CommittedLate:        float64(stored.Mass[LatentCommittedLate]) / scale,
		CommittedFirst:       float64(stored.Mass[LatentCommittedFirst]) / scale,
		VerificationUnknown:  float64(stored.Mass[LatentVerificationUnknown]) / scale,
	}, true
}

// StoreVerifierProgress uses largest-remainder quantization so the persisted
// masses sum exactly to the scale on every platform. Invalid input is repaired
// by the controller's existing normalization path before quantization.
func StoreVerifierProgress(posterior VerifierProgressPosterior) StoredVerifierProgress {
	values, _ := normalizeProbabilityValues(posteriorValues(posterior))
	type remainder struct {
		index int
		value float64
	}
	stored := StoredVerifierProgress{
		Version:       StoredVerifierProgressVersion,
		PolicyVersion: VerifierProgressPolicyVersion,
	}
	remainders := make([]remainder, 0, len(stored.Mass))
	assigned := uint16(0)
	for index, probability := range values {
		scaled := probability * float64(StoredVerifierProgressScale)
		base := uint16(math.Floor(scaled))
		stored.Mass[index] = base
		assigned += base
		remainders = append(remainders, remainder{
			index: index,
			value: scaled - float64(base),
		})
	}
	for assigned < StoredVerifierProgressScale {
		best := 0
		for index := 1; index < len(remainders); index++ {
			if remainders[index].value > remainders[best].value {
				best = index
			}
		}
		stored.Mass[remainders[best].index]++
		remainders[best].value = -1
		assigned++
	}
	return stored
}
