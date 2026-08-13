package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/passkey"
)

const (
	passkeyRegistrationBeginPath      = "/api/v1/passkeys/registration:begin"
	passkeyRegistrationFinishPath     = "/api/v1/passkeys/registration:finish"
	passkeyAuthenticationBeginPath    = "/api/v1/passkeys/authentication:begin"
	passkeyAuthenticationFinishPath   = "/api/v1/passkeys/authentication:finish"
	passkeyCredentialBeginPath        = "/api/v1/passkeys/credentials/registration:begin"
	passkeyCredentialFinishPath       = "/api/v1/passkeys/credentials/registration:finish"
	passkeyCredentialsPath            = "/api/v1/passkeys/credentials"
	passkeyCredentialRevokePath       = "/api/v1/passkeys/credentials:revoke"
	passkeyAccountDeletePath          = "/api/v1/passkeys/account:delete"
	passkeyVoiceAuthorizationAge      = 5 * time.Minute
	passkeyManagementAuthorizationAge = 5 * time.Minute
	passkeyMaxCredentialBody          = 256 * 1024
)

type PasskeyService interface {
	BeginRegistration(context.Context, string) (passkey.BeginRegistrationResult, error)
	FinishRegistration(context.Context, string, string, *http.Request) (passkey.FinishResult, error)
	BeginAuthentication(context.Context, string) (passkey.BeginAuthenticationResult, error)
	FinishAuthentication(context.Context, string, string, *http.Request) (passkey.FinishResult, error)
	BeginCredentialRegistration(context.Context, string, string) (passkey.BeginRegistrationResult, error)
	FinishCredentialRegistration(context.Context, string, string, string, *http.Request) error
	ListCredentials(context.Context, string) ([]passkey.CredentialSummary, error)
	RevokeCredential(context.Context, string, passkey.CredentialReference) error
	DeleteAccount(context.Context, string) error
}

const accountDeletionConfirmation = "この仮名アカウントを完全に削除する"

type credentialSummaryResponse struct {
	Reference  string `json:"reference"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt"`
}

type credentialListResponse struct {
	Credentials []credentialSummaryResponse `json:"credentials"`
}

func (s *Server) listPasskeyCredentials(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || s.passkeys == nil || r.URL.RawQuery != "" {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_credential_management_failed", "Passkey credential management failed.")
		return
	}
	summaries, err := s.passkeys.ListCredentials(r.Context(), principal.UID)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_credential_management_failed", "Passkey credential management failed.")
		return
	}
	result := credentialListResponse{Credentials: make([]credentialSummaryResponse, 0, len(summaries))}
	for _, summary := range summaries {
		result.Credentials = append(result.Credentials, credentialSummaryResponse{
			Reference: summary.Reference.String(), CreatedAt: summary.CreatedAt.UTC().Format(time.RFC3339), LastUsedAt: summary.LastUsedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) revokePasskeyCredential(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || s.passkeys == nil || r.URL.RawQuery != "" || !isJSONContentType(r) {
		writeProblem(w, http.StatusBadRequest, "passkey_credential_not_found", "The passkey credential could not be found.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body struct {
		Reference string `json:"reference"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeProblem(w, http.StatusBadRequest, "passkey_credential_not_found", "The passkey credential could not be found.")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeProblem(w, http.StatusBadRequest, "passkey_credential_not_found", "The passkey credential could not be found.")
		return
	}
	reference, err := passkey.ParseCredentialReference(body.Reference)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "passkey_credential_not_found", "The passkey credential could not be found.")
		return
	}
	err = s.passkeys.RevokeCredential(r.Context(), principal.UID, reference)
	if errors.Is(err, passkey.ErrLastCredential) {
		writeProblem(w, http.StatusConflict, "passkey_last_credential", "The final passkey cannot be revoked.")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusNotFound, "passkey_credential_not_found", "The passkey credential could not be found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deletePasskeyAccount(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || s.passkeys == nil || r.URL.RawQuery != "" || !isJSONContentType(r) {
		writeProblem(w, http.StatusBadRequest, "passkey_account_deletion_failed", "Passkey account deletion failed.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512)
	var body struct {
		Confirmation string `json:"confirmation"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || body.Confirmation != accountDeletionConfirmation || decoder.Decode(&struct{}{}) != io.EOF {
		writeProblem(w, http.StatusBadRequest, "passkey_account_deletion_failed", "Passkey account deletion failed.")
		return
	}
	if err := s.passkeys.DeleteAccount(r.Context(), principal.UID); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_account_deletion_failed", "Passkey account deletion failed.")
		return
	}
	w.Header().Set("Clear-Site-Data", "\"cache\", \"storage\"")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) beginPasskeyCredentialRegistration(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || s.passkeys == nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_credential_registration_failed", "Passkey credential registration failed.")
		return
	}
	if !s.consumePasskeyQuota(w, r, principal.AppID) {
		return
	}
	if !emptyJSONRequest(r) {
		writeProblem(w, http.StatusBadRequest, "passkey_credential_registration_failed", "Passkey credential registration failed.")
		return
	}
	result, err := s.passkeys.BeginCredentialRegistration(
		r.Context(),
		principal.AppID,
		principal.UID,
	)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "passkey_credential_registration_failed", "Passkey credential registration failed.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) finishPasskeyCredentialRegistration(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || s.passkeys == nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_credential_registration_failed", "Passkey credential registration failed.")
		return
	}
	if !s.consumePasskeyQuota(w, r, principal.AppID) {
		return
	}
	ceremonyID, ok := exactCeremonyID(r)
	if !ok || !isJSONContentType(r) {
		writeProblem(w, http.StatusBadRequest, "passkey_credential_registration_failed", "Passkey credential registration failed.")
		return
	}
	if !boundPasskeyCredentialBody(w, r) {
		return
	}
	body, ok := managementCredentialBody(w, r)
	if !ok {
		return
	}
	defer clear(body)
	if err := s.passkeys.FinishCredentialRegistration(
		r.Context(),
		principal.AppID,
		principal.UID,
		ceremonyID,
		r,
	); err != nil {
		writeProblem(w, http.StatusBadRequest, "passkey_credential_registration_failed", "Passkey credential registration failed.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	appID, ok := appIDFromContext(r.Context())
	if !ok || s.passkeys == nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey registration is unavailable.")
		return
	}
	if !s.consumePasskeyQuota(w, r, appID) {
		return
	}
	if !emptyJSONRequest(r) {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "The request is invalid.")
		return
	}
	result, err := s.passkeys.BeginRegistration(r.Context(), appID)
	if errors.Is(err, passkey.ErrCredentialConflict) {
		writeProblem(w, http.StatusConflict, "passkey_limit_reached", "No more passkeys can be registered for this account.")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey registration is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	appID, ok := appIDFromContext(r.Context())
	if !ok || s.passkeys == nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey registration is unavailable.")
		return
	}
	if !s.consumePasskeyQuota(w, r, appID) {
		return
	}
	ceremonyID, ok := exactCeremonyID(r)
	if !ok || !isJSONContentType(r) {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "The request is invalid.")
		return
	}
	if !boundPasskeyCredentialBody(w, r) {
		return
	}
	result, err := s.passkeys.FinishRegistration(r.Context(), appID, ceremonyID, r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "passkey_registration_failed", "Passkey registration failed.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) beginPasskeyAuthentication(w http.ResponseWriter, r *http.Request) {
	appID, ok := appIDFromContext(r.Context())
	if !ok || s.passkeys == nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey authentication is unavailable.")
		return
	}
	if !s.consumePasskeyQuota(w, r, appID) {
		return
	}
	if !emptyJSONRequest(r) {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "The request is invalid.")
		return
	}
	result, err := s.passkeys.BeginAuthentication(r.Context(), appID)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey authentication is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) consumePasskeyQuota(w http.ResponseWriter, r *http.Request, appID string) bool {
	if s.passkeyClientRateLimiter == nil || s.passkeyAppCircuitBreaker == nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey authentication is unavailable.")
		return false
	}
	var clientQuotaKey string
	var ok bool
	if principal, authenticated := principalFromContext(r.Context()); authenticated {
		if principal.AppID != appID {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication or app attestation failed.")
			return false
		}
		clientQuotaKey, ok = authenticatedPasskeyQuotaKey(appID, principal.UID)
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication or app attestation failed.")
			return false
		}
	} else {
		clientQuotaKey, ok = anonymousPasskeyQuotaKeyFromContext(r.Context())
		if !ok {
			writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey authentication is unavailable.")
			return false
		}
	}
	appQuotaKey, ok := appPasskeyQuotaKey(appID)
	if !ok {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey authentication is unavailable.")
		return false
	}
	at := time.Now().UTC()
	// The high app-wide breaker protects backend capacity even when an attacker
	// rotates valid App Check tokens. It is intentionally consumed first; the
	// lower opaque client bucket then limits one attested token or verified UID.
	if err := s.passkeyAppCircuitBreaker.Consume(r.Context(), appQuotaKey, at); err != nil {
		return writePasskeyQuotaFailure(w, err)
	}
	if err := s.passkeyClientRateLimiter.Consume(r.Context(), clientQuotaKey, at); err != nil {
		return writePasskeyQuotaFailure(w, err)
	}
	return true
}

func writePasskeyQuotaFailure(w http.ResponseWriter, err error) bool {
	if errors.Is(err, guard.ErrRateLimitExceeded) {
		writeProblem(w, http.StatusTooManyRequests, "rate_limit_exceeded", "The passkey request limit has been reached.")
		return false
	}
	writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey authentication is unavailable.")
	return false
}

func (s *Server) finishPasskeyAuthentication(w http.ResponseWriter, r *http.Request) {
	appID, ok := appIDFromContext(r.Context())
	if !ok || s.passkeys == nil {
		writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey authentication is unavailable.")
		return
	}
	if !s.consumePasskeyQuota(w, r, appID) {
		return
	}
	ceremonyID, ok := exactCeremonyID(r)
	if !ok || !isJSONContentType(r) {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "The request is invalid.")
		return
	}
	if !boundPasskeyCredentialBody(w, r) {
		return
	}
	result, err := s.passkeys.FinishAuthentication(r.Context(), appID, ceremonyID, r)
	if err != nil {
		// Deliberately identical for an unknown credential, invalid signature,
		// expired challenge, wrong origin, and counter race.
		writeProblem(w, http.StatusUnauthorized, "passkey_authentication_failed", "Passkey authentication failed.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) requireAppAttestation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("X-Firebase-AppCheck")
		if len(values) != 1 || len(values[0]) > 8*1024 || strings.TrimSpace(values[0]) == "" || s.appVerifier == nil {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "App attestation failed.")
			return
		}
		appCheckToken := strings.TrimSpace(values[0])
		appID, err := s.appVerifier.VerifyApp(r.Context(), appCheckToken)
		appID = strings.TrimSpace(appID)
		if err != nil || appID == "" || len(appID) > 256 {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "App attestation failed.")
			return
		}
		anonymousQuotaKey, ok := anonymousPasskeyQuotaKey(appID, appCheckToken)
		if !ok {
			writeProblem(w, http.StatusServiceUnavailable, "passkey_unavailable", "Passkey authentication is unavailable.")
			return
		}
		ctx := context.WithValue(r.Context(), appIDContextKey{}, appID)
		ctx = context.WithValue(ctx, anonymousPasskeyQuotaKeyContextKey{}, anonymousQuotaKey)

		authHeaders := r.Header.Values("Authorization")
		if len(authHeaders) == 0 {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		if len(authHeaders) != 1 || s.verifier == nil {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication or app attestation failed.")
			return
		}
		authFields := strings.Fields(authHeaders[0])
		if len(authFields) != 2 || !strings.EqualFold(authFields[0], "Bearer") ||
			authFields[1] == "" || len(authFields[1]) > 8*1024 {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication or app attestation failed.")
			return
		}
		principal, err := s.verifier.Verify(ctx, authFields[1], appCheckToken)
		if err != nil || principal.AppID != appID || strings.TrimSpace(principal.UID) == "" {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication or app attestation failed.")
			return
		}
		ctx = context.WithValue(ctx, principalContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireFreshPasskey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.voice.RequireRecentPasskey {
			next.ServeHTTP(w, r)
			return
		}
		principal, ok := principalFromContext(r.Context())
		if !ok || !voiceAccessAuthorized(principal, time.Now().UTC(), s.voice.GuestModeEnabled) {
			writeProblem(w, http.StatusUnauthorized, "passkey_required", "Recent passkey authentication is required for voice.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requirePasskeyManagementIdentity is intentionally independent of all voice
// feature flags. It collapses every missing, invalid, non-passkey, and stale
// identity into one stable problem code before credential mutation is reached.
func (s *Server) requirePasskeyManagementIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders := r.Header.Values("Authorization")
		appCheckHeaders := r.Header.Values("X-Firebase-AppCheck")
		if len(authHeaders) != 1 || len(appCheckHeaders) != 1 || s.verifier == nil {
			writeProblem(w, http.StatusUnauthorized, "passkey_management_reauthentication_required", "Recent passkey authentication is required for credential management.")
			return
		}
		authFields := strings.Fields(authHeaders[0])
		appCheckToken := strings.TrimSpace(appCheckHeaders[0])
		if len(authFields) != 2 || !strings.EqualFold(authFields[0], "Bearer") ||
			authFields[1] == "" || len(authFields[1]) > 8*1024 ||
			appCheckToken == "" || len(appCheckToken) > 8*1024 {
			writeProblem(w, http.StatusUnauthorized, "passkey_management_reauthentication_required", "Recent passkey authentication is required for credential management.")
			return
		}
		principal, err := s.verifier.Verify(r.Context(), authFields[1], appCheckToken)
		if err != nil || !passkeyManagementAuthorized(principal, time.Now().UTC()) {
			writeProblem(w, http.StatusUnauthorized, "passkey_management_reauthentication_required", "Recent passkey authentication is required for credential management.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
	})
}

func passkeyManagementAuthorized(principal identity.Principal, now time.Time) bool {
	if now.IsZero() || strings.TrimSpace(principal.UID) == "" ||
		strings.TrimSpace(principal.AppID) == "" || !principal.AccountVerified ||
		principal.Provider != "custom" || principal.AuthMethod != "passkey-v1" ||
		principal.PasskeyAt.IsZero() {
		return false
	}
	age := now.Sub(principal.PasskeyAt)
	return age >= -30*time.Second && age <= passkeyManagementAuthorizationAge
}

func voiceAuthorized(principal identity.Principal, now time.Time) bool {
	if principal.Provider == "development" && principal.AccountVerified {
		return true
	}
	if !principal.AccountVerified || principal.Provider != "custom" || principal.AuthMethod != "passkey-v1" || principal.PasskeyAt.IsZero() {
		return false
	}
	age := now.Sub(principal.PasskeyAt)
	return age >= -30*time.Second && age <= passkeyVoiceAuthorizationAge
}

func voiceAccessAuthorized(principal identity.Principal, now time.Time, guestModeEnabled bool) bool {
	return (guestModeEnabled && principal.IsGuest()) || voiceAuthorized(principal, now)
}

func exactCeremonyID(r *http.Request) (string, bool) {
	query := r.URL.Query()
	values, exists := query["ceremonyId"]
	if !exists || len(query) != 1 || len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	return value, value != "" && len(value) <= 128
}

func emptyJSONRequest(r *http.Request) bool {
	if r.URL.RawQuery != "" || (r.ContentLength != 0 && r.ContentLength != -1) {
		return false
	}
	return r.Body == nil || r.ContentLength == 0
}

func isJSONContentType(r *http.Request) bool {
	value := strings.TrimSpace(r.Header.Get("Content-Type"))
	return value == "application/json" || strings.HasPrefix(value, "application/json;")
}

func boundPasskeyCredentialBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength > passkeyMaxCredentialBody {
		writeProblem(w, http.StatusRequestEntityTooLarge, "request_too_large", "The passkey response is too large.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, passkeyMaxCredentialBody)
	return true
}

func managementCredentialBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		clear(body)
		writeProblem(w, http.StatusBadRequest, "passkey_credential_registration_failed", "Passkey credential registration failed.")
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		clear(body)
		writeProblem(w, http.StatusBadRequest, "passkey_credential_registration_failed", "Passkey credential registration failed.")
		return nil, false
	}
	for key := range fields {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "uid", "userid", "userhandle", "accountid", "account":
			clear(body)
			writeProblem(w, http.StatusBadRequest, "passkey_credential_registration_failed", "Passkey credential registration failed.")
			return nil, false
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return body, true
}

func appIDFromContext(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(appIDContextKey{}).(string)
	return value, ok && value != ""
}

type appIDContextKey struct{}
type anonymousPasskeyQuotaKeyContextKey struct{}

func anonymousPasskeyQuotaKeyFromContext(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(anonymousPasskeyQuotaKeyContextKey{}).(string)
	return value, ok && value != ""
}

func anonymousPasskeyQuotaKey(appID, appCheckToken string) (string, bool) {
	if appID == "" || len(appID) > 256 || appCheckToken == "" || len(appCheckToken) > 8*1024 {
		return "", false
	}
	return digestPasskeyQuotaKey("anonymous-v1", appID, appCheckToken), true
}

func authenticatedPasskeyQuotaKey(appID, uid string) (string, bool) {
	uid = strings.TrimSpace(uid)
	if appID == "" || len(appID) > 256 || uid == "" || len(uid) > 256 {
		return "", false
	}
	return digestPasskeyQuotaKey("authenticated-v1", appID, uid), true
}

func appPasskeyQuotaKey(appID string) (string, bool) {
	appID = strings.TrimSpace(appID)
	if appID == "" || len(appID) > 256 {
		return "", false
	}
	return digestPasskeyQuotaKey("app-circuit-v1", appID), true
}

func digestPasskeyQuotaKey(scope string, parts ...string) string {
	digest := sha256.New()
	writePasskeyQuotaPart(digest, scope)
	for _, part := range parts {
		writePasskeyQuotaPart(digest, part)
	}
	return "passkey:v2:" + hex.EncodeToString(digest.Sum(nil))
}

type passkeyQuotaHash interface {
	Write([]byte) (int, error)
}

func writePasskeyQuotaPart(digest passkeyQuotaHash, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}
