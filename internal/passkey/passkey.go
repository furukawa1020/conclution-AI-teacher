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
	ceremonyTTL         = 5 * time.Minute
	registrationUse     = "registration-v1"
	authenticationUse   = "authentication-v1"
	passkeyAuthMethod   = "passkey-v1"
	passkeyAtClaim      = "kotae_passkey_at"
	maxExactJSONInteger = int64(1<<53 - 1)
	maxCredentialBody   = 256 * 1024
	maxCredentials      = 8
)

var (
	ErrCeremonyInvalid     = errors.New("passkey ceremony is invalid")
	ErrCredentialNotFound  = errors.New("passkey credential was not found")
	ErrCredentialConflict  = errors.New("passkey credential already exists")
	ErrAuthentication      = errors.New("passkey authentication failed")
	ErrRegistration        = errors.New("passkey registration failed")
	ErrConcurrentAssertion = errors.New("passkey credential changed concurrently")
)

type TokenMinter interface {
	MintCustomToken(context.Context, string, map[string]any) (string, error)
}

type Config struct {
	RPID          string
	RPDisplayName string
	Origin        string
	Store         Store
	TokenMinter   TokenMinter
	Now           func() time.Time
	Random        io.Reader
}

type Service struct {
	webAuthn *webauthn.WebAuthn
	store    Store
	minter   TokenMinter
	now      func() time.Time
	random   io.Reader
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
		webAuthn: wa,
		store:    cfg.Store,
		minter:   cfg.TokenMinter,
		now:      cfg.Now,
		random:   cfg.Random,
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
	options, session, err := s.webAuthn.BeginRegistration(
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
	credential, err := s.webAuthn.FinishRegistration(user, session, response)
	if err != nil || credential == nil || !credential.Flags.UserVerified {
		return FinishResult{}, ErrRegistration
	}
	verifiedAt := s.now().UTC()
	if err := s.store.CreateCredential(ctx, user, *credential, verifiedAt); err != nil {
		return FinishResult{}, ErrRegistration
	}
	return s.mint(ctx, user.UID, verifiedAt)
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
