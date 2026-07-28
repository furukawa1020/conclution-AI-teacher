package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/contracts"
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

func testHandler(evaluator *fakeEvaluator, evaluationStore *fakeStore) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return New(
		logger,
		fakeVerifier{principal: identity.Principal{
			UID:   "user-123",
			AppID: "app-123",
			Roles: map[string]bool{"user": true},
		}},
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
