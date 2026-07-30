package httpapi

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestVoicePipelineFailureContainsOnlyValidatedStage(t *testing.T) {
	t.Parallel()

	for _, stage := range []VoicePipelineStage{
		VoicePipelineStageTranscribe,
		VoicePipelineStageConversation,
		VoicePipelineStageSynthesize,
	} {
		stage := stage
		t.Run(string(stage), func(t *testing.T) {
			t.Parallel()

			err := NewVoicePipelineFailure(stage)
			wrapped := fmt.Errorf("outer boundary: %w", err)
			got, classified := VoicePipelineStageOf(wrapped)
			if !classified || got != stage {
				t.Fatalf(
					"stage = %q, classified = %v; want %q",
					got,
					classified,
					stage,
				)
			}
			if err.Error() != "voice pipeline failed: "+string(stage) {
				t.Fatalf("error = %q", err)
			}
		})
	}
}

func TestVoicePipelineFailureRejectsArbitraryDiagnosticText(t *testing.T) {
	t.Parallel()

	const privateText = "provider transcript SECRET 田中"
	err := NewVoicePipelineFailure(VoicePipelineStage(privateText))
	if strings.Contains(err.Error(), privateText) {
		t.Fatalf("invalid stage leaked private text: %q", err)
	}
	if stage, classified := VoicePipelineStageOf(err); classified || stage != "" {
		t.Fatalf("invalid stage was classified: %q, %v", stage, classified)
	}
	if stage, classified := VoicePipelineStageOf(errors.New(privateText)); classified || stage != "" {
		t.Fatalf("arbitrary error was classified: %q, %v", stage, classified)
	}
}
