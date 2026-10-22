package conversation

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestGuestTrialOverloadRetriesOnlyOnceWithinOriginalDeadline(t *testing.T) {
	for _, succeeds := range []bool{true, false} {
		agent := guestTrialAgent(t)
		calls := 0
		ctx, cancel := context.WithTimeout(context.Background(), voiceCriticTimeout)
		deadline, _ := ctx.Deadline()
		var first time.Time
		agent.generator = trialEvidenceGenerator(func(callCtx context.Context, _ string, _ []*genai.Content, _ *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
			calls++
			if d, _ := callCtx.Deadline(); !d.Equal(deadline) {
				t.Fatal("retry extended deadline")
			}
			if calls == 1 {
				first = time.Now()
			} else if time.Since(first) < time.Second {
				t.Fatal("overload retried immediately")
			}
			if succeeds && calls == 2 {
				return trialEvidenceResponse(`{"relation":"no_answer","span":""}`), nil
			}
			return nil, genai.APIError{Code: 429}
		})
		_, err := agent.generateGuestTrialEvidence(ctx, "fast", nil, nil)
		cancel()
		if calls != 2 || (err == nil) != succeeds {
			t.Fatal(calls, err)
		}
	}
}

func TestGuestTrialDoesNotRetryOtherFailuresOrInsufficientBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		budget time.Duration
	}{
		{"success", nil, voiceCriticTimeout},
		{"auth", genai.APIError{Code: 401}, voiceCriticTimeout},
		{"permission", genai.APIError{Code: 403}, voiceCriticTimeout},
		{"invalid", genai.APIError{Code: 400}, voiceCriticTimeout},
		{"unknown", errors.New("private error"), voiceCriticTimeout},
		{"short", genai.APIError{Code: 429}, time.Second},
		{"unbounded", genai.APIError{Code: 429}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := guestTrialAgent(t)
			calls := 0
			agent.generator = trialEvidenceGenerator(func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
				calls++
				return nil, tc.err
			})
			ctx := context.Background()
			if tc.budget > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.budget)
				defer cancel()
			}
			_, err := agent.generateGuestTrialEvidence(ctx, "fast", nil, nil)
			if calls != 1 || !reflect.DeepEqual(err, tc.err) {
				t.Fatal(calls, err)
			}
		})
	}
}

func TestGuestTrialCancellationInterruptsOverloadWait(t *testing.T) {
	agent := guestTrialAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), voiceCriticTimeout)
	defer cancel()
	calls := 0
	agent.generator = trialEvidenceGenerator(func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
		calls++
		time.AfterFunc(20*time.Millisecond, cancel)
		return nil, genai.APIError{Code: 429}
	})
	_, err := agent.generateGuestTrialEvidence(ctx, "fast", nil, nil)
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatal(calls, err)
	}
}
