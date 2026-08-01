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
	calls int
	input contracts.EvaluationInput
}

func (s *evaluatorSpy) Evaluate(
	_ context.Context,
	input contracts.EvaluationInput,
) (contracts.EvaluationResult, error) {
	s.calls++
	s.input = input
	return contracts.EvaluationResult{ModelLogicalID: "spy"}, nil
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
	if len(protector.calls) != 2 ||
		protector.calls[0] != rawQuestion ||
		protector.calls[1] != rawAnswer {
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
}
