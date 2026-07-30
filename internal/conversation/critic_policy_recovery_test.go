package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/genai"
)

type promptBlockedCriticGenerator struct {
	planner     *fakeGenerator
	criticCalls int
}

func (generator *promptBlockedCriticGenerator) GenerateContent(
	ctx context.Context,
	model string,
	contents []*genai.Content,
	config *genai.GenerateContentConfig,
) (*genai.GenerateContentResponse, error) {
	for _, content := range contents {
		for _, part := range content.Parts {
			if part != nil && strings.Contains(part.Text, "<lac_critic_data>") {
				generator.criticCalls++
				return &genai.GenerateContentResponse{
					PromptFeedback: &genai.GenerateContentResponsePromptFeedback{
						BlockReason: genai.BlockedReasonSafety,
						BlockReasonMessage: "provider detail SECRET must not " +
							"cross the trust boundary",
					},
				}, nil
			}
		}
	}
	return generator.planner.GenerateContent(ctx, model, contents, config)
}

func TestCriticPromptBlockFailsClosedWithoutRetryOrModelHop(t *testing.T) {
	plan := validModelPlan()
	generator := &promptBlockedCriticGenerator{
		planner: &fakeGenerator{generations: []fakeGeneration{{
			body: encodePlan(t, plan),
		}}},
	}
	agent := newTestAgent(t, generator)

	result, err := agent.Process(
		context.Background(),
		"uid-critic-prompt-block",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "この質問に答えてください",
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "verification-unavailable" ||
		result.SpokenReply == plan.SpokenReply ||
		strings.Contains(result.SpokenReply, "SECRET") ||
		len(generator.planner.calls) != 1 ||
		generator.criticCalls != 1 {
		t.Fatalf(
			"critic policy block retried, model-hopped, or published draft: "+
				"result=%#v planner_calls=%d critic_calls=%d",
			result,
			len(generator.planner.calls),
			generator.criticCalls,
		)
	}
}

func TestCriticPolicyFailuresAreFiniteAndNeverRecoverable(t *testing.T) {
	blocked := criticFinishFailure(&genai.GenerateContentResponse{
		PromptFeedback: &genai.GenerateContentResponsePromptFeedback{
			BlockReason:        genai.BlockedReasonModelArmor,
			BlockReasonMessage: "provider detail SECRET",
		},
	})
	if !errors.Is(blocked, errCriticPromptBlocked) ||
		retryableCriticFailure(blocked) ||
		recoverableCriticFailure(blocked) ||
		criticFailureClass(blocked) != "prompt_blocked" ||
		criticFailureStage(blocked) != "prompt_blocked" ||
		strings.Contains(blocked.Error(), "SECRET") {
		t.Fatalf("critic prompt block escaped finite boundary: %v", blocked)
	}

	tests := []struct {
		name       string
		reason     genai.FinishReason
		wantMarker error
		wantClass  string
	}{
		{
			name:       "safety",
			reason:     genai.FinishReasonSafety,
			wantMarker: errCriticFinishSafety,
			wantClass:  "safety",
		},
		{
			name:       "blocklist",
			reason:     genai.FinishReasonBlocklist,
			wantMarker: errCriticFinishSafety,
			wantClass:  "safety",
		},
		{
			name:       "prohibited content",
			reason:     genai.FinishReasonProhibitedContent,
			wantMarker: errCriticFinishSafety,
			wantClass:  "safety",
		},
		{
			name:       "sensitive PII",
			reason:     genai.FinishReasonSPII,
			wantMarker: errCriticFinishSafety,
			wantClass:  "safety",
		},
		{
			name:       "output limit",
			reason:     genai.FinishReasonMaxTokens,
			wantMarker: errCriticFinishLimit,
			wantClass:  "output_limit",
		},
	}
	for _, reason := range []genai.FinishReason{
		genai.FinishReasonRecitation,
		genai.FinishReasonLanguage,
		genai.FinishReasonOther,
		genai.FinishReasonMalformedFunctionCall,
		genai.FinishReasonUnexpectedToolCall,
		genai.FinishReasonImageSafety,
		genai.FinishReasonImageProhibitedContent,
		genai.FinishReasonNoImage,
		genai.FinishReasonImageRecitation,
		genai.FinishReasonImageOther,
		genai.FinishReason("FUTURE_UNKNOWN_REASON"),
	} {
		tests = append(tests, struct {
			name       string
			reason     genai.FinishReason
			wantMarker error
			wantClass  string
		}{
			name:       string(reason),
			reason:     reason,
			wantMarker: errCriticFinishPolicy,
			wantClass:  "finish_policy",
		})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := criticFinishFailure(&genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{{FinishReason: test.reason}},
			})
			if !errors.Is(failure, test.wantMarker) ||
				retryableCriticFailure(failure) ||
				recoverableCriticFailure(failure) ||
				criticFailureClass(failure) != test.wantClass ||
				criticFailureStage(failure) != "finish" {
				t.Fatalf(
					"critic finish %q crossed policy boundary: %v",
					test.reason,
					failure,
				)
			}
		})
	}

	for _, failure := range []error{
		errors.Join(ErrModelUnavailable, errCriticDeadline),
		errors.Join(ErrModelUnavailable, errCriticCanceled),
	} {
		if retryableCriticFailure(failure) ||
			recoverableCriticFailure(failure) {
			t.Fatalf("critic context failure became recoverable: %v", failure)
		}
	}
}
