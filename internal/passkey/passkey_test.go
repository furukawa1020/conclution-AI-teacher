package passkey

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type recordingMinter struct {
	uid    string
	claims map[string]any
}

type successfulAccountDataCleaner struct{}

func (successfulAccountDataCleaner) DisableAndDelete(context.Context, string) error { return nil }

func (m *recordingMinter) DeleteAccount(_ context.Context, uid string) error {
	m.uid = uid
	return nil
}

func TestDeleteAccountRemovesPasskeysAndIsIdempotentForAuthRetry(t *testing.T) {
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	user := lifecycleUser("pk_delete_account", 0x71)
	if err := store.CreateCredential(context.Background(), user, lifecycleCredential("delete-account"), now); err != nil {
		t.Fatal(err)
	}
	minter := &recordingMinter{}
	service := newTestService(t, store, minter, now.Add(time.Minute))
	if err := service.DeleteAccount(context.Background(), user.UID); err != nil {
		t.Fatal(err)
	}
	if minter.uid != user.UID {
		t.Fatalf("deleted uid = %q", minter.uid)
	}
	if _, err := store.LoadUserByUID(context.Background(), user.UID); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("user remains: %v", err)
	}
	if err := service.DeleteAccount(context.Background(), user.UID); err != nil {
		t.Fatalf("retry = %v", err)
	}
}

type retryingAccountDeleter struct{ calls int }

func (d *retryingAccountDeleter) DeleteAccount(context.Context, string) error {
	d.calls++
	if d.calls == 1 {
		return errors.New("temporary auth failure")
	}
	return nil
}

func TestDeleteAccountRetriesAuthAfterDataDeletionWithoutCredentials(t *testing.T) {
	now := time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	user := lifecycleUser("pk_delete_retry", 0x72)
	if err := store.CreateCredential(context.Background(), user, lifecycleCredential("delete-retry"), now); err != nil {
		t.Fatal(err)
	}
	deleter := &retryingAccountDeleter{}
	service, err := New(Config{RPID: "kotae-ai.web.app", Origin: "https://kotae-ai.web.app", Store: store, TokenMinter: &recordingMinter{}, AccountDataCleaner: successfulAccountDataCleaner{}, AccountDeleter: deleter, Now: func() time.Time { return now.Add(time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAccount(context.Background(), user.UID); !errors.Is(err, ErrAccountDeletion) {
		t.Fatalf("first = %v", err)
	}
	if _, err := store.LoadUserByUID(context.Background(), user.UID); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("data remains: %v", err)
	}
	if err := service.DeleteAccount(context.Background(), user.UID); err != nil {
		t.Fatalf("retry = %v", err)
	}
	if deleter.calls != 2 {
		t.Fatalf("calls = %d", deleter.calls)
	}
}

func (m *recordingMinter) MintCustomToken(
	_ context.Context,
	uid string,
	claims map[string]any,
) (string, error) {
	m.uid = uid
	m.claims = claims
	return "firebase-custom-token", nil
}

func newTestService(t *testing.T, store Store, minter TokenMinter, now time.Time) *Service {
	t.Helper()
	service, err := New(Config{
		RPID:               "kotae-ai.web.app",
		RPDisplayName:      "コタエーAI",
		Origin:             "https://kotae-ai.web.app",
		Store:              store,
		TokenMinter:        minter,
		AccountDataCleaner: successfulAccountDataCleaner{},
		Now:                func() time.Time { return now },
		Random:             bytes.NewReader(bytes.Repeat([]byte{0x5a}, 1024)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestBeginRegistrationCreatesBoundFiveMinuteDiscoverableCeremony(t *testing.T) {
	now := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(t, store, &recordingMinter{}, now)

	result, err := service.BeginRegistration(context.Background(), "firebase-app-id")
	if err != nil {
		t.Fatal(err)
	}
	if result.CeremonyID == "" || result.Options == nil {
		t.Fatalf("result = %+v", result)
	}
	selection := result.Options.Response.AuthenticatorSelection
	if selection.RequireResidentKey == nil || !*selection.RequireResidentKey ||
		selection.ResidentKey != protocol.ResidentKeyRequirementRequired ||
		selection.UserVerification != protocol.VerificationRequired {
		t.Fatalf("authenticator selection = %+v", selection)
	}
	if result.Options.Response.Attestation != protocol.PreferNoAttestation {
		t.Fatalf("attestation = %q", result.Options.Response.Attestation)
	}

	store.mu.Lock()
	record, exists := store.ceremonies[documentID([]byte(result.CeremonyID))]
	store.mu.Unlock()
	if !exists {
		t.Fatal("server-side ceremony was not stored")
	}
	if len(record.UserHandle) != 64 || record.TargetUID[:3] != "pk_" {
		t.Fatalf("target identity = %q / %d-byte handle", record.TargetUID, len(record.UserHandle))
	}
	if !record.ExpiresAt.Equal(now.Add(5*time.Minute)) ||
		!constantTimeEqual(record.AppIDDigest, digestString("firebase-app-id")) {
		t.Fatalf("ceremony binding = %+v", record)
	}
}

func TestBeginAuthenticationRequiresDiscoverableCredentialAndUV(t *testing.T) {
	now := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(t, store, &recordingMinter{}, now)

	result, err := service.BeginAuthentication(context.Background(), "firebase-app-id")
	if err != nil {
		t.Fatal(err)
	}
	if result.Options.Response.UserVerification != protocol.VerificationRequired {
		t.Fatalf("user verification = %q", result.Options.Response.UserVerification)
	}
	if len(result.Options.Response.AllowedCredentials) != 0 {
		t.Fatalf("discoverable login unexpectedly constrained credentials: %+v", result.Options.Response.AllowedCredentials)
	}
	store.mu.Lock()
	record := store.ceremonies[documentID([]byte(result.CeremonyID))]
	store.mu.Unlock()
	if record.Purpose != authenticationUse || !record.ExpiresAt.Equal(now.Add(ceremonyTTL)) {
		t.Fatalf("authentication ceremony = %+v", record)
	}
}

func TestCeremonyIsSingleUseAndBoundEvenOnFailure(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC)
	record := Ceremony{
		Purpose:     registrationUse,
		AppIDDigest: digestString("right-app"),
		ExpiresAt:   now.Add(ceremonyTTL),
	}
	if err := store.PutCeremony(context.Background(), "opaque-id", record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeCeremony(
		context.Background(), "opaque-id", registrationUse, digestString("wrong-app"), now,
	); !errors.Is(err, ErrCeremonyInvalid) {
		t.Fatalf("wrong binding error = %v", err)
	}
	if _, err := store.ConsumeCeremony(
		context.Background(), "opaque-id", registrationUse, digestString("right-app"), now,
	); !errors.Is(err, ErrCeremonyInvalid) {
		t.Fatalf("replayed ceremony error = %v", err)
	}
}

func TestMemoryStoreBoundsCredentialsAndAtomicallyVersionsCounter(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	user := &User{UID: "pk_user", UserHandle: bytes.Repeat([]byte{7}, 64)}
	for index := 0; index < maxCredentials; index++ {
		credential := webauthn.Credential{ID: []byte{byte(index + 1)}, PublicKey: []byte{1, 2, 3}}
		if err := store.CreateCredential(ctx, user, credential, now); err != nil {
			t.Fatalf("credential %d: %v", index, err)
		}
	}
	if err := store.CreateCredential(
		ctx,
		user,
		webauthn.Credential{ID: []byte{99}, PublicKey: []byte{1}},
		now,
	); !errors.Is(err, ErrCredentialConflict) {
		t.Fatalf("ninth credential error = %v", err)
	}

	stored, err := store.FindCredential(ctx, []byte{1}, user.UserHandle)
	if err != nil {
		t.Fatal(err)
	}
	updated := stored.Credential
	updated.Authenticator.SignCount = 4
	if err := store.UpdateCredential(ctx, updated.ID, stored.Version, updated, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateCredential(ctx, updated.ID, stored.Version, updated, now); !errors.Is(err, ErrConcurrentAssertion) {
		t.Fatalf("stale version error = %v", err)
	}
}

func TestMintUsesOnlyExplicitPasskeyAssuranceClaims(t *testing.T) {
	minter := &recordingMinter{}
	verifiedAt := time.Date(2026, 8, 1, 4, 5, 6, 789, time.UTC)
	service := newTestService(t, NewMemoryStore(), minter, verifiedAt)
	result, err := service.mint(context.Background(), "pk_account", verifiedAt)
	if err != nil {
		t.Fatal(err)
	}
	if result.CustomToken == "" || result.AuthMethod != passkeyAuthMethod || minter.uid != "pk_account" {
		t.Fatalf("mint result = %+v, uid = %q", result, minter.uid)
	}
	if minter.claims["kotae_account_verified"] != true ||
		minter.claims["kotae_authn"] != passkeyAuthMethod ||
		minter.claims[passkeyAtClaim] != verifiedAt.Unix() ||
		len(minter.claims) != 3 {
		t.Fatalf("claims = %#v", minter.claims)
	}
}

func TestMintRejectsInvalidPasskeyVerificationTime(t *testing.T) {
	minter := &recordingMinter{}
	service := newTestService(t, NewMemoryStore(), minter, time.Now())

	for _, verifiedAt := range []time.Time{{}, time.Unix(-1, 0), time.Unix(maxExactJSONInteger+1, 0)} {
		if _, err := service.mint(context.Background(), "pk_account", verifiedAt); err == nil {
			t.Fatalf("mint accepted verification time %v", verifiedAt)
		}
	}
	if minter.claims != nil {
		t.Fatalf("invalid verification time reached token minter: %#v", minter.claims)
	}
}
