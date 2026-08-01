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

func (spy *evaluatorSpy) Evaluate(
	_ context.Context,
	input contracts.EvaluationInput,
) (contracts.EvaluationResult, error) {
	spy.calls++
	spy.input = input
	return contracts.EvaluationResult{ModelLogicalID: "spy"}, nil
}

type protectorStub struct {
	protect func(string) (privacyguard.Result, error)
	calls   []string
}

func (stub *protectorStub) Protect(
	_ context.Context,
	text string,
) (privacyguard.Result, error) {
	stub.calls = append(stub.calls, text)
	return stub.protect(text)
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
			return privacyguard.Result{Text: "[EMAIL] に連絡すべきですか", Redacted: true}, nil
		case rawAnswer:
			return privacyguard.Result{Text: "はい、電話番号は[PHONE]です", Redacted: true}, nil
		default:
			t.Fatalf("unexpected protector input")
			return privacyguard.Result{}, errors.New("unexpected input")
		}
	}}
	evaluator, err := NewProtectedEvaluator(delegate, protector)
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.Evaluate(context.Background(), contracts.EvaluationInput{
		Question: rawQuestion,
		Answer:   rawAnswer,
		Mode:     "daily",
	})
	if err != nil || result.ModelLogicalID != "spy" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if delegate.calls != 1 ||
		delegate.input.Question != "[EMAIL] に連絡すべきですか" ||
		delegate.input.Answer != "はい、電話番号は[PHONE]です" ||
		delegate.input.Mode != "daily" {
		t.Fatalf("delegate calls=%d input=%+v", delegate.calls, delegate.input)
	}
}

func TestProtectedEvaluatorFailsClosedWithoutCallingDelegate(t *testing.T) {
	const (
		rawQuestion = "secret-question@example.com"
		rawAnswer   = "secret answer 090-0000-0000"
	)
	for _, test := range []struct {
		name   string
		failOn string
		calls  int
	}{
		{name: "question", failOn: rawQuestion, calls: 1},
		{name: "answer", failOn: rawAnswer, calls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			delegate := &evaluatorSpy{}
			protector := &protectorStub{protect: func(text string) (privacyguard.Result, error) {
				if text == test.failOn {
					return privacyguard.Result{}, errors.New("provider echoed: " + text)
				}
				return privacyguard.Result{Text: "[PROTECTED]", Redacted: true}, nil
			}}
			evaluator, err := NewProtectedEvaluator(delegate, protector)
			if err != nil {
				t.Fatal(err)
			}
			result, err := evaluator.Evaluate(context.Background(), contracts.EvaluationInput{
				Question: rawQuestion,
				Answer:   rawAnswer,
				Mode:     "daily",
			})
			if !errors.Is(err, ErrEvaluationProtectionFailed) ||
				!reflect.DeepEqual(result, contracts.EvaluationResult{}) ||
				delegate.calls != 0 || len(protector.calls) != test.calls {
				t.Fatalf("result=%+v error=%v delegate=%d protector=%d", result, err, delegate.calls, len(protector.calls))
			}
			if strings.Contains(err.Error(), rawQuestion) || strings.Contains(err.Error(), rawAnswer) {
				t.Fatalf("error leaked raw input: %q", err)
			}
		})
	}
}

func TestNewProtectedEvaluatorRequiresDependencies(t *testing.T) {
	protector := &protectorStub{protect: func(text string) (privacyguard.Result, error) {
		return privacyguard.Result{Text: text}, nil
	}}
	delegate := &evaluatorSpy{}
	if _, err := NewProtectedEvaluator(nil, protector); !errors.Is(err, ErrInvalidPrivacyBoundary) {
		t.Fatalf("nil delegate error=%v", err)
	}
	if _, err := NewProtectedEvaluator(delegate, nil); !errors.Is(err, ErrInvalidPrivacyBoundary) {
		t.Fatalf("nil protector error=%v", err)
	}
}
