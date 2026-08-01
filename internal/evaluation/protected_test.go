package evaluation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/contracts"
	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
)

type evaluatorSpy struct {
	calls  int
	input  contracts.EvaluationInput
	result *contracts.EvaluationResult
	err    error
}

func (s *evaluatorSpy) Evaluate(
	_ context.Context,
	input contracts.EvaluationInput,
) (contracts.EvaluationResult, error) {
	s.calls++
	s.input = input
	if s.err != nil {
		return contracts.EvaluationResult{}, s.err
	}
	if s.result != nil {
		return *s.result, nil
	}
	return validProtectedEvaluationResult(), nil
}

func validProtectedEvaluationResult() contracts.EvaluationResult {
	return contracts.EvaluationResult{
		Answered:              true,
		ConclusionStartRune:   0,
		ConclusionFirst:       true,
		DirectnessScore:       80,
		FirstSentenceComplete: true,
		CalibrationScore:      80,
		PrimaryIssue:          "none",
		SecondaryIssues:       []string{},
		Feedback:              "safe feedback",
		RetryInstruction:      "safe retry",
		Confidence:            0.8,
		ModelLogicalID:        "spy",
		RubricVersion:         "spy-rubric",
		PromptVersion:         "spy-prompt",
	}
}

type protectorStub struct {
	protect func(string) (privacyguard.Result, error)
	calls   []string
}

func (s *protectorStub) Protect(
	_ context.Context,
	text string,
) (privacyguard.Result, error) {
	s.calls = append(s.calls, text)
	return s.protect(text)
}

func TestProtectedEvaluatorPassesOnlyProtectedQuestionAndAnswer(t *testing.T) {
	const (
		rawQuestion = "alice@example.com に連絡すべきですか"
		rawAnswer   = "はい、電話番号は090-1234-5678です"
	)
	delegate := &evaluatorSpy{}
	protector := &protectorStub{protect: func(text string) (privacyguard.Result, error) {
		switch text {
		case rawQuestion:
			return privacyguard.Result{
				Text:     "[EMAIL] に連絡すべきですか",
				Redacted: true,
			}, nil
		case rawAnswer:
			return privacyguard.Result{
				Text:     "はい、電話番号は[PHONE]です",
				Redacted: true,
			}, nil
		case "", "safe feedback", "safe retry":
			return privacyguard.Result{Text: text}, nil
		default:
			t.Fatalf("unexpected text passed to protector")
			return privacyguard.Result{}, nil
		}
	}}

	evaluator, err := NewProtectedEvaluator(delegate, protector)
	if err != nil {
		t.Fatalf("NewProtectedEvaluator() error = %v", err)
	}

	result, err := evaluator.Evaluate(context.Background(), contracts.EvaluationInput{
		Question: rawQuestion,
		Answer:   rawAnswer,
		Mode:     "daily",
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result.ModelLogicalID != "spy" {
		t.Fatalf("Evaluate() result = %#v", result)
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
	if delegate.input.Question != "[EMAIL] に連絡すべきですか" {
		t.Fatalf("delegate question = %q", delegate.input.Question)
	}
	if delegate.input.Answer != "はい、電話番号は[PHONE]です" {
		t.Fatalf("delegate answer = %q", delegate.input.Answer)
	}
	if delegate.input.Mode != "daily" {
		t.Fatalf("delegate mode = %q", delegate.input.Mode)
	}
	if !reflect.DeepEqual(protector.calls, []string{
		rawQuestion,
		rawAnswer,
		"",
		"safe feedback",
		"safe retry",
		"",
	}) {
		t.Fatalf("protector calls = %#v", protector.calls)
	}
}

func TestProtectedEvaluatorFailsClosedWithoutCallingDelegate(t *testing.T) {
	const (
		rawQuestion = "secret-question@example.com"
		rawAnswer   = "secret answer 090-0000-0000"
	)

	tests := []struct {
		name   string
		failOn string
		calls  int
	}{
		{name: "question", failOn: rawQuestion, calls: 1},
		{name: "answer", failOn: rawAnswer, calls: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delegate := &evaluatorSpy{}
			protector := &protectorStub{protect: func(text string) (privacyguard.Result, error) {
				if text == test.failOn {
					// The wrapper must suppress even a misbehaving provider error
					// which contains the raw value.
					return privacyguard.Result{}, errors.New("provider echoed: " + text)
				}
				return privacyguard.Result{Text: "[PROTECTED]", Redacted: true}, nil
			}}
			evaluator, err := NewProtectedEvaluator(delegate, protector)
			if err != nil {
				t.Fatalf("NewProtectedEvaluator() error = %v", err)
			}

			result, err := evaluator.Evaluate(context.Background(), contracts.EvaluationInput{
				Question: rawQuestion,
				Answer:   rawAnswer,
				Mode:     "daily",
			})
			if !errors.Is(err, ErrEvaluationProtectionFailed) {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if !reflect.DeepEqual(result, contracts.EvaluationResult{}) {
				t.Fatalf("Evaluate() result = %#v, want zero value", result)
			}
			if delegate.calls != 0 {
				t.Fatalf("delegate calls = %d, want 0", delegate.calls)
			}
			if len(protector.calls) != test.calls {
				t.Fatalf("protector calls = %d, want %d", len(protector.calls), test.calls)
			}
			if strings.Contains(err.Error(), rawQuestion) || strings.Contains(err.Error(), rawAnswer) {
				t.Fatalf("error leaked raw evaluation input: %q", err)
			}
		})
	}
}

func TestProtectedEvaluatorProtectsEveryFreeformResultField(t *testing.T) {
	const (
		question             = "safe question"
		answer               = "safe evidence and answer"
		conclusion           = "reviewer@example.invalid に連絡"
		feedback             = "reviewer@example.invalid を含む feedback"
		retry                = "090-1234-5678 を含む retry"
		evidence             = "safe evidence"
		protectedConclusion  = "[EMAIL] に連絡"
		protectedFeedback    = "[EMAIL] を含む feedback"
		protectedInstruction = "[PHONE] を含む retry"
	)
	delegateResult := validProtectedEvaluationResult()
	delegateResult.EstimatedConclusion = conclusion
	delegateResult.Feedback = feedback
	delegateResult.RetryInstruction = retry
	delegateResult.EvidenceExcerpt = evidence
	delegate := &evaluatorSpy{result: &delegateResult}
	protector := &protectorStub{protect: func(text string) (privacyguard.Result, error) {
		switch text {
		case question, answer, evidence:
			return privacyguard.Result{Text: text}, nil
		case conclusion:
			return privacyguard.Result{Text: protectedConclusion, Redacted: true}, nil
		case feedback:
			return privacyguard.Result{Text: protectedFeedback, Redacted: true}, nil
		case retry:
			return privacyguard.Result{Text: protectedInstruction, Redacted: true}, nil
		default:
			t.Fatalf("unexpected protector input %q", text)
			return privacyguard.Result{}, nil
		}
	}}
	evaluator, err := NewProtectedEvaluator(delegate, protector)
	if err != nil {
		t.Fatal(err)
	}

	result, err := evaluator.Evaluate(context.Background(), contracts.EvaluationInput{
		Question: question,
		Answer:   answer,
		Mode:     "daily",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EstimatedConclusion != protectedConclusion ||
		result.Feedback != protectedFeedback ||
		result.RetryInstruction != protectedInstruction ||
		result.EvidenceExcerpt != evidence {
		t.Fatalf("protected result = %#v", result)
	}
	if !reflect.DeepEqual(protector.calls, []string{
		question,
		answer,
		conclusion,
		feedback,
		retry,
		evidence,
	}) {
		t.Fatalf("protector calls = %#v", protector.calls)
	}
}

func TestProtectedEvaluatorFailsClosedOnEveryFreeformOutput(t *testing.T) {
	const (
		question   = "safe question"
		answer     = "safe evidence and answer"
		conclusion = "output conclusion"
		feedback   = "output feedback"
		retry      = "output retry"
		evidence   = "safe evidence"
	)
	tests := []struct {
		name      string
		failOn    string
		wantCalls int
	}{
		{name: "estimated conclusion", failOn: conclusion, wantCalls: 3},
		{name: "feedback", failOn: feedback, wantCalls: 4},
		{name: "retry instruction", failOn: retry, wantCalls: 5},
		{name: "evidence excerpt", failOn: evidence, wantCalls: 6},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			delegateResult := validProtectedEvaluationResult()
			delegateResult.EstimatedConclusion = conclusion
			delegateResult.Feedback = feedback
			delegateResult.RetryInstruction = retry
			delegateResult.EvidenceExcerpt = evidence
			delegate := &evaluatorSpy{result: &delegateResult}
			protector := &protectorStub{protect: func(text string) (privacyguard.Result, error) {
				if text == test.failOn {
					return privacyguard.Result{}, errors.New("provider included " + text)
				}
				return privacyguard.Result{Text: text}, nil
			}}
			evaluator, err := NewProtectedEvaluator(delegate, protector)
			if err != nil {
				t.Fatal(err)
			}

			result, err := evaluator.Evaluate(context.Background(), contracts.EvaluationInput{
				Question: question,
				Answer:   answer,
				Mode:     "daily",
			})
			if !errors.Is(err, ErrEvaluationProtectionFailed) ||
				!reflect.DeepEqual(result, contracts.EvaluationResult{}) {
				t.Fatalf("error = %v, result = %#v", err, result)
			}
			if delegate.calls != 1 || len(protector.calls) != test.wantCalls {
				t.Fatalf(
					"delegate calls = %d, protector calls = %#v",
					delegate.calls,
					protector.calls,
				)
			}
			if strings.Contains(err.Error(), test.failOn) {
				t.Fatalf("error leaked model output: %q", err)
			}
		})
	}
}

func TestProtectedEvaluatorRevalidatesProtectedOutput(t *testing.T) {
	delegateResult := validProtectedEvaluationResult()
	delegateResult.EvidenceExcerpt = "safe evidence"
	delegate := &evaluatorSpy{result: &delegateResult}
	protector := &protectorStub{protect: func(text string) (privacyguard.Result, error) {
		if text == delegateResult.EvidenceExcerpt {
			return privacyguard.Result{Text: "invented excerpt", Redacted: true}, nil
		}
		return privacyguard.Result{Text: text}, nil
	}}
	evaluator, err := NewProtectedEvaluator(delegate, protector)
	if err != nil {
		t.Fatal(err)
	}

	result, err := evaluator.Evaluate(context.Background(), contracts.EvaluationInput{
		Question: "safe question",
		Answer:   "safe evidence and answer",
		Mode:     "daily",
	})
	if !errors.Is(err, ErrEvaluationProtectionFailed) ||
		!reflect.DeepEqual(result, contracts.EvaluationResult{}) {
		t.Fatalf("error = %v, result = %#v", err, result)
	}
}

func TestProtectedEvaluatorSuppressesDelegateErrorContent(t *testing.T) {
	const sensitiveErrorContent = "provider echoed protected answer"
	delegate := &evaluatorSpy{err: errors.New(sensitiveErrorContent)}
	protector := &protectorStub{protect: func(text string) (privacyguard.Result, error) {
		return privacyguard.Result{Text: text}, nil
	}}
	evaluator, err := NewProtectedEvaluator(delegate, protector)
	if err != nil {
		t.Fatal(err)
	}

	result, err := evaluator.Evaluate(context.Background(), contracts.EvaluationInput{
		Question: "safe question",
		Answer:   "safe answer",
		Mode:     "daily",
	})
	if !errors.Is(err, ErrEvaluationProtectionFailed) ||
		!reflect.DeepEqual(result, contracts.EvaluationResult{}) {
		t.Fatalf("error = %v, result = %#v", err, result)
	}
	if strings.Contains(err.Error(), sensitiveErrorContent) {
		t.Fatalf("delegate error content leaked: %q", err)
	}
}

func TestNewProtectedEvaluatorRequiresBothDependencies(t *testing.T) {
	protector := &protectorStub{protect: func(text string) (privacyguard.Result, error) {
		return privacyguard.Result{Text: text}, nil
	}}
	delegate := &evaluatorSpy{}

	if _, err := NewProtectedEvaluator(nil, protector); !errors.Is(err, ErrInvalidPrivacyBoundary) {
		t.Fatalf("nil delegate error = %v", err)
	}
	if _, err := NewProtectedEvaluator(delegate, nil); !errors.Is(err, ErrInvalidPrivacyBoundary) {
		t.Fatalf("nil protector error = %v", err)
	}
	evaluator, err := NewProtectedEvaluator(delegate, protector)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.Evaluate(nil, contracts.EvaluationInput{}); !errors.Is(err, ErrInvalidPrivacyBoundary) {
		t.Fatalf("nil context error = %v", err)
	}
}
