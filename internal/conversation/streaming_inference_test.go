package conversation

import (
	"context"
	"iter"
	"strings"
	"testing"

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

func TestStreamedInferencePublishesCompleteCandidateBeforePlannerTail(t *testing.T) {
	const first = `{"assistance_target":"assistant","respondent_stage":"none",` +
		`"answer_attempt":"","spoken_reply":"Aです。"`
	const second = `,"domain":"daily"}`
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
				candidate.RespondentStage != "none" ||
				candidate.AnswerAttempt != "" ||
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
		`{"assistance_target":"assistant","respondent_stage":"none",` +
			`"answer_attempt":"","spoken_reply":"引用は \"A\"`,
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
		`{"assistance_target":"assistant","respondent_stage":"none",` +
			`"answer_attempt":"","spoken_reply":"https://example.com を開いて"}`,
	)
	if _, ready := earlyCandidateFromJSON(raw); ready {
		t.Fatal("unsafe actuator text became publishable")
	}
}

func TestPlannerSchemaOrdersAuditableCandidateBeforePlannerTail(t *testing.T) {
	schema := modelResponseSchema(false)
	order, ok := schema["propertyOrdering"].([]string)
	if !ok || len(order) < 4 {
		t.Fatalf("propertyOrdering=%#v", schema["propertyOrdering"])
	}
	want := []string{
		"assistance_target",
		"respondent_stage",
		"answer_attempt",
		"spoken_reply",
	}
	if strings.Join(order[:4], ",") != strings.Join(want, ",") {
		t.Fatalf("first properties=%v, want %v", order[:4], want)
	}
}
