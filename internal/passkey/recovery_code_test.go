package passkey

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func recoveryCodeService(t *testing.T) (*Service, *MemoryStore, string, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	uid := "recovery-code-user"
	store := NewMemoryStore()
	user := lifecycleUser(uid, 0x31)
	if err := store.CreateCredential(context.Background(), user, lifecycleCredential("recovery-code"), now); err != nil {
		t.Fatal(err)
	}
	random := append(bytes.Repeat([]byte{0x41}, recoveryCodeBytes), bytes.Repeat([]byte{0x42}, recoveryCodeBytes)...)
	service, err := New(Config{
		RPID:        "kotae-ai.web.app",
		Origin:      "https://kotae-ai.web.app",
		Store:       store,
		TokenMinter: &recordingMinter{},
		Now:         func() time.Time { return now.Add(time.Minute) },
		Random:      bytes.NewReader(random),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, uid, now.Add(time.Minute)
}

func TestRecoveryCodeIssueStoresOnlyDigestAndReissueRevokesOldCode(t *testing.T) {
	service, store, uid, issuedAt := recoveryCodeService(t)
	first, err := service.IssueRecoveryCode(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.Code, recoveryCodePrefix) || first.ExpiresIn != int64(RecoveryCodeTTL/time.Second) {
		t.Fatalf("first result = %+v", first)
	}
	firstDigest, err := recoveryCodeDigest(first.Code)
	if err != nil {
		t.Fatal(err)
	}
	accountKey := documentID([]byte(uid))
	record := store.recovery[accountKey]
	if record.Digest != firstDigest || !record.IssuedAt.Equal(issuedAt) || !record.ExpiresAt.Equal(issuedAt.Add(RecoveryCodeTTL)) {
		t.Fatalf("stored recovery = %+v", record)
	}
	if _, ok := store.recoveryByCode[first.Code]; ok {
		t.Fatal("raw recovery code was used as a store key")
	}

	second, err := service.IssueRecoveryCode(context.Background(), uid)
	if err != nil || second.Code == first.Code {
		t.Fatalf("second result=%+v err=%v", second, err)
	}
	secondDigest, _ := recoveryCodeDigest(second.Code)
	if _, exists := store.recoveryByCode[recoveryCodeDocumentID(firstDigest)]; exists {
		t.Fatal("old recovery digest remained active")
	}
	if owner := store.recoveryByCode[recoveryCodeDocumentID(secondDigest)]; owner != accountKey {
		t.Fatalf("new recovery owner = %q", owner)
	}

	if err := store.DeleteAccountData(context.Background(), uid, issuedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(store.recovery) != 0 || len(store.recoveryByCode) != 0 {
		t.Fatal("account deletion retained recovery state")
	}
}

func TestRecoveryCodeIssueFailsClosedForUnknownAccountOrRandomFailure(t *testing.T) {
	service, store, _, _ := recoveryCodeService(t)
	if _, err := service.IssueRecoveryCode(context.Background(), "foreign-user"); !errors.Is(err, ErrRecoveryCode) {
		t.Fatalf("unknown account error = %v", err)
	}
	if len(store.recovery) != 0 || len(store.recoveryByCode) != 0 {
		t.Fatal("unknown account mutated recovery state")
	}
	service.random = bytes.NewReader([]byte{1})
	if _, err := service.IssueRecoveryCode(context.Background(), "recovery-code-user"); !errors.Is(err, ErrRecoveryCode) {
		t.Fatalf("short randomness error = %v", err)
	}
	if len(store.recovery) != 0 || len(store.recoveryByCode) != 0 {
		t.Fatal("random failure mutated recovery state")
	}
}

func TestRecoveryCodeParserRequiresCanonicalFiniteCode(t *testing.T) {
	valid := recoveryCodePrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x21}, recoveryCodeBytes))
	if _, err := recoveryCodeDigest(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", valid + "=", "KRC1_" + strings.TrimPrefix(valid, recoveryCodePrefix), valid[:len(valid)-1], recoveryCodePrefix + strings.Repeat("_", 43)} {
		if _, err := recoveryCodeDigest(invalid); !errors.Is(err, ErrRecoveryCode) {
			t.Fatalf("invalid code accepted: %q err=%v", invalid, err)
		}
	}
}

func TestBeginRecoveryRegistrationBindsCodeAppAccountAndFiveMinuteCeremony(t *testing.T) {
	service, store, uid, issuedAt := recoveryCodeService(t)
	code, err := service.IssueRecoveryCode(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	registrations := &recordingRegistrationCeremonies{}
	service.registrations = registrations
	result, err := service.BeginRecoveryRegistration(context.Background(), "firebase-app-id", code.Code)
	if err != nil {
		t.Fatal(err)
	}
	if result.CeremonyID == "" || result.Options == nil || registrations.beginCalls != 1 {
		t.Fatalf("result=%+v beginCalls=%d", result, registrations.beginCalls)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(uid)) || bytes.Contains(encoded, []byte(code.Code)) {
		t.Fatalf("recovery begin exposed account capability: %s", encoded)
	}
	digest, _ := recoveryCodeDigest(code.Code)
	store.mu.Lock()
	record := cloneCeremony(store.ceremonies[documentID([]byte(result.CeremonyID))])
	store.mu.Unlock()
	if record.Purpose != recoveryRegistrationUse || record.TargetUID != "" ||
		!constantTimeEqual(record.AppIDDigest, digestString("firebase-app-id")) ||
		!constantTimeEqual(record.PrincipalDigest, digestString(uid)) ||
		!constantTimeEqual(record.RecoveryCodeDigest, digest[:]) ||
		!record.ExpiresAt.Equal(issuedAt.Add(ceremonyTTL)) {
		t.Fatalf("recovery ceremony binding = %+v", record)
	}
}

func TestBeginRecoveryRegistrationRejectsExpiredReissuedAndMalformedCodes(t *testing.T) {
	service, store, uid, issuedAt := recoveryCodeService(t)
	old, err := service.IssueRecoveryCode(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	newCode, err := service.IssueRecoveryCode(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	service.registrations = &recordingRegistrationCeremonies{}
	for _, code := range []string{"", old.Code, newCode.Code + "="} {
		if _, err := service.BeginRecoveryRegistration(context.Background(), "firebase-app-id", code); !errors.Is(err, ErrRecoveryCode) {
			t.Fatalf("invalid recovery code accepted: err=%v", err)
		}
	}
	accountKey := documentID([]byte(uid))
	store.mu.Lock()
	recovery := store.recovery[accountKey]
	recovery.ExpiresAt = issuedAt
	store.recovery[accountKey] = recovery
	store.mu.Unlock()
	if _, err := service.BeginRecoveryRegistration(context.Background(), "firebase-app-id", newCode.Code); !errors.Is(err, ErrRecoveryCode) {
		t.Fatalf("expired recovery code accepted: %v", err)
	}
}
