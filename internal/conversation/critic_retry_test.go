package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/genai"
)

type transientCriticGenerator struct {
	plannerBody    string
	criticFailures int
	plannerCalls   int
	criticCalls    int
	criticModels   []string
	criticPrompts  []string
	criticThinking []genai.ThinkingLevel
}

func (generator *transientCriticGenerator) GenerateContent(
	_ context.Context,
	model string,
	contents []*genai.Content,
	config *genai.GenerateContentConfig,
) (*genai.GenerateContentResponse, error) {
	var prompt string
	for _, content := range contents {
		for _, part := range content.Parts {
			if part != nil {
				prompt += part.Text
			}
		}
	}
	if !strings.Contains(prompt, "<lac_critic_data>") {
		generator.plannerCalls++
		return generatedTextResponse(generator.plannerBody), nil
	}

	generator.criticCalls++
	generator.criticModels = append(generator.criticModels, model)
	generator.criticPrompts = append(generator.criticPrompts, prompt)
	thinking := genai.ThinkingLevelUnspecified
	if config != nil && config.ThinkingConfig != nil {
		thinking = config.ThinkingConfig.ThinkingLevel
	}
	generator.criticThinking = append(generator.criticThinking, thinking)
	if generator.criticCalls <= generator.criticFailures {
		return nil, errors.New("temporary provider transport failure")
	}
	body, err := defaultCriticBody(prompt)
	if err != nil {
		return nil, err
	}
	return generatedTextResponse(body), nil
}

func generatedTextResponse(body string) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content:      genai.NewContentFromText(body, genai.RoleModel),
			FinishReason: genai.FinishReasonStop,
		}},
	}
}

func TestTransientOrdinaryCriticFailureRetriesSameIsolatedAuditOnce(t *testing.T) {
	plan := validModelPlan()
	generator := &transientCriticGenerator{
		plannerBody:    encodePlan(t, plan),
		criticFailures: 1,
	}
	agent := newTestAgent(t, generator)

	result, err := agent.Process(
		context.Background(),
		"uid-critic-transient-recovery",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "次に何をすればいいですか？",
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if generator.plannerCalls != 1 || generator.criticCalls != 2 {
		t.Fatalf(
			"unexpected bounded calls: planner=%d critic=%d",
			generator.plannerCalls,
			generator.criticCalls,
		)
	}
	if len(generator.criticModels) != 2 ||
		generator.criticModels[0] != agent.fastModel ||
		generator.criticModels[1] != agent.fastModel ||
		generator.criticThinking[0] != genai.ThinkingLevelLow ||
		generator.criticThinking[1] != genai.ThinkingLevelLow ||
		generator.criticPrompts[0] != generator.criticPrompts[1] {
		t.Fatalf("critic retry changed its trust boundary: %#v", generator)
	}
	if result.Route != "fast" || result.SpokenReply != plan.SpokenReply {
		t.Fatalf("verified draft was not recovered: %#v", result)
	}
}

func TestTransientOrdinaryCriticFailureStopsAfterOneRetryWithoutBlamingSpeaker(
	t *testing.T,
) {
	plan := validModelPlan()
	generator := &transientCriticGenerator{
		plannerBody:    encodePlan(t, plan),
		criticFailures: 2,
	}
	agent := newTestAgent(t, generator)

	result, err := agent.Process(
		context.Background(),
		"uid-critic-transient-fallback",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "次に何をすればいいですか？",
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if generator.plannerCalls != 1 || generator.criticCalls != 2 {
		t.Fatalf(
			"critic failure was not bounded: planner=%d critic=%d",
			generator.plannerCalls,
			generator.criticCalls,
		)
	}
	if result.Route != "verification-unavailable" ||
		result.SpokenReply != verificationUnavailableSpokenReply ||
		result.SpokenReply == plan.SpokenReply ||
		result.NeedsClarification ||
		strings.Contains(result.SpokenReply, "回答の意味") ||
		strings.Contains(result.SpokenReply, "もう一度") ||
		strings.Contains(result.SpokenReply, "試して") {
		t.Fatalf("critic failure blamed the speaker or published a draft: %#v", result)
	}
}
