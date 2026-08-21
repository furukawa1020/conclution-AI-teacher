// Package passkey implements the server side of the WebAuthn ceremonies used
// to create an explicitly requested pseudonymous passkey account and to sign
// back in to that same account with a discoverable credential.
//
// A successful ceremony proves control of a registered authenticator with
// user verification. It is not voice biometrics and is not proof of a legal or
// civil identity.
package passkey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	ceremonyTTL             = 5 * time.Minute
	registrationUse         = "registration-v1"
	credentialAdditionUse   = "credential-addition-v1"
	recoveryRegistrationUse = "recovery-registration-v1"
	authenticationUse       = "authentication-v1"
	passkeyAuthMethod       = "passkey-v1"
	passkeyAtClaim          = "kotae_passkey_at"
	maxExactJSONInteger     = int64(1<<53 - 1)
	maxCredentialBody       = 256 * 1024
	maxCredentials          = 8
)

var (
	ErrCeremonyInvalid            = errors.New("passkey ceremony is invalid")
	ErrCredentialNotFound         = errors.New("passkey credential was not found")
	ErrCredentialConflict         = errors.New("passkey credential already exists")
	ErrCredentialReferenceInvalid = errors.New("passkey credential reference is invalid")
	ErrCredentialStateInvalid     = errors.New("passkey credential state is invalid")
	ErrLastCredential             = errors.New("last passkey credential cannot be revoked")
	ErrAuthentication             = errors.New("passkey authentication failed")
	ErrRegistration               = errors.New("passkey registration failed")
	ErrCredentialRegistration     = errors.New("passkey credential registration failed")
	ErrConcurrentAssertion        = errors.New("passkey credential changed concurrently")
	ErrAccountDeletion            = errors.New("passkey account deletion failed")
)

type TokenMinter interface {
	MintCustomToken(context.Context, string, map[string]any) (string, error)
}

type AccountDeleter interface {
	DeleteAccount(context.Context, string) error
}

type Config struct {
	RPID               string
	RPDisplayName      string
	Origin             string
	Store              Store
	TokenMinter        TokenMinter
	AccountDataCleaner AccountDataCleaner
	AccountDeleter     AccountDeleter
	Now                func() time.Time
	Random             io.Reader
}

type Service struct {
	webAuthn      *webauthn.WebAuthn
	registrations registrationCeremonies
	store         Store
	minter        TokenMinter
	cleaner       AccountDataCleaner
	deleter       AccountDeleter
	now           func() time.Time
	random        io.Reader
}

type registrationCeremonies interface {
	BeginRegistration(
		webauthn.User,
		...webauthn.RegistrationOption,
	) (*protocol.CredentialCreation, *webauthn.SessionData, error)
	FinishRegistration(
		webauthn.User,
		webauthn.SessionData,
		*http.Request,
	) (*webauthn.Credential, error)
}

type BeginRegistrationResult struct {
	CeremonyID string                       `json:"ceremonyId"`
	Options    *protocol.CredentialCreation `json:"options"`
}

type BeginAuthenticationResult struct {
	CeremonyID string                        `json:"ceremonyId"`
	Options    *protocol.CredentialAssertion `json:"options"`
}

type FinishResult struct {
	CustomToken string `json:"customToken"`
	AuthMethod  string `json:"authMethod"`
}

func New(cfg Config) (*Service, error) {
	if strings.TrimSpace(cfg.RPID) == "" || strings.TrimSpace(cfg.Origin) == "" {
		return nil, errors.New("passkey RP ID and origin are required")
	}
	if cfg.Store == nil || cfg.TokenMinter == nil {
		return nil, errors.New("passkey store and token minter are required")
	}
	if cfg.AccountDeleter == nil {
		cfg.AccountDeleter, _ = cfg.TokenMinter.(AccountDeleter)
	}
	if strings.TrimSpace(cfg.RPDisplayName) == "" {
		cfg.RPDisplayName = "コタエーAI"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}

	requireResident := true
	wa, err := webauthn.New(&webauthn.Config{
		RPID:                  cfg.RPID,
		RPDisplayName:         cfg.RPDisplayName,
		RPOrigins:             []string{cfg.Origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: &requireResident,
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize WebAuthn: %w", err)
	}
	return &Service{
		webAuthn:      wa,
		registrations: wa,
		store:         cfg.Store,
		minter:        cfg.TokenMinter,
		cleaner:       cfg.AccountDataCleaner,
		deleter:       cfg.AccountDeleter,
		now:           cfg.Now,
		random:        cfg.Random,
	}, nil
}

func (s *Service) BeginRegistration(
	ctx context.Context,
	appID string,
) (BeginRegistrationResult, error) {
	if strings.TrimSpace(appID) == "" {
		return BeginRegistrationResult{}, ErrRegistration
	}
	uid, err := s.randomToken(24)
	if err != nil {
		return BeginRegistrationResult{}, fmt.Errorf("generate passkey UID: %w", err)
	}
	handle, err := s.randomBytes(64)
	if err != nil {
		return BeginRegistrationResult{}, fmt.Errorf("generate passkey user handle: %w", err)
	}
	user := &User{UID: "pk_" + uid, UserHandle: handle}
	options, session, err := s.registrations.BeginRegistration(
		user,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			RequireResidentKey: boolPtr(true),
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	if err != nil {
		return BeginRegistrationResult{}, fmt.Errorf("begin passkey registration: %w", err)
	}
	issuedAt := s.now().UTC()
	session.Expires = issuedAt.Add(ceremonyTTL)
	ceremonyID, err := s.randomToken(32)
	if err != nil {
		return BeginRegistrationResult{}, fmt.Errorf("generate passkey ceremony ID: %w", err)
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return BeginRegistrationResult{}, fmt.Errorf("encode passkey session: %w", err)
	}
	record := Ceremony{
		Purpose:     registrationUse,
		AppIDDigest: digestString(appID),
		TargetUID:   user.UID,
		UserHandle:  append([]byte(nil), user.UserHandle...),
		SessionJSON: sessionJSON,
		ExpiresAt:   session.Expires,
		CreatedAt:   issuedAt,
	}
	if err := s.store.PutCeremony(ctx, ceremonyID, record); err != nil {
		return BeginRegistrationResult{}, fmt.Errorf("store passkey ceremony: %w", err)
	}
	return BeginRegistrationResult{CeremonyID: ceremonyID, Options: options}, nil
}

func (s *Service) FinishRegistration(
	ctx context.Context,
	appID string,
	ceremonyID string,
	response *http.Request,
) (FinishResult, error) {
	if response == nil || strings.TrimSpace(appID) == "" {
		return FinishResult{}, ErrRegistration
	}
	record, err := s.store.ConsumeCeremony(
		ctx,
		strings.TrimSpace(ceremonyID),
		registrationUse,
		digestString(appID),
		s.now().UTC(),
	)
	if err != nil {
		return FinishResult{}, ErrRegistration
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(record.SessionJSON, &session); err != nil {
		return FinishResult{}, ErrRegistration
	}

	user, err := s.registrationUser(ctx, record)
	if err != nil {
		return FinishResult{}, ErrRegistration
	}
	limitCredentialRequest(response)
	credential, err := s.registrations.FinishRegistration(user, session, response)
	if err != nil || credential == nil || !credential.Flags.UserVerified {
		return FinishResult{}, ErrRegistration
	}
	verifiedAt := s.now().UTC()
	if err := s.store.CreateCredential(ctx, user, *credential, verifiedAt); err != nil {
		return FinishResult{}, ErrRegistration
	}
	return s.mint(ctx, user.UID, verifiedAt)
}

// BeginCredentialRegistration starts an additional credential ceremony for
// an already verified account. The caller must supply the UID and app ID from
// the verified principal, never from request-controlled identifiers.
func (s *Service) BeginCredentialRegistration(
	ctx context.Context,
	appID, principalUID string,
) (BeginRegistrationResult, error) {
	appID = strings.TrimSpace(appID)
	principalUID = strings.TrimSpace(principalUID)
	if appID == "" || principalUID == "" {
		return BeginRegistrationResult{}, ErrCredentialRegistration
	}
	user, err := s.store.LoadUserByUID(ctx, principalUID)
	if err != nil || user == nil || user.UID != principalUID ||
		len(user.UserHandle) == 0 || len(user.Credentials) == 0 ||
		len(user.Credentials) >= maxCredentials {
		return BeginRegistrationResult{}, ErrCredentialRegistration
	}
	ceremonyUser := credentialRegistrationUser{user: user}
	options, session, err := s.registrations.BeginRegistration(
		ceremonyUser,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			RequireResidentKey: boolPtr(true),
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	if err != nil || options == nil || session == nil {
		return BeginRegistrationResult{}, ErrCredentialRegistration
	}
	issuedAt := s.now().UTC()
	if issuedAt.IsZero() {
		return BeginRegistrationResult{}, ErrCredentialRegistration
	}
	session.Expires = issuedAt.Add(ceremonyTTL)
	ceremonyID, err := s.randomToken(32)
	if err != nil {
		return BeginRegistrationResult{}, ErrCredentialRegistration
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return BeginRegistrationResult{}, ErrCredentialRegistration
	}
	record := Ceremony{
		Purpose:         credentialAdditionUse,
		AppIDDigest:     digestString(appID),
		PrincipalDigest: digestString(principalUID),
		UserHandle:      append([]byte(nil), user.UserHandle...),
		SessionJSON:     sessionJSON,
		ExpiresAt:       session.Expires,
		CreatedAt:       issuedAt,
	}
	if err := s.store.PutCeremony(ctx, ceremonyID, record); err != nil {
		return BeginRegistrationResult{}, ErrCredentialRegistration
	}
	return BeginRegistrationResult{CeremonyID: ceremonyID, Options: options}, nil
}

// FinishCredentialRegistration consumes the principal-bound ceremony before
// any detailed validation. A substituted principal or malformed response can
// therefore never leave a replayable management ceremony behind.
func (s *Service) FinishCredentialRegistration(
	ctx context.Context,
	appID, principalUID, ceremonyID string,
	response *http.Request,
) error {
	appID = strings.TrimSpace(appID)
	principalUID = strings.TrimSpace(principalUID)
	if response == nil || appID == "" || principalUID == "" {
		return ErrCredentialRegistration
	}
	record, err := s.store.ConsumeCeremony(
		ctx,
		strings.TrimSpace(ceremonyID),
		credentialAdditionUse,
		digestString(appID),
		s.now().UTC(),
	)
	if err != nil || record.TargetUID != "" ||
		!constantTimeEqual(record.PrincipalDigest, digestString(principalUID)) {
		return ErrCredentialRegistration
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(record.SessionJSON, &session); err != nil ||
		!constantTimeEqual(session.UserID, record.UserHandle) {
		return ErrCredentialRegistration
	}
	user, err := s.store.LoadUserByUID(ctx, principalUID)
	if err != nil || user == nil || user.UID != principalUID ||
		len(user.Credentials) == 0 || len(user.Credentials) >= maxCredentials ||
		!constantTimeEqual(user.UserHandle, record.UserHandle) {
		return ErrCredentialRegistration
	}
	limitCredentialRequest(response)
	credential, err := s.registrations.FinishRegistration(
		credentialRegistrationUser{user: user},
		session,
		response,
	)
	if err != nil || credential == nil || !credential.Flags.UserVerified ||
		credential.Authenticator.CloneWarning {
		return ErrCredentialRegistration
	}
	if err := s.store.CreateCredential(ctx, user, *credential, s.now().UTC()); err != nil {
		return ErrCredentialRegistration
	}
	return nil
}

// ListCredentials exposes only the minimal, non-credential management view
// after the HTTP layer has established a recent principal-bound passkey proof.
func (s *Service) ListCredentials(ctx context.Context, principalUID string) ([]CredentialSummary, error) {
	principalUID = strings.TrimSpace(principalUID)
	if principalUID == "" {
		return nil, ErrCredentialStateInvalid
	}
	return s.store.ListCredentials(ctx, principalUID)
}

// RevokeCredential delegates the atomic last-credential invariant to Store.
func (s *Service) RevokeCredential(ctx context.Context, principalUID string, reference CredentialReference) error {
	principalUID = strings.TrimSpace(principalUID)
	if principalUID == "" {
		return ErrCredentialNotFound
	}
	return s.store.RevokeCredential(ctx, principalUID, reference, s.now().UTC())
}

func (s *Service) DeleteAccount(ctx context.Context, principalUID string) error {
	principalUID = strings.TrimSpace(principalUID)
	if principalUID == "" || s.cleaner == nil || s.deleter == nil {
		return ErrAccountDeletion
	}
	if err := s.cleaner.DisableAndDelete(ctx, principalUID); err != nil {
		return ErrAccountDeletion
	}
	if err := s.store.DeleteAccountData(ctx, principalUID, s.now().UTC()); err != nil {
		return ErrAccountDeletion
	}
	if err := s.deleter.DeleteAccount(ctx, principalUID); err != nil {
		return ErrAccountDeletion
	}
	return nil
}

// credentialRegistrationUser keeps the protocol-required opaque user handle
// while preventing the raw Firebase UID from entering WebAuthn options.
type credentialRegistrationUser struct {
	user *User
}

func (u credentialRegistrationUser) WebAuthnID() []byte {
	return append([]byte(nil), u.user.UserHandle...)
}

func (credentialRegistrationUser) WebAuthnName() string { return "kotae-account" }

func (credentialRegistrationUser) WebAuthnDisplayName() string { return "コタエーAI利用者" }

func (u credentialRegistrationUser) WebAuthnCredentials() []webauthn.Credential {
	return u.user.WebAuthnCredentials()
}

func (s *Service) BeginAuthentication(
	ctx context.Context,
	appID string,
) (BeginAuthenticationResult, error) {
	if strings.TrimSpace(appID) == "" {
		return BeginAuthenticationResult{}, ErrAuthentication
	}
	options, session, err := s.webAuthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		return BeginAuthenticationResult{}, fmt.Errorf("begin passkey authentication: %w", err)
	}
	issuedAt := s.now().UTC()
	session.Expires = issuedAt.Add(ceremonyTTL)
	ceremonyID, err := s.randomToken(32)
	if err != nil {
		return BeginAuthenticationResult{}, fmt.Errorf("generate passkey ceremony ID: %w", err)
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return BeginAuthenticationResult{}, fmt.Errorf("encode passkey session: %w", err)
	}
	if err := s.store.PutCeremony(ctx, ceremonyID, Ceremony{
		Purpose:     authenticationUse,
		AppIDDigest: digestString(appID),
		SessionJSON: sessionJSON,
		ExpiresAt:   session.Expires,
		CreatedAt:   issuedAt,
	}); err != nil {
		return BeginAuthenticationResult{}, fmt.Errorf("store passkey ceremony: %w", err)
	}
	return BeginAuthenticationResult{CeremonyID: ceremonyID, Options: options}, nil
}

func (s *Service) FinishAuthentication(
	ctx context.Context,
	appID, ceremonyID string,
	response *http.Request,
) (FinishResult, error) {
	if response == nil || strings.TrimSpace(appID) == "" {
		return FinishResult{}, ErrAuthentication
	}
	record, err := s.store.ConsumeCeremony(
		ctx,
		strings.TrimSpace(ceremonyID),
		authenticationUse,
		digestString(appID),
		s.now().UTC(),
	)
	if err != nil {
		return FinishResult{}, ErrAuthentication
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(record.SessionJSON, &session); err != nil {
		return FinishResult{}, ErrAuthentication
	}

	var loaded *StoredCredential
	limitCredentialRequest(response)
	user, credential, err := s.webAuthn.FinishPasskeyLogin(
		func(rawID, userHandle []byte) (webauthn.User, error) {
			stored, lookupErr := s.store.FindCredential(ctx, rawID, userHandle)
			if lookupErr != nil {
				return nil, ErrAuthentication
			}
			loaded = stored
			return stored.User, nil
		},
		session,
		response,
	)
	if err != nil || user == nil || credential == nil || loaded == nil ||
		!credential.Flags.UserVerified || credential.Authenticator.CloneWarning {
		return FinishResult{}, ErrAuthentication
	}
	account, ok := user.(*User)
	if !ok || account.UID != loaded.User.UID {
		return FinishResult{}, ErrAuthentication
	}
	verifiedAt := s.now().UTC()
	if err := s.store.UpdateCredential(
		ctx,
		credential.ID,
		loaded.Version,
		*credential,
		verifiedAt,
	); err != nil {
		return FinishResult{}, ErrAuthentication
	}
	return s.mint(ctx, account.UID, verifiedAt)
}

func (s *Service) registrationUser(ctx context.Context, record Ceremony) (*User, error) {
	stored, err := s.store.LoadUserByUID(ctx, record.TargetUID)
	if err == nil {
		if subtle.ConstantTimeCompare(stored.UserHandle, record.UserHandle) != 1 {
			return nil, ErrRegistration
		}
		return stored, nil
	}
	if !errors.Is(err, ErrCredentialNotFound) {
		return nil, err
	}
	return &User{
		UID:        record.TargetUID,
		UserHandle: append([]byte(nil), record.UserHandle...),
	}, nil
}

func (s *Service) mint(ctx context.Context, uid string, verifiedAt time.Time) (FinishResult, error) {
	verifiedAtSeconds := verifiedAt.UTC().Unix()
	if verifiedAt.IsZero() || verifiedAtSeconds <= 0 || verifiedAtSeconds > maxExactJSONInteger {
		return FinishResult{}, errors.New("valid passkey verification time is required")
	}
	token, err := s.minter.MintCustomToken(ctx, uid, map[string]any{
		"kotae_account_verified": true,
		"kotae_authn":            passkeyAuthMethod,
		passkeyAtClaim:           verifiedAtSeconds,
	})
	if err != nil {
		return FinishResult{}, fmt.Errorf("mint passkey Firebase token: %w", err)
	}
	return FinishResult{CustomToken: token, AuthMethod: passkeyAuthMethod}, nil
}

func (s *Service) randomToken(size int) (string, error) {
	value, err := s.randomBytes(size)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *Service) randomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return nil, err
	}
	return value, nil
}

func digestString(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

func boolPtr(value bool) *bool { return &value }

func limitCredentialRequest(request *http.Request) {
	if request.Body != nil {
		request.Body = http.MaxBytesReader(nil, request.Body, maxCredentialBody)
	}
}
