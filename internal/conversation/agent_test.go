package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"google.golang.org/genai"
)

type fakeGeneration struct {
	body string
	err  error
}

type generatorCall struct {
	model          string
	thinkingLevel  genai.ThinkingLevel
	responseMIME   string
	hasJSONSchema  bool
	temperatureSet bool
	pdfMIME        string
	pdfData        []byte
	prompt          string
}

type fakeGenerator struct {
	generations []fakeGeneration
	calls       []generatorCall
}

func (fake *fakeGenerator) GenerateContent(
	_ context.Context,
	model string,
	contents []*genai.Content,
	config *genai.GenerateContentConfig,
) (*genai.GenerateContentResponse, error) {
	call := generatorCall{model: model}
	if config != nil {
		call.responseMIME = config.ResponseMIMEType
		call.hasJSONSchema = config.ResponseJsonSchema != nil
		call.temperatureSet = config.Temperature != nil
		if config.ThinkingConfig != nil {
			call.thinkingLevel = config.ThinkingConfig.ThinkingLevel
		}
	}
	for _, content := range contents {
		for _, part := range content.Parts {
			if part.Text != "" {
				call.prompt += part.Text
			}
			if part.InlineData != nil {
				call.pdfMIME = part.InlineData.MIMEType
				call.pdfData = append([]byte(nil), part.InlineData.Data...)
			}
		}
	}
	fake.calls = append(fake.calls, call)
	index := len(fake.calls) - 1
	if index >= len(fake.generations) {
		return nil, errors.New("unexpected generation")
	}
	generation := fake.generations[index]
	if generation.err != nil {
		return nil, generation.err
	}
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: genai.NewContentFromText(generation.body, genai.RoleModel),
		}},
	}, nil
}

func TestAgentFastPathAndInitialState(t *testing.T) {
	plan := validModelPlan()
	plan.ThoughtStateDelta.Claims = []string{"検証可能性を優先する"}
	fake := &fakeGenerator{generations: []fakeGeneration{{body: encodePlan(t, plan)}}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), "uid-1", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "まず何から確認すればいい？",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "fast" || result.SpokenReply != plan.SpokenReply {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(fake.calls))
	}
	call := fake.calls[0]
	if call.model != DefaultFastModel ||
		call.thinkingLevel != genai.ThinkingLevelLow ||
		call.responseMIME != "application/json" ||
		!call.hasJSONSchema ||
		call.temperatureSet {
		t.Fatalf("unexpected fast generation config: %#v", call)
	}
	state, err := agent.codec.open("uid-1", result.StateToken)
	if err != nil {
		t.Fatalf("open initial state: %v", err)
	}
	if state.Turn != 1 || len(state.Graph.Claims) != 1 {
		t.Fatalf("unexpected initial state: %#v", state)
	}
}

func TestAgentPrecisionPathAndFastFallback(t *testing.T) {
	t.Run("precision succeeds", func(t *testing.T) {
		fast := validModelPlan()
		fast.Domain = "research"
		fast.SpokenReply = "予備判断です。"
		precision := fast
		precision.SpokenReply = "研究設計を見ると、対照条件の確認が先です。"
		precision.Confidence = 0.92
		fake := &fakeGenerator{generations: []fakeGeneration{
			{body: encodePlan(t, fast)},
			{body: encodePlan(t, precision)},
		}}
		agent := newTestAgent(t, fake)

		result, err := agent.Process(context.Background(), "uid-r", VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "この研究設計をどう評価する？",
		})
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "precision" || result.SpokenReply != precision.SpokenReply {
			t.Fatalf("unexpected precision result: %#v", result)
		}
		if len(fake.calls) != 2 ||
			fake.calls[1].model != DefaultPrecisionModel ||
			fake.calls[1].thinkingLevel != genai.ThinkingLevelHigh ||
			!strings.Contains(fake.calls[1].prompt, `"preliminary_plan"`) {
			t.Fatalf("unexpected precision calls: %#v", fake.calls)
		}
	})

	t.Run("preview failure keeps validated fast plan", func(t *testing.T) {
		fast := validModelPlan()
		fast.Domain = "research"
		fast.Confidence = 0.84
		fast.SpokenReply = "予備判断では、標本数の根拠がまだ弱いです。"
		fake := &fakeGenerator{generations: []fakeGeneration{
			{body: encodePlan(t, fast)},
			{err: errors.New("preview unavailable: sensitive provider detail")},
		}}
		agent := newTestAgent(t, fake)

		result, err := agent.Process(context.Background(), "uid-r", VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "論文として十分？",
		})
		if err != nil {
			t.Fatalf("Process should fall back: %v", err)
		}
		if result.Route != "fast-fallback" ||
			result.Confidence != fast.Confidence ||
			result.SpokenReply != fast.SpokenReply {
			t.Fatalf("fast uncertainty was not preserved: %#v", result)
		}
	})
}

func TestAgentAmbiguousPrecisionAsksExactlyOneQuestion(t *testing.T) {
	fast := validModelPlan()
	fast.Confidence = 0.4
	precision := validModelPlan()
	precision.Confidence = 0.7
	precision.SpokenReply = "比較したいのは費用ですか？ それとも安全性ですか？"
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, fast)},
		{body: encodePlan(t, precision)},
	}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), "uid-q", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "どっちがいいかな",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !result.NeedsClarification || result.Intervention.Act != "clarify" {
		t.Fatalf("expected clarification: %#v", result)
	}
	if countQuestions(result.SpokenReply) != 1 {
		t.Fatalf("not exactly one question: %q", result.SpokenReply)
	}
}

func TestAgentAmbientLowEVIAndSelfRepairStaySilent(t *testing.T) {
	tests := []struct {
		name  string
		plan  modelPlan
		score float64
	}{
		{
			name: "low EVI",
			plan: func() modelPlan {
				plan := validModelPlan()
				plan.Intervention = modelArbiter{
					Benefit: 0.1, InterruptionCost: 0.8, Urgency: 0,
					Confidence: 0.5, Act: "reflect",
				}
				return plan
			}(),
		},
		{
			name: "self correction grace",
			plan: func() modelPlan {
				plan := validModelPlan()
				plan.SelfCorrectionGrace = true
				plan.Intervention = modelArbiter{
					Benefit: 1, InterruptionCost: 0, Urgency: 0.5,
					Confidence: 1, Act: "reflect",
				}
				return plan
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeGenerator{generations: []fakeGeneration{{
				body: encodePlan(t, test.plan),
			}}}
			agent := newTestAgent(t, fake)
			result, err := agent.Process(context.Background(), "uid-a", VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "ええと、たぶん、いや",
				Ambient:       true,
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if result.Intervention.Act != "silent" ||
				result.SpokenReply != "" ||
				result.InterventionPolicy != "wait" {
				t.Fatalf("ambient turn interrupted: %#v", result)
			}
		})
	}
}

func TestAgentPDFIsInlineThenZeroizedAndOnlySummaryEntersState(t *testing.T) {
	utterance := "この秘密の逐語発話XYZをそのまま保存しないで"
	pdf := []byte("%PDF-1.7\nRAW-PDF-SECRET")
	plan := validModelPlan()
	plan.ConversationSummary = utterance
	plan.ThoughtStateDelta.Claims = []string{utterance}
	plan.DocumentSummary = "資料は小規模な比較実験と三つの限界を示す"
	fake := &fakeGenerator{generations: []fakeGeneration{{body: encodePlan(t, plan)}}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), "uid-p", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     utterance,
		PDF:           &InlinePDF{MIMEType: "application/pdf", Data: pdf},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if fake.calls[0].pdfMIME != "application/pdf" ||
		!bytes.Contains(fake.calls[0].pdfData, []byte("RAW-PDF-SECRET")) {
		t.Fatalf("PDF was not sent inline: %#v", fake.calls[0])
	}
	if !allZero(pdf) {
		t.Fatalf("PDF bytes were not cleared: %q", pdf)
	}

	state, err := agent.codec.open("uid-p", result.StateToken)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	if state.DocumentSummary != plan.DocumentSummary ||
		state.ConversationSummary != "" ||
		len(state.Graph.Claims) != 0 {
		t.Fatalf("unsafe state derivation: %#v", state)
	}
	plaintextView, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal test state: %v", err)
	}
	if bytes.Contains(plaintextView, []byte(utterance)) ||
		bytes.Contains(plaintextView, []byte("RAW-PDF-SECRET")) ||
		bytes.Contains(plaintextView, []byte("%PDF-")) {
		t.Fatalf("raw input entered state: %s", plaintextView)
	}
}

func TestAgentStateTokenIsBoundToUIDBeforeGeneration(t *testing.T) {
	plan := validModelPlan()
	fake := &fakeGenerator{generations: []fakeGeneration{{body: encodePlan(t, plan)}}}
	agent := newTestAgent(t, fake)
	first, err := agent.Process(context.Background(), "uid-owner", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "最初の質問",
	})
	if err != nil {
		t.Fatalf("first Process: %v", err)
	}
	_, err = agent.Process(context.Background(), "uid-other", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "次の質問",
		StateToken:    first.StateToken,
	})
	if !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("wrong UID: got %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("model called before UID binding check: %d", len(fake.calls))
	}
}

func TestAgentRejectsInvalidFastOutputAndSanitizesProviderError(t *testing.T) {
	t.Run("unknown model field", func(t *testing.T) {
		fake := &fakeGenerator{generations: []fakeGeneration{{body: `{"unexpected":true}`}}}
		agent := newTestAgent(t, fake)
		_, err := agent.Process(context.Background(), "uid", VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "質問",
		})
		if !errors.Is(err, ErrModelOutputInvalid) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("provider detail is not returned", func(t *testing.T) {
		fake := &fakeGenerator{generations: []fakeGeneration{{
			err: errors.New("provider leaked request body SECRET"),
		}}}
		agent := newTestAgent(t, fake)
		_, err := agent.Process(context.Background(), "uid", VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "質問",
		})
		if !errors.Is(err, ErrModelUnavailable) || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("unsanitized error: %v", err)
		}
	})
}

func newTestAgent(t *testing.T, generator ContentGenerator) *vertexAgent {
	t.Helper()
	created, err := NewAgent(
		generator,
		"vertexai/"+DefaultFastModel,
		"vertexai/"+DefaultPrecisionModel,
		bytes.Repeat([]byte{0x33}, 32),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	return created.(*vertexAgent)
}

func validModelPlan() modelPlan {
	return modelPlan{
		Domain:              "general",
		Intent:              "answer",
		LatentQuestion:      "次の一歩は何か",
		ArgumentStructure:   "conclusion_reason",
		InterventionPolicy:  "answer",
		SpokenReply:         "結論から言うと、小さく試して結果を確かめるのが先です。",
		Confidence:          0.9,
		ConversationSummary: "次の行動を選ぶため検証方法を整理中",
		DocumentSummary:     "",
		ThoughtStateDelta: ThoughtStateDelta{
			Claims:         []string{},
			Grounds:        []string{},
			Assumptions:    []string{},
			OpenLoops:      []string{},
			Contradictions: []string{},
			Decisions:      []string{},
		},
		SelfCorrectionGrace: false,
		Intervention: modelArbiter{
			Benefit: 0.8, InterruptionCost: 0.1, Urgency: 0.2,
			Confidence: 0.9, Act: "reflect",
		},
	}
}

func encodePlan(t *testing.T, plan modelPlan) string {
	t.Helper()
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	return string(encoded)
}

func countQuestions(value string) int {
	return strings.Count(value, "?") + strings.Count(value, "？")
}

func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}
