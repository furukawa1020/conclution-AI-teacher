package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
