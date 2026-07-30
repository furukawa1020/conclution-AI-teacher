package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
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
	calls   int
	err     error
	wantKey string
	keys    []string
}

type fakeVoiceService struct {
	calls  int
	input  VoiceTurnInput
	result VoiceTurnResult
	err    error
}

func (s *fakeVoiceService) Process(
	_ context.Context,
	uid string,
	input VoiceTurnInput,
) (VoiceTurnResult, error) {
	s.calls++
	if uid != "user-123" {
		return VoiceTurnResult{}, errors.New("unexpected uid")
	}
	s.input = input
	return s.result, s.err
}

func (l *fakeLimiter) Consume(_ context.Context, uid string, _ time.Time) error {
	l.calls++
	l.keys = append(l.keys, uid)
	wantKey := l.wantKey
	if wantKey == "" {
		wantKey = "user-123"
	}
	if uid != wantKey {
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
	request.Header.Set("Origin", allowedWebOrigin)
	return request
}

func testCrossrefResearchRecord() ResearchRecord {
	return ResearchRecord{
		Title:     "KOTAEの回答支援に関する研究",
		DOI:       "10.1234/kotae.2026",
		URL:       "https://doi.org/10.1234/kotae.2026",
		Published: "2026-07-29",
		Source:    "Crossref",
	}
}

func testVoiceHandler(service *fakeVoiceService, limiter *fakeLimiter) http.Handler {
	return testVoiceHandlerWithAppLimiter(
		service,
		limiter,
		&fakeLimiter{wantKey: "app:app-123"},
	)
}

func testVoiceHandlerWithAppLimiter(
	service *fakeVoiceService,
	limiter *fakeLimiter,
	appLimiter *fakeLimiter,
) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewWithVoice(
		logger,
		fakeVerifier{principal: identity.Principal{
			UID:   "user-123",
			AppID: "app-123",
			Roles: map[string]bool{"user": true},
		}},
		&fakeLimiter{},
		&fakeEvaluator{},
		&fakeStore{},
		2*time.Second,
		4*1024,
		VoiceOptions{
			Service:         service,
			RateLimiter:     limiter,
			AppRateLimiter:  appLimiter,
			RequestTimeout:  2 * time.Second,
			MaxRequestBytes: 13 * 1024 * 1024,
		},
	)
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

func TestWriteOriginMustExactlyMatchFirebaseHosting(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		origin string
		omit   bool
	}{
		{name: "missing", omit: true},
		{name: "null", origin: "null"},
		{name: "attacker", origin: "https://attacker.example"},
		{name: "lookalike", origin: "https://kotae-ai.web.app.attacker.example"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			handler := testHandler(&fakeEvaluator{}, &fakeStore{})
			request := authenticatedRequest(
				http.MethodPost,
				"/api/v1/evaluations",
				`{"question":"実施しますか","answer":"実施します。","mode":"decision"}`,
			)
			request.Header.Set("Content-Type", "application/json")
			if test.omit {
				request.Header.Del("Origin")
			} else {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d; want %d", response.Code, http.StatusForbidden)
			}
		})
	}

	handler := testHandler(&fakeEvaluator{}, &fakeStore{})
	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		healthResponse,
		httptest.NewRequest(http.MethodGet, "/healthz", nil),
	)
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("GET health without Origin = %d", healthResponse.Code)
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

func TestVoiceTurnAcceptsOnlyAttestedBoundedAudio(t *testing.T) {
	t.Parallel()

	service := &fakeVoiceService{
		result: VoiceTurnResult{
			Audio:            []byte("mp3"),
			AudioMIMEType:    "audio/mpeg",
			Caption:          "音声と同じ最終回答です。",
			StateToken:       "opaque-encrypted-state",
			DetectedDomain:   "casual",
			AssistanceTarget: "respondent",
			RespondentStage:  "restructure",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "fast",
		},
	}
	limiter := &fakeLimiter{}
	handler := testVoiceHandler(service, limiter)
	audio := []byte("RIFF-safe-test-audio")
	body := fmt.Sprintf(
		`{"audioBase64":%q,"mimeType":"audio/wav","sessionState":"","turnMode":"intentional"}`,
		base64.StdEncoding.EncodeToString(audio),
	)
	request := authenticatedRequest(http.MethodPost, "/api/v1/voice/turns", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d; body = %s", response.Code, http.StatusCreated, response.Body.String())
	}
	if service.calls != 1 || limiter.calls != 1 {
		t.Fatalf("service calls = %d, limiter calls = %d; want 1, 1", service.calls, limiter.calls)
	}
	if service.input.MIMEType != "audio/wav" ||
		service.input.TurnMode != VoiceTurnIntentional ||
		service.input.Ambient {
		t.Fatalf("voice input = %+v", service.input)
	}
	if strings.Contains(response.Body.String(), "RIFF-safe-test-audio") {
		t.Fatal("response exposed input audio")
	}
	if !strings.Contains(response.Body.String(), `"caption":"音声と同じ最終回答です。"`) {
		t.Fatalf("response caption = %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"assistanceTarget":"respondent"`) ||
		!strings.Contains(response.Body.String(), `"respondentStage":"restructure"`) {
		t.Fatalf("response assistance metadata = %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"researchStatus":"none"`) ||
		!strings.Contains(response.Body.String(), `"researchRecords":[]`) {
		t.Fatalf("response research metadata = %s", response.Body.String())
	}
}

func TestVoiceTurnFailureLogsOnlyFinitePipelineStage(t *testing.T) {
	t.Parallel()

	const (
		privateProviderText = "provider-response-SECRET"
		privateTranscript   = "田中さんの診療記録"
		privateState        = "STATE-SECRET"
	)
	service := &fakeVoiceService{
		err: fmt.Errorf(
			"%s %s: %w",
			privateProviderText,
			privateTranscript,
			NewVoicePipelineFailure(VoicePipelineStageConversation),
		),
	}
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
	handler := NewWithVoice(
		logger,
		fakeVerifier{principal: identity.Principal{
			UID:   "user-123",
			AppID: "app-123",
			Roles: map[string]bool{"user": true},
		}},
		&fakeLimiter{},
		&fakeEvaluator{},
		&fakeStore{},
		2*time.Second,
		4*1024,
		VoiceOptions{
			Service:         service,
			RateLimiter:     &fakeLimiter{},
			AppRateLimiter:  &fakeLimiter{wantKey: "app:app-123"},
			RequestTimeout:  2 * time.Second,
			MaxRequestBytes: 13 * 1024 * 1024,
		},
	)
	body := fmt.Sprintf(
		`{"audioBase64":%q,"mimeType":"audio/wav","sessionState":%q,"turnMode":"intentional"}`,
		base64.StdEncoding.EncodeToString([]byte("AUDIO-SECRET")),
		privateState,
	)
	request := authenticatedRequest(http.MethodPost, "/api/v1/voice/turns", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	logged := logOutput.String()
	if !strings.Contains(logged, `"pipeline_stage":"conversation"`) {
		t.Fatalf("finite pipeline stage missing from log: %s", logged)
	}
	for _, forbidden := range []string{
		privateProviderText,
		privateTranscript,
		privateState,
		"AUDIO-SECRET",
		"user-123",
		"app-123",
	} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("voice failure log exposed private content %q: %s", forbidden, logged)
		}
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("voice failure response exposed private content %q", forbidden)
		}
	}
}

func TestVoiceTurnReturnsValidatedResearchMetadata(t *testing.T) {
	t.Parallel()

	record := testCrossrefResearchRecord()
	service := &fakeVoiceService{
		result: VoiceTurnResult{
			Audio:            []byte("mp3"),
			AudioMIMEType:    "audio/mpeg",
			Caption:          "候補を1件見つけました。内容の検証には一次資料が必要です。",
			StateToken:       "opaque-encrypted-state",
			DetectedDomain:   "research",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			ResearchStatus:   "needs_primary_evidence",
			ResearchRecords:  []ResearchRecord{record},
			Route:            "research-metadata",
		},
	}
	handler := testVoiceHandler(service, &fakeLimiter{})
	request := authenticatedRequest(
		http.MethodPost,
		"/api/v1/voice/turns",
		`{"audioBase64":"YXVkaW8=","mimeType":"audio/webm","sessionState":"","turnMode":"intentional"}`,
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		`"researchStatus":"needs_primary_evidence"`,
		`"researchRecords":[{`,
		`"doi":"10.1234/kotae.2026"`,
		`"url":"https://doi.org/10.1234/kotae.2026"`,
		`"source":"Crossref"`,
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("research response missing %s: %s", expected, response.Body.String())
		}
	}
}

func TestMaximumPublishedVoicePayloadFitsThirteenMiBEnvelope(t *testing.T) {
	t.Parallel()

	const (
		jsonAndMaximumNameAllowance = 2 * 1024
		twelveMiB                   = 12 * 1024 * 1024
		thirteenMiB                 = 13 * 1024 * 1024
	)
	maximumEncodedRequest := base64.StdEncoding.EncodedLen(maxAudioBytes) +
		base64.StdEncoding.EncodedLen(maxDocumentBytes) +
		maxStateBytes +
		jsonAndMaximumNameAllowance
	if maximumEncodedRequest <= twelveMiB {
		t.Fatalf(
			"test no longer covers the historical 12 MiB overflow: %d",
			maximumEncodedRequest,
		)
	}
	if maximumEncodedRequest >= thirteenMiB {
		t.Fatalf(
			"maximum published payload %d does not fit 13 MiB envelope",
			maximumEncodedRequest,
		)
	}
}

func TestVoiceTurnConsumesUIDAndAppQuotaBeforeBodyDecode(t *testing.T) {
	t.Parallel()

	service := &fakeVoiceService{}
	uidLimiter := &fakeLimiter{}
	appLimiter := &fakeLimiter{wantKey: "app:app-123"}
	handler := testVoiceHandlerWithAppLimiter(service, uidLimiter, appLimiter)
	request := authenticatedRequest(
		http.MethodPost,
		"/api/v1/voice/turns",
		`{"audioBase64":"***","mimeType":"audio/webm","sessionState":"","turnMode":"intentional"}`,
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity ||
		service.calls != 0 ||
		uidLimiter.calls != 1 ||
		appLimiter.calls != 1 {
		t.Fatalf(
			"status=%d service=%d uid_quota=%d app_quota=%d body=%s",
			response.Code,
			service.calls,
			uidLimiter.calls,
			appLimiter.calls,
			response.Body.String(),
		)
	}
}

func TestVoiceAppQuotaStopsBeforeDecodeAndService(t *testing.T) {
	t.Parallel()

	service := &fakeVoiceService{}
	uidLimiter := &fakeLimiter{}
	appLimiter := &fakeLimiter{
		wantKey: "app:app-123",
		err:     guard.ErrRateLimitExceeded,
	}
	handler := testVoiceHandlerWithAppLimiter(service, uidLimiter, appLimiter)
	request := authenticatedRequest(
		http.MethodPost,
		"/api/v1/voice/turns",
		`not-json-and-must-not-be-decoded`,
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests ||
		service.calls != 0 ||
		uidLimiter.calls != 1 ||
		appLimiter.calls != 1 {
		t.Fatalf(
			"status=%d service=%d uid_quota=%d app_quota=%d",
			response.Code,
			service.calls,
			uidLimiter.calls,
			appLimiter.calls,
		)
	}
}

func TestVoiceTurnModeIsExplicitAndIndependentOfState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mode      VoiceTurnMode
		state     string
		wantError bool
		ambient   bool
	}{
		{
			name:  "intentional with state remains intentional",
			mode:  VoiceTurnIntentional,
			state: "v1.opaque-bound-state",
		},
		{
			name:    "ambient can be first turn",
			mode:    VoiceTurnAmbient,
			ambient: true,
		},
		{
			name:      "missing mode",
			wantError: true,
		},
		{
			name:      "unknown mode",
			mode:      VoiceTurnMode("background"),
			wantError: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := voiceTurnRequest{
				AudioBase64:  "YXVkaW8=",
				MIMEType:     "audio/webm;codecs=opus",
				SessionState: test.state,
				TurnMode:     test.mode,
			}
			input, err := decodeVoiceTurn(request)
			if test.wantError {
				if err == nil {
					clearVoiceInput(&input)
					t.Fatal("decode succeeded; want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer clearVoiceInput(&input)
			if input.Ambient != test.ambient || input.TurnMode != test.mode {
				t.Fatalf("voice mode = %+v", input)
			}
		})
	}
}

func TestVoiceStateLimitMatchesConversationAndClientBoundary(t *testing.T) {
	t.Parallel()

	if maxStateBytes != 16*1024 {
		t.Fatalf("state cap = %d; want 16 KiB", maxStateBytes)
	}
	request := voiceTurnRequest{
		AudioBase64:  "YXVkaW8=",
		MIMEType:     "audio/webm",
		SessionState: strings.Repeat("x", maxStateBytes+1),
		TurnMode:     VoiceTurnIntentional,
	}
	input, err := decodeVoiceTurn(request)
	if err == nil {
		clearVoiceInput(&input)
		t.Fatal("oversized state token was accepted")
	}
}

func TestVoiceTurnConsumesQuotaBeforeDecodingMalformedPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "invalid base64",
			body: `{"audioBase64":"***","mimeType":"audio/webm","sessionState":"","turnMode":"intentional"}`,
		},
		{
			name: "unsupported audio type",
			body: `{"audioBase64":"YXVkaW8=","mimeType":"text/plain","sessionState":"","turnMode":"intentional"}`,
		},
		{
			name: "non PDF document",
			body: `{"audioBase64":"YXVkaW8=","mimeType":"audio/webm","sessionState":"","turnMode":"intentional","document":{"base64":"YQ==","mimeType":"text/plain","name":"paper.txt"}}`,
		},
		{
			name: "PDF MIME with invalid magic",
			body: `{"audioBase64":"YXVkaW8=","mimeType":"audio/webm","sessionState":"","turnMode":"intentional","document":{"base64":"bm90LWEtcGRm","mimeType":"application/pdf","name":"paper.pdf"}}`,
		},
		{
			name: "unknown field",
			body: `{"audioBase64":"YXVkaW8=","mimeType":"audio/webm","sessionState":"","turnMode":"intentional","transcript":"secret"}`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := &fakeVoiceService{}
			limiter := &fakeLimiter{}
			handler := testVoiceHandler(service, limiter)
			request := authenticatedRequest(http.MethodPost, "/api/v1/voice/turns", test.body)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest &&
				response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
			}
			if service.calls != 0 || limiter.calls != 1 {
				t.Fatalf("service calls = %d, limiter calls = %d; want 0, 1", service.calls, limiter.calls)
			}
		})
	}
}

func TestVoiceTurnAllowsDeliberateSilence(t *testing.T) {
	t.Parallel()

	service := &fakeVoiceService{
		result: VoiceTurnResult{
			StateToken:       "opaque-encrypted-state",
			DetectedDomain:   "planning",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "silent",
		},
	}
	handler := testVoiceHandler(service, &fakeLimiter{})
	request := authenticatedRequest(
		http.MethodPost,
		"/api/v1/voice/turns",
		`{"audioBase64":"YXVkaW8=","mimeType":"audio/webm;codecs=opus","sessionState":"","turnMode":"ambient"}`,
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"audioBase64":""`) {
		t.Fatalf("silent response = %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"caption":null`) {
		t.Fatalf("silent caption must be null: %s", response.Body.String())
	}
}

func TestVoiceResultCaptionMustMatchSpokenShapeAndStayBounded(t *testing.T) {
	t.Parallel()

	base := VoiceTurnResult{
		Audio:            []byte("audio"),
		AudioMIMEType:    "audio/mpeg",
		Caption:          "最終回答",
		StateToken:       "state",
		DetectedDomain:   "general",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		ResearchStatus:   "none",
		ResearchRecords:  []ResearchRecord{},
		Route:            "fast",
	}
	if err := validateVoiceResult(base); err != nil {
		t.Fatalf("valid spoken result: %v", err)
	}
	for _, mutate := range []func(*VoiceTurnResult){
		func(result *VoiceTurnResult) { result.Caption = "" },
		func(result *VoiceTurnResult) { result.Caption = strings.Repeat("あ", maxCaptionRunes+1) },
		func(result *VoiceTurnResult) { result.AssistanceTarget = "operator" },
		func(result *VoiceTurnResult) { result.RespondentStage = "drafting" },
		func(result *VoiceTurnResult) {
			result.Audio = nil
			result.AudioMIMEType = ""
		},
	} {
		result := base
		mutate(&result)
		if err := validateVoiceResult(result); err == nil {
			t.Fatalf("unsafe caption accepted: %#v", result)
		}
	}
}

func TestVoiceResultRejectsInconsistentAssistanceMetadata(t *testing.T) {
	t.Parallel()

	base := VoiceTurnResult{
		Audio:            []byte("audio"),
		AudioMIMEType:    "audio/mpeg",
		Caption:          "回答です。",
		StateToken:       "state",
		DetectedDomain:   "general",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		ResearchStatus:   "none",
		ResearchRecords:  []ResearchRecord{},
		Route:            "fast",
	}
	tests := []struct {
		name   string
		mutate func(*VoiceTurnResult)
	}{
		{
			name: "assistant cannot use respondent restructure stage",
			mutate: func(result *VoiceTurnResult) {
				result.RespondentStage = "restructure"
			},
		},
		{
			name: "respondent requires a respondent stage",
			mutate: func(result *VoiceTurnResult) {
				result.AssistanceTarget = "respondent"
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := base
			test.mutate(&result)
			if err := validateVoiceResult(result); err == nil {
				t.Fatalf("inconsistent assistance metadata accepted: %+v", result)
			}
		})
	}
}

func TestVoiceResultValidatesCrossrefResearchRecords(t *testing.T) {
	t.Parallel()

	record := testCrossrefResearchRecord()
	base := VoiceTurnResult{
		Audio:            []byte("audio"),
		AudioMIMEType:    "audio/mpeg",
		Caption:          "一次資料の確認が必要です。",
		StateToken:       "state",
		DetectedDomain:   "research",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		ResearchStatus:   "needs_primary_evidence",
		ResearchRecords:  []ResearchRecord{record},
		Route:            "research-metadata",
	}
	if err := validateVoiceResult(base); err != nil {
		t.Fatalf("valid Crossref record rejected: %v", err)
	}

	for _, status := range []string{"none", "unavailable"} {
		status := status
		t.Run("records rejected with status "+status, func(t *testing.T) {
			t.Parallel()
			result := base
			result.ResearchStatus = status
			if err := validateVoiceResult(result); err == nil {
				t.Fatalf("records accepted with research status %q", status)
			}
		})
	}

	tests := []struct {
		name   string
		mutate func(*ResearchRecord)
	}{
		{
			name: "malformed DOI",
			mutate: func(record *ResearchRecord) {
				record.DOI = "10.1234/contains space"
			},
		},
		{
			name: "non DOI URL",
			mutate: func(record *ResearchRecord) {
				record.URL = "https://example.com/10.1234/kotae.2026"
			},
		},
		{
			name: "mismatched DOI URL",
			mutate: func(record *ResearchRecord) {
				record.URL = "https://doi.org/10.1234/different"
			},
		},
		{
			name: "HTTP DOI URL is not canonical",
			mutate: func(record *ResearchRecord) {
				record.URL = "http://doi.org/10.1234/kotae.2026"
			},
		},
		{
			name: "legacy DOI host is not canonical",
			mutate: func(record *ResearchRecord) {
				record.URL = "https://dx.doi.org/10.1234/kotae.2026"
			},
		},
		{
			name: "invalid publication date",
			mutate: func(record *ResearchRecord) {
				record.Published = "2026-99-99"
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := base
			malformed := record
			test.mutate(&malformed)
			result.ResearchRecords = []ResearchRecord{malformed}
			if err := validateVoiceResult(result); err == nil {
				t.Fatalf("malformed research record accepted: %+v", malformed)
			}
		})
	}
	nilRecords := base
	nilRecords.ResearchStatus = "none"
	nilRecords.ResearchRecords = nil
	if err := validateVoiceResult(nilRecords); err == nil {
		t.Fatal("nil research records accepted; JSON would serialize null")
	}
}

func TestPreInferenceRecognitionFallbackMayKeepStateEmpty(t *testing.T) {
	t.Parallel()

	for _, result := range []VoiceTurnResult{
		{
			DetectedDomain:   "unknown",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "stt-silent",
		},
		{
			Audio:            []byte("audio"),
			AudioMIMEType:    "audio/mpeg",
			Caption:          "もう一度話してもらえますか？",
			DetectedDomain:   "unknown",
			AssistanceTarget: "assistant",
			RespondentStage:  "none",
			ResearchStatus:   "none",
			ResearchRecords:  []ResearchRecord{},
			Route:            "stt-clarify",
		},
	} {
		if err := validateVoiceResult(result); err != nil {
			t.Fatalf("safe pre-inference fallback rejected: %+v: %v", result, err)
		}
	}
	invalidFallback := VoiceTurnResult{
		DetectedDomain:   "unknown",
		AssistanceTarget: "untrusted",
		RespondentStage:  "none",
		ResearchStatus:   "none",
		ResearchRecords:  []ResearchRecord{},
		Route:            "stt-silent",
	}
	if err := validateVoiceResult(invalidFallback); err == nil {
		t.Fatal("STT fallback bypassed assistance enum validation")
	}
	normal := VoiceTurnResult{
		DetectedDomain:   "general",
		AssistanceTarget: "assistant",
		RespondentStage:  "none",
		ResearchStatus:   "none",
		ResearchRecords:  []ResearchRecord{},
		Route:            "fast",
	}
	if err := validateVoiceResult(normal); err == nil {
		t.Fatal("normal model result without encrypted state was accepted")
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
