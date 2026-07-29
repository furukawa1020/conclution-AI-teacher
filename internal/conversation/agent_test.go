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
	body         string
	err          error
	finishReason genai.FinishReason
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
		if strings.Contains(call.prompt, "<lac_critic_data>") {
			body, err := defaultCriticBody(call.prompt)
			if err != nil {
				return nil, err
			}
			return &genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{{
					Content: genai.NewContentFromText(body, genai.RoleModel),
				}},
			}, nil
		}
		return nil, errors.New("unexpected generation")
	}
	generation := fake.generations[index]
	if generation.err != nil {
		return nil, generation.err
	}
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content:      genai.NewContentFromText(generation.body, genai.RoleModel),
			FinishReason: generation.finishReason,
		}},
	}, nil
}

func defaultCriticBody(prompt string) (string, error) {
	const (
		startMarker = "<lac_critic_data>\n"
		endMarker   = "\n</lac_critic_data>"
	)
	start := strings.Index(prompt, startMarker)
	end := strings.Index(prompt, endMarker)
	if start < 0 || end <= start {
		return "", errors.New("critic payload not found")
	}
	var payload struct {
		CandidateSpokenReply string `json:"candidate_spoken_reply"`
	}
	if err := json.Unmarshal(
		[]byte(prompt[start+len(startMarker):end]),
		&payload,
	); err != nil {
		return "", err
	}
	contract := validCriticContract(payload.CandidateSpokenReply)
	encoded, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func validCriticContract(candidate string) answercontract.Contract {
	firstCommitment := candidate
	firstRunes := []rune(firstCommitment)
	if len(firstRunes) > answercontract.MaxFirstCommitmentRunes {
		firstCommitment = string(firstRunes[:answercontract.MaxFirstCommitmentRunes])
	}
	commitment := answercontract.CommitmentFront{
		PositionClass: answercontract.PositionAbsent,
		Calibration:   answercontract.CalibrationAbstain,
		Issue:         answercontract.IssueTargetMissing,
		FilledSlots:   []answercontract.RequiredSlot{},
	}
	if candidate != "" {
		commitment = answercontract.CommitmentFront{
			FirstCommitment: firstCommitment,
			FillsTarget:     true,
			TargetCoverage:  1,
			FilledSlots:     []answercontract.RequiredSlot{answercontract.SlotPosition},
			PositionClass:   answercontract.PositionFirst,
			Calibration:     answercontract.CalibrationCommitted,
			Issue:           answercontract.IssueNone,
		}
	}
	return answercontract.Contract{
		QuestionFrame: answercontract.QuestionFrame{
			Operator:      answercontract.OperatorOpen,
			Subject:       "current request",
			RequiredSlots: []answercontract.RequiredSlot{answercontract.SlotPosition},
			Hypotheses: []answercontract.Hypothesis{{
				Interpretation: "answer the current request",
				Confidence:     1,
			}},
		},
		CommitmentFront: commitment,
		CounterfactualRepair: answercontract.CounterfactualRepair{
			MinimalAnswer:                 firstCommitment,
			ReconstructedAnswer:           candidate,
			MeaningPreservationConfidence: 1,
			RepairGain:                    0,
		},
	}
}

func TestAgentFastPathAndInitialState(t *testing.T) {
	plan := validModelPlan()
	plan.ThoughtStateDelta.Claims = []string{"検証可能性を優先する"}
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, validCriticContract(plan.SpokenReply))},
	}}
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
	if len(fake.calls) != 2 {
		t.Fatalf("calls = %d, want planner plus independent critic", len(fake.calls))
	}
	call := fake.calls[0]
	if call.model != DefaultFastModel ||
		call.thinkingLevel != genai.ThinkingLevelLow ||
		call.responseMIME != "application/json" ||
		!call.hasJSONSchema ||
		call.temperatureSet {
		t.Fatalf("unexpected fast generation config: %#v", call)
	}
	criticCall := fake.calls[1]
	if criticCall.model != DefaultFastModel ||
		criticCall.thinkingLevel != genai.ThinkingLevelHigh ||
		criticCall.responseMIME != "application/json" ||
		!criticCall.hasJSONSchema ||
		!strings.Contains(criticCall.prompt, "<lac_critic_data>") ||
		strings.Contains(criticCall.prompt, `"answer_contract"`) {
		t.Fatalf("unexpected independent critic config: %#v", criticCall)
	}
	state, err := agent.codec.open("uid-1", result.StateToken)
	if err != nil {
		t.Fatalf("open initial state: %v", err)
	}
	if state.Turn != 1 || len(state.Graph.Claims) != 1 {
		t.Fatalf("unexpected initial state: %#v", state)
	}
}

func TestAgentCanonicalizesCriticDerivedFields(t *testing.T) {
	plan := validModelPlan()
	critic := validCriticContract(plan.SpokenReply)
	critic.CommitmentFront.FillsTarget = false
	critic.CommitmentFront.TargetCoverage = 0
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, critic)},
	}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), "uid-derived", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "結論は何ですか？",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "fast" ||
		result.AnswerContract.TargetSlotCoverage != 1 ||
		len(fake.calls) != 2 {
		t.Fatalf("derived contract fields were not canonicalized: %#v", result)
	}
}

func TestAgentRetriesOnlyRetryableCriticFailures(t *testing.T) {
	t.Run("precision model recovers two primary provider failures", func(t *testing.T) {
		plan := validModelPlan()
		fake := &fakeGenerator{generations: []fakeGeneration{
			{body: encodePlan(t, plan)},
			{err: errors.New("primary critic provider failure")},
			{err: errors.New("primary critic provider failure again")},
			{body: encodeContract(t, validCriticContract(plan.SpokenReply))},
		}}
		agent := newTestAgent(t, fake)

		result, err := agent.Process(context.Background(), "uid-critic-recovery", VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "結論は何ですか？",
		})
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "fast" ||
			len(fake.calls) != 4 ||
			fake.calls[3].model != DefaultPrecisionModel ||
			fake.calls[3].thinkingLevel != genai.ThinkingLevelMedium {
			t.Fatalf("precision critic recovery failed: result=%#v calls=%#v", result, fake.calls)
		}
	})

	t.Run("provider unavailable is retried once", func(t *testing.T) {
		plan := validModelPlan()
		fake := &fakeGenerator{generations: []fakeGeneration{
			{body: encodePlan(t, plan)},
			{err: errors.New("temporary critic provider failure")},
			{body: encodeContract(t, validCriticContract(plan.SpokenReply))},
		}}
		agent := newTestAgent(t, fake)

		result, err := agent.Process(context.Background(), "uid-critic-provider", VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "結論は何ですか？",
		})
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "fast" || len(fake.calls) != 3 {
			t.Fatalf("provider critic retry failed: %#v", result)
		}
	})

	t.Run("invalid JSON is retried once", func(t *testing.T) {
		plan := validModelPlan()
		fake := &fakeGenerator{generations: []fakeGeneration{
			{body: encodePlan(t, plan)},
			{body: "{"},
			{body: encodeContract(t, validCriticContract(plan.SpokenReply))},
		}}
		agent := newTestAgent(t, fake)

		result, err := agent.Process(context.Background(), "uid-retry", VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "結論は何ですか？",
		})
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "fast" || len(fake.calls) != 3 {
			t.Fatalf("retryable critic failure was not recovered: %#v", result)
		}
		for _, call := range fake.calls[1:] {
			if call.model != DefaultFastModel ||
				!strings.Contains(call.prompt, "<lac_critic_data>") {
				t.Fatalf("retry escaped isolated critic call: %#v", call)
			}
		}
	})

	t.Run("safety finish is never retried", func(t *testing.T) {
		plan := validModelPlan()
		fake := &fakeGenerator{generations: []fakeGeneration{
			{body: encodePlan(t, plan)},
			{finishReason: genai.FinishReasonSafety},
		}}
		agent := newTestAgent(t, fake)

		result, err := agent.Process(context.Background(), "uid-safety-finish", VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "答えて",
		})
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "verification-unavailable" || len(fake.calls) != 2 {
			t.Fatalf("safety finish was retried or published: %#v", result)
		}
	})
}

func TestAgentRetriesStructuredInferenceButNotSafetyFinish(t *testing.T) {
	for _, test := range []struct {
		name  string
		first fakeGeneration
	}{
		{
			name:  "provider unavailable",
			first: fakeGeneration{err: errors.New("temporary provider failure")},
		},
		{
			name:  "invalid structured output",
			first: fakeGeneration{body: "{"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := validModelPlan()
			fake := &fakeGenerator{generations: []fakeGeneration{
				test.first,
				{body: encodePlan(t, plan)},
				{body: encodeContract(t, validCriticContract(plan.SpokenReply))},
			}}
			agent := newTestAgent(t, fake)

			result, err := agent.Process(context.Background(), "uid-infer-retry", VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "日本の首都はどこ？",
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if result.Route != "fast" || len(fake.calls) != 3 {
				t.Fatalf("structured inference retry failed: %#v", result)
			}
			if fake.calls[0].model != DefaultFastModel ||
				fake.calls[1].model != DefaultFastModel {
				t.Fatalf("retry changed model role: %#v", fake.calls)
			}
		})
	}

	t.Run("safety finish is not retried", func(t *testing.T) {
		fake := &fakeGenerator{generations: []fakeGeneration{
			{finishReason: genai.FinishReasonSafety},
		}}
		agent := newTestAgent(t, fake)
		_, err := agent.Process(context.Background(), "uid-infer-safety", VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "答えて",
		})
		if !errors.Is(err, ErrModelOutputInvalid) || len(fake.calls) != 1 {
			t.Fatalf("safety finish was retried: calls=%d err=%v", len(fake.calls), err)
		}
	})
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
		if len(fake.calls) != 3 ||
			fake.calls[1].model != DefaultPrecisionModel ||
			fake.calls[1].thinkingLevel != genai.ThinkingLevelHigh ||
			!strings.Contains(fake.calls[1].prompt, `"preliminary_plan"`) ||
			fake.calls[2].model != DefaultFastModel ||
			fake.calls[2].thinkingLevel != genai.ThinkingLevelHigh ||
			!strings.Contains(fake.calls[2].prompt, "<lac_critic_data>") {
			t.Fatalf("unexpected precision calls: %#v", fake.calls)
		}
	})

	t.Run("precision failure fails closed", func(t *testing.T) {
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
		if result.Route != "precision-unavailable" ||
			!result.NeedsClarification ||
			result.SpokenReply == fast.SpokenReply ||
			strings.Contains(result.SpokenReply, "標本数") {
			t.Fatalf("high-risk fast draft escaped fail-closed path: %#v", result)
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
			if result.Route != "precision" || len(fake.calls) != 3 {
				t.Fatalf("%s did not use precision: %#v", domain, result)
			}
		})
	}
}

func TestAgentRejectsLowUrgencySafetyIntervention(t *testing.T) {
	plan := validModelPlan()
	plan.InterventionPolicy = "safety"
	plan.Intervention.Urgency = 0.4
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, plan.AnswerContract)},
	}}
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
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, plan.AnswerContract)},
	}}
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
			fake := &fakeGenerator{generations: []fakeGeneration{
				{body: encodePlan(t, plan)},
				{body: encodeContract(t, plan.AnswerContract)},
			}}
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
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, plan.AnswerContract)},
	}}
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

func TestAgentRespondentAwaitsAnswerWithoutInventingIt(t *testing.T) {
	plan := respondentAwaitingPlan()
	fake := &fakeGenerator{generations: []fakeGeneration{{
		body: encodePlan(t, plan),
	}}}
	agent := newTestAgent(t, fake)
	utterance := "上司に結局何のために入れるのと聞かれたけど、うまく答えられない"

	result, err := agent.Process(context.Background(), "uid-respondent-await", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     utterance,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "respondent-awaiting-fast" ||
		result.AssistanceTarget != "respondent" ||
		result.RespondentStage != "awaiting_answer" ||
		result.Intervention.Act != "clarify" ||
		!result.NeedsClarification ||
		result.SpokenReply != plan.SpokenReply {
		t.Fatalf("respondent awaiting result: %#v", result)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("awaiting answer invoked a critic or extra model: %#v", fake.calls)
	}

	state, err := agent.codec.open("uid-respondent-await", result.StateToken)
	if err != nil {
		t.Fatalf("open respondent state: %v", err)
	}
	if !state.PendingAnswer.Active ||
		state.PendingAnswer.Operator != answercontract.OperatorPurpose ||
		state.PendingAnswer.Subject != "導入目的" ||
		len(state.PendingAnswer.RequiredSlots) != 1 ||
		state.PendingAnswer.RequiredSlots[0] != answercontract.SlotPurpose {
		t.Fatalf("pending question frame: %#v", state.PendingAnswer)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal respondent state: %v", err)
	}
	if bytes.Contains(stateJSON, []byte(utterance)) ||
		bytes.Contains(stateJSON, []byte(plan.SpokenReply)) {
		t.Fatalf("awaiting state retained turn prose: %s", stateJSON)
	}
}

func TestAgentRespondentRestructuresExistingClausesAndClearsPendingFrame(t *testing.T) {
	awaiting := respondentAwaitingPlan()
	restructure := respondentRestructurePlan(
		"判断のばらつきを減らします。目的は評価基準をそろえることです。",
		"目的は評価基準をそろえることです。判断のばらつきを減らします。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, awaiting)},
		{body: encodePlan(t, restructure)},
		{body: encodeContract(t, validCriticContract(restructure.SpokenReply))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), "uid-respondent-flow", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "結局何のために入れるのかと聞かれたけど、答えがまとまらない",
	})
	if err != nil {
		t.Fatalf("awaiting Process: %v", err)
	}
	secondUtterance := "今の答えは「" + restructure.AnswerAttempt + "」です"
	second, err := agent.Process(context.Background(), "uid-respondent-flow", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     secondUtterance,
		StateToken:    first.StateToken,
	})
	if err != nil {
		t.Fatalf("restructure Process: %v", err)
	}
	if second.Route != "respondent-restructure-fast" ||
		second.SpokenReply != restructure.SpokenReply ||
		second.Intervention.Act != "restructure" ||
		second.NeedsClarification {
		t.Fatalf("safe respondent restructure: %#v", second)
	}
	if len(fake.calls) != 3 ||
		!strings.Contains(fake.calls[1].prompt, `"pending_answer":{"active":true`) {
		t.Fatalf("pending frame did not reach the next planner: %#v", fake.calls)
	}

	state, err := agent.codec.open("uid-respondent-flow", second.StateToken)
	if err != nil {
		t.Fatalf("open resolved state: %v", err)
	}
	if state.PendingAnswer.Active || len(state.PendingAnswer.RequiredSlots) != 0 {
		t.Fatalf("resolved pending frame survived: %#v", state.PendingAnswer)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal resolved state: %v", err)
	}
	for _, forbidden := range []string{
		restructure.AnswerAttempt,
		restructure.SpokenReply,
		secondUtterance,
	} {
		if bytes.Contains(stateJSON, []byte(forbidden)) {
			t.Fatalf("resolved respondent prose entered state: %s", stateJSON)
		}
	}
}

func TestAgentRespondentRejectsMeaningChangingReconstruction(t *testing.T) {
	plan := respondentChoicePlan(
		"費用は3万円です。A案を選びます。",
		"B案を選びます。費用は4万円です。",
		[]string{"A案"},
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, validCriticContract(plan.SpokenReply))},
	}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), "uid-respondent-reject", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "自分の答えは「" + plan.AnswerAttempt + "」です",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "respondent-meaning-clarify-fast" ||
		result.Intervention.Act != "clarify" ||
		!result.NeedsClarification ||
		result.SpokenReply == plan.SpokenReply ||
		strings.Contains(result.SpokenReply, "B案") ||
		strings.Contains(result.SpokenReply, "4万円") {
		t.Fatalf("meaning-changing respondent answer escaped: %#v", result)
	}
}

func TestAgentRespondentPendingFrameRetainsNoAnswerAttempt(t *testing.T) {
	plan := respondentChoicePlan(
		"田中さんの判断です。A案を選びます。",
		"佐藤さんの判断です。B案を選びます。",
		[]string{"田中さん", "A案"},
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, validCriticContract(plan.SpokenReply))},
	}}
	agent := newTestAgent(t, fake)
	utterance := "回答としては「" + plan.AnswerAttempt + "」と伝えたい"

	result, err := agent.Process(context.Background(), "uid-respondent-retention", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     utterance,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	state, err := agent.codec.open("uid-respondent-retention", result.StateToken)
	if err != nil {
		t.Fatalf("open blocked respondent state: %v", err)
	}
	if !state.PendingAnswer.Active ||
		state.PendingAnswer.Operator != answercontract.OperatorChoice ||
		state.PendingAnswer.Subject != "採用案" ||
		len(state.PendingAnswer.RequiredSlots) != 1 ||
		state.PendingAnswer.RequiredSlots[0] != answercontract.SlotSelection {
		t.Fatalf("blocked pending frame: %#v", state.PendingAnswer)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal blocked respondent state: %v", err)
	}
	for _, forbidden := range []string{
		utterance,
		plan.AnswerAttempt,
		plan.SpokenReply,
		plan.RespondentEvidence[0].Span,
		"田中さん",
		"佐藤さん",
		"A案",
		"B案",
	} {
		if bytes.Contains(stateJSON, []byte(forbidden)) {
			t.Fatalf("pending frame retained respondent prose %q: %s", forbidden, stateJSON)
		}
	}
}

func TestAgentDraftLACCannotBypassIndependentCritic(t *testing.T) {
	plan := validModelPlan()
	plan.SpokenReply = "前置きだけで、まだ答えは述べません。"
	// The draft falsely claims full, answer-first coverage.
	plan.AnswerContract = validCriticContract(plan.SpokenReply)

	critic := validCriticContract(plan.SpokenReply)
	critic.CommitmentFront = answercontract.CommitmentFront{
		FillsTarget:    false,
		TargetCoverage: 0,
		FilledSlots:    []answercontract.RequiredSlot{},
		PositionClass:  answercontract.PositionAbsent,
		Calibration:    answercontract.CalibrationAbstain,
		Issue:          answercontract.IssueTargetMissing,
	}
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, critic)},
	}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), "uid-independent", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "結論は何ですか？",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Intervention.Act != "clarify" ||
		!result.NeedsClarification ||
		result.AnswerContract.TargetSlotCoverage != 0 {
		t.Fatalf("draft-side LAC bypassed critic: %#v", result)
	}
	if !strings.Contains(fake.calls[1].prompt, "candidate_spoken_reply") ||
		strings.Contains(fake.calls[1].prompt, "answer_contract") {
		t.Fatalf("critic was not independent of draft LAC: %q", fake.calls[1].prompt)
	}
}

func TestAgentCriticUnavailableNeverPublishesUnauditedDraft(t *testing.T) {
	for _, ambient := range []bool{false, true} {
		t.Run(map[bool]string{false: "intentional", true: "ambient"}[ambient], func(t *testing.T) {
			plan := validModelPlan()
			plan.SpokenReply = "監査前の実質回答を読み上げてはいけない。"
			fake := &fakeGenerator{generations: []fakeGeneration{
				{body: encodePlan(t, plan)},
				{err: errors.New("critic unavailable with provider detail")},
				{err: errors.New("critic still unavailable with provider detail")},
				{err: errors.New("precision recovery unavailable with provider detail")},
			}}
			agent := newTestAgent(t, fake)
			result, err := agent.Process(context.Background(), "uid-critic", VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "答えて",
				Ambient:       ambient,
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if result.Route != "verification-unavailable" ||
				strings.Contains(result.SpokenReply, "実質回答") {
				t.Fatalf("unaudited draft escaped: %#v", result)
			}
			if ambient {
				if result.SpokenReply != "" || result.Intervention.Act != "silent" {
					t.Fatalf("ambient critic failure spoke: %#v", result)
				}
			} else if result.SpokenReply == "" ||
				result.Intervention.Act != "clarify" ||
				countQuestions(result.SpokenReply) != 1 {
				t.Fatalf("intentional critic failure did not ask one question: %#v", result)
			}
		})
	}
}

func TestCriticFailureClassIsFiniteAndContentFree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		wantClass string
		wantStage string
	}{
		{
			name:      "deadline",
			err:       errors.Join(ErrModelUnavailable, errCriticDeadline),
			wantClass: "deadline",
			wantStage: "generate",
		},
		{
			name:      "canceled",
			err:       errors.Join(ErrModelUnavailable, errCriticCanceled),
			wantClass: "canceled",
			wantStage: "generate",
		},
		{
			name:      "provider",
			err:       ErrModelUnavailable,
			wantClass: "provider_unavailable",
			wantStage: "generate",
		},
		{
			name:      "safety",
			err:       errors.Join(ErrModelOutputInvalid, errCriticFinishSafety),
			wantClass: "safety",
			wantStage: "finish",
		},
		{
			name:      "contract",
			err:       errors.Join(ErrModelOutputInvalid, errCriticContract),
			wantClass: "contract_invalid",
			wantStage: "contract",
		},
		{
			name:      "response",
			err:       ErrModelOutputInvalid,
			wantClass: "response_invalid",
			wantStage: "internal",
		},
		{
			name:      "unknown provider detail",
			err:       errors.New("SECRET provider response"),
			wantClass: "internal",
			wantStage: "internal",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gotClass := criticFailureClass(test.err)
			gotStage := criticFailureStage(test.err)
			if gotClass != test.wantClass ||
				gotStage != test.wantStage ||
				strings.Contains(gotClass+gotStage, "SECRET") {
				t.Fatalf(
					"critic failure = (%q, %q); want (%q, %q)",
					gotClass,
					gotStage,
					test.wantClass,
					test.wantStage,
				)
			}
		})
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

func TestAgentUrgentSafetyOutranksLowConfidenceAndAmbiguousLAC(t *testing.T) {
	plan := validModelPlan()
	plan.Confidence = 0.35
	plan.InterventionPolicy = "safety"
	plan.SpokenReply = "監査で曖昧とされたdraftは読み上げない。"
	plan.Intervention = modelArbiter{
		Benefit: 0, InterruptionCost: 1, Urgency: 0.95,
		Confidence: 1, Act: "reflect",
	}
	critic := validCriticContract(plan.SpokenReply)
	critic.QuestionFrame.Hypotheses = []answercontract.Hypothesis{
		{Interpretation: "事故の危険", Confidence: 0.51},
		{Interpretation: "別の危険", Confidence: 0.49},
	}
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, critic)},
	}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(context.Background(), "uid-safety-priority", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "いま目の前で危険が迫っている",
		Ambient:       true,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.SpokenReply == "" ||
		result.SpokenReply == plan.SpokenReply ||
		result.Intervention.Act == "silent" ||
		result.Intervention.Act == "clarify" ||
		result.InterventionPolicy != "safety" {
		t.Fatalf("urgent safety was rewritten by ambiguity: %#v", result)
	}
}

func TestAgentLexicalHighRiskCannotBypassPrecision(t *testing.T) {
	fast := validModelPlan()
	fast.Domain = "general"
	fast.SpokenReply = "早いモデルの薬の回答。"
	precision := fast
	precision.SpokenReply = "薬の量は処方元か薬剤師に確認してください。"
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, fast)},
		{body: encodePlan(t, precision)},
		{body: encodeContract(t, validCriticContract(precision.SpokenReply))},
	}}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(context.Background(), "uid-lexical", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "この薬の用量を変えてもいい？",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "precision" ||
		len(fake.calls) != 3 ||
		fake.calls[1].model != DefaultPrecisionModel ||
		result.SpokenReply != precision.SpokenReply {
		t.Fatalf("lexical high-risk signal bypassed precision: %#v", result)
	}
}

func TestAgentPDFIsInlineThenZeroizedAndNoFreeTextEntersState(t *testing.T) {
	utterance := "この秘密の逐語発話XYZをそのまま保存しないで"
	pdf := []byte("%PDF-1.7\nRAW-PDF-SECRET")
	plan := validModelPlan()
	plan.ConversationSummary = utterance
	plan.ThoughtStateDelta.Claims = []string{utterance}
	plan.DocumentSummary = "資料は小規模な比較実験と三つの限界を示す"
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodePlan(t, plan)},
		{body: encodeContract(t, validCriticContract(plan.SpokenReply))},
	}}
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
	if result.Route != "precision" ||
		len(fake.calls) != 3 ||
		fake.calls[1].model != DefaultPrecisionModel {
		t.Fatalf("PDF did not force precision and independent audit: %#v", fake.calls)
	}
	if !allZero(pdf) {
		t.Fatalf("PDF bytes were not cleared: %q", pdf)
	}

	state, err := agent.codec.open("uid-p", result.StateToken)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	if state.DocumentSummary != "" ||
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

func TestGraphStateDropsPIITokensAndPartialQuotes(t *testing.T) {
	utterance := "秘密の計画は来週火曜に実行するつもりです"
	graph := mergeGraph(ThoughtStateGraph{}, ThoughtStateDelta{
		Claims: []string{
			"秘密の計画は来週火曜に実行する",
			"連絡先はalice@example.com",
			"電話は090-1234-5678",
			"Bearer eyJhbGciOiJIUzI1NiJ9.payload.signature",
			"意思決定の条件を整理する",
		},
	}, utterance)
	if len(graph.Claims) != 1 || graph.Claims[0] != "意思決定の条件を整理する" {
		t.Fatalf("unsafe graph nodes survived: %#v", graph.Claims)
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
	callsBeforeWrongUID := len(fake.calls)
	_, err = agent.Process(context.Background(), "uid-other", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "次の質問",
		StateToken:    first.StateToken,
	})
	if !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("wrong UID: got %v", err)
	}
	if len(fake.calls) != callsBeforeWrongUID {
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
		AssistanceTarget:    "assistant",
		RespondentStage:     "none",
		AnswerAttempt:       "",
		RespondentEvidence:  []modelSlotEvidence{},
		RespondentProtected: []string{},
		ResearchAction:      "none",
		ResearchQuery:       "",
		LatentQuestion:      "次の一歩は何か",
		ArgumentStructure:   "conclusion_reason",
		InterventionPolicy:  "answer",
		SpokenReply:         "結論から言うと、小さく試して結果を確かめるのが先です。",
		Confidence:          0.9,
		ConversationSummary: "次の行動を選ぶため検証方法を整理中",
		DocumentSummary:     "",
		ThoughtStateDelta: ThoughtStateDelta{
			Goals:          []string{},
			Claims:         []string{},
			Grounds:        []string{},
			Assumptions:    []string{},
			Constraints:    []string{},
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

func respondentAwaitingPlan() modelPlan {
	plan := validModelPlan()
	plan.Domain = "work"
	plan.Intent = "practice"
	plan.AssistanceTarget = "respondent"
	plan.RespondentStage = "awaiting_answer"
	plan.LatentQuestion = "導入目的に対する本人の回答は何か"
	plan.ArgumentStructure = "clarifying_question"
	plan.InterventionPolicy = "clarify"
	plan.SpokenReply = "まとまっていなくていいので、今の答えをそのまま話してもらえますか？"
	plan.Intervention = modelArbiter{
		Benefit: 0.8, InterruptionCost: 0.1, Urgency: 0.1,
		Confidence: 0.95, Act: "clarify",
	}
	plan.AnswerContract = respondentDraftContract(
		answercontract.OperatorPurpose,
		"導入目的",
		[]answercontract.RequiredSlot{answercontract.SlotPurpose},
		"",
		"",
	)
	return plan
}

func respondentRestructurePlan(answerAttempt, reconstruction string) modelPlan {
	plan := validModelPlan()
	plan.Domain = "work"
	plan.Intent = "practice"
	plan.AssistanceTarget = "respondent"
	plan.RespondentStage = "restructure"
	plan.AnswerAttempt = answerAttempt
	plan.RespondentEvidence = []modelSlotEvidence{{
		Slot: answercontract.SlotPurpose,
		Span: "目的は評価基準をそろえることです",
	}}
	plan.LatentQuestion = "導入目的への本人の答えを先にする"
	plan.ArgumentStructure = "direct_answer"
	plan.InterventionPolicy = "coach"
	plan.SpokenReply = reconstruction
	plan.Intervention = modelArbiter{
		Benefit: 0.9, InterruptionCost: 0.05, Urgency: 0.1,
		Confidence: 0.98, Act: "restructure",
	}
	plan.AnswerContract = respondentDraftContract(
		answercontract.OperatorPurpose,
		"導入目的",
		[]answercontract.RequiredSlot{answercontract.SlotPurpose},
		"目的は評価基準をそろえることです",
		reconstruction,
	)
	return plan
}

func respondentChoicePlan(
	answerAttempt,
	reconstruction string,
	protected []string,
) modelPlan {
	plan := validModelPlan()
	plan.Domain = "work"
	plan.Intent = "practice"
	plan.AssistanceTarget = "respondent"
	plan.RespondentStage = "restructure"
	plan.AnswerAttempt = answerAttempt
	plan.RespondentEvidence = []modelSlotEvidence{{
		Slot: answercontract.SlotSelection,
		Span: "A案を選びます",
	}}
	plan.RespondentProtected = append([]string(nil), protected...)
	plan.LatentQuestion = "採用案への本人の答えを先にする"
	plan.ArgumentStructure = "direct_answer"
	plan.InterventionPolicy = "coach"
	plan.SpokenReply = reconstruction
	plan.Intervention = modelArbiter{
		Benefit: 0.9, InterruptionCost: 0.05, Urgency: 0.1,
		Confidence: 0.98, Act: "restructure",
	}
	firstCommitment := reconstruction
	if clauses := strings.Split(reconstruction, "。"); len(clauses) > 0 {
		firstCommitment = clauses[0]
	}
	plan.AnswerContract = respondentDraftContract(
		answercontract.OperatorChoice,
		"採用案",
		[]answercontract.RequiredSlot{answercontract.SlotSelection},
		firstCommitment,
		reconstruction,
	)
	return plan
}

func respondentDraftContract(
	operator answercontract.Operator,
	subject string,
	required []answercontract.RequiredSlot,
	firstCommitment,
	candidate string,
) answercontract.Contract {
	commitment := answercontract.CommitmentFront{
		FillsTarget:    false,
		TargetCoverage: 0,
		FilledSlots:    []answercontract.RequiredSlot{},
		PositionClass:  answercontract.PositionAbsent,
		Calibration:    answercontract.CalibrationAbstain,
		Issue:          answercontract.IssueTargetMissing,
	}
	if firstCommitment != "" {
		commitment = answercontract.CommitmentFront{
			FirstCommitment: firstCommitment,
			FillsTarget:     true,
			TargetCoverage:  1,
			FilledSlots:     append([]answercontract.RequiredSlot(nil), required...),
			PositionClass:   answercontract.PositionFirst,
			Calibration:     answercontract.CalibrationCommitted,
			Issue:           answercontract.IssueNone,
		}
	}
	return answercontract.Contract{
		QuestionFrame: answercontract.QuestionFrame{
			Operator:      operator,
			Subject:       subject,
			RequiredSlots: append([]answercontract.RequiredSlot(nil), required...),
			Hypotheses: []answercontract.Hypothesis{{
				Interpretation: "本人が他者の質問へ答える",
				Confidence:     1,
			}},
		},
		CommitmentFront: commitment,
		CounterfactualRepair: answercontract.CounterfactualRepair{
			MinimalAnswer:                 firstCommitment,
			ReconstructedAnswer:           candidate,
			MeaningPreservationConfidence: 1,
			RepairGain:                    0,
		},
	}
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

func encodeContract(t *testing.T, contract answercontract.Contract) string {
	t.Helper()
	encoded, err := json.Marshal(contract)
	if err != nil {
		t.Fatalf("marshal answer contract: %v", err)
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
