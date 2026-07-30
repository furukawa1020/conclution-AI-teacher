package httpapi

import "errors"

// VoicePipelineStage is the only diagnostic detail that may cross the
// voiceflow-to-HTTP trust boundary after an unexpected processing failure.
type VoicePipelineStage string

const (
	VoicePipelineStageTranscribe   VoicePipelineStage = "transcribe"
	VoicePipelineStageConversation VoicePipelineStage = "conversation"
	VoicePipelineStageSynthesize   VoicePipelineStage = "synthesize"
)

type voicePipelineFailure struct {
	stage VoicePipelineStage
}

func (failure voicePipelineFailure) Error() string {
	return "voice pipeline failed: " + string(failure.stage)
}

func (failure voicePipelineFailure) voicePipelineStage() VoicePipelineStage {
	return failure.stage
}

// NewVoicePipelineFailure deliberately does not accept or retain the provider
// error. Provider messages can contain transcripts, model output, document
// text, identifiers, or other PII and must not reach logs or callers.
func NewVoicePipelineFailure(stage VoicePipelineStage) error {
	if !stage.valid() {
		return errors.New("voice pipeline failed")
	}
	return voicePipelineFailure{stage: stage}
}

// VoicePipelineStageOf extracts only one of the finite, privacy-reviewed stage
// values. Arbitrary errors and invalid stage strings remain unclassified.
func VoicePipelineStageOf(err error) (VoicePipelineStage, bool) {
	var staged interface {
		voicePipelineStage() VoicePipelineStage
	}
	if !errors.As(err, &staged) {
		return "", false
	}
	stage := staged.voicePipelineStage()
	return stage, stage.valid()
}

func (stage VoicePipelineStage) valid() bool {
	switch stage {
	case VoicePipelineStageTranscribe,
		VoicePipelineStageConversation,
		VoicePipelineStageSynthesize:
		return true
	default:
		return false
	}
}
