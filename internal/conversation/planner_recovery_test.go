package conversation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"google.golang.org/genai"
)

func TestAgentRecoversRepeatedStructuralPlannerFailureWithPrecision(t *testing.T) {
	recovered := validModelPlan()
	recovered.SpokenReply = "日本の首都は東京です。"
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: "{"},
		{body: "{"},
		{body: encodePlan(t, recovered)},
		{body: encodeContract(t, validCriticContract(recovered.SpokenReply))},
	}}
	agent := newTestAgent(t, fake)

	result, err := agent.Process(
		context.Background(),
		"uid-planner-precision-recovery",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "日本の首都はどこですか？",
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "precision-recovery" ||
		result.SpokenReply != recovered.SpokenReply ||
		len(fake.calls) != 4 {
		t.Fatalf("unexpected recovery result=%#v calls=%#v", result, fake.calls)
	}
	if fake.calls[0].model != DefaultFastModel ||
		fake.calls[1].model != DefaultFastModel ||
		fake.calls[2].model != DefaultPrecisionModel ||
		fake.calls[2].thinkingLevel != genai.ThinkingLevelHigh ||
		fake.calls[2].deadline <= 0 ||
		fake.calls[2].deadline > voicePrecisionInferenceTimeout ||
		strings.Contains(fake.calls[2].prompt, `"preliminary_plan"`) ||
		strings.Contains(fake.calls[2].prompt, `"unexpected"`) ||
		fake.calls[3].model != DefaultFastModel ||
		!strings.Contains(fake.calls[3].prompt, "<lac_critic_data>") {
		t.Fatalf("unsafe or redundant recovery call sequence: %#v", fake.calls)
	}
}

func TestAgentPrecisionRecoveryCannotEscalateToOutboundResearch(t *testing.T) {
	const (
		topic       = "量子エラー訂正"
		secretDraft = "RECOVERED-RESEARCH-DRAFT-MUST-NOT-ESCAPE"
	)
	recovered := recentPapersPlan(topic)
	recovered.SpokenReply = secretDraft
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: "{"},
		{body: "{"},
		{body: encodePlan(t, recovered)},
	}}
	verifier := &fakeResearchVerifier{}
	agent := newTestAgent(t, fake)
	attachResearchVerifier(t, agent, verifier)

	result, err := agent.Process(
		context.Background(),
		"uid-planner-recovery-research",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     japaneseRecentRequest(topic),
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "planner-unavailable" ||
		result.SpokenReply != plannerUnavailableSpokenReply ||
		!result.NeedsClarification ||
		result.ResearchStatus != "none" ||
		len(result.ResearchRecords) != 0 ||
		result.StateToken == "" ||
		strings.Contains(result.SpokenReply, secretDraft) {
		t.Fatalf("recovery escalated research capability: %#v", result)
	}
	if len(verifier.calls) != 0 {
		t.Fatalf("precision recovery reached outbound verifier: %#v", verifier.calls)
	}
	if len(fake.calls) != 3 ||
		fake.calls[0].model != DefaultFastModel ||
		fake.calls[1].model != DefaultFastModel ||
		fake.calls[2].model != DefaultPrecisionModel ||
		strings.Contains(fake.calls[2].prompt, "<lac_critic_data>") {
		t.Fatalf("unexpected recovery call sequence: %#v", fake.calls)
	}
}

func TestAgentPlannerRecoveryFailsClosedWithoutPublishingDraft(t *testing.T) {
	t.Run("precision recovery fails", func(t *testing.T) {
		fake := &fakeGenerator{generations: []fakeGeneration{
			{body: "{"},
			{body: "{"},
			{body: "{"},
		}}
		agent := newTestAgent(t, fake)

		result, err := agent.Process(
			context.Background(),
			"uid-planner-failed-closed",
			VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "この質問に直接答えてください",
			},
		)
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "planner-unavailable" ||
			result.SpokenReply != plannerUnavailableSpokenReply ||
			!result.NeedsClarification ||
			result.StateToken == "" ||
			len(fake.calls) != 3 {
			t.Fatalf("unsafe failed-closed result=%#v calls=%#v", result, fake.calls)
		}
	})

	t.Run("provider failure does not model hop", func(t *testing.T) {
		fake := &fakeGenerator{generations: []fakeGeneration{
			{err: errors.New("provider detail one")},
			{err: errors.New("provider detail two")},
		}}
		agent := newTestAgent(t, fake)

		result, err := agent.Process(
			context.Background(),
			"uid-planner-provider-failed-closed",
			VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "質問です",
			},
		)
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "planner-unavailable" ||
			strings.Contains(result.SpokenReply, "provider detail") ||
			len(fake.calls) != 2 ||
			fake.calls[0].model != DefaultFastModel ||
			fake.calls[1].model != DefaultFastModel {
			t.Fatalf("provider failure escaped boundary: result=%#v calls=%#v", result, fake.calls)
		}
	})

	t.Run("respondent guard does not retry or model hop", func(t *testing.T) {
		blocked := validModelPlan()
		blocked.AssistanceTarget = "respondent"
		blocked.RespondentStage = "none"
		fake := &fakeGenerator{generations: []fakeGeneration{{
			body: encodePlan(t, blocked),
		}}}
		agent := newTestAgent(t, fake)

		result, err := agent.Process(
			context.Background(),
			"uid-planner-respondent-guard",
			VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "日本の首都はどこですか？",
			},
		)
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "planner-unavailable" || len(fake.calls) != 1 {
			t.Fatalf("hard guard was retried: result=%#v calls=%#v", result, fake.calls)
		}
	})
}

func TestAgentPlannerUnavailablePreservesStateAndGivesAmbientNotice(t *testing.T) {
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: "{"},
		{body: "{"},
		{body: "{"},
	}}
	agent := newTestAgent(t, fake)
	initial := conversationState{
		Turn: 3,
		Graph: ThoughtStateGraph{
			Goals:          []string{"既存の目標"},
			Claims:         []string{"既存の主張"},
			Grounds:        []string{},
			Assumptions:    []string{},
			Constraints:    []string{},
			OpenLoops:      []string{},
			Contradictions: []string{},
			Decisions:      []string{},
		},
		PendingAnswer: PendingAnswerFrame{
			Active:        true,
			Operator:      answercontract.OperatorPurpose,
			Subject:       "既存の保留質問",
			RequiredSlots: []answercontract.RequiredSlot{answercontract.SlotPurpose},
		},
		SelfCorrectionGrace: true,
		LastIntervention:    ArbiterDecision{Act: "clarify"},
	}
	token, err := agent.codec.seal("uid-planner-state", initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}
	initial, err = agent.codec.open("uid-planner-state", token)
	if err != nil {
		t.Fatalf("open normalized initial state: %v", err)
	}

	result, err := agent.Process(
		context.Background(),
		"uid-planner-state",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "考え途中の独り言",
			StateToken:    token,
			Ambient:       true,
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "planner-unavailable" ||
		result.SpokenReply != plannerUnavailableSpokenReply ||
		!result.NeedsClarification ||
		result.StateToken == "" ||
		result.StateToken == token {
		t.Fatalf("ambient fallback notice was not bounded and fresh: %#v", result)
	}
	next, err := agent.codec.open("uid-planner-state", result.StateToken)
	if err != nil {
		t.Fatalf("open fallback state: %v", err)
	}
	if next.Turn != initial.Turn+1 ||
		!reflect.DeepEqual(next.Graph, initial.Graph) ||
		!reflect.DeepEqual(next.PendingAnswer, initial.PendingAnswer) ||
		next.SelfCorrectionGrace != initial.SelfCorrectionGrace ||
		next.LastIntervention.Act != initial.LastIntervention.Act {
		t.Fatalf("fallback changed semantic state: got=%#v want=%#v", next, initial)
	}
}

func TestAgentPlannerUnavailableForegroundGetsFixedNoticeAndIsolatesState(
	t *testing.T,
) {
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: "{"},
		{body: "{"},
		{body: "{"},
	}}
	agent := newTestAgent(t, fake)
	initial := conversationState{
		Turn: 2,
		Graph: ThoughtStateGraph{
			Goals:          []string{},
			Claims:         []string{"既存の主張"},
			Grounds:        []string{},
			Assumptions:    []string{},
			Constraints:    []string{},
			OpenLoops:      []string{},
			Contradictions: []string{},
			Decisions:      []string{},
		},
		PendingAnswer: PendingAnswerFrame{
			Active:        true,
			Operator:      answercontract.OperatorOpen,
			Subject:       "既存の問い",
			RequiredSlots: []answercontract.RequiredSlot{answercontract.SlotPosition},
		},
		LastIntervention: ArbiterDecision{
			Benefit: 0.6, Confidence: 1, Act: "clarify", Score: 0.6,
		},
	}
	token, err := agent.codec.seal("uid-foreground-planner", initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}
	initial, err = agent.codec.open("uid-foreground-planner", token)
	if err != nil {
		t.Fatalf("open normalized initial state: %v", err)
	}
	result, err := agent.Process(
		context.Background(),
		"uid-foreground-planner",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "続きの質問です",
			StateToken:    token,
			Ambient:       true,
			Foreground:    true,
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "planner-unavailable" ||
		result.SpokenReply != plannerUnavailableSpokenReply ||
		!result.NeedsClarification {
		t.Fatalf("foreground planner fallback did not speak fixed notice: %#v", result)
	}
	next, err := agent.codec.open(
		"uid-foreground-planner",
		result.StateToken,
	)
	if err != nil {
		t.Fatalf("open fallback state: %v", err)
	}
	if next.Turn != initial.Turn+1 ||
		!reflect.DeepEqual(next.Graph, initial.Graph) ||
		!reflect.DeepEqual(next.PendingAnswer, initial.PendingAnswer) ||
		next.LastIntervention != initial.LastIntervention {
		t.Fatalf("foreground fallback changed isolated state: %#v", next)
	}
	for index, call := range fake.calls {
		if !strings.Contains(call.prompt, `"foreground":true`) {
			t.Fatalf("call %d omitted foreground planner data: %s", index, call.prompt)
		}
	}
}

func TestAgentPrecisionRecoveryDoesNotRepeatPendingPlannerOrPrecision(t *testing.T) {
	recovered := respondentAwaitingPlan()
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: "{"},
		{body: "{"},
		{body: encodePlan(t, recovered)},
	}}
	agent := newTestAgent(t, fake)
	initial := plannerRecoveryState()
	token, err := agent.codec.seal("uid-pending-precision-recovery", initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}

	result, err := agent.Process(
		context.Background(),
		"uid-pending-precision-recovery",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "導入目的をどう答えるか考えています",
			StateToken:    token,
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "respondent-awaiting-precision-recovery" ||
		result.SpokenReply != purposeCoachPrompt() ||
		len(fake.calls) != 3 ||
		fake.calls[0].model != DefaultFastModel ||
		fake.calls[1].model != DefaultFastModel ||
		fake.calls[2].model != DefaultPrecisionModel {
		t.Fatalf(
			"precision recovery repeated a pending planner: result=%#v calls=%#v",
			result,
			fake.calls,
		)
	}
}

func TestAgentFailedPendingRecoveryPreservesPendingState(t *testing.T) {
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, respondentAwaitingPlan())},
		{err: errors.New("pending recovery provider detail one")},
		{err: errors.New("pending recovery provider detail two")},
	}}
	agent := newTestAgent(t, fake)
	initial := plannerRecoveryState()
	token, err := agent.codec.seal("uid-pending-failed-closed", initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}
	initial, err = agent.codec.open("uid-pending-failed-closed", token)
	if err != nil {
		t.Fatalf("open normalized initial state: %v", err)
	}

	result, err := agent.Process(
		context.Background(),
		"uid-pending-failed-closed",
		VoiceTurn{
			SchemaVersion: SchemaVersion,
			Utterance:     "今日は別の話をしたいです",
			StateToken:    token,
		},
	)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Route != "interpretation-clarify-fast" ||
		result.StateToken == "" ||
		len(fake.calls) != 3 {
		t.Fatalf(
			"pending recovery failure was not bounded: result=%#v calls=%#v",
			result,
			fake.calls,
		)
	}
	next, err := agent.codec.open(
		"uid-pending-failed-closed",
		result.StateToken,
	)
	if err != nil {
		t.Fatalf("open fallback state: %v", err)
	}
	if next.Turn != initial.Turn+1 ||
		!reflect.DeepEqual(next.Graph, initial.Graph) ||
		!reflect.DeepEqual(next.PendingAnswer, initial.PendingAnswer) ||
		next.SelfCorrectionGrace != initial.SelfCorrectionGrace ||
		next.LastIntervention.Act != "clarify" {
		t.Fatalf("failed pending recovery changed state: got=%#v want=%#v", next, initial)
	}
}

func TestAgentReservesTimeForCriticAndSpeechResponse(t *testing.T) {
	t.Run("planner precision recovery is skipped without post budget", func(t *testing.T) {
		fake := &fakeGenerator{generations: []fakeGeneration{
			{body: "{"},
			{body: "{"},
		}}
		agent := newTestAgent(t, fake)
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		result, err := agent.Process(
			ctx,
			"uid-planner-budget",
			VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "この質問に答えてください",
			},
		)
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "planner-unavailable" ||
			result.SpokenReply != plannerUnavailableSpokenReply ||
			len(fake.calls) != 0 {
			t.Fatalf(
				"planner consumed response reserve: result=%#v calls=%#v",
				result,
				fake.calls,
			)
		}
	})

	t.Run("critic is skipped without speech reserve", func(t *testing.T) {
		plan := validModelPlan()
		fake := &fakeGenerator{generations: []fakeGeneration{{
			body: encodePlan(t, plan),
		}}}
		agent := newTestAgent(t, fake)
		ctx, cancel := context.WithTimeout(
			context.Background(),
			voiceResponseReserve+500*time.Millisecond,
		)
		defer cancel()

		result, err := agent.Process(
			ctx,
			"uid-critic-budget",
			VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "結論を一つだけ教えてください",
			},
		)
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "verification-unavailable" ||
			result.SpokenReply !=
				"回答の意味を安全に確認できませんでした。もう一度試してもらえますか？" ||
			result.StateToken == "" ||
			len(fake.calls) != 1 {
			t.Fatalf(
				"critic consumed speech reserve: result=%#v calls=%#v",
				result,
				fake.calls,
			)
		}
	})

	t.Run("pending recovery is skipped without speech reserve", func(t *testing.T) {
		fake := &fakeGenerator{generations: []fakeGeneration{{
			body:               encodePlan(t, respondentAwaitingPlan()),
			waitForContext:     true,
			returnAfterContext: true,
		}}}
		agent := newTestAgent(t, fake)
		initial := plannerRecoveryState()
		token, err := agent.codec.seal("uid-pending-budget", initial)
		if err != nil {
			t.Fatalf("seal initial state: %v", err)
		}
		ctx, cancel := context.WithTimeout(
			context.Background(),
			voiceResponseReserve+75*time.Millisecond,
		)
		defer cancel()

		result, err := agent.Process(
			ctx,
			"uid-pending-budget",
			VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     "can you explain something else?",
				StateToken:    token,
			},
		)
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.Route != "interpretation-clarify-fast" ||
			result.SpokenReply == "" ||
			len(fake.calls) != 1 {
			t.Fatalf(
				"pending recovery consumed speech reserve: result=%#v calls=%#v",
				result,
				fake.calls,
			)
		}
	})

	for _, test := range []struct {
		name      string
		domain    string
		wantRoute string
	}{
		{
			name:      "normal precision is skipped",
			domain:    "technical",
			wantRoute: "verification-unavailable",
		},
		{
			name:      "fail closed precision is skipped",
			domain:    "health",
			wantRoute: "precision-unavailable",
		},
	} {
		test := test
		t.Run(test.name+" without speech reserve", func(t *testing.T) {
			plan := validModelPlan()
			plan.Domain = test.domain
			fake := &fakeGenerator{generations: []fakeGeneration{{
				body:               encodePlan(t, plan),
				waitForContext:     true,
				returnAfterContext: true,
			}}}
			agent := newTestAgent(t, fake)
			ctx, cancel := context.WithTimeout(
				context.Background(),
				voiceResponseReserve+75*time.Millisecond,
			)
			defer cancel()

			result, err := agent.Process(
				ctx,
				"uid-precision-budget-"+test.domain,
				VoiceTurn{
					SchemaVersion: SchemaVersion,
					Utterance:     "explain the mechanism",
				},
			)
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if result.Route != test.wantRoute ||
				result.SpokenReply == "" ||
				len(fake.calls) != 1 {
				t.Fatalf(
					"precision consumed speech reserve: result=%#v calls=%#v",
					result,
					fake.calls,
				)
			}
		})
	}
}

func plannerRecoveryState() conversationState {
	return conversationState{
		Turn: 4,
		Graph: ThoughtStateGraph{
			Goals:          []string{"既存の目標"},
			Claims:         []string{"既存の主張"},
			Grounds:        []string{},
			Assumptions:    []string{},
			Constraints:    []string{},
			OpenLoops:      []string{},
			Contradictions: []string{},
			Decisions:      []string{},
		},
		PendingAnswer: PendingAnswerFrame{
			Active:        true,
			Operator:      answercontract.OperatorPurpose,
			Subject:       "導入目的",
			RequiredSlots: []answercontract.RequiredSlot{answercontract.SlotPurpose},
		},
		SelfCorrectionGrace: true,
		LastIntervention:    ArbiterDecision{Act: "clarify"},
	}
}

func TestInferenceFailureReasonsAreFiniteAndPolicyBlocksDoNotRetry(t *testing.T) {
	promptBlocked := inferenceFinishFailure(&genai.GenerateContentResponse{
		PromptFeedback: &genai.GenerateContentResponsePromptFeedback{
			BlockReason:        genai.BlockedReasonSafety,
			BlockReasonMessage: "sensitive provider detail",
		},
	})
	if !errors.Is(promptBlocked, errInferencePromptBlocked) ||
		retryableInferenceFailure(promptBlocked) ||
		precisionPlannerRecoveryAllowed(promptBlocked) ||
		inferenceFailureStage(promptBlocked) != "prompt_blocked" ||
		strings.Contains(inferenceFailureStage(promptBlocked), "sensitive") {
		t.Fatalf("prompt block was not fail-closed: %v", promptBlocked)
	}

	for _, reason := range []genai.FinishReason{
		genai.FinishReasonSafety,
		genai.FinishReasonMaxTokens,
		genai.FinishReasonRecitation,
		genai.FinishReasonLanguage,
		genai.FinishReasonOther,
		genai.FinishReasonMalformedFunctionCall,
		genai.FinishReasonUnexpectedToolCall,
		genai.FinishReasonImageSafety,
		genai.FinishReason("FUTURE_UNKNOWN_REASON"),
	} {
		err := inferenceFinishFailure(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{{FinishReason: reason}},
		})
		if err == nil ||
			retryableInferenceFailure(err) ||
			precisionPlannerRecoveryAllowed(err) {
			t.Fatalf("finish reason %q became recoverable: %v", reason, err)
		}
	}

	for _, test := range []struct {
		err   error
		stage string
	}{
		{
			err: errors.Join(
				ErrModelOutputInvalid,
				errInferenceResponseShape,
			),
			stage: "response_shape",
		},
		{
			err:   errors.Join(ErrModelOutputInvalid, errInferenceJSON),
			stage: "json",
		},
		{
			err: errors.Join(
				ErrModelOutputInvalid,
				errInferenceTrailingJSON,
			),
			stage: "trailing_json",
		},
		{
			err: errors.Join(
				ErrModelOutputInvalid,
				errInferencePlanEnvelope,
			),
			stage: "plan_envelope",
		},
		{
			err: errors.Join(
				ErrModelOutputInvalid,
				errInferenceAnswerContract,
			),
			stage: "answer_contract",
		},
	} {
		if !retryableInferenceFailure(test.err) ||
			!precisionPlannerRecoveryAllowed(test.err) ||
			inferenceFailureStage(test.err) != test.stage {
			t.Fatalf("stage %q was not allowlisted: %v", test.stage, test.err)
		}
	}
}
