package passkey

import (
	"context"
	"crypto/subtle"
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
	TargetUID   string
	UserHandle  []byte
	SessionJSON []byte
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

type StoredCredential struct {
	User       *User
	Credential webauthn.Credential
	Version    int64
}

type Store interface {
	PutCeremony(context.Context, string, Ceremony) error
	ConsumeCeremony(
		ctx context.Context,
		ceremonyID, purpose string,
		appIDDigest []byte,
		now time.Time,
	) (Ceremony, error)
	LoadUserByUID(context.Context, string) (*User, error)
	CreateCredential(context.Context, *User, webauthn.Credential, time.Time) error
	FindCredential(context.Context, []byte, []byte) (*StoredCredential, error)
	UpdateCredential(context.Context, []byte, int64, webauthn.Credential, time.Time) error
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
		errors.Is(err, ErrConcurrentAssertion) {
		return err
	}
	return err
}
