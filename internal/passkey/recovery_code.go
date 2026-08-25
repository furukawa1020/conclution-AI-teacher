package passkey

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	recoveryCodePrefix = "krc1_"
	recoveryCodeBytes  = 32
	RecoveryCodeTTL    = 30 * 24 * time.Hour
)

var ErrRecoveryCode = errors.New("passkey recovery code operation failed")

type RecoveryCodeResult struct {
	Code      string
	ExpiresIn int64
}

type RecoveryCodeStore interface {
	ReplaceRecoveryCode(
		context.Context,
		string,
		[sha256.Size]byte,
		time.Time,
		time.Time,
	) error
	LoadRecoveryUser(context.Context, [sha256.Size]byte, time.Time) (*User, error)
	LoadRecoveryUserByHandle(context.Context, []byte, []byte, time.Time) (*User, error)
	CreateRecoveryCredential(
		context.Context,
		*User,
		webauthn.Credential,
		[sha256.Size]byte,
		time.Time,
	) error
}

func (s *Service) IssueRecoveryCode(ctx context.Context, principalUID string) (RecoveryCodeResult, error) {
	principalUID = strings.TrimSpace(principalUID)
	store, ok := s.store.(RecoveryCodeStore)
	if principalUID == "" || !ok || store == nil {
		return RecoveryCodeResult{}, ErrRecoveryCode
	}
	material := make([]byte, recoveryCodeBytes)
	defer clear(material)
	if _, err := io.ReadFull(s.random, material); err != nil {
		return RecoveryCodeResult{}, ErrRecoveryCode
	}
	code := recoveryCodePrefix + base64.RawURLEncoding.EncodeToString(material)
	digest, err := recoveryCodeDigest(code)
	if err != nil {
		return RecoveryCodeResult{}, ErrRecoveryCode
	}
	now := s.now().UTC()
	expiresAt := now.Add(RecoveryCodeTTL)
	if err := store.ReplaceRecoveryCode(ctx, principalUID, digest, expiresAt, now); err != nil {
		return RecoveryCodeResult{}, ErrRecoveryCode
	}
	return RecoveryCodeResult{Code: code, ExpiresIn: int64(RecoveryCodeTTL / time.Second)}, nil
}

func recoveryCodeDigest(code string) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	if !strings.HasPrefix(code, recoveryCodePrefix) || len(code) != len(recoveryCodePrefix)+43 {
		return empty, ErrRecoveryCode
	}
	material, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(code, recoveryCodePrefix))
	if err != nil || len(material) != recoveryCodeBytes ||
		recoveryCodePrefix+base64.RawURLEncoding.EncodeToString(material) != code {
		clear(material)
		return empty, ErrRecoveryCode
	}
	defer clear(material)
	digestInput := append([]byte("kotae-passkey-recovery-code-v1\x00"), material...)
	digest := sha256.Sum256(digestInput)
	clear(digestInput)
	return digest, nil
}

func recoveryCodeDocumentID(digest [sha256.Size]byte) string {
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *Service) BeginRecoveryRegistration(
	ctx context.Context,
	appID, code string,
) (BeginRegistrationResult, error) {
	appID = strings.TrimSpace(appID)
	store, ok := s.store.(RecoveryCodeStore)
	digest, digestErr := recoveryCodeDigest(code)
	if appID == "" || !ok || store == nil || digestErr != nil {
		return BeginRegistrationResult{}, ErrRecoveryCode
	}
	now := s.now().UTC()
	user, err := store.LoadRecoveryUser(ctx, digest, now)
	if err != nil || user == nil || user.UID == "" || len(user.UserHandle) == 0 ||
		len(user.Credentials) == 0 || len(user.Credentials) >= maxCredentials {
		return BeginRegistrationResult{}, ErrRecoveryCode
	}
	options, session, err := s.registrations.BeginRegistration(
		credentialRegistrationUser{user: user},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			RequireResidentKey: boolPtr(true),
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	if err != nil || options == nil || session == nil || now.IsZero() {
		return BeginRegistrationResult{}, ErrRecoveryCode
	}
	session.Expires = now.Add(ceremonyTTL)
	ceremonyID, err := s.randomToken(32)
	if err != nil {
		return BeginRegistrationResult{}, ErrRecoveryCode
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return BeginRegistrationResult{}, ErrRecoveryCode
	}
	record := Ceremony{
		Purpose:            recoveryRegistrationUse,
		AppIDDigest:        digestString(appID),
		PrincipalDigest:    digestString(user.UID),
		RecoveryCodeDigest: append([]byte(nil), digest[:]...),
		UserHandle:         append([]byte(nil), user.UserHandle...),
		SessionJSON:        sessionJSON,
		ExpiresAt:          session.Expires,
		CreatedAt:          now,
	}
	if err := s.store.PutCeremony(ctx, ceremonyID, record); err != nil {
		return BeginRegistrationResult{}, ErrRecoveryCode
	}
	return BeginRegistrationResult{CeremonyID: ceremonyID, Options: options}, nil
}
