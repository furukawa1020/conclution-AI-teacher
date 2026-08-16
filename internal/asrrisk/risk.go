// Package asrrisk owns the content-free decision boundary between speech
// recognition and semantic inference. It must never receive audio, a
// transcript, a user identifier, or a device identifier.
package asrrisk

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"math"
)

const (
	ScoreScale                   uint32  = 1_000_000
	BootstrapConfidenceThreshold float32 = 0.65
	SchemaVersion                        = 1
	BucketProviderConfidenceV1           = "provider-confidence-v1"
)

type Decision string

const (
	Accept    Decision = "accept"
	Reobserve Decision = "reobserve"
	Reject    Decision = "reject"
)

type Reason string

const (
	ReasonWithinBoundary      Reason = "within-boundary"
	ReasonOutsideBoundary     Reason = "outside-boundary"
	ReasonCoverageUnavailable Reason = "coverage-unavailable"
	ReasonInvalidEvidence     Reason = "invalid-evidence"
	ReasonNativeCommit        Reason = "native-commit"
)

type Evidence struct {
	Confidence         float32
	ConfidenceObserved bool
	// NativeFinalCommitted is server-authored after the Native Audio final
	// caption and deterministic route gate commit. It must not be decoded from
	// an HTTP or WebSocket client field.
	NativeFinalCommitted bool
}

type Result struct {
	Decision           Decision
	Reason             Reason
	PolicyVersion      string
	CoverageCalibrated bool
}

// Artifact contains only quantized nonconformity scores and provenance. It is
// deliberately incapable of carrying recognized text or acoustic samples.
type Artifact struct {
	SchemaVersion          uint32
	PolicyVersion          string
	Bucket                 string
	AlphaPPM               uint32
	CalibrationSampleCount uint32
	ScoresPPM              []uint32
	DigestSHA256           string
}

type Controller struct {
	policyVersion string
	thresholdPPM  uint32
	calibrated    bool
}

var ErrInvalidArtifact = errors.New("asrrisk: invalid calibration artifact")

// NewBootstrapController preserves the audited 0.65 operational boundary but
// explicitly reports that it has no finite-sample coverage claim. The existing
// provider convention for omitted confidence remains operationally accepted,
// but is marked coverage-unavailable; a calibrated controller reobserves it.
func NewBootstrapController() Controller {
	return Controller{
		policyVersion: "bootstrap-operational-v1",
		thresholdPPM:  confidenceToScore(BootstrapConfidenceThreshold),
	}
}

func (controller Controller) Valid() bool {
	return controller.policyVersion != "" && controller.thresholdPPM <= ScoreScale
}

// NewCalibratedController validates a split-conformal calibration artifact and
// uses the ceiling rank ceil((n+1)*(1-alpha)). If that rank does not exist in
// the finite sample, calibration fails rather than rounding down coverage.
func NewCalibratedController(artifact Artifact) (Controller, error) {
	if artifact.SchemaVersion != SchemaVersion ||
		artifact.PolicyVersion == "" ||
		artifact.Bucket != BucketProviderConfidenceV1 ||
		artifact.AlphaPPM == 0 || artifact.AlphaPPM >= ScoreScale ||
		artifact.CalibrationSampleCount == 0 ||
		artifact.CalibrationSampleCount != uint32(len(artifact.ScoresPPM)) ||
		artifact.DigestSHA256 == "" ||
		artifact.DigestSHA256 != Digest(artifact) {
		return Controller{}, ErrInvalidArtifact
	}
	for index, score := range artifact.ScoresPPM {
		if score > ScoreScale || (index > 0 && artifact.ScoresPPM[index-1] > score) {
			return Controller{}, ErrInvalidArtifact
		}
	}
	n := uint64(artifact.CalibrationSampleCount)
	numerator := (n + 1) * uint64(ScoreScale-artifact.AlphaPPM)
	rank := (numerator + uint64(ScoreScale) - 1) / uint64(ScoreScale)
	if rank == 0 || rank > n {
		return Controller{}, ErrInvalidArtifact
	}
	return Controller{
		policyVersion: artifact.PolicyVersion,
		thresholdPPM:  artifact.ScoresPPM[rank-1],
		calibrated:    true,
	}, nil
}

func (controller Controller) Decide(evidence Evidence) Result {
	result := Result{
		PolicyVersion:      controller.policyVersion,
		CoverageCalibrated: controller.calibrated,
	}
	if evidence.NativeFinalCommitted {
		if evidence.ConfidenceObserved || evidence.Confidence != 0 {
			result.Decision = Reject
			result.Reason = ReasonInvalidEvidence
			return result
		}
		result.Decision = Accept
		result.Reason = ReasonNativeCommit
		return result
	}
	confidence := float64(evidence.Confidence)
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) ||
		confidence < 0 || confidence > 1 ||
		(!evidence.ConfidenceObserved && evidence.Confidence != 0) ||
		(evidence.ConfidenceObserved && evidence.Confidence == 0) {
		result.Decision = Reject
		result.Reason = ReasonInvalidEvidence
		return result
	}
	if !evidence.ConfidenceObserved {
		if controller.calibrated {
			result.Decision = Reobserve
		} else {
			result.Decision = Accept
		}
		result.Reason = ReasonCoverageUnavailable
		return result
	}
	if confidenceToScore(evidence.Confidence) <= controller.thresholdPPM {
		result.Decision = Accept
		result.Reason = ReasonWithinBoundary
		return result
	}
	result.Decision = Reobserve
	result.Reason = ReasonOutsideBoundary
	return result
}

// Digest returns the canonical artifact digest, excluding DigestSHA256 itself.
func Digest(artifact Artifact) string {
	digest := sha256.New()
	writeUint32(digest, artifact.SchemaVersion)
	writeString(digest, artifact.PolicyVersion)
	writeString(digest, artifact.Bucket)
	writeUint32(digest, artifact.AlphaPPM)
	writeUint32(digest, artifact.CalibrationSampleCount)
	writeUint32(digest, uint32(len(artifact.ScoresPPM)))
	for _, score := range artifact.ScoresPPM {
		writeUint32(digest, score)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func confidenceToScore(confidence float32) uint32 {
	score := math.Round((1 - float64(confidence)) * float64(ScoreScale))
	return uint32(math.Max(0, math.Min(float64(ScoreScale), score)))
}

func writeUint32(target hash.Hash, value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, _ = target.Write(encoded[:])
}

func writeString(target hash.Hash, value string) {
	writeUint32(target, uint32(len(value)))
	_, _ = target.Write([]byte(value))
}
