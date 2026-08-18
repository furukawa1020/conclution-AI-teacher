package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/longmemory"
)

type fakeSessionContext struct {
	calls      int
	uid        string
	appID      string
	capability string
	token      string
	expires    int64
	err        error
}

func (f *fakeSessionContext) ConsumeSessionContext(_ context.Context, uid, appID, capability string) (string, int64, error) {
	f.calls++
	f.uid, f.appID, f.capability = uid, appID, capability
	return f.token, f.expires, f.err
}

func TestLongTermMemorySessionConsumeIsExactPrincipalBoundAndOpaque(t *testing.T) {
	issuer := &fakeSessionContext{token: "kms1.opaque-session", expires: 900}
	principal := identity.Principal{UID: "account-uid", AppID: "app-123", Provider: "custom", AuthMethod: "passkey-v1", AccountVerified: true, PasskeyAt: time.Now().UTC()}
	var logs bytes.Buffer
	server := &Server{logger: slog.New(slog.NewJSONHandler(&logs, nil)), verifier: fakeVerifier{principal: principal}, sessionContext: issuer}
	request := httptest.NewRequest(http.MethodPost, longTermMemoryContextConsumePath, strings.NewReader(`{"capability":"kmc1.opaque-capability"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer id-token")
	request.Header.Set("X-Firebase-AppCheck", "app-check-token")
	response := httptest.NewRecorder()
	server.requirePasskeyManagementIdentity(http.HandlerFunc(server.consumeLongTermMemoryContext)).ServeHTTP(response, request)
	if response.Code != http.StatusOK || issuer.calls != 1 || issuer.uid != principal.UID || issuer.appID != principal.AppID || issuer.capability != "kmc1.opaque-capability" {
		t.Fatalf("status=%d issuer=%+v body=%s", response.Code, issuer, response.Body.String())
	}
	if response.Body.String() != `{"sessionContext":"kms1.opaque-session","expiresInSeconds":900}`+"\n" {
		t.Fatalf("response=%s", response.Body.String())
	}
	for _, forbidden := range []string{principal.UID, principal.AppID, issuer.capability, issuer.token} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("log exposed %q: %s", forbidden, logs.String())
		}
	}
	if !strings.Contains(logs.String(), `"outcome":"issued"`) {
		t.Fatalf("issued outcome missing: %s", logs.String())
	}
}

func TestLongTermMemorySessionConsumeRejectsMalformedWithoutIssuer(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"capability":"short"}`,
		`{"capability":"kmc1.valid","uid":"foreign"}`,
		`{"capability":"kmc1.valid"}{}`,
	} {
		issuer := &fakeSessionContext{token: "must-not-issue", expires: 900}
		server := &Server{sessionContext: issuer}
		request := httptest.NewRequest(http.MethodPost, longTermMemoryContextConsumePath, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, identity.Principal{UID: "account-uid", AppID: "app-123"}))
		response := httptest.NewRecorder()
		server.consumeLongTermMemoryContext(response, request)
		if response.Code != http.StatusBadRequest || issuer.calls != 0 || !strings.Contains(response.Body.String(), "conversation_memory_context_consume_failed") {
			t.Fatalf("body=%q status=%d calls=%d response=%s", body, response.Code, issuer.calls, response.Body.String())
		}
	}
}

func TestLongTermMemorySessionReplayHasFixedResponseAndContentFreeLog(t *testing.T) {
	issuer := &fakeSessionContext{err: longmemory.ErrReplay}
	var logs bytes.Buffer
	server := &Server{logger: slog.New(slog.NewJSONHandler(&logs, nil)), sessionContext: issuer}
	request := httptest.NewRequest(http.MethodPost, longTermMemoryContextConsumePath, strings.NewReader(`{"capability":"kmc1.replayed-capability"}`))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, identity.Principal{UID: "account-uid", AppID: "app-123"}))
	response := httptest.NewRecorder()
	server.consumeLongTermMemoryContext(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "conversation_memory_context_consume_failed") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(logs.String(), `"outcome":"replay_rejected"`) || strings.Contains(logs.String(), issuer.capability) || strings.Contains(logs.String(), "account-uid") {
		t.Fatalf("unsafe replay log: %s", logs.String())
	}
}

func TestGuestCannotReachLongTermMemorySessionConsume(t *testing.T) {
	issuer := &fakeSessionContext{token: "must-not-issue", expires: 900}
	guest := identity.Principal{UID: "guest-uid", AppID: "app-123", Provider: "anonymous", AuthMethod: "guest-v1"}
	server := &Server{verifier: fakeVerifier{principal: guest}, sessionContext: issuer}
	request := httptest.NewRequest(http.MethodPost, longTermMemoryContextConsumePath, strings.NewReader(`{"capability":"kmc1.guest-capability"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer guest-token")
	request.Header.Set("X-Firebase-AppCheck", "app-check-token")
	response := httptest.NewRecorder()
	server.requirePasskeyManagementIdentity(http.HandlerFunc(server.consumeLongTermMemoryContext)).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || issuer.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, issuer.calls, response.Body.String())
	}
}

func TestLongTermMemorySessionIssuerInconsistencyFailsClosed(t *testing.T) {
	issuer := &fakeSessionContext{token: "kms1.unexpected", expires: 899, err: errors.New("failure")}
	server := &Server{sessionContext: issuer}
	request := httptest.NewRequest(http.MethodPost, longTermMemoryContextConsumePath, strings.NewReader(`{"capability":"kmc1.valid-capability"}`))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, identity.Principal{UID: "account-uid", AppID: "app-123"}))
	response := httptest.NewRecorder()
	server.consumeLongTermMemoryContext(response, request)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), issuer.token) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMemoryContextBrowserPreflightIsPathExact(t *testing.T) {
	server := &Server{}
	handler := server.voiceStreamCORS(server.securityHeaders(http.HandlerFunc(server.memoryContextPreflight)))
	for _, test := range []struct {
		path    string
		headers string
	}{
		{longTermMemoryContextBeginPath, "authorization, x-firebase-appcheck"},
		{longTermMemoryContextConsumePath, "authorization, content-type, x-firebase-appcheck"},
	} {
		request := httptest.NewRequest(http.MethodOptions, test.path, nil)
		request.Header.Set("Origin", allowedWebOrigin)
		request.Header.Set("Access-Control-Request-Method", http.MethodPost)
		request.Header.Set("Access-Control-Request-Headers", test.headers)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent ||
			response.Header().Get("Access-Control-Allow-Origin") != allowedWebOrigin ||
			response.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" ||
			response.Header().Get("Access-Control-Allow-Methods") != http.MethodPost {
			t.Fatalf("path=%q status=%d headers=%v body=%s", test.path, response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestMemoryContextPreflightRejectsMissingOrSurplusHeaders(t *testing.T) {
	server := &Server{}
	for _, headers := range []string{
		"authorization, x-firebase-appcheck",
		"authorization, content-type, x-firebase-appcheck, x-uid",
	} {
		request := httptest.NewRequest(http.MethodOptions, longTermMemoryContextConsumePath, nil)
		request.Header.Set("Origin", allowedWebOrigin)
		request.Header.Set("Access-Control-Request-Method", http.MethodPost)
		request.Header.Set("Access-Control-Request-Headers", headers)
		response := httptest.NewRecorder()
		server.memoryContextPreflight(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("headers=%q status=%d", headers, response.Code)
		}
	}
}
