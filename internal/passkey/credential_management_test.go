package passkey

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type recordingRegistrationCeremonies struct {
	mu           sync.Mutex
	beginCalls   int
	finishCalls  int
	beginName    string
	beginDisplay string
	beginUserID  []byte
	finishErr    error
}

func (r *recordingRegistrationCeremonies) BeginRegistration(
	user webauthn.User,
	_ ...webauthn.RegistrationOption,
) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beginCalls++
	r.beginName = user.WebAuthnName()
	r.beginDisplay = user.WebAuthnDisplayName()
	r.beginUserID = append([]byte(nil), user.WebAuthnID()...)
	return &protocol.CredentialCreation{}, &webauthn.SessionData{
		Challenge: "server-generated-test-challenge",
		UserID:    user.WebAuthnID(),
	}, nil
}

func (r *recordingRegistrationCeremonies) FinishRegistration(
	_ webauthn.User,
	_ webauthn.SessionData,
	request *http.Request,
) (*webauthn.Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishCalls++
	if r.finishErr != nil {
		return nil, r.finishErr
	}
	id := strings.TrimSpace(request.Header.Get("X-Test-Credential-ID"))
	if id == "" {
		return nil, errors.New("test credential ID is required")
	}
	return &webauthn.Credential{
		ID:        []byte(id),
		PublicKey: []byte("test-public-key"),
		Flags: webauthn.CredentialFlags{
			UserPresent:  true,
			UserVerified: true,
		},
	}, nil
}

func seedCredentialManagementUser(
	t *testing.T,
	store *MemoryStore,
	count int,
	now time.Time,
) *User {
	t.Helper()
	user := &User{
		UID:        "private-firebase-uid",
		UserHandle: bytes.Repeat([]byte{0x71}, 64),
	}
	for index := 0; index < count; index++ {
		credential := webauthn.Credential{
			ID:        []byte{0x40, byte(index + 1)},
			PublicKey: []byte{1, 2, 3},
		}
		if err := store.CreateCredential(context.Background(), user, credential, now); err != nil {
			t.Fatalf("seed credential %d: %v", index, err)
		}
	}
	return user
}

func TestBeginCredentialRegistrationBindsExistingPrincipalWithoutExposingUID(t *testing.T) {
	now := time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)
	store := NewMemoryStore()
	user := seedCredentialManagementUser(t, store, 1, now)
	service := newTestService(t, store, &recordingMinter{}, now)

	result, err := service.BeginCredentialRegistration(
		context.Background(),
		"firebase-app-id",
		user.UID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.CeremonyID == "" || result.Options == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.Options.Response.User.Name != "kotae-account" ||
		result.Options.Response.User.DisplayName != "コタエーAI利用者" {
		t.Fatalf("public WebAuthn user = %+v", result.Options.Response.User)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	existingID := base64.StdEncoding.EncodeToString([]byte{0x40, 1})
	if bytes.Contains(encoded, []byte(user.UID)) ||
		bytes.Contains(encoded, []byte(existingID)) ||
		bytes.Contains(encoded, []byte("customToken")) {
		t.Fatalf("credential begin response exposed protected account material: %s", encoded)
	}

	store.mu.Lock()
	record := cloneCeremony(store.ceremonies[documentID([]byte(result.CeremonyID))])
	store.mu.Unlock()
	if record.Purpose != credentialAdditionUse || record.TargetUID != "" ||
		!constantTimeEqual(record.AppIDDigest, digestString("firebase-app-id")) ||
		!constantTimeEqual(record.PrincipalDigest, digestString(user.UID)) ||
		!constantTimeEqual(record.UserHandle, user.UserHandle) ||
		!record.ExpiresAt.Equal(now.Add(ceremonyTTL)) {
		t.Fatalf("credential ceremony binding = %+v", record)
	}
	if bytes.Contains(record.PrincipalDigest, []byte(user.UID)) {
		t.Fatal("ceremony persisted a raw principal UID")
	}
}

func TestCredentialRegistrationRejectsPrincipalAndAppSwapAndConsumesCeremony(t *testing.T) {
	for _, test := range []struct {
		name      string
		finishApp string
		finishUID string
	}{
		{name: "principal swap", finishApp: "firebase-app-id", finishUID: "attacker-uid"},
		{name: "app swap", finishApp: "other-app-id", finishUID: "private-firebase-uid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)
			store := NewMemoryStore()
			user := seedCredentialManagementUser(t, store, 1, now)
			registration := &recordingRegistrationCeremonies{}
			service := newTestService(t, store, &recordingMinter{}, now)
			service.registrations = registration
			result, err := service.BeginCredentialRegistration(context.Background(), "firebase-app-id", user.UID)
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader(`{"id":"credential"}`))
			request.Header.Set("X-Test-Credential-ID", "new-credential")
			if err := service.FinishCredentialRegistration(
				context.Background(), test.finishApp, test.finishUID, result.CeremonyID, request,
			); !errors.Is(err, ErrCredentialRegistration) {
				t.Fatalf("swapped binding error = %v", err)
			}
			if err := service.FinishCredentialRegistration(
				context.Background(), "firebase-app-id", user.UID, result.CeremonyID, request,
			); !errors.Is(err, ErrCredentialRegistration) {
				t.Fatalf("replay error = %v", err)
			}
			if registration.finishCalls != 0 {
				t.Fatalf("swapped ceremony reached WebAuthn finish %d times", registration.finishCalls)
			}
		})
	}
}

func TestCredentialRegistrationExpiresAtFiveMinutesAndCannotReplay(t *testing.T) {
	now := time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)
	store := NewMemoryStore()
	user := seedCredentialManagementUser(t, store, 1, now)
	registration := &recordingRegistrationCeremonies{}
	service := newTestService(t, store, &recordingMinter{}, now)
	service.registrations = registration
	service.now = func() time.Time { return now }

	result, err := service.BeginCredentialRegistration(context.Background(), "firebase-app-id", user.UID)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(ceremonyTTL)
	request, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader(`{"id":"credential"}`))
	request.Header.Set("X-Test-Credential-ID", "new-credential")
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.FinishCredentialRegistration(
			context.Background(), "firebase-app-id", user.UID, result.CeremonyID, request,
		); !errors.Is(err, ErrCredentialRegistration) {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
	}
	if registration.finishCalls != 0 {
		t.Fatalf("expired ceremony reached WebAuthn finish %d times", registration.finishCalls)
	}
}

func TestFinishCredentialRegistrationAddsOneCredentialAndRejectsReplay(t *testing.T) {
	now := time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)
	store := NewMemoryStore()
	user := seedCredentialManagementUser(t, store, 1, now)
	registration := &recordingRegistrationCeremonies{}
	service := newTestService(t, store, &recordingMinter{}, now)
	service.registrations = registration

	result, err := service.BeginCredentialRegistration(context.Background(), "firebase-app-id", user.UID)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader(`{"id":"credential"}`))
	request.Header.Set("X-Test-Credential-ID", "new-credential")
	if err := service.FinishCredentialRegistration(
		context.Background(), "firebase-app-id", user.UID, result.CeremonyID, request,
	); err != nil {
		t.Fatal(err)
	}
	if err := service.FinishCredentialRegistration(
		context.Background(), "firebase-app-id", user.UID, result.CeremonyID, request,
	); !errors.Is(err, ErrCredentialRegistration) {
		t.Fatalf("replay error = %v", err)
	}
	loaded, err := store.LoadUserByUID(context.Background(), user.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Credentials) != 2 || registration.finishCalls != 1 {
		t.Fatalf("credentials=%d finishCalls=%d", len(loaded.Credentials), registration.finishCalls)
	}
}

func TestConcurrentCredentialRegistrationAtLimitFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)
	store := NewMemoryStore()
	user := seedCredentialManagementUser(t, store, maxCredentials-1, now)
	registration := &recordingRegistrationCeremonies{}
	service := newTestService(t, store, &recordingMinter{}, now)
	service.registrations = registration
	service.random = bytes.NewReader(append(
		bytes.Repeat([]byte{0x31}, 32),
		bytes.Repeat([]byte{0x32}, 32)...,
	))

	results := make([]BeginRegistrationResult, 2)
	for index := range results {
		var err error
		results[index], err = service.BeginCredentialRegistration(
			context.Background(), "firebase-app-id", user.UID,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	var wait sync.WaitGroup
	errorsByAttempt := make([]error, 2)
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			request, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader(`{"id":"credential"}`))
			request.Header.Set("X-Test-Credential-ID", "concurrent-credential-"+string(rune('a'+index)))
			errorsByAttempt[index] = service.FinishCredentialRegistration(
				context.Background(), "firebase-app-id", user.UID, results[index].CeremonyID, request,
			)
		}(index)
	}
	wait.Wait()
	successes := 0
	failures := 0
	for _, err := range errorsByAttempt {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrCredentialRegistration) {
			failures++
		} else {
			t.Fatalf("unexpected concurrent error = %v", err)
		}
	}
	loaded, err := store.LoadUserByUID(context.Background(), user.UID)
	if err != nil {
		t.Fatal(err)
	}
	if successes != 1 || failures != 1 || len(loaded.Credentials) != maxCredentials {
		t.Fatalf("successes=%d failures=%d credentials=%d", successes, failures, len(loaded.Credentials))
	}
	if _, err := service.BeginCredentialRegistration(
		context.Background(), "firebase-app-id", user.UID,
	); !errors.Is(err, ErrCredentialRegistration) {
		t.Fatalf("ninth begin error = %v", err)
	}
}
