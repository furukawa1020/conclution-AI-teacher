package conversation

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"google.golang.org/genai"
)

// The complete candidate starts the independent audit, but the planner's
// terminal response is held so its tail can consume the remaining time budget.
type heldPlannerAuditGenerator struct {
	planner        string
	plannerHeld    chan struct{}
	releasePlanner chan struct{}
	criticErr      error
	holdCritic     bool
	criticCalls    atomic.Int32
}

func TestReadySpeculativeAuditPreservesAuthorityAndReserve(t *testing.T) {
	type fixture struct {
		audit  *speculativeAudit
		turn   VoiceTurn
		plan   modelPlan
		route  string
		policy criticPolicy
	}
	for _, test := range []struct {
		name       string
		edit       func(*fixture)
		budget     time.Duration
		context    string
		assessment answercontract.Outcome
		wantReady  bool
	}{
		{name: "completed keep", wantReady: true},
		{name: "completed clarify is preserved", assessment: answercontract.OutcomeClarify, wantReady: true},
		{name: "completed reject is preserved", assessment: answercontract.OutcomeReject, wantReady: true},
		{name: "exact response reserve", budget: voiceResponseReserve, wantReady: true},
		{name: "no deadline", context: "background", wantReady: true},
		{name: "below response reserve", budget: voiceResponseReserve - time.Nanosecond},
		{name: "canceled even with completed audit", context: "canceled"},
		{name: "expired even with completed audit", context: "expired"},
		{name: "nil context", context: "nil"},
		{name: "missing audit", edit: func(f *fixture) { f.audit = nil }},
		{name: "unfinished audit", edit: func(f *fixture) { f.audit.done = make(chan struct{}) }},
		{name: "failed audit", edit: func(f *fixture) { f.audit.result.err = ErrModelUnavailable }},
		{name: "changed reply", edit: func(f *fixture) { f.plan.SpokenReply += "別の回答" }},
		{name: "changed answer attempt", edit: func(f *fixture) { f.plan.AnswerAttempt = "別の回答" }},
		{name: "changed assistance target", edit: func(f *fixture) { f.plan.AssistanceTarget = "respondent" }},
		{name: "changed respondent stage", edit: func(f *fixture) { f.plan.RespondentStage = "restructure" }},
		{name: "precision route", edit: func(f *fixture) { f.route = "precision" }},
		{name: "high-risk policy", edit: func(f *fixture) { f.policy.thinkingLevel = genai.ThinkingLevelHigh }},
		{name: "different timeout policy", edit: func(f *fixture) { f.policy.timeout += time.Second }},
		{name: "research action", edit: func(f *fixture) { f.plan.ResearchAction = "search" }},
		{name: "passive ambient", edit: func(f *fixture) { f.turn.Ambient = true }},
		{name: "inline document", edit: func(f *fixture) { f.turn.PDF = &InlinePDF{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				budget := test.budget
				if budget == 0 {
					budget = voiceResponseReserve + time.Second
				}
				if test.context == "expired" {
					budget = -time.Second
				}
				ctx, cancel := context.WithTimeout(context.Background(), budget)
				defer cancel()
				switch test.context {
				case "background":
					ctx = context.Background()
				case "canceled":
					cancel()
				case "nil":
					ctx = nil
				}
				assessment := test.assessment
				if assessment == "" {
					assessment = answercontract.OutcomeKeep
				}
				plan := validModelPlan()
				turn := VoiceTurn{SchemaVersion: SchemaVersion, Utterance: "次に何をすればいいですか"}
				f := fixture{
					turn: turn, plan: plan, route: "fast", policy: criticPolicyFor(turn, plan, "fast"),
					audit: &speculativeAudit{
						candidate: plan,
						cancel:    func() {},
						done:      make(chan struct{}),
						result: speculativeAuditResult{
							assessment: answercontract.Assessment{Outcome: assessment},
						},
					},
				}
				close(f.audit.done)
				if test.edit != nil {
					test.edit(&f)
				}
				started := time.Now()
				for reader := 0; reader < 2; reader++ {
					result, ready := readySpeculativeAudit(ctx, f.audit, f.turn, f.plan, f.route, f.policy)
					if ready != test.wantReady || (ready && result.Outcome != assessment) ||
						(!ready && result.Outcome != "") {
						t.Fatalf("reader=%d ready=%v result=%+v", reader, ready, result)
					}
				}
				if !time.Now().Equal(started) {
					t.Fatal("readiness probe waited for an unfinished audit")
				}
				if test.wantReady {
					result, err := awaitSpeculativeAudit(ctx, f.audit)
					if err != nil || result.Outcome != assessment {
						t.Fatalf("readiness probe consumed the replayable result: %+v %v", result, err)
					}
				}
			})
		})
	}
}

func (generator *heldPlannerAuditGenerator) GenerateContentStream(
	ctx context.Context, _ string, _ []*genai.Content, _ *genai.GenerateContentConfig,
) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		if !yield(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{{
				Content:      genai.NewContentFromText(generator.planner, genai.RoleModel),
				FinishReason: genai.FinishReasonStop,
			}},
		}, nil) {
			return
		}
		close(generator.plannerHeld)
		select {
		case <-generator.releasePlanner:
		case <-ctx.Done():
		}
	}
}

func (generator *heldPlannerAuditGenerator) GenerateContent(
	ctx context.Context, _ string, contents []*genai.Content, _ *genai.GenerateContentConfig,
) (*genai.GenerateContentResponse, error) {
	generator.criticCalls.Add(1)
	if generator.holdCritic {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if generator.criticErr != nil {
		return nil, generator.criticErr
	}
	var prompt strings.Builder
	for _, content := range contents {
		for _, part := range content.Parts {
			prompt.WriteString(part.Text)
		}
	}
	body, err := defaultCriticBody(prompt.String())
	if err != nil {
		return nil, err
	}
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content:      genai.NewContentFromText(body, genai.RoleModel),
			FinishReason: genai.FinishReasonStop,
		}},
	}, nil
}

func TestAgentReusesCompletedAuditWithoutBudgetForAnotherCritic(t *testing.T) {
	for _, test := range []struct {
		name       string
		criticErr  error
		holdCritic bool
		wantRoute  string
	}{
		{name: "completed successful audit", wantRoute: "fast"},
		{name: "failed audit keeps fallback", criticErr: ErrModelOutputInvalid, wantRoute: "verification-unavailable"},
		{name: "unfinished audit is not awaited", holdCritic: true, wantRoute: "verification-unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), voiceResponseReserve+voiceCriticTimeout+time.Second)
				defer cancel()
				plan := validModelPlan()
				generator := &heldPlannerAuditGenerator{
					planner:        encodePlan(t, plan),
					plannerHeld:    make(chan struct{}),
					releasePlanner: make(chan struct{}),
					criticErr:      test.criticErr,
					holdCritic:     test.holdCritic,
				}
				agent := newTestAgent(t, generator)
				type outcome struct {
					result VoiceTurnResult
					err    error
				}
				done := make(chan outcome, 1)
				go func() {
					result, err := agent.Process(ctx, "ready-audit-user", VoiceTurn{
						SchemaVersion: SchemaVersion,
						Utterance:     "次に何をすればいいですか",
					})
					done <- outcome{result, err}
				}()
				<-generator.plannerHeld
				// Wait for audit completion (or its explicit provider wait), then
				// advance fake time only. No scheduler timing or real sleep is used.
				synctest.Wait()
				if generator.criticCalls.Load() != 1 {
					t.Fatalf("early critic calls=%d", generator.criticCalls.Load())
				}
				time.Sleep(2 * time.Second)
				if !contextHasTimeBudget(ctx, voiceResponseReserve) ||
					contextHasTimeBudget(ctx, voiceCriticTimeout+voiceResponseReserve) {
					t.Fatal("fixture did not reach the reserved-only budget")
				}
				finishedAt := time.Now()
				close(generator.releasePlanner)
				synctest.Wait()
				select {
				case completed := <-done:
					if completed.err != nil || completed.result.Route != test.wantRoute {
						t.Fatalf("result=%+v err=%v", completed.result, completed.err)
					}
					if test.wantRoute == "fast" && completed.result.SpokenReply != plan.SpokenReply {
						t.Fatalf("audited answer was replaced: %q", completed.result.SpokenReply)
					}
					if test.wantRoute != "fast" && completed.result.SpokenReply == plan.SpokenReply {
						t.Fatal("unverified answer was published")
					}
				default:
					t.Fatal("final response waited for a critic without a critic budget")
				}
				if generator.criticCalls.Load() != 1 || !time.Now().Equal(finishedAt) {
					t.Fatalf("extra critic call/wait: calls=%d elapsed=%v", generator.criticCalls.Load(), time.Since(finishedAt))
				}
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					t.Fatal("response consumed its reserved budget")
				}
			})
		})
	}
}
