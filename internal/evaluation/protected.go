package evaluation

import (
	"context"
	"errors"

	"github.com/furukawa1020/conclution-ai-teacher/internal/contracts"
	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
)

var (
	ErrInvalidPrivacyBoundary = errors.New("evaluation privacy boundary is invalid")
	// The provider error is deliberately suppressed because a misbehaving
	// implementation could include raw evaluation input in its error text.
	ErrEvaluationProtectionFailed = errors.New("evaluation privacy protection failed")
)

// ProtectedEvaluator is the mandatory privacy boundary in front of an
// evaluator that may send text to a managed model.
type ProtectedEvaluator struct {
	delegate  Evaluator
	protector privacyguard.Protector
}

var _ Evaluator = (*ProtectedEvaluator)(nil)

func NewProtectedEvaluator(
	delegate Evaluator,
	protector privacyguard.Protector,
) (*ProtectedEvaluator, error) {
	if delegate == nil || protector == nil {
		return nil, ErrInvalidPrivacyBoundary
	}
	return &ProtectedEvaluator{delegate: delegate, protector: protector}, nil
}

func (e *ProtectedEvaluator) Evaluate(
	ctx context.Context,
	input contracts.EvaluationInput,
) (contracts.EvaluationResult, error) {
	if e == nil || e.delegate == nil || e.protector == nil {
		return contracts.EvaluationResult{}, ErrInvalidPrivacyBoundary
	}
	question, err := e.protector.Protect(ctx, input.Question)
	if err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}
	answer, err := e.protector.Protect(ctx, input.Answer)
	if err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}
	protectedInput := input
	protectedInput.Question = question.Text
	protectedInput.Answer = answer.Text
	return e.delegate.Evaluate(ctx, protectedInput)
}
