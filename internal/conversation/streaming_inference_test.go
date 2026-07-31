package conversation

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

type scriptedInferenceStream struct {
	chunks             []string
	beforeSecond       func()
	finishReason       genai.FinishReason
	streamErrorAtIndex int
}

func (stream scriptedInferenceStream) GenerateContentStream(
	_ context.Context,
	_ string,
	_ []*genai.Content,
	_ *genai.GenerateContentConfig,
) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		for index, chunk := range stream.chunks {
			if index == 1 && stream.beforeSecond != nil {
				stream.beforeSecond()
			}
			if stream.streamErrorAtIndex == index+1 {
				yield(nil, context.Canceled)
				return
			}
			finishReason := genai.FinishReasonUnspecified
			if index == len(stream.chunks)-1 {
				finishReason = stream.finishReason
			}
			if !yield(&genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{{
					Content:      genai.NewContentFromText(chunk, genai.RoleModel),
					FinishReason: finishReason,
				}},
			}, nil) {
				return
			}
		}
	}
}

type scriptedInferenceResponseStream struct {
	responses []*genai.GenerateContentResponse
}

func (stream scriptedInferenceResponseStream) GenerateContentStream(
	_ context.Context,
	_ string,
	_ []*genai.Content,
	_ *genai.GenerateContentConfig,
) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		for _, response := range stream.responses {
			if !yield(response, nil) {
				return
			}
		}
	}
}

func TestStreamedInferencePublishesCompleteCandidateBeforePlannerTail(t *testing.T) {
	const first = `{"domain":"daily","assistance_target":"assistant",` +
		`"respondent_stage":"none","answer_attempt":"","research_action":"none",` +
		`"intervention_policy":"answer","spoken_reply":"Aです。"`
	const second = `,"intent":"question"}`
	published := false
	stream := scriptedInferenceStream{
		chunks: []string{first, second},
		beforeSecond: func() {
			if !published {
				t.Fatal("planner tail arrived before the complete candidate was published")
			}
		},
		finishReason: genai.FinishReasonStop,
	}

	raw, err := streamedInferenceText(
		context.Background(),
		stream,
		DefaultFastModel,
		nil,
		nil,
		func(candidate modelPlan) {
			published = true
			if candidate.AssistanceTarget != "assistant" ||
				candidate.Domain != "daily" ||
				candidate.RespondentStage != "none" ||
				candidate.AnswerAttempt != "" ||
				candidate.ResearchAction != "none" ||
				candidate.InterventionPolicy != "answer" ||
				candidate.SpokenReply != "Aです。" {
				t.Fatalf("unexpected early candidate: %#v", candidate)
			}
		},
	)
	if err != nil {
		t.Fatalf("streamedInferenceText: %v", err)
	}
	if !published || string(raw) != first+second {
		t.Fatalf("published=%v raw=%q", published, raw)
	}
}

func TestEarlyCandidateParserWaitsForACompleteJSONString(t *testing.T) {
	prefix := []byte(
		`{"domain":"daily","assistance_target":"assistant",` +
			`"respondent_stage":"none","answer_attempt":"","research_action":"none",` +
			`"intervention_policy":"answer","spoken_reply":"引用は \"A\"`,
	)
	if _, ready := earlyCandidateFromJSON(prefix); ready {
		t.Fatal("unterminated JSON string became publishable")
	}
	complete := append(prefix, []byte(` です。"`)...)
	candidate, ready := earlyCandidateFromJSON(complete)
	if !ready || candidate.SpokenReply != `引用は "A" です。` {
		t.Fatalf("candidate=%#v ready=%v", candidate, ready)
	}
}

func TestEarlyCandidateParserRejectsUnsafeSpeech(t *testing.T) {
	raw := []byte(
		`{"domain":"daily","assistance_target":"assistant",` +
			`"respondent_stage":"none","answer_attempt":"","research_action":"none",` +
			`"intervention_policy":"answer",` +
			`"spoken_reply":"https://example.com を開いて"}`,
	)
	if _, ready := earlyCandidateFromJSON(raw); ready {
		t.Fatal("unsafe actuator text became publishable")
	}
}

func TestEarlyCandidateParserRetainsRiskRoutingFields(t *testing.T) {
	raw := []byte(
		`{"domain":"technical","assistance_target":"assistant",` +
			`"respondent_stage":"none","answer_attempt":"","research_action":"none",` +
			`"intervention_policy":"paper_check","spoken_reply":"Aです。"}`,
	)
	candidate, ready := earlyCandidateFromJSON(raw)
	if !ready ||
		candidate.Domain != "technical" ||
		candidate.ResearchAction != "none" ||
		candidate.InterventionPolicy != "paper_check" {
		t.Fatalf("risk routing fields were not retained: candidate=%#v ready=%v", candidate, ready)
	}
}

func TestPlannerSchemaOrdersAuditableCandidateBeforePlannerTail(t *testing.T) {
	schema := modelResponseSchema(false)
	order, ok := schema["propertyOrdering"].([]string)
	if !ok || len(order) < 7 {
		t.Fatalf("propertyOrdering=%#v", schema["propertyOrdering"])
	}
	want := []string{
		"domain",
		"assistance_target",
		"respondent_stage",
		"answer_attempt",
		"research_action",
		"intervention_policy",
		"spoken_reply",
	}
	if strings.Join(order[:7], ",") != strings.Join(want, ",") {
		t.Fatalf("first properties=%v, want %v", order[:7], want)
	}
}

func TestStreamedResponseAllowsOnlyTerminalSignatureMetadata(t *testing.T) {
	response := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{
					ThoughtSignature: []byte("authenticated-provider-metadata"),
				}},
			},
			FinishReason: genai.FinishReasonStop,
		}},
	}
	chunk, err := streamedResponseChunkText(response)
	if err != nil || len(chunk) != 0 {
		t.Fatalf("terminal signature metadata: chunk=%q err=%v", chunk, err)
	}

	response.Candidates[0].FinishReason = genai.FinishReasonUnspecified
	if _, err := streamedResponseChunkText(response); err == nil {
		t.Fatal("non-terminal empty signature part was accepted")
	}

	response.Candidates[0].FinishReason = genai.FinishReasonStop
	response.Candidates[0].Content.Parts[0].ThoughtSignature = nil
	if _, err := streamedResponseChunkText(response); err == nil {
		t.Fatal("unsigned empty terminal part was accepted")
	}

	response.Candidates[0].Content.Parts = []*genai.Part{
		{ThoughtSignature: []byte("authenticated-provider-metadata")},
		{Text: `{"late":true}`},
	}
	if _, err := streamedResponseChunkText(response); err == nil {
		t.Fatal("non-final signature metadata was accepted")
	}
}

func TestResponseParsersShareTerminalMetadataAndActuatorBoundary(t *testing.T) {
	validText := `{"safe":true}`
	response := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{
					Text: validText,
				}, {
					ThoughtSignature: []byte("authenticated-provider-metadata"),
				}},
			},
			FinishReason: genai.FinishReasonStop,
		}},
	}
	output, err := responseText(response)
	if err != nil || string(output) != validText {
		t.Fatalf("unary terminal metadata: output=%q err=%v", output, err)
	}

	response.Candidates[0].Content.Parts = []*genai.Part{{
		Text: validText,
	}, {
		Thought:      true,
		FunctionCall: &genai.FunctionCall{Name: "unsafe"},
	}}
	if _, err := responseText(response); err == nil {
		t.Fatal("unary actuator-bearing thought part was accepted")
	}
	if _, err := streamedResponseChunkText(response); err == nil {
		t.Fatal("stream actuator-bearing thought part was accepted")
	}
}

func TestStreamedInferenceRequiresAValidatedStopFrame(t *testing.T) {
	textResponse := func(
		text string,
		finishReason genai.FinishReason,
	) *genai.GenerateContentResponse {
		return &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{{
				Content:      genai.NewContentFromText(text, genai.RoleModel),
				FinishReason: finishReason,
			}},
		}
	}
	signatureStop := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{
					ThoughtSignature: []byte("authenticated-provider-metadata"),
				}},
			},
			FinishReason: genai.FinishReasonStop,
		}},
	}
	const completeJSON = `{"safe":true}`

	t.Run("complete JSON without stop is rejected", func(t *testing.T) {
		stream := scriptedInferenceResponseStream{responses: []*genai.GenerateContentResponse{
			textResponse(completeJSON, genai.FinishReasonUnspecified),
		}}
		if _, err := streamedInferenceText(
			context.Background(),
			stream,
			DefaultFastModel,
			nil,
			nil,
			func(modelPlan) {},
		); err == nil || inferenceFailureStage(err) != "response_shape" {
			t.Fatalf("unterminated stream error = %v", err)
		}
	})

	t.Run("canceled stream cannot publish complete JSON", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stream := scriptedInferenceResponseStream{responses: []*genai.GenerateContentResponse{
			textResponse(completeJSON, genai.FinishReasonUnspecified),
		}}
		if _, err := streamedInferenceText(
			ctx,
			stream,
			DefaultFastModel,
			nil,
			nil,
			func(modelPlan) {},
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled stream error = %v", err)
		}
	})

	t.Run("signature stop completes JSON", func(t *testing.T) {
		stream := scriptedInferenceResponseStream{responses: []*genai.GenerateContentResponse{
			textResponse(completeJSON, genai.FinishReasonUnspecified),
			signatureStop,
			{Candidates: []*genai.Candidate{}},
		}}
		raw, err := streamedInferenceText(
			context.Background(),
			stream,
			DefaultFastModel,
			nil,
			nil,
			func(modelPlan) {},
		)
		if err != nil || string(raw) != completeJSON {
			t.Fatalf("clean stream raw=%q err=%v", raw, err)
		}
	})

	t.Run("model content after stop is rejected", func(t *testing.T) {
		stream := scriptedInferenceResponseStream{responses: []*genai.GenerateContentResponse{
			textResponse(completeJSON, genai.FinishReasonUnspecified),
			signatureStop,
			textResponse(`{"late":true}`, genai.FinishReasonUnspecified),
		}}
		if _, err := streamedInferenceText(
			context.Background(),
			stream,
			DefaultFastModel,
			nil,
			nil,
			func(modelPlan) {},
		); err == nil || inferenceFailureStage(err) != "response_shape" {
			t.Fatalf("post-stop content error = %v", err)
		}
	})
}

func TestUnaryInferenceAndCriticRequireAValidatedStop(t *testing.T) {
	response := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: genai.NewContentFromText(`{"safe":true}`, genai.RoleModel),
		}},
	}
	if err := inferenceUnaryFinishFailure(response); err == nil ||
		inferenceFailureStage(err) != "response_shape" {
		t.Fatalf("unterminated unary inference error = %v", err)
	}
	if err := criticUnaryFinishFailure(response); err == nil ||
		criticFailureStage(err) != "response_shape" {
		t.Fatalf("unterminated unary critic error = %v", err)
	}

	response.Candidates[0].FinishReason = genai.FinishReasonStop
	if err := inferenceUnaryFinishFailure(response); err != nil {
		t.Fatalf("stopped unary inference error = %v", err)
	}
	if err := criticUnaryFinishFailure(response); err != nil {
		t.Fatalf("stopped unary critic error = %v", err)
	}
}

func TestTimeoutBudgetPreservesVoiceResponseReserve(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	budget, ok := timeoutBudgetWithReserve(
		ctx,
		200*time.Millisecond,
		150*time.Millisecond,
	)
	if !ok ||
		budget <= 0 ||
		budget > 100*time.Millisecond {
		t.Fatalf("reserved budget = %s, ok=%v", budget, ok)
	}
	if _, ok := timeoutBudgetWithReserve(
		ctx,
		200*time.Millisecond,
		300*time.Millisecond,
	); ok {
		t.Fatal("inference budget consumed the response reserve")
	}
}
