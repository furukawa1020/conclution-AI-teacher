package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/longmemory"
)

type fakeLongTermMemory struct {
	statusCalls  int
	enableCalls  int
	disableCalls int
	uid          string
}

type fakeLongTermMemoryContext struct {
	calls      int
	uid        string
	appID      string
	capability string
	available  bool
	err        error
}

func (f *fakeLongTermMemoryContext) BeginContext(_ context.Context, uid, appID string) (string, bool, error) {
	f.calls++
	f.uid = uid
	f.appID = appID
	return f.capability, f.available, f.err
}

type fakeLongTermMemoryQueue struct {
	calls atomic.Int32
	uid   string
	token string
}

type commitCheckingMemoryQueue struct {
	response  *httptest.ResponseRecorder
	committed bool
}

func (q *commitCheckingMemoryQueue) Enqueue(string, string) bool {
	q.committed = q.response.Code == http.StatusCreated &&
		strings.Contains(q.response.Body.String(), `"sessionState":"opaque-state"`)
	return true
}

func (f *fakeLongTermMemoryQueue) Enqueue(uid string, token string) bool {
	f.calls.Add(1)
	f.uid = uid
	f.token = token
	return true
}

func TestLongTermMemoryQueueAcceptsOnlyVerifiedPasskeyPrincipal(t *testing.T) {
	queue := &fakeLongTermMemoryQueue{}
	server := &Server{voice: VoiceOptions{LongTermMemoryQueue: queue}}
	verified := identity.Principal{UID: "account-uid", AppID: "app-123", Provider: "custom", AuthMethod: "passkey-v1", AccountVerified: true}
	server.enqueueLongTermMemory(verified, VoiceTurnResult{StateToken: "opaque-state"})
	if queue.calls.Load() != 1 || queue.uid != verified.UID || queue.token != "opaque-state" {
		t.Fatalf("verified enqueue=%d uid=%q token=%q", queue.calls.Load(), queue.uid, queue.token)
	}
	guestsAndInvalid := []identity.Principal{
		{UID: "guest", AppID: "app-123", Provider: "anonymous", AuthMethod: "guest-v1"},
		{UID: "account-uid", AppID: "app-123", Provider: "custom", AuthMethod: "passkey-v1"},
		{UID: "account-uid", AppID: "app-123", Provider: "google.com", AuthMethod: "google.com", AccountVerified: true},
	}
	for _, principal := range guestsAndInvalid {
		server.enqueueLongTermMemory(principal, VoiceTurnResult{StateToken: "must-not-cross"})
	}
	server.enqueueLongTermMemory(verified, VoiceTurnResult{})
	if queue.calls.Load() != 1 {
		t.Fatalf("ineligible principals crossed queue boundary: %d", queue.calls.Load())
	}
}

func TestVoiceTurnEnqueuesLongTermMemoryOnlyAfterResponseCommit(t *testing.T) {
	response := httptest.NewRecorder()
	queue := &commitCheckingMemoryQueue{response: response}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := NewWithVoice(
		logger,
		fakeVerifier{principal: identity.Principal{
			UID: "user-123", AppID: "app-123", Provider: "custom",
			AuthMethod: "passkey-v1", AccountVerified: true,
		}},
		&fakeLimiter{},
		&fakeEvaluator{},
		&fakeStore{},
		2*time.Second,
		4*1024,
		VoiceOptions{
			Service: &fakeVoiceService{result: VoiceTurnResult{
				Audio: []byte("mp3"), AudioMIMEType: "audio/mpeg",
				Caption: "safe", StateToken: "opaque-state", DetectedDomain: "casual",
				AssistanceTarget: "respondent", RespondentStage: "restructure",
				CoachPhase: "awaiting_restatement", CoachAction: "restate",
				ResearchStatus: "none", ResearchRecords: []ResearchRecord{}, Route: "fast",
			}},
			RateLimiter: &fakeLimiter{}, AppRateLimiter: &fakeLimiter{wantKey: "app:app-123"},
			RequestTimeout: 2 * time.Second, MaxRequestBytes: 13 * 1024 * 1024,
			LongTermMemoryQueue: queue,
		},
	)
	request := authenticatedRequest(
		http.MethodPost,
		"/api/v1/voice/turns",
		`{"audioBase64":"YXVkaW8=","mimeType":"audio/webm","sessionState":"","turnMode":"intentional"}`,
	)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !queue.committed {
		t.Fatalf("status=%d enqueue observed committed response=%v body=%s", response.Code, queue.committed, response.Body.String())
	}
}

func (f *fakeLongTermMemory) Status(context.Context, string) (longmemory.Consent, error) {
	f.statusCalls++
	return longmemory.Consent{Enabled: true, Generation: 7}, nil
}

func (f *fakeLongTermMemory) Enable(_ context.Context, uid string) (longmemory.Consent, error) {
	f.enableCalls++
	f.uid = uid
	return longmemory.Consent{Enabled: true, Generation: 7}, nil
}

func (f *fakeLongTermMemory) DisableAndDelete(_ context.Context, uid string) error {
	f.disableCalls++
	f.uid = uid
	return nil
}

func memoryRequest(method, body string) *http.Request {
	request := httptest.NewRequest(method, longTermMemoryPath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer id-token")
	request.Header.Set("X-Firebase-AppCheck", "app-check-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func TestLongTermMemoryManagementIsPrincipalBoundAndContentFree(t *testing.T) {
	memory := &fakeLongTermMemory{}
	principal := identity.Principal{UID: "account-uid", AppID: "app-123", Provider: "custom", AuthMethod: "passkey-v1", AccountVerified: true, PasskeyAt: time.Now().UTC()}
	server := &Server{verifier: fakeVerifier{principal: principal}, longTermMemory: memory}

	for _, test := range []struct {
		method     string
		body       string
		handler    http.HandlerFunc
		wantStatus int
	}{
		{http.MethodGet, "", server.longTermMemoryStatus, http.StatusOK},
		{http.MethodPut, `{"enabled":true}`, server.enableLongTermMemory, http.StatusOK},
		{http.MethodDelete, "", server.disableLongTermMemory, http.StatusNoContent},
	} {
		response := httptest.NewRecorder()
		server.requirePasskeyManagementIdentity(test.handler).ServeHTTP(response, memoryRequest(test.method, test.body))
		if response.Code != test.wantStatus {
			t.Fatalf("%s status=%d body=%s", test.method, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "account-uid") || strings.Contains(response.Body.String(), "generation") {
			t.Fatalf("%s exposed capability: %s", test.method, response.Body.String())
		}
	}
	if memory.uid != "account-uid" || memory.enableCalls != 1 || memory.disableCalls != 1 {
		t.Fatalf("memory calls=%+v", memory)
	}
}

func TestLongTermMemoryContextBeginIsPrincipalBoundAndOpaque(t *testing.T) {
	issuer := &fakeLongTermMemoryContext{capability: "kmc1.opaque", available: true}
	principal := identity.Principal{UID: "account-uid", AppID: "app-123", Provider: "custom", AuthMethod: "passkey-v1", AccountVerified: true, PasskeyAt: time.Now().UTC()}
	var logs bytes.Buffer
	server := &Server{logger: slog.New(slog.NewJSONHandler(&logs, nil)), verifier: fakeVerifier{principal: principal}, memoryContext: issuer}
	request := httptest.NewRequest(http.MethodPost, longTermMemoryContextBeginPath, nil)
	request.Header.Set("Authorization", "Bearer id-token")
	request.Header.Set("X-Firebase-AppCheck", "app-check-token")
	response := httptest.NewRecorder()
	server.requirePasskeyManagementIdentity(http.HandlerFunc(server.beginLongTermMemoryContext)).ServeHTTP(response, request)
	if response.Code != http.StatusOK || issuer.calls != 1 || issuer.uid != principal.UID || issuer.appID != principal.AppID {
		t.Fatalf("status=%d calls=%d uid=%q app=%q body=%s", response.Code, issuer.calls, issuer.uid, issuer.appID, response.Body.String())
	}
	if response.Body.String() != "{\"available\":true,\"capability\":\"kmc1.opaque\"}\n" ||
		strings.Contains(response.Body.String(), principal.UID) || strings.Contains(response.Body.String(), principal.AppID) {
		t.Fatalf("context response was not exact and opaque: %s", response.Body.String())
	}
	if !strings.Contains(logs.String(), `"outcome":"issued"`) {
		t.Fatalf("finite outcome missing: %s", logs.String())
	}
	for _, forbidden := range []string{principal.UID, principal.AppID, issuer.capability} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("context log exposed %q: %s", forbidden, logs.String())
		}
	}
}

func TestLongTermMemoryContextUnavailableHasOneContentFreeShape(t *testing.T) {
	issuer := &fakeLongTermMemoryContext{}
	server := &Server{memoryContext: issuer}
	request := httptest.NewRequest(http.MethodPost, longTermMemoryContextBeginPath, nil)
	request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, identity.Principal{UID: "account-uid", AppID: "app-123"}))
	response := httptest.NewRecorder()
	server.beginLongTermMemoryContext(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "{\"available\":false}\n" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLongTermMemoryContextRejectsInconsistentIssuerResult(t *testing.T) {
	for _, issuer := range []*fakeLongTermMemoryContext{
		{capability: "kmc1.unexpected", available: false},
		{capability: "", available: true},
	} {
		server := &Server{memoryContext: issuer}
		request := httptest.NewRequest(http.MethodPost, longTermMemoryContextBeginPath, nil)
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, identity.Principal{UID: "account-uid", AppID: "app-123"}))
		response := httptest.NewRecorder()
		server.beginLongTermMemoryContext(response, request)
		if response.Code != http.StatusServiceUnavailable ||
			(issuer.capability != "" && strings.Contains(response.Body.String(), issuer.capability)) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestGuestAndStalePasskeyCannotReachLongTermMemoryContext(t *testing.T) {
	for name, principal := range map[string]identity.Principal{
		"guest": {UID: "guest-uid", AppID: "app-123", Provider: "anonymous", AuthMethod: "guest-v1"},
		"stale": {UID: "account-uid", AppID: "app-123", Provider: "custom", AuthMethod: "passkey-v1", AccountVerified: true, PasskeyAt: time.Now().UTC().Add(-6 * time.Minute)},
	} {
		t.Run(name, func(t *testing.T) {
			issuer := &fakeLongTermMemoryContext{capability: "must-not-issue", available: true}
			server := &Server{verifier: fakeVerifier{principal: principal}, memoryContext: issuer}
			request := httptest.NewRequest(http.MethodPost, longTermMemoryContextBeginPath, nil)
			request.Header.Set("Authorization", "Bearer id-token")
			request.Header.Set("X-Firebase-AppCheck", "app-check-token")
			response := httptest.NewRecorder()
			server.requirePasskeyManagementIdentity(http.HandlerFunc(server.beginLongTermMemoryContext)).ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || issuer.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, issuer.calls, response.Body.String())
			}
		})
	}
}

func TestLongTermMemoryContextRejectsBodyAndQueryBeforeIssuer(t *testing.T) {
	for _, target := range []string{longTermMemoryContextBeginPath + "?uid=foreign", longTermMemoryContextBeginPath} {
		issuer := &fakeLongTermMemoryContext{capability: "must-not-issue", available: true}
		server := &Server{memoryContext: issuer}
		body := ""
		if target == longTermMemoryContextBeginPath {
			body = `{}`
		}
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, identity.Principal{UID: "account-uid", AppID: "app-123"}))
		response := httptest.NewRecorder()
		server.beginLongTermMemoryContext(response, request)
		if response.Code != http.StatusServiceUnavailable || issuer.calls != 0 || !strings.Contains(response.Body.String(), "conversation_memory_context_failed") {
			t.Fatalf("target=%q status=%d calls=%d body=%s", target, response.Code, issuer.calls, response.Body.String())
		}
	}
}

func TestGuestAndStalePasskeyCannotReachLongTermMemoryStore(t *testing.T) {
	for name, principal := range map[string]identity.Principal{
		"guest": {UID: "guest-uid", AppID: "app-123", Provider: "anonymous", AuthMethod: "guest-v1"},
		"stale": {UID: "account-uid", AppID: "app-123", Provider: "custom", AuthMethod: "passkey-v1", AccountVerified: true, PasskeyAt: time.Now().UTC().Add(-6 * time.Minute)},
	} {
		t.Run(name, func(t *testing.T) {
			memory := &fakeLongTermMemory{}
			server := &Server{verifier: fakeVerifier{principal: principal}, longTermMemory: memory}
			response := httptest.NewRecorder()
			server.requirePasskeyManagementIdentity(http.HandlerFunc(server.enableLongTermMemory)).ServeHTTP(response, memoryRequest(http.MethodPut, `{"enabled":true}`))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if memory.statusCalls+memory.enableCalls+memory.disableCalls != 0 {
				t.Fatalf("store calls=%+v", memory)
			}
			if !strings.Contains(response.Body.String(), "passkey_management_reauthentication_required") {
				t.Fatalf("unstable problem=%s", response.Body.String())
			}
		})
	}
}

func TestLongTermMemoryEnableRejectsUIDAndUnknownFieldsBeforeStore(t *testing.T) {
	memory := &fakeLongTermMemory{}
	principal := identity.Principal{UID: "account-uid", AppID: "app-123", Provider: "custom", AuthMethod: "passkey-v1", AccountVerified: true, PasskeyAt: time.Now().UTC()}
	server := &Server{verifier: fakeVerifier{principal: principal}, longTermMemory: memory}
	response := httptest.NewRecorder()
	server.requirePasskeyManagementIdentity(http.HandlerFunc(server.enableLongTermMemory)).ServeHTTP(response, memoryRequest(http.MethodPut, `{"enabled":true,"uid":"foreign"}`))
	if response.Code != http.StatusServiceUnavailable || memory.enableCalls != 0 {
		t.Fatalf("status=%d calls=%d", response.Code, memory.enableCalls)
	}
}
