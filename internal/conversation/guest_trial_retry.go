package conversation

import (
	"context"
	"math/rand/v2"
	"net/http"
	"time"

	"google.golang.org/genai"
)

// Only a failed, side-effect-free extraction is retried. Never replay a ledger
// mutation, successful extraction, invalid model output or authentication error.
// The enclosing verifier's 3.5s deadline is not extended. Maximum: two provider
// attempts per role, four per verification, including initial calls.
func (agent *vertexAgent) generateGuestTrialEvidence(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	response, err := agent.generator.GenerateContent(ctx, model, contents, config)
	code, _ := providerStatusCode(err)
	if err == nil || code != http.StatusTooManyRequests || ctx.Err() != nil {
		return response, err
	}
	// Provider guidance: wait at least one second. Jitter avoids synchronizing
	// the two roles or replicas. Reserve one second for the last attempt; this
	// is an admission floor, not a promise that the model will finish in it.
	delay := time.Second + time.Duration(rand.IntN(251))*time.Millisecond
	deadline, bounded := ctx.Deadline()
	if !bounded || time.Until(deadline) < delay+time.Second {
		return nil, err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return agent.generator.GenerateContent(ctx, model, contents, config)
}
