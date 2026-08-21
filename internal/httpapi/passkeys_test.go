package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/passkey"
	"github.com/go-webauthn/webauthn/protocol"
)

type passkeyHTTPVerifier struct {
	principal  identity.Principal
	principals map[string]identity.Principal
	appID      string
	err        error
}

func (v passkeyHTTPVerifier) Verify(_ context.Context, idToken, _ string) (identity.Principal, error) {
	if v.err != nil {
		return identity.Principal{}, v.err
	}
	if v.principals != nil {
		principal, ok := v.principals[idToken]
		if !ok {
			return identity.Principal{}, identity.ErrUnauthenticated
		}
		return principal, nil
	}
	return v.principal, nil
}

func (v passkeyHTTPVerifier) VerifyApp(context.Context, string) (string, error) {
	if v.err != nil || v.appID == "" {
		return "", identity.ErrUnauthenticated
	}
	return v.appID, nil
}

type recordingPasskeyHTTPService struct {
	registrationAppID           string
	authenticationAppID         string
	registrationBeginCalls      int
	authenticationBeginCalls    int
	registrationFinishCalls     int
	authenticationFinishCalls   int
	finishErr                   error
	credentialRegistrationAppID string
	credentialRegistrationUID   string
	credentialBeginCalls        int
	credentialFinishCalls       int
	credentialSummaries         []passkey.CredentialSummary
	credentialListCalls         int
	credentialRevokeCalls       int
	credentialRevokeReference   passkey.CredentialReference
	accountDeleteCalls          int
	recoveryCodeCalls           int
	recoveryCodeResult          passkey.RecoveryCodeResult
	recoveryBeginCalls          int
	recoveryBeginAppID          string
	recoveryBeginCode           string
}

func (s *recordingPasskeyHTTPService) BeginRecoveryRegistration(_ context.Context, appID, code string) (passkey.BeginRegistrationResult, error) {
	s.recoveryBeginCalls++
	s.recoveryBeginAppID = appID
	s.recoveryBeginCode = code
	return passkey.BeginRegistrationResult{CeremonyID: "recovery-registration-ceremony", Options: &protocol.CredentialCreation{}}, s.finishErr
}

func (s *recordingPasskeyHTTPService) IssueRecoveryCode(_ context.Context, uid string) (passkey.RecoveryCodeResult, error) {
	s.credentialRegistrationUID = uid
	s.recoveryCodeCalls++
	return s.recoveryCodeResult, s.finishErr
}

func (s *recordingPasskeyHTTPService) DeleteAccount(_ context.Context, uid string) error {
	s.credentialRegistrationUID = uid
	s.accountDeleteCalls++
	return s.finishErr
}

func (s *recordingPasskeyHTTPService) ListCredentials(_ context.Context, uid string) ([]passkey.CredentialSummary, error) {
	s.credentialRegistrationUID = uid
	s.credentialListCalls++
	return append([]passkey.CredentialSummary(nil), s.credentialSummaries...), s.finishErr
}

func (s *recordingPasskeyHTTPService) RevokeCredential(_ context.Context, uid string, reference passkey.CredentialReference) error {
	s.credentialRegistrationUID = uid
	s.credentialRevokeReference = reference
	s.credentialRevokeCalls++
	return s.finishErr
}

func (s *recordingPasskeyHTTPService) BeginCredentialRegistration(
	_ context.Context,
	appID, uid string,
) (passkey.BeginRegistrationResult, error) {
	s.credentialRegistrationAppID = appID
	s.credentialRegistrationUID = uid
	s.credentialBeginCalls++
	return passkey.BeginRegistrationResult{CeremonyID: "credential-registration-ceremony"}, s.finishErr
}

func (s *recordingPasskeyHTTPService) FinishCredentialRegistration(
	_ context.Context,
	appID, uid, _ string,
	_ *http.Request,
) error {
	s.credentialRegistrationAppID = appID
	s.credentialRegistrationUID = uid
	s.credentialFinishCalls++
	return s.finishErr
}

func (s *recordingPasskeyHTTPService) BeginRegistration(
	_ context.Context,
	appID string,
) (passkey.BeginRegistrationResult, error) {
	s.registrationAppID = appID
	s.registrationBeginCalls++
	return passkey.BeginRegistrationResult{CeremonyID: "registration-ceremony"}, nil
}

func (s *recordingPasskeyHTTPService) FinishRegistration(
	_ context.Context,
	appID, _ string,
	_ *http.Request,
) (passkey.FinishResult, error) {
	s.registrationAppID = appID
	s.registrationFinishCalls++
	return passkey.FinishResult{CustomToken: "registration-token", AuthMethod: "passkey-v1"}, s.finishErr
}

func (s *recordingPasskeyHTTPService) BeginAuthentication(
	_ context.Context,
	appID string,
) (passkey.BeginAuthenticationResult, error) {
	s.authenticationAppID = appID
	s.authenticationBeginCalls++
	return passkey.BeginAuthenticationResult{CeremonyID: "authentication-ceremony"}, nil
}

func (s *recordingPasskeyHTTPService) FinishAuthentication(
	_ context.Context,
	appID, _ string,
	_ *http.Request,
) (passkey.FinishResult, error) {
	s.authenticationAppID = appID
	s.authenticationFinishCalls++
	return passkey.FinishResult{CustomToken: "authentication-token", AuthMethod: "passkey-v1"}, s.finishErr
}

func newPasskeyHTTPHandler(t *testing.T, service PasskeyService, limiter guard.Limiter) http.Handler {
	t.Helper()
	return newPasskeyHTTPHandlerWithVerifier(
		t,
		service,
		limiter,
		passkeyHTTPVerifier{appID: "firebase-app-id"},
	)
}

func newPasskeyHTTPHandlerWithVerifier(
	t *testing.T,
	service PasskeyService,
	clientLimiter guard.Limiter,
	verifier passkeyHTTPVerifier,
) http.Handler {
	t.Helper()
	return newPasskeyHTTPHandlerWithQuotas(
		t,
		service,
		clientLimiter,
		&recordingPasskeyQuotaLimiter{},
		verifier,
	)
}

func newPasskeyHTTPHandlerWithQuotas(
	t *testing.T,
	service PasskeyService,
	clientLimiter guard.Limiter,
	appCircuitBreaker guard.Limiter,
	verifier passkeyHTTPVerifier,
) http.Handler {
	t.Helper()
	return NewWithVoiceAndPasskeys(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		verifier,
		nil,
		nil,
		nil,
		5*time.Second,
		32*1024,
		VoiceOptions{},
		service,
		clientLimiter,
		appCircuitBreaker,
	)
}

func passkeyPOST(path string, body io.Reader) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, body)
	request.Header.Set("Origin", allowedWebOrigin)
	request.Header.Set("X-Firebase-AppCheck", "valid-app-check")
	return request
}

func TestDeletePasskeyAccountRequiresExactConfirmationAndUsesPrincipalUID(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	now := time.Now().UTC()
	principal := identity.Principal{UID: "private-firebase-uid", AppID: "firebase-app-id", Provider: "custom", AuthMethod: "passkey-v1", PasskeyAt: now, AccountVerified: true}
	handler := newPasskeyHTTPHandlerWithVerifier(t, service, &recordingPasskeyQuotaLimiter{}, passkeyHTTPVerifier{principal: principal, appID: principal.AppID})
	bad := passkeyPOST(passkeyAccountDeletePath, strings.NewReader(`{"confirmation":"delete"}`))
	bad.Header.Set("Authorization", "Bearer valid")
	bad.Header.Set("Content-Type", "application/json")
	badRecorder := httptest.NewRecorder()
	handler.ServeHTTP(badRecorder, bad)
	if badRecorder.Code != http.StatusBadRequest || service.accountDeleteCalls != 0 {
		t.Fatalf("bad delete = %d calls=%d", badRecorder.Code, service.accountDeleteCalls)
	}
	body := `{"confirmation":` + strconv.Quote(accountDeletionConfirmation) + `}`
	request := passkeyPOST(passkeyAccountDeletePath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer valid")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || service.accountDeleteCalls != 1 || service.credentialRegistrationUID != principal.UID {
		t.Fatalf("delete = %d calls=%d uid=%q", recorder.Code, service.accountDeleteCalls, service.credentialRegistrationUID)
	}
	if recorder.Header().Get("Clear-Site-Data") == "" {
		t.Fatal("Clear-Site-Data missing")
	}
}

func TestIssuePasskeyRecoveryCodeUsesRecentPrincipalAndReturnsOneSecret(t *testing.T) {
	now := time.Now().UTC()
	principal := identity.Principal{UID: "private-firebase-uid", AppID: "firebase-app-id", Provider: "custom", AuthMethod: "passkey-v1", PasskeyAt: now, AccountVerified: true}
	service := &recordingPasskeyHTTPService{recoveryCodeResult: passkey.RecoveryCodeResult{
		Code: "krc1_" + strings.Repeat("A", 43), ExpiresIn: int64(passkey.RecoveryCodeTTL / time.Second),
	}}
	handler := newPasskeyHTTPHandlerWithVerifier(t, service, &recordingPasskeyQuotaLimiter{}, passkeyHTTPVerifier{principal: principal, appID: principal.AppID})
	request := passkeyPOST(passkeyRecoveryCodeIssuePath, nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.recoveryCodeCalls != 1 ||
		service.credentialRegistrationUID != principal.UID ||
		strings.Contains(response.Body.String(), principal.UID) ||
		strings.Count(response.Body.String(), service.recoveryCodeResult.Code) != 1 {
		t.Fatalf("response=%d body=%q calls=%d uid=%q", response.Code, response.Body.String(), service.recoveryCodeCalls, service.credentialRegistrationUID)
	}

	stale := principal
	stale.PasskeyAt = now.Add(-passkeyManagementAuthorizationAge - time.Second)
	staleHandler := newPasskeyHTTPHandlerWithVerifier(t, service, &recordingPasskeyQuotaLimiter{}, passkeyHTTPVerifier{principal: stale, appID: stale.AppID})
	staleRequest := passkeyPOST(passkeyRecoveryCodeIssuePath, nil)
	staleRequest.Header.Set("Authorization", "Bearer stale")
	staleResponse := httptest.NewRecorder()
	staleHandler.ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code == http.StatusOK || service.recoveryCodeCalls != 1 || strings.Contains(staleResponse.Body.String(), service.recoveryCodeResult.Code) {
		t.Fatalf("stale response=%d body=%q calls=%d", staleResponse.Code, staleResponse.Body.String(), service.recoveryCodeCalls)
	}
}

func TestBeginPasskeyRecoveryUsesAppCheckCodeAndNeverEchoesCapability(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	handler := newPasskeyHTTPHandler(t, service, &recordingPasskeyQuotaLimiter{})
	code := "krc1_" + strings.Repeat("B", 43)
	request := passkeyPOST(passkeyRecoveryRegistrationBeginPath, strings.NewReader(`{"recoveryCode":"`+code+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.recoveryBeginCalls != 1 ||
		service.recoveryBeginAppID != "firebase-app-id" || service.recoveryBeginCode != code ||
		strings.Contains(response.Body.String(), code) ||
		!strings.Contains(response.Body.String(), "recovery-registration-ceremony") {
		t.Fatalf("response=%d body=%q calls=%d app=%q", response.Code, response.Body.String(), service.recoveryBeginCalls, service.recoveryBeginAppID)
	}

	bad := passkeyPOST(passkeyRecoveryRegistrationBeginPath, strings.NewReader(`{"recoveryCode":"`+code+`","uid":"foreign"}`))
	bad.Header.Set("Content-Type", "application/json")
	badResponse := httptest.NewRecorder()
	handler.ServeHTTP(badResponse, bad)
	if badResponse.Code != http.StatusBadRequest || service.recoveryBeginCalls != 1 || strings.Contains(badResponse.Body.String(), code) {
		t.Fatalf("bad response=%d body=%q calls=%d", badResponse.Code, badResponse.Body.String(), service.recoveryBeginCalls)
	}
}

type recordingPasskeyQuotaLimiter struct {
	keys []string
	err  error
}

func (l *recordingPasskeyQuotaLimiter) Consume(_ context.Context, key string, _ time.Time) error {
	l.keys = append(l.keys, key)
	return l.err
}

func TestPasskeyRegistrationBootstrapsWithAppCheckOnly(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	limiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 2, PerDay: 2})
	if err != nil {
		t.Fatal(err)
	}
	handler := newPasskeyHTTPHandler(t, service, limiter)
	request := passkeyPOST(passkeyRegistrationBeginPath, nil)
	if request.Header.Get("Authorization") != "" {
		t.Fatal("test unexpectedly sent an account credential")
	}
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.registrationAppID != "firebase-app-id" ||
		!strings.Contains(response.Body.String(), "registration-ceremony") {
		t.Fatalf("app ID = %q, body = %s", service.registrationAppID, response.Body.String())
	}
}

func TestPasskeyBeginRequiresAppCheckAndIsAnonymousClientRateLimited(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	limiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 1, PerDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	handler := newPasskeyHTTPHandler(t, service, limiter)

	missing := passkeyPOST(passkeyAuthenticationBeginPath, nil)
	missing.Header.Del("X-Firebase-AppCheck")
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusUnauthorized {
		t.Fatalf("missing App Check status = %d", missingResponse.Code)
	}

	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, passkeyPOST(passkeyAuthenticationBeginPath, nil))
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", firstResponse.Code, firstResponse.Body.String())
	}
	limitedResponse := httptest.NewRecorder()
	handler.ServeHTTP(limitedResponse, passkeyPOST(passkeyAuthenticationBeginPath, nil))
	if limitedResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status = %d, body = %s", limitedResponse.Code, limitedResponse.Body.String())
	}
}

func TestPasskeyFinishConsumesClientQuotaBeforeInputAndService(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		calls func(*recordingPasskeyHTTPService) int
	}{
		{
			name: "registration finish",
			path: passkeyRegistrationFinishPath,
			calls: func(service *recordingPasskeyHTTPService) int {
				return service.registrationFinishCalls
			},
		},
		{
			name: "authentication finish",
			path: passkeyAuthenticationFinishPath,
			calls: func(service *recordingPasskeyHTTPService) int {
				return service.authenticationFinishCalls
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			service := &recordingPasskeyHTTPService{}
			limiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 1, PerDay: 1})
			if err != nil {
				t.Fatal(err)
			}
			handler := newPasskeyHTTPHandler(t, service, limiter)

			invalid := passkeyPOST(test.path, strings.NewReader(`{"id":"credential"}`))
			invalid.Header.Set("Content-Type", "application/json")
			invalidResponse := httptest.NewRecorder()
			handler.ServeHTTP(invalidResponse, invalid)
			if invalidResponse.Code != http.StatusBadRequest {
				t.Fatalf("invalid status = %d, body = %s", invalidResponse.Code, invalidResponse.Body.String())
			}

			valid := passkeyPOST(test.path+"?ceremonyId=opaque", strings.NewReader(`{"id":"credential"}`))
			valid.Header.Set("Content-Type", "application/json")
			limitedResponse := httptest.NewRecorder()
			handler.ServeHTTP(limitedResponse, valid)
			if limitedResponse.Code != http.StatusTooManyRequests {
				t.Fatalf("limited status = %d, body = %s", limitedResponse.Code, limitedResponse.Body.String())
			}
			if calls := test.calls(service); calls != 0 {
				t.Fatalf("finish service calls = %d; want 0", calls)
			}
		})
	}
}

func TestAllPasskeyEndpointsShareOneAnonymousClientQuota(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	limiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 4, PerDay: 4})
	if err != nil {
		t.Fatal(err)
	}
	handler := newPasskeyHTTPHandler(t, service, limiter)

	requests := []*http.Request{
		passkeyPOST(passkeyRegistrationBeginPath, nil),
		passkeyPOST(passkeyAuthenticationBeginPath, nil),
		passkeyPOST(passkeyRegistrationFinishPath+"?ceremonyId=registration", strings.NewReader(`{"id":"credential"}`)),
		passkeyPOST(passkeyAuthenticationFinishPath+"?ceremonyId=authentication", strings.NewReader(`{"id":"credential"}`)),
	}
	requests[2].Header.Set("Content-Type", "application/json")
	requests[3].Header.Set("Content-Type", "application/json")
	for index, request := range requests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, body = %s", index, response.Code, response.Body.String())
		}
	}

	limited := httptest.NewRecorder()
	handler.ServeHTTP(limited, passkeyPOST(passkeyRegistrationBeginPath, nil))
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("shared client quota status = %d, body = %s", limited.Code, limited.Body.String())
	}
}

func TestPasskeyAnonymousQuotaIsSeparatedByAttestationToken(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	limiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 1, PerDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	handler := newPasskeyHTTPHandler(t, service, limiter)

	requestFor := func(appCheckToken string) *http.Request {
		request := passkeyPOST(passkeyRegistrationBeginPath, nil)
		request.Header.Set("X-Firebase-AppCheck", appCheckToken)
		request.RemoteAddr = "198.51.100.24:43210"
		return request
	}
	for _, token := range []string{"attestation-client-a", "attestation-client-b"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, requestFor(token))
		if response.Code != http.StatusOK {
			t.Fatalf("token %q status = %d, body = %s", token, response.Code, response.Body.String())
		}
	}

	limited := httptest.NewRecorder()
	handler.ServeHTTP(limited, requestFor("attestation-client-a"))
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("reused anonymous identity status = %d, body = %s", limited.Code, limited.Body.String())
	}
}

func TestPasskeyAppCircuitBreakerStopsRotatingAttestationTokensFirst(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	clientLimiter := &recordingPasskeyQuotaLimiter{}
	appLimiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 2, PerDay: 2})
	if err != nil {
		t.Fatal(err)
	}
	handler := newPasskeyHTTPHandlerWithQuotas(
		t,
		service,
		clientLimiter,
		appLimiter,
		passkeyHTTPVerifier{appID: "firebase-app-id"},
	)

	for index, token := range []string{"rotating-token-a", "rotating-token-b", "rotating-token-c"} {
		request := passkeyPOST(passkeyRegistrationBeginPath, nil)
		request.Header.Set("X-Firebase-AppCheck", token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusOK
		if index == 2 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("rotated token %d status = %d, body = %s; want %d", index, response.Code, response.Body.String(), want)
		}
	}
	if len(clientLimiter.keys) != 2 {
		t.Fatalf("client limiter calls = %d; app breaker must reject third request first", len(clientLimiter.keys))
	}
	if service.registrationBeginCalls != 2 {
		t.Fatalf("registration begin calls = %d; want 2", service.registrationBeginCalls)
	}
}

func TestPasskeyQuotaStoreFailuresFailClosedBeforeService(t *testing.T) {
	storeFailure := errors.New("quota store unavailable")
	tests := []struct {
		name            string
		clientLimiter   *recordingPasskeyQuotaLimiter
		appLimiter      *recordingPasskeyQuotaLimiter
		wantClientCalls int
		wantAppCalls    int
	}{
		{
			name:            "app circuit breaker store fails first",
			clientLimiter:   &recordingPasskeyQuotaLimiter{},
			appLimiter:      &recordingPasskeyQuotaLimiter{err: storeFailure},
			wantClientCalls: 0,
			wantAppCalls:    1,
		},
		{
			name:            "client quota store fails after app",
			clientLimiter:   &recordingPasskeyQuotaLimiter{err: storeFailure},
			appLimiter:      &recordingPasskeyQuotaLimiter{},
			wantClientCalls: 1,
			wantAppCalls:    1,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			service := &recordingPasskeyHTTPService{}
			handler := newPasskeyHTTPHandlerWithQuotas(
				t,
				service,
				test.clientLimiter,
				test.appLimiter,
				passkeyHTTPVerifier{appID: "firebase-app-id"},
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, passkeyPOST(passkeyAuthenticationBeginPath, nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if len(test.clientLimiter.keys) != test.wantClientCalls || len(test.appLimiter.keys) != test.wantAppCalls {
				t.Fatalf("quota calls client/app = %d/%d; want %d/%d", len(test.clientLimiter.keys), len(test.appLimiter.keys), test.wantClientCalls, test.wantAppCalls)
			}
			if service.authenticationBeginCalls != 0 {
				t.Fatalf("failed quota reached passkey service %d times", service.authenticationBeginCalls)
			}
		})
	}
}

func TestPasskeyAnonymousQuotaDoesNotReadForwardingHeaders(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	limiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 1, PerDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	handler := newPasskeyHTTPHandler(t, service, limiter)

	for index, spoofed := range []string{"203.0.113.10", "203.0.113.200"} {
		request := passkeyPOST(passkeyAuthenticationBeginPath, nil)
		request.RemoteAddr = "198.51.100.24:43210"
		request.Header.Set("X-Forwarded-For", spoofed)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusOK
		if index == 1 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("spoofed X-Forwarded-For %q status = %d; want %d", spoofed, response.Code, want)
		}
	}
}

func TestPasskeyAuthenticatedQuotaIsScopedByUID(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	limiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 1, PerDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	verifier := passkeyHTTPVerifier{
		appID: "firebase-app-id",
		principals: map[string]identity.Principal{
			"id-token-a": {UID: "uid-a", AppID: "firebase-app-id", AccountVerified: true},
			"id-token-b": {UID: "uid-b", AppID: "firebase-app-id", AccountVerified: true},
		},
	}
	handler := newPasskeyHTTPHandlerWithVerifier(t, service, limiter, verifier)
	requestFor := func(idToken string) *http.Request {
		request := passkeyPOST(passkeyRegistrationBeginPath, nil)
		request.Header.Set("Authorization", "Bearer "+idToken)
		request.Header.Set("X-Firebase-AppCheck", "shared-attestation-token")
		request.RemoteAddr = "198.51.100.24:43210"
		return request
	}

	for _, idToken := range []string{"id-token-a", "id-token-b"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, requestFor(idToken))
		if response.Code != http.StatusOK {
			t.Fatalf("identity %q status = %d, body = %s", idToken, response.Code, response.Body.String())
		}
	}
	limited := httptest.NewRecorder()
	handler.ServeHTTP(limited, requestFor("id-token-a"))
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("reused UID status = %d, body = %s", limited.Code, limited.Body.String())
	}
}

func TestPasskeyOptionalIdentityFailsClosedAndQuotaKeysAreOpaque(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	clientLimiter := &recordingPasskeyQuotaLimiter{}
	appLimiter := &recordingPasskeyQuotaLimiter{}
	verifier := passkeyHTTPVerifier{
		appID: "firebase-app-id",
		principals: map[string]identity.Principal{
			"private-id-token": {
				UID: "private-firebase-uid", AppID: "firebase-app-id", AccountVerified: true,
			},
		},
	}
	handler := newPasskeyHTTPHandlerWithQuotas(t, service, clientLimiter, appLimiter, verifier)

	authenticated := passkeyPOST(passkeyRegistrationBeginPath, nil)
	authenticated.Header.Set("Authorization", "Bearer private-id-token")
	authenticated.Header.Set("X-Firebase-AppCheck", "private-app-check-token")
	authenticated.RemoteAddr = "198.51.100.24:43210"
	authenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(authenticatedResponse, authenticated)
	if authenticatedResponse.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, body = %s", authenticatedResponse.Code, authenticatedResponse.Body.String())
	}

	malformed := passkeyPOST(passkeyRegistrationBeginPath, nil)
	malformed.Header.Set("Authorization", "Bearer unknown-token")
	malformedResponse := httptest.NewRecorder()
	handler.ServeHTTP(malformedResponse, malformed)
	if malformedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("invalid optional identity status = %d", malformedResponse.Code)
	}
	if len(clientLimiter.keys) != 1 || len(appLimiter.keys) != 1 {
		t.Fatalf("quota keys after rejected identity client/app = %d/%d; want 1/1", len(clientLimiter.keys), len(appLimiter.keys))
	}
	for scope, key := range map[string]string{
		"client": clientLimiter.keys[0],
		"app":    appLimiter.keys[0],
	} {
		if len(key) != len("passkey:v2:")+sha256.Size*2 ||
			strings.Contains(key, "private-firebase-uid") ||
			strings.Contains(key, "private-app-check-token") ||
			strings.Contains(key, "198.51.100") ||
			strings.Contains(key, "firebase-app-id") {
			t.Fatalf("%s quota key is not a fixed opaque digest: %q", scope, key)
		}
	}
}

func TestUnknownCredentialAndBadAssertionHaveSameHTTPResponse(t *testing.T) {
	limiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 2, PerDay: 2})
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingPasskeyHTTPService{}
	handler := newPasskeyHTTPHandler(t, service, limiter)
	errorsToTest := []error{passkey.ErrCredentialNotFound, errors.New("bad assertion signature")}
	var firstStatus int
	var firstBody string
	for index, finishErr := range errorsToTest {
		service.finishErr = finishErr
		request := passkeyPOST(passkeyAuthenticationFinishPath+"?ceremonyId=opaque", strings.NewReader(`{"id":"credential"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if index == 0 {
			firstStatus, firstBody = response.Code, response.Body.String()
		} else if response.Code != firstStatus || response.Body.String() != firstBody {
			t.Fatalf("responses differ: (%d, %q) vs (%d, %q)", firstStatus, firstBody, response.Code, response.Body.String())
		}
	}
	if firstStatus != http.StatusUnauthorized || !strings.Contains(firstBody, "passkey_authentication_failed") {
		t.Fatalf("status = %d, body = %s", firstStatus, firstBody)
	}
}

func TestPasskeyFinishRejectsOversizedCredentialBeforeService(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	limiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 1, PerDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	handler := newPasskeyHTTPHandler(t, service, limiter)
	request := passkeyPOST(
		passkeyAuthenticationFinishPath+"?ceremonyId=opaque",
		strings.NewReader(strings.Repeat("x", passkeyMaxCredentialBody+1)),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.authenticationAppID != "" {
		t.Fatal("oversized response reached passkey service")
	}
}

func TestRecentPasskeyVoiceGateIsOptInAndExpires(t *testing.T) {
	now := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		principal identity.Principal
		want      bool
	}{
		{
			name: "fresh passkey",
			principal: identity.Principal{
				Provider: "custom", AuthMethod: "passkey-v1", AuthTime: now, PasskeyAt: now.Add(-4 * time.Minute), AccountVerified: true,
			},
			want: true,
		},
		{
			name: "delayed exchange cannot refresh stale passkey",
			principal: identity.Principal{
				Provider: "custom", AuthMethod: "passkey-v1", AuthTime: now, PasskeyAt: now.Add(-5*time.Minute - time.Second), AccountVerified: true,
			},
		},
		{
			name: "fresh auth time without passkey timestamp",
			principal: identity.Principal{
				Provider: "custom", AuthMethod: "passkey-v1", AuthTime: now, AccountVerified: true,
			},
		},
		{
			name: "gate ignores stale auth time when immutable passkey time is fresh",
			principal: identity.Principal{
				Provider: "custom", AuthMethod: "passkey-v1", AuthTime: now.Add(-time.Hour), PasskeyAt: now.Add(-time.Minute), AccountVerified: true,
			},
			want: true,
		},
		{
			name: "Google is rejected by recent passkey gate",
			principal: identity.Principal{
				Provider: "google.com", AuthMethod: "google.com", AuthTime: now, AccountVerified: true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := voiceAuthorized(test.principal, now); got != test.want {
				t.Fatalf("voiceAuthorized = %v; want %v", got, test.want)
			}
		})
	}

	server := &Server{voice: VoiceOptions{RequireRecentPasskey: false}}
	called := false
	handler := server.requireFreshPasskey(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	if !called {
		t.Fatal("default-off staged gate blocked an existing voice request")
	}
}

func TestRecentPasskeyGateWrapsBufferedAndStreamingVoiceRoutes(t *testing.T) {
	limiter, err := guard.NewMemoryLimiter(guard.Limits{PerMinute: 2, PerDay: 2})
	if err != nil {
		t.Fatal(err)
	}
	stale := identity.Principal{
		UID:             "pk_user",
		AppID:           "firebase-app-id",
		Provider:        "custom",
		AuthMethod:      "passkey-v1",
		AuthTime:        time.Now(),
		PasskeyAt:       time.Now().Add(-6 * time.Minute),
		AccountVerified: true,
	}
	handler := NewWithVoice(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		passkeyHTTPVerifier{principal: stale, appID: stale.AppID},
		limiter,
		nil,
		nil,
		5*time.Second,
		32*1024,
		VoiceOptions{RequireRecentPasskey: true},
	)
	for _, path := range []string{"/api/v1/voice/turns", voiceStreamPath} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Header.Set("Origin", allowedWebOrigin)
		request.Header.Set("Authorization", "Bearer id-token")
		request.Header.Set("X-Firebase-AppCheck", "app-check")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "passkey_required") {
			t.Fatalf("path %s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestGuestVoiceAccessIsExplicitAndNeverPasskeyManagement(t *testing.T) {
	now := time.Now().UTC()
	guest := identity.Principal{UID: "guest-uid", AppID: "firebase-app-id", Provider: "anonymous", AuthMethod: "guest-v1", AuthTime: now}
	if voiceAccessAuthorized(guest, now, false) {
		t.Fatal("disabled guest mode authorized a guest")
	}
	if !voiceAccessAuthorized(guest, now, true) {
		t.Fatal("enabled guest mode rejected an exact guest principal")
	}
	if passkeyManagementAuthorized(guest, now) {
		t.Fatal("guest crossed the passkey management boundary")
	}
}

func TestPasskeyManagementGateIsAlwaysOnAndUsesImmutablePasskeyTime(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name      string
		principal identity.Principal
		want      bool
	}{
		{
			name: "fresh verified passkey principal",
			principal: identity.Principal{
				UID: "private-uid", AppID: "firebase-app-id", Provider: "custom",
				AuthMethod: "passkey-v1", AuthTime: now, PasskeyAt: now.Add(-4 * time.Minute),
				AccountVerified: true,
			},
			want: true,
		},
		{
			name: "ordinary Firebase provider is insufficient",
			principal: identity.Principal{
				UID: "private-uid", AppID: "firebase-app-id", Provider: "google.com",
				AuthMethod: "google.com", AuthTime: now, AccountVerified: true,
			},
		},
		{
			name: "fresh token cannot refresh stale passkey proof",
			principal: identity.Principal{
				UID: "private-uid", AppID: "firebase-app-id", Provider: "custom",
				AuthMethod: "passkey-v1", AuthTime: now, PasskeyAt: now.Add(-5*time.Minute - time.Second),
				AccountVerified: true,
			},
		},
		{
			name: "future proof outside skew",
			principal: identity.Principal{
				UID: "private-uid", AppID: "firebase-app-id", Provider: "custom",
				AuthMethod: "passkey-v1", AuthTime: now, PasskeyAt: now.Add(31 * time.Second),
				AccountVerified: true,
			},
		},
		{
			name: "missing verified principal UID",
			principal: identity.Principal{
				AppID: "firebase-app-id", Provider: "custom", AuthMethod: "passkey-v1",
				AuthTime: now, PasskeyAt: now, AccountVerified: true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := passkeyManagementAuthorized(test.principal, now); got != test.want {
				t.Fatalf("passkeyManagementAuthorized() = %v; want %v", got, test.want)
			}
		})
	}

	stale := tests[2].principal
	service := &recordingPasskeyHTTPService{}
	handler := newPasskeyHTTPHandlerWithVerifier(
		t,
		service,
		&recordingPasskeyQuotaLimiter{},
		passkeyHTTPVerifier{principal: stale, appID: stale.AppID},
	)
	request := passkeyPOST(passkeyCredentialBeginPath, nil)
	request.Header.Set("Authorization", "Bearer ordinary-firebase-id-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized ||
		!strings.Contains(response.Body.String(), "passkey_management_reauthentication_required") ||
		service.credentialBeginCalls != 0 {
		t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), service.credentialBeginCalls)
	}
}

func TestPasskeyCredentialRoutesDeriveOnlyVerifiedPrincipalAndReturnNoToken(t *testing.T) {
	now := time.Now().UTC()
	principal := identity.Principal{
		UID:             "private-firebase-uid",
		AppID:           "firebase-app-id",
		Provider:        "custom",
		AuthMethod:      "passkey-v1",
		AuthTime:        now,
		PasskeyAt:       now.Add(-time.Minute),
		AccountVerified: true,
	}
	service := &recordingPasskeyHTTPService{}
	handler := newPasskeyHTTPHandlerWithVerifier(
		t,
		service,
		&recordingPasskeyQuotaLimiter{},
		passkeyHTTPVerifier{principal: principal, appID: principal.AppID},
	)

	begin := passkeyPOST(passkeyCredentialBeginPath, nil)
	begin.Header.Set("Authorization", "Bearer verified-passkey-token")
	beginResponse := httptest.NewRecorder()
	handler.ServeHTTP(beginResponse, begin)
	if beginResponse.Code != http.StatusOK ||
		service.credentialRegistrationUID != principal.UID ||
		service.credentialRegistrationAppID != principal.AppID ||
		service.credentialBeginCalls != 1 {
		t.Fatalf("status=%d body=%s uid=%q app=%q calls=%d", beginResponse.Code, beginResponse.Body.String(), service.credentialRegistrationUID, service.credentialRegistrationAppID, service.credentialBeginCalls)
	}
	if strings.Contains(beginResponse.Body.String(), principal.UID) ||
		strings.Contains(beginResponse.Body.String(), "token") {
		t.Fatalf("begin response exposed protected material: %s", beginResponse.Body.String())
	}

	finish := passkeyPOST(
		passkeyCredentialFinishPath+"?ceremonyId=opaque-ceremony",
		strings.NewReader(`{"id":"credential-response"}`),
	)
	finish.Header.Set("Authorization", "Bearer verified-passkey-token")
	finish.Header.Set("Content-Type", "application/json")
	finishResponse := httptest.NewRecorder()
	handler.ServeHTTP(finishResponse, finish)
	if finishResponse.Code != http.StatusNoContent || finishResponse.Body.Len() != 0 ||
		service.credentialFinishCalls != 1 {
		t.Fatalf("status=%d body=%s calls=%d", finishResponse.Code, finishResponse.Body.String(), service.credentialFinishCalls)
	}
}

func TestPasskeyCredentialRoutesRejectRequestControlledAccountIdentifiers(t *testing.T) {
	now := time.Now().UTC()
	principal := identity.Principal{
		UID: "private-firebase-uid", AppID: "firebase-app-id", Provider: "custom",
		AuthMethod: "passkey-v1", AuthTime: now, PasskeyAt: now, AccountVerified: true,
	}
	service := &recordingPasskeyHTTPService{}
	handler := newPasskeyHTTPHandlerWithVerifier(
		t,
		service,
		&recordingPasskeyQuotaLimiter{},
		passkeyHTTPVerifier{principal: principal, appID: principal.AppID},
	)
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "begin query UID", path: passkeyCredentialBeginPath + "?uid=attacker"},
		{name: "begin body UID", path: passkeyCredentialBeginPath, body: `{"uid":"attacker"}`},
		{name: "finish extra query UID", path: passkeyCredentialFinishPath + "?ceremonyId=opaque&uid=attacker", body: `{"id":"credential"}`},
		{name: "finish body UID", path: passkeyCredentialFinishPath + "?ceremonyId=opaque", body: `{"id":"credential","uid":"attacker"}`},
		{name: "finish body user handle", path: passkeyCredentialFinishPath + "?ceremonyId=opaque", body: `{"id":"credential","userHandle":"private-handle"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body io.Reader
			if test.body != "" {
				body = strings.NewReader(test.body)
			}
			request := passkeyPOST(test.path, body)
			request.Header.Set("Authorization", "Bearer verified-passkey-token")
			if body != nil {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest ||
				!strings.Contains(response.Body.String(), "passkey_credential_registration_failed") ||
				strings.Contains(response.Body.String(), "attacker") ||
				strings.Contains(response.Body.String(), principal.UID) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if service.credentialBeginCalls != 0 || service.credentialFinishCalls != 0 {
		t.Fatalf("request-controlled identifier reached service: begin=%d finish=%d", service.credentialBeginCalls, service.credentialFinishCalls)
	}
}

func TestPasskeyManagementMissingCredentialsUseOneFixedProblemCode(t *testing.T) {
	service := &recordingPasskeyHTTPService{}
	handler := newPasskeyHTTPHandlerWithVerifier(
		t,
		service,
		&recordingPasskeyQuotaLimiter{},
		passkeyHTTPVerifier{appID: "firebase-app-id"},
	)
	request := passkeyPOST(passkeyCredentialBeginPath, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized ||
		!strings.Contains(response.Body.String(), "passkey_management_reauthentication_required") ||
		service.credentialBeginCalls != 0 {
		t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), service.credentialBeginCalls)
	}
}

func TestPasskeyCredentialListAndRevokeUseOnlyVerifiedPrincipal(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	principal := identity.Principal{UID: "private-firebase-uid", AppID: "firebase-app-id", Provider: "custom", AuthMethod: "passkey-v1", PasskeyAt: now, AccountVerified: true}
	reference, err := passkey.ParseCredentialReference("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingPasskeyHTTPService{credentialSummaries: []passkey.CredentialSummary{{Reference: reference, CreatedAt: now.Add(-time.Hour), LastUsedAt: now}}}
	handler := newPasskeyHTTPHandlerWithVerifier(t, service, &recordingPasskeyQuotaLimiter{}, passkeyHTTPVerifier{principal: principal, appID: principal.AppID})

	list := httptest.NewRequest(http.MethodGet, passkeyCredentialsPath, nil)
	list.Header.Set("Origin", allowedWebOrigin)
	list.Header.Set("Authorization", "Bearer verified-passkey-token")
	list.Header.Set("X-Firebase-AppCheck", "valid-app-check")
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK || service.credentialListCalls != 1 || service.credentialRegistrationUID != principal.UID || strings.Contains(listResponse.Body.String(), principal.UID) || !strings.Contains(listResponse.Body.String(), reference.String()) {
		t.Fatalf("status=%d body=%s uid=%q calls=%d", listResponse.Code, listResponse.Body.String(), service.credentialRegistrationUID, service.credentialListCalls)
	}

	revoke := passkeyPOST(passkeyCredentialRevokePath, strings.NewReader(`{"reference":"`+reference.String()+`"}`))
	revoke.Header.Set("Authorization", "Bearer verified-passkey-token")
	revoke.Header.Set("Content-Type", "application/json")
	revokeResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusNoContent || service.credentialRevokeCalls != 1 || service.credentialRevokeReference != reference || service.credentialRegistrationUID != principal.UID {
		t.Fatalf("status=%d body=%s reference=%q uid=%q calls=%d", revokeResponse.Code, revokeResponse.Body.String(), service.credentialRevokeReference, service.credentialRegistrationUID, service.credentialRevokeCalls)
	}
}

func TestPasskeyCredentialRevokeCollapsesUnknownAndProtectsLastCredential(t *testing.T) {
	now := time.Now().UTC()
	principal := identity.Principal{UID: "private-firebase-uid", AppID: "firebase-app-id", Provider: "custom", AuthMethod: "passkey-v1", PasskeyAt: now, AccountVerified: true}
	for _, test := range []struct {
		name       string
		body       string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		{name: "unknown", body: `{"reference":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`, serviceErr: passkey.ErrCredentialNotFound, wantStatus: http.StatusNotFound, wantCode: "passkey_credential_not_found"},
		{name: "invalid reference", body: `{"reference":"raw-credential-id"}`, wantStatus: http.StatusNotFound, wantCode: "passkey_credential_not_found"},
		{name: "extra field", body: `{"reference":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","uid":"attacker"}`, wantStatus: http.StatusBadRequest, wantCode: "passkey_credential_not_found"},
		{name: "last credential", body: `{"reference":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`, serviceErr: passkey.ErrLastCredential, wantStatus: http.StatusConflict, wantCode: "passkey_last_credential"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &recordingPasskeyHTTPService{finishErr: test.serviceErr}
			handler := newPasskeyHTTPHandlerWithVerifier(t, service, &recordingPasskeyQuotaLimiter{}, passkeyHTTPVerifier{principal: principal, appID: principal.AppID})
			request := passkeyPOST(passkeyCredentialRevokePath, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer verified-passkey-token")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) || strings.Contains(response.Body.String(), "attacker") || strings.Contains(response.Body.String(), principal.UID) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
