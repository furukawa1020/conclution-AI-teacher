package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
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
	prompt         string
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

func TestAgentHighStakesDomainsAlwaysUsePrecision(t *testing.T) {
	for _, domain := range []string{"health", "legal", "finance"} {
		t.Run(domain, func(t *testing.T) {
			fast := validModelPlan()
			fast.Domain = domain
			fast.Confidence = 0.99
			precision := fast
			precision.SpokenReply = "不確実性があるため、最新情報と専門家への確認が必要です。"
			fake := &fakeGenerator{generations: []fakeGeneration{
				{body: encodePlan(t, fast)},
				{body: encodePlan(t, precision)},
			}}
			agent := newTestAgent(t, fake)
			result, err := agent.Process(context.Background(), "uid-h", VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "判断してほしい",
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if result.Route != "precision" || len(fake.calls) != 2 {
				t.Fatalf("%s did not use precision: %#v", domain, result)
			}
		})
	}
}

func TestAgentRejectsLowUrgencySafetyIntervention(t *testing.T) {
	plan := validModelPlan()
	plan.InterventionPolicy = "safety"
	plan.Intervention.Urgency = 0.4
	fake := &fakeGenerator{generations: []fakeGeneration{{body: encodePlan(t, plan)}}}
	agent := newTestAgent(t, fake)
	_, err := agent.Process(context.Background(), "uid-s", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "一般的な相談",
	})
	if !errors.Is(err, ErrModelOutputInvalid) {
		t.Fatalf("low-urgency safety accepted: %v", err)
	}
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

func TestAgentLACForcesMeaningPreservingRestructureAndPublishesMetrics(t *testing.T) {
	plan := validModelPlan()
	plan.SpokenReply = "理由を先に説明します。A案です。"
	plan.AnswerContract = answercontract.Contract{
		QuestionFrame: answercontract.QuestionFrame{
			Operator:      answercontract.OperatorChoice,
			Subject:       "A案とB案の選択",
			RequiredSlots: []answercontract.RequiredSlot{answercontract.SlotSelection},
			Hypotheses: []answercontract.Hypothesis{{
				Interpretation: "一案を選ぶ",
				Confidence:     1,
			}},
		},
		CommitmentFront: answercontract.CommitmentFront{
			FirstCommitment: "A案です",
			FillsTarget:     true,
			TargetCoverage:  1,
			FilledSlots:     []answercontract.RequiredSlot{answercontract.SlotSelection},
			PositionClass:   answercontract.PositionLater,
			Calibration:     answercontract.CalibrationCommitted,
			Issue:           answercontract.IssueBackgroundFirst,
		},
		CounterfactualRepair: answercontract.CounterfactualRepair{
			MinimalAnswer:                 "A案です",
			ReconstructedAnswer:           "A案です。理由を先に説明します。",
			MeaningPreservationConfidence: 0.98,
			RepairGain:                    0.40,
		},
	}
	fake := &fakeGenerator{generations: []fakeGeneration{{body: encodePlan(t, plan)}}}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(context.Background(), "uid-lac", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "A案とB案ならどちら？",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Intervention.Act != "restructure" ||
		result.SpokenReply != plan.AnswerContract.CounterfactualRepair.ReconstructedAnswer {
		t.Fatalf("LAC repair was not forced: %#v", result)
	}
	if result.AnswerContract.TargetSlotCoverage != 1 ||
		result.AnswerContract.CommitmentFrontPosition != answercontract.PositionLater ||
		result.AnswerContract.MeaningPreservation != 0.98 {
		t.Fatalf("unexpected public LAC metrics: %+v", result.AnswerContract)
	}

	state, err := agent.codec.open("uid-lac", result.StateToken)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	for _, currentTurnText := range []string{
		plan.AnswerContract.QuestionFrame.Subject,
		plan.AnswerContract.CommitmentFront.FirstCommitment,
		plan.AnswerContract.CounterfactualRepair.MinimalAnswer,
	} {
		if bytes.Contains(stateJSON, []byte(currentTurnText)) {
			t.Fatalf("current-turn LAC text entered state: %s", stateJSON)
		}
	}
}

func TestAgentLACAmbiguityClarifiesOrStaysSilent(t *testing.T) {
	for _, ambient := range []bool{false, true} {
		name := "intentional"
		if ambient {
			name = "ambient"
		}
		t.Run(name, func(t *testing.T) {
			plan := validModelPlan()
			plan.AnswerContract.QuestionFrame.Hypotheses = []answercontract.Hypothesis{
				{Interpretation: "Aを尋ねている", Confidence: 0.55},
				{Interpretation: "Bを尋ねている", Confidence: 0.45},
			}
			fake := &fakeGenerator{generations: []fakeGeneration{{
				body: encodePlan(t, plan),
			}}}
			agent := newTestAgent(t, fake)
			result, err := agent.Process(context.Background(), "uid-lac", VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "それはどちら？",
				Ambient:       ambient,
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if result.AnswerContract.HypothesisGap != 0.1 {
				t.Fatalf("server gap = %v", result.AnswerContract.HypothesisGap)
			}
			if ambient {
				if result.Intervention.Act != "silent" || result.SpokenReply != "" {
					t.Fatalf("ambiguous ambient turn spoke: %#v", result)
				}
				return
			}
			if result.Intervention.Act != "clarify" ||
				!result.NeedsClarification ||
				countQuestions(result.SpokenReply) != 1 {
				t.Fatalf("intentional ambiguity did not clarify: %#v", result)
			}
		})
	}
}

func TestAgentLACRejectsMeaningChangingRepair(t *testing.T) {
	plan := validModelPlan()
	plan.SpokenReply = "理由のあとではA案です。"
	plan.AnswerContract = answercontract.Contract{
		QuestionFrame: answercontract.QuestionFrame{
			Operator:      answercontract.OperatorChoice,
			Subject:       "A案とB案の選択",
			RequiredSlots: []answercontract.RequiredSlot{answercontract.SlotSelection},
			Hypotheses: []answercontract.Hypothesis{{
				Interpretation: "一案を選ぶ",
				Confidence:     1,
			}},
		},
		CommitmentFront: answercontract.CommitmentFront{
			FirstCommitment: "A案です",
			FillsTarget:     true,
			TargetCoverage:  1,
			FilledSlots:     []answercontract.RequiredSlot{answercontract.SlotSelection},
			PositionClass:   answercontract.PositionLater,
			Calibration:     answercontract.CalibrationCommitted,
			Issue:           answercontract.IssueBackgroundFirst,
		},
		CounterfactualRepair: answercontract.CounterfactualRepair{
			MinimalAnswer:                 "B案です",
			ReconstructedAnswer:           "B案です。理由は同じです。",
			MeaningPreservationConfidence: 0.99,
			RepairGain:                    0.50,
		},
	}
	fake := &fakeGenerator{generations: []fakeGeneration{{body: encodePlan(t, plan)}}}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(context.Background(), "uid-lac", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "A案とB案ならどちら？",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Intervention.Act != "clarify" ||
		strings.Contains(result.SpokenReply, "B案") ||
		result.AnswerContract.MeaningPreservation != 0 {
		t.Fatalf("meaning-changing repair escaped gate: %#v", result)
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

func TestAgentAmbientUrgentSafetyIntervenes(t *testing.T) {
	plan := validModelPlan()
	plan.InterventionPolicy = "safety"
	plan.SpokenReply = "今は安全を優先して、すぐに緊急窓口へ連絡してください。"
	plan.Intervention = modelArbiter{
		Benefit: 0, InterruptionCost: 1, Urgency: 0.9,
		Confidence: 1, Act: "reflect",
	}
	fake := &fakeGenerator{generations: []fakeGeneration{{body: encodePlan(t, plan)}}}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(context.Background(), "uid-s", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "危険が迫っている",
		Ambient:       true,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Intervention.Act == "silent" || result.SpokenReply == "" {
		t.Fatalf("urgent safety intervention was suppressed: %#v", result)
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
	plan := modelPlan{
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
	plan.AnswerContract = answercontract.Contract{
		QuestionFrame: answercontract.QuestionFrame{
			Operator:      answercontract.OperatorOpen,
			Subject:       "next action",
			RequiredSlots: []answercontract.RequiredSlot{answercontract.SlotPosition},
			Hypotheses: []answercontract.Hypothesis{{
				Interpretation: "choose the next action",
				Confidence:     1,
			}},
		},
		CommitmentFront: answercontract.CommitmentFront{
			FirstCommitment: "small verified step first",
			FillsTarget:     true,
			TargetCoverage:  1,
			FilledSlots:     []answercontract.RequiredSlot{answercontract.SlotPosition},
			PositionClass:   answercontract.PositionFirst,
			Calibration:     answercontract.CalibrationCommitted,
			Issue:           answercontract.IssueNone,
		},
		CounterfactualRepair: answercontract.CounterfactualRepair{
			MinimalAnswer:                 plan.SpokenReply,
			ReconstructedAnswer:           plan.SpokenReply,
			MeaningPreservationConfidence: 1,
			RepairGain:                    0,
		},
	}
	return plan
}

func encodePlan(t *testing.T, plan modelPlan) string {
	t.Helper()
	if plan.AnswerContract.CounterfactualRepair.RepairGain == 0 {
		plan.AnswerContract.CounterfactualRepair.MinimalAnswer = plan.SpokenReply
		plan.AnswerContract.CounterfactualRepair.ReconstructedAnswer = plan.SpokenReply
	}
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
