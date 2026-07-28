package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/firebase/genkit/go/core"
	"github.com/furukawa1020/conclution-ai-teacher/internal/contracts"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
)

type fakeVerifier struct {
	principal identity.Principal
	err       error
}

func (v fakeVerifier) Verify(_ context.Context, idToken, appCheckToken string) (identity.Principal, error) {
	if idToken != "id-token" || appCheckToken != "app-check-token" {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	return v.principal, v.err
}

type fakeEvaluator struct {
	calls int
}

func (e *fakeEvaluator) Evaluate(_ context.Context, _ contracts.EvaluationInput) (contracts.EvaluationResult, error) {
	e.calls++
	return contracts.EvaluationResult{
		Answered:              true,
		EstimatedConclusion:   "実施します",
		ConclusionStartRune:   0,
		ConclusionFirst:       true,
		DirectnessScore:       92,
		FirstSentenceComplete: true,
		CalibrationScore:      90,
		PrimaryIssue:          "none",
		Feedback:              "結論が先にあります。",
		RetryInstruction:      "同じ構造を保ってください。",
		Confidence:            0.95,
		EvidenceExcerpt:       "実施します",
		ModelLogicalID:        "test-model",
		RubricVersion:         "test-rubric",
		PromptVersion:         "test-prompt",
	}, nil
}

type fakeStore struct {
	calls int
}

func (s *fakeStore) Save(
	_ context.Context,
	uid string,
	_ string,
	_ contracts.EvaluationInput,
	_ contracts.EvaluationResult,
) (string, error) {
	s.calls++
	if uid != "user-123" {
		return "", errors.New("unexpected uid")
	}
	return "attempt-123", nil
}

type fakeLimiter struct {
	calls int
	err   error
}

func (l *fakeLimiter) Consume(_ context.Context, uid string, _ time.Time) error {
	l.calls++
	if uid != "user-123" {
		return errors.New("unexpected uid")
	}
	return l.err
}

func testHandler(evaluator *fakeEvaluator, evaluationStore *fakeStore) http.Handler {
	return testHandlerWithLimiter(evaluator, evaluationStore, &fakeLimiter{})
}

func testHandlerWithLimiter(
	evaluator *fakeEvaluator,
	evaluationStore *fakeStore,
	rateLimiter *fakeLimiter,
) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return New(
		logger,
		fakeVerifier{principal: identity.Principal{
			UID:   "user-123",
			AppID: "app-123",
			Roles: map[string]bool{"user": true},
		}},
		rateLimiter,
		evaluator,
		evaluationStore,
		2*time.Second,
		4*1024,
	)
}

func authenticatedRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer id-token")
	request.Header.Set("X-Firebase-AppCheck", "app-check-token")
	return request
}

func TestIdentityHeadersAreStrictlyParsed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*http.Request)
		wantStatus int
	}{
		{
			name:       "valid lowercase scheme",
			mutate:     func(request *http.Request) { request.Header.Set("Authorization", "bearer id-token") },
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing authorization",
			mutate:     func(request *http.Request) { request.Header.Del("Authorization") },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "duplicate authorization",
			mutate: func(request *http.Request) {
				request.Header.Add("Authorization", "Bearer another-token")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "oversized app check token",
			mutate: func(request *http.Request) {
				request.Header.Set("X-Firebase-AppCheck", strings.Repeat("x", 8*1024+1))
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "extra bearer component",
			mutate:     func(request *http.Request) { request.Header.Set("Authorization", "Bearer id-token extra") },
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := testHandler(&fakeEvaluator{}, &fakeStore{})
			request := authenticatedRequest(http.MethodGet, "/api/v1/me", "")
			test.mutate(request)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d; want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestEvaluationRequiresJSONAndRejectsAmbiguousBodies(t *testing.T) {
	t.Parallel()

	validBody := `{"question":"実施しますか","answer":"実施します。理由は二つあります。","mode":"decision"}`
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantCalls   int
	}{
		{
			name:        "valid",
			contentType: "application/json; charset=utf-8",
			body:        validBody,
			wantStatus:  http.StatusCreated,
			wantCalls:   1,
		},
		{
			name:        "wrong media type",
			contentType: "text/plain",
			body:        validBody,
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "unknown field",
			contentType: "application/json",
			body:        `{"question":"Q","answer":"A","mode":"decision","uid":"victim"}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "second JSON value",
			contentType: "application/json",
			body:        validBody + `{}`,
			wantStatus:  http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evaluator := &fakeEvaluator{}
			evaluationStore := &fakeStore{}
			handler := testHandler(evaluator, evaluationStore)
			request := authenticatedRequest(http.MethodPost, "/api/v1/evaluations", test.body)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d; want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if evaluator.calls != test.wantCalls {
				t.Fatalf("evaluator calls = %d; want %d", evaluator.calls, test.wantCalls)
			}
			if evaluationStore.calls != test.wantCalls {
				t.Fatalf("store calls = %d; want %d", evaluationStore.calls, test.wantCalls)
			}
		})
	}
}

func TestCrossSiteWriteIsRejectedBeforeAuthentication(t *testing.T) {
	t.Parallel()

	handler := testHandler(&fakeEvaluator{}, &fakeStore{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/evaluations", strings.NewReader(`{}`))
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d", response.Code, http.StatusForbidden)
	}
}

func TestEvaluationRateLimitStopsBeforeModelInvocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		limiterErr error
		wantStatus int
	}{
		{
			name:       "quota exceeded",
			limiterErr: guard.ErrRateLimitExceeded,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "counter store unavailable",
			limiterErr: errors.New("firestore unavailable"),
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evaluator := &fakeEvaluator{}
			evaluationStore := &fakeStore{}
			rateLimiter := &fakeLimiter{err: test.limiterErr}
			handler := testHandlerWithLimiter(evaluator, evaluationStore, rateLimiter)
			request := authenticatedRequest(
				http.MethodPost,
				"/api/v1/evaluations",
				`{"question":"実施しますか","answer":"実施します。","mode":"decision"}`,
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d; want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if rateLimiter.calls != 1 {
				t.Fatalf("limiter calls = %d; want 1", rateLimiter.calls)
			}
			if evaluator.calls != 0 {
				t.Fatalf("evaluator calls = %d; want 0", evaluator.calls)
			}
			if evaluationStore.calls != 0 {
				t.Fatalf("store calls = %d; want 0", evaluationStore.calls)
			}
		})
	}
}

func TestUnauthenticatedEvaluationDoesNotConsumeRateLimit(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	rateLimiter := &fakeLimiter{}
	evaluator := &fakeEvaluator{}
	evaluationStore := &fakeStore{}
	handler := New(
		logger,
		fakeVerifier{err: identity.ErrUnauthenticated},
		rateLimiter,
		evaluator,
		evaluationStore,
		2*time.Second,
		4*1024,
	)
	request := authenticatedRequest(
		http.MethodPost,
		"/api/v1/evaluations",
		`{"question":"実施しますか","answer":"実施します。","mode":"decision"}`,
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want %d", response.Code, http.StatusUnauthorized)
	}
	if rateLimiter.calls != 0 {
		t.Fatalf("limiter calls = %d; want 0", rateLimiter.calls)
	}
	if evaluator.calls != 0 {
		t.Fatalf("evaluator calls = %d; want 0", evaluator.calls)
	}
}

func TestEvaluationProviderStatusIsFiniteAndDoesNotExposeMessage(t *testing.T) {
	t.Parallel()

	sensitive := "answer text must not appear"
	err := fmt.Errorf(
		"wrapped: %w",
		core.NewError(core.NOT_FOUND, "%s", sensitive),
	)
	status := evaluationProviderStatus(err)
	if status != "not_found" {
		t.Fatalf("provider status = %q; want not_found", status)
	}
	if strings.Contains(status, sensitive) {
		t.Fatal("provider status must not expose an error message")
	}
	if got := evaluationProviderStatus(errors.New(sensitive)); got != "internal" {
		t.Fatalf("plain error status = %q; want internal", got)
	}
}

func TestSecurityHeadersAreApplied(t *testing.T) {
	t.Parallel()

	handler := testHandler(&fakeEvaluator{}, &fakeStore{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	for name, want := range map[string]string{
		"Cache-Control":                "no-store",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
	} {
		if got := response.Header().Get(name); got != want {
			t.Errorf("%s = %q; want %q", name, got, want)
		}
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID is missing")
	}
}
