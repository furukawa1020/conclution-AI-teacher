package asrrisk

import (
	"errors"
	"math"
	"testing"
)

func calibratedArtifact(scores []uint32, alphaPPM uint32) Artifact {
	artifact := Artifact{
		SchemaVersion:          SchemaVersion,
		PolicyVersion:          "fixture-calibration-v1",
		Bucket:                 BucketProviderConfidenceV1,
		AlphaPPM:               alphaPPM,
		CalibrationSampleCount: uint32(len(scores)),
		ScoresPPM:              scores,
	}
	artifact.DigestSHA256 = Digest(artifact)
	return artifact
}

func TestSplitConformalUsesFiniteSampleCeilingRank(t *testing.T) {
	scores := make([]uint32, 19)
	for index := range scores {
		scores[index] = uint32((index + 1) * 10_000)
	}
	controller, err := NewCalibratedController(calibratedArtifact(scores, 100_000))
	if err != nil {
		t.Fatal(err)
	}
	// ceil((19+1)*0.9)=18, hence threshold score 0.18.
	accepted := controller.Decide(Evidence{Confidence: 0.82, ConfidenceObserved: true})
	reobserved := controller.Decide(Evidence{Confidence: 0.819, ConfidenceObserved: true})
	if accepted.Decision != Accept || !accepted.CoverageCalibrated ||
		reobserved.Decision != Reobserve {
		t.Fatalf("boundary decisions: accepted=%+v reobserved=%+v", accepted, reobserved)
	}
}

func TestFiniteSampleTooSmallDoesNotRoundCoverageDown(t *testing.T) {
	artifact := calibratedArtifact(make([]uint32, 18), 50_000)
	if _, err := NewCalibratedController(artifact); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("error = %v; want invalid artifact", err)
	}
}

func TestArtifactIntegrityAndOrderingFailClosed(t *testing.T) {
	valid := calibratedArtifact([]uint32{100_000, 200_000, 300_000}, 500_000)
	for name, mutate := range map[string]func(*Artifact){
		"digest": func(value *Artifact) { value.DigestSHA256 = "00" },
		"bucket": func(value *Artifact) { value.Bucket = "unknown" },
		"count":  func(value *Artifact) { value.CalibrationSampleCount++ },
		"order": func(value *Artifact) {
			value.ScoresPPM = []uint32{200_000, 100_000, 300_000}
			value.DigestSHA256 = Digest(*value)
		},
		"range": func(value *Artifact) { value.ScoresPPM[2] = ScoreScale + 1; value.DigestSHA256 = Digest(*value) },
	} {
		t.Run(name, func(t *testing.T) {
			artifact := valid
			artifact.ScoresPPM = append([]uint32(nil), valid.ScoresPPM...)
			mutate(&artifact)
			if _, err := NewCalibratedController(artifact); !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("error = %v; want invalid artifact", err)
			}
		})
	}
}

func TestBootstrapDistinguishesMissingInvalidAndNativeEvidence(t *testing.T) {
	controller := NewBootstrapController()
	for _, test := range []struct {
		name     string
		evidence Evidence
		decision Decision
		reason   Reason
	}{
		{"measured high", Evidence{Confidence: .65, ConfidenceObserved: true}, Accept, ReasonWithinBoundary},
		{"measured low", Evidence{Confidence: .649, ConfidenceObserved: true}, Reobserve, ReasonOutsideBoundary},
		{"missing bootstrap", Evidence{}, Accept, ReasonCoverageUnavailable},
		{"invalid nan", Evidence{Confidence: float32(math.NaN()), ConfidenceObserved: true}, Reject, ReasonInvalidEvidence},
		{"invalid infinity", Evidence{Confidence: float32(math.Inf(1)), ConfidenceObserved: true}, Reject, ReasonInvalidEvidence},
		{"inconsistent missing", Evidence{Confidence: .8}, Reject, ReasonInvalidEvidence},
		{"native committed", Evidence{NativeFinalCommitted: true}, Accept, ReasonNativeCommit},
		{"native mixed authority", Evidence{Confidence: .9, ConfidenceObserved: true, NativeFinalCommitted: true}, Reject, ReasonInvalidEvidence},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := controller.Decide(test.evidence)
			if result.Decision != test.decision || result.Reason != test.reason || result.CoverageCalibrated {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestCalibratedControllerReobservesMissingCoverage(t *testing.T) {
	scores := make([]uint32, 19)
	controller, err := NewCalibratedController(calibratedArtifact(scores, 100_000))
	if err != nil {
		t.Fatal(err)
	}
	result := controller.Decide(Evidence{})
	if result.Decision != Reobserve || result.Reason != ReasonCoverageUnavailable ||
		!result.CoverageCalibrated {
		t.Fatalf("result = %+v", result)
	}
}

func TestOneHundredThousandCounterexamplesNeverCommit(t *testing.T) {
	controller := NewBootstrapController()
	for index := 0; index < 100_000; index++ {
		confidence := float32(index%650_000) / float32(ScoreScale)
		result := controller.Decide(Evidence{Confidence: confidence, ConfidenceObserved: true})
		if result.Decision == Accept {
			t.Fatalf("counterexample %d committed at confidence %f", index, confidence)
		}
	}
}
