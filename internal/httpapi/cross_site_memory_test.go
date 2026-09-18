package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
)

func TestBrowserCrossSiteWritesAreLimitedToExactAuthenticatedPaths(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		path       string
		origin     string
		wantStatus int
	}{
		{name: "context begin", path: longTermMemoryContextBeginPath, origin: allowedWebOrigin, wantStatus: http.StatusNoContent},
		{name: "context consume", path: longTermMemoryContextConsumePath, origin: allowedWebOrigin, wantStatus: http.StatusNoContent},
		{name: "voice stream remains allowed", path: voiceStreamPath, origin: allowedWebOrigin, wantStatus: http.StatusNoContent},
		{name: "other write remains blocked", path: "/api/v1/evaluations", origin: allowedWebOrigin, wantStatus: http.StatusForbidden},
		{name: "begin wrong origin", path: longTermMemoryContextBeginPath, origin: "https://attacker.example", wantStatus: http.StatusForbidden},
		{name: "consume missing origin", path: longTermMemoryContextConsumePath, wantStatus: http.StatusForbidden},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			called := 0
			server := &Server{}
			handler := server.rejectCrossSiteWrites(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called++
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request.Header.Set("Sec-Fetch-Site", "cross-site")
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d", response.Code, test.wantStatus)
			}
			wantCalled := 0
			if test.wantStatus == http.StatusNoContent {
				wantCalled = 1
			}
			if called != wantCalled {
				t.Fatalf("next called=%d want=%d", called, wantCalled)
			}
		})
	}
}

func TestBrowserMemoryContextBeginStillRequiresRecentPasskey(t *testing.T) {
	t.Parallel()

	issuer := &fakeLongTermMemoryContext{capability: "kmc1.opaque", available: true}
	principal := identity.Principal{
		UID: "account-uid", AppID: "app-123", Provider: "custom",
		AuthMethod: "passkey-v1", AccountVerified: true, PasskeyAt: time.Now().UTC(),
	}
	server := &Server{verifier: fakeVerifier{principal: principal}, memoryContext: issuer}
	handler := server.rejectCrossSiteWrites(server.requirePasskeyManagementIdentity(http.HandlerFunc(server.beginLongTermMemoryContext)))
	request := httptest.NewRequest(http.MethodPost, longTermMemoryContextBeginPath, nil)
	request.Header.Set("Origin", allowedWebOrigin)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Authorization", "Bearer id-token")
	request.Header.Set("X-Firebase-AppCheck", "app-check-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || issuer.calls != 1 {
		t.Fatalf("authorized status=%d calls=%d", response.Code, issuer.calls)
	}

	missingAuth := httptest.NewRequest(http.MethodPost, longTermMemoryContextBeginPath, nil)
	missingAuth.Header.Set("Origin", allowedWebOrigin)
	missingAuth.Header.Set("Sec-Fetch-Site", "cross-site")
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, missingAuth)
	if denied.Code != http.StatusUnauthorized || issuer.calls != 1 {
		t.Fatalf("unauthorized status=%d calls=%d", denied.Code, issuer.calls)
	}
}
