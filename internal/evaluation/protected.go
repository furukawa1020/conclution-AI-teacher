package evaluation

import (
	"context"
	"errors"
	"strings"

	"github.com/furukawa1020/conclution-ai-teacher/internal/contracts"
	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
)

var (
	// ErrInvalidPrivacyBoundary means the protected evaluator was assembled
	// without one of its mandatory fail-closed dependencies.
	ErrInvalidPrivacyBoundary = errors.New("evaluation privacy boundary is invalid")
	// ErrEvaluationProtectionFailed deliberately omits the provider error. A
	// Protector implementation must never be able to echo evaluation input into
	// an HTTP response or application log through this error path.
	ErrEvaluationProtectionFailed = errors.New("evaluation privacy protection failed")
)

// ProtectedEvaluator is the mandatory privacy boundary in front of an
// evaluator that may send text to a managed model. It never falls back to the
// original Question or Answer when protection fails.
type ProtectedEvaluator struct {
	delegate  Evaluator
	protector privacyguard.Protector
}

var _ Evaluator = (*ProtectedEvaluator)(nil)

// NewProtectedEvaluator wraps delegate with fail-closed text protection.
func NewProtectedEvaluator(
	delegate Evaluator,
	protector privacyguard.Protector,
) (*ProtectedEvaluator, error) {
	if delegate == nil || protector == nil {
		return nil, ErrInvalidPrivacyBoundary
	}
	return &ProtectedEvaluator{
		delegate:  delegate,
		protector: protector,
	}, nil
}

// Evaluate protects Question and Answer independently, then gives the
// delegate only the returned protected text. Provider errors are intentionally
// replaced with a stable error that cannot contain raw user input.
func (e *ProtectedEvaluator) Evaluate(
	ctx context.Context,
	input contracts.EvaluationInput,
) (contracts.EvaluationResult, error) {
	if ctx == nil || e == nil || e.delegate == nil || e.protector == nil {
		return contracts.EvaluationResult{}, ErrInvalidPrivacyBoundary
	}
	question, err := e.protectText(ctx, input.Question)
	if err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}
	answer, err := e.protectText(ctx, input.Answer)
	if err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}
	protectedInput := input
	protectedInput.Question = question
	protectedInput.Answer = answer
	if err := protectedInput.Validate(); err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}

	result, err := e.delegate.Evaluate(ctx, protectedInput)
	if err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}
	// Do not trust a delegate implementation to keep output bounded or keep its
	// evidence excerpt inside the already-protected answer.
	if err := result.Validate(protectedInput.Answer); err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}

	protectedResult := result
	protectedResult.EstimatedConclusion, err = e.protectText(
		ctx,
		result.EstimatedConclusion,
	)
	if err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}
	protectedResult.Feedback, err = e.protectText(ctx, result.Feedback)
	if err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}
	protectedResult.RetryInstruction, err = e.protectText(
		ctx,
		result.RetryInstruction,
	)
	if err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}
	protectedResult.EvidenceExcerpt, err = e.protectText(
		ctx,
		result.EvidenceExcerpt,
	)
	if err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}
	if err := protectedResult.Validate(protectedInput.Answer); err != nil {
		return contracts.EvaluationResult{}, ErrEvaluationProtectionFailed
	}
	return protectedResult, nil
}

func (e *ProtectedEvaluator) protectText(
	ctx context.Context,
	text string,
) (string, error) {
	protected, err := e.protector.Protect(ctx, text)
	if err != nil {
		return "", ErrEvaluationProtectionFailed
	}
	if text == "" {
		if protected.Text != "" {
			return "", ErrEvaluationProtectionFailed
		}
		return "", nil
	}
	if strings.TrimSpace(protected.Text) == "" {
		return "", ErrEvaluationProtectionFailed
	}
	return protected.Text, nil
}
