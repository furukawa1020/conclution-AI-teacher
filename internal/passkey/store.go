package passkey

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

type User struct {
	UID         string
	UserHandle  []byte
	Credentials []webauthn.Credential
}

func (u *User) WebAuthnID() []byte { return append([]byte(nil), u.UserHandle...) }

func (u *User) WebAuthnName() string { return u.UID }

func (u *User) WebAuthnDisplayName() string { return "コタエーAI利用者" }

func (u *User) WebAuthnCredentials() []webauthn.Credential {
	return append([]webauthn.Credential(nil), u.Credentials...)
}

type Ceremony struct {
	Purpose     string
	AppIDDigest []byte
	// PrincipalDigest binds authenticated management ceremonies to the
	// verified principal without persisting the raw Firebase UID in the
	// short-lived ceremony document.
	PrincipalDigest []byte
	// RecoveryCodeDigest binds a recovery registration ceremony to one
	// pre-issued capability without storing its raw code.
	RecoveryCodeDigest []byte
	TargetUID          string
	UserHandle         []byte
	SessionJSON        []byte
	ExpiresAt          time.Time
	CreatedAt          time.Time
}

type StoredCredential struct {
	User       *User
	Credential webauthn.Credential
	Version    int64
}

// CredentialReference is a stable one-way identifier that avoids returning a
// raw WebAuthn credential ID. It is not an authorization or bearer-secret
// boundary and may be returned only inside an authorized account-management
// flow.
type CredentialReference string

// CredentialSummary is the deliberately minimal credential-management view.
// It never contains credential material, a user handle, or authenticator data.
type CredentialSummary struct {
	Reference  CredentialReference
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// CredentialReferenceForRawID derives the canonical reference used by the
// credential store without exposing the raw credential ID.
func CredentialReferenceForRawID(rawID []byte) (CredentialReference, error) {
	if len(rawID) == 0 {
		return "", ErrCredentialReferenceInvalid
	}
	return credentialReference(rawID), nil
}

// ParseCredentialReference accepts only the canonical, unpadded base64url
// encoding of a SHA-256 digest.
func ParseCredentialReference(value string) (CredentialReference, error) {
	if len(value) != 43 {
		return "", ErrCredentialReferenceInvalid
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(decoded) != value {
		return "", ErrCredentialReferenceInvalid
	}
	return CredentialReference(value), nil
}

func (reference CredentialReference) String() string { return string(reference) }

type Store interface {
	PutCeremony(context.Context, string, Ceremony) error
	ConsumeCeremony(
		ctx context.Context,
		ceremonyID, purpose string,
		appIDDigest []byte,
		now time.Time,
	) (Ceremony, error)
	ConsumeRegistrationCeremony(
		ctx context.Context,
		ceremonyID string,
		appIDDigest []byte,
		now time.Time,
	) (Ceremony, error)
	LoadUserByUID(context.Context, string) (*User, error)
	CreateCredential(context.Context, *User, webauthn.Credential, time.Time) error
	FindCredential(context.Context, []byte, []byte) (*StoredCredential, error)
	UpdateCredential(context.Context, []byte, int64, webauthn.Credential, time.Time) error
	ListCredentials(context.Context, string) ([]CredentialSummary, error)
	RevokeCredential(context.Context, string, CredentialReference, time.Time) error
	DeleteAccountData(context.Context, string, time.Time) error
}

func credentialReference(raw []byte) CredentialReference {
	digest := sha256.Sum256(raw)
	return CredentialReference(base64.RawURLEncoding.EncodeToString(digest[:]))
}

func documentID(raw []byte) string { return credentialReference(raw).String() }

func credentialReferences(credentials []webauthn.Credential) ([]CredentialReference, error) {
	if len(credentials) == 0 || len(credentials) > maxCredentials {
		return nil, ErrCredentialStateInvalid
	}
	references := make([]CredentialReference, 0, len(credentials))
	seen := make(map[CredentialReference]struct{}, len(credentials))
	for _, credential := range credentials {
		reference, err := CredentialReferenceForRawID(credential.ID)
		if err != nil {
			return nil, ErrCredentialStateInvalid
		}
		if _, duplicate := seen[reference]; duplicate {
			return nil, ErrCredentialStateInvalid
		}
		seen[reference] = struct{}{}
		references = append(references, reference)
	}
	return references, nil
}

func newCredentialSummary(
	reference CredentialReference,
	createdAt, lastUsedAt time.Time,
) (CredentialSummary, error) {
	parsed, err := ParseCredentialReference(reference.String())
	if err != nil || parsed != reference || createdAt.IsZero() || lastUsedAt.IsZero() ||
		lastUsedAt.Before(createdAt) {
		return CredentialSummary{}, ErrCredentialStateInvalid
	}
	return CredentialSummary{
		Reference:  reference,
		CreatedAt:  createdAt.UTC(),
		LastUsedAt: lastUsedAt.UTC(),
	}, nil
}

func validateCredentialCreation(
	user *User,
	credential webauthn.Credential,
	now time.Time,
) error {
	if user == nil || user.UID == "" || len(user.UserHandle) == 0 ||
		len(credential.ID) == 0 || now.IsZero() {
		return ErrCredentialStateInvalid
	}
	return nil
}

func validateCredentialUpdate(
	rawID []byte,
	credential webauthn.Credential,
	now time.Time,
) error {
	if len(rawID) == 0 || len(credential.ID) == 0 || now.IsZero() ||
		!constantTimeEqual(rawID, credential.ID) {
		return ErrCredentialStateInvalid
	}
	return nil
}

func validateCredentialTimeProgression(
	reference CredentialReference,
	createdAt, lastUsedAt, nextUsedAt time.Time,
) error {
	if _, err := newCredentialSummary(reference, createdAt, lastUsedAt); err != nil ||
		validateLifecycleTimeProgression(createdAt, lastUsedAt, nextUsedAt) != nil {
		return ErrCredentialStateInvalid
	}
	return nil
}

func validateLifecycleTimeProgression(
	createdAt, updatedAt, nextUpdatedAt time.Time,
) error {
	if createdAt.IsZero() || updatedAt.IsZero() || nextUpdatedAt.IsZero() ||
		updatedAt.Before(createdAt) || nextUpdatedAt.Before(updatedAt) {
		return ErrCredentialStateInvalid
	}
	return nil
}

func ceremonyMatches(
	record Ceremony,
	purpose string,
	appIDDigest []byte,
	now time.Time,
) bool {
	if record.Purpose != purpose || !record.ExpiresAt.After(now) ||
		!constantTimeEqual(record.AppIDDigest, appIDDigest) {
		return false
	}
	return true
}

func constantTimeEqual(left, right []byte) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare(left, right) == 1
}

func normalizeStoreError(err error) error {
	if errors.Is(err, ErrCredentialNotFound) ||
		errors.Is(err, ErrCredentialConflict) ||
		errors.Is(err, ErrConcurrentAssertion) ||
		errors.Is(err, ErrCredentialReferenceInvalid) ||
		errors.Is(err, ErrCredentialStateInvalid) ||
		errors.Is(err, ErrLastCredential) {
		return err
	}
	return err
}
