package passkey

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestCredentialReferenceIsCanonicalSHA256Base64URL(t *testing.T) {
	rawID := []byte("credential-raw-id")
	const expected = "mHAakOSU2lqgdjbqREgg57IXDhdm-JOCXylgznYWqJE"

	reference, err := CredentialReferenceForRawID(rawID)
	if err != nil {
		t.Fatal(err)
	}
	if reference.String() != expected {
		t.Fatalf("reference = %q, want %q", reference.String(), expected)
	}
	if len(reference.String()) != 43 || strings.Contains(reference.String(), "=") {
		t.Fatalf("reference is not unpadded SHA-256 base64url: %q", reference)
	}

	parsed, err := ParseCredentialReference(expected)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != reference || parsed.String() != expected {
		t.Fatalf("parsed reference = %q, want %q", parsed, reference)
	}

	for _, invalid := range []string{
		"",
		expected[:len(expected)-1],
		expected + "A",
		expected + "=",
		" " + expected,
		expected + "\n",
		strings.Replace(expected, "-", "+", 1),
		expected[:len(expected)-1] + "!",
		// The final character has unused low bits. F decodes to the same
		// significant bits as canonical E in permissive decoders, but it is
		// not the canonical base64url encoding.
		expected[:len(expected)-1] + "F",
	} {
		if _, err := ParseCredentialReference(invalid); !errors.Is(err, ErrCredentialReferenceInvalid) {
			t.Errorf("ParseCredentialReference(%q) error = %v, want ErrCredentialReferenceInvalid", invalid, err)
		}
	}
	for _, empty := range [][]byte{nil, {}} {
		if _, err := CredentialReferenceForRawID(empty); !errors.Is(err, ErrCredentialReferenceInvalid) {
			t.Errorf("CredentialReferenceForRawID(%v) error = %v, want ErrCredentialReferenceInvalid", empty, err)
		}
	}
}

func TestMemoryCredentialListTracksCreationAndLastUseWithoutRawCredentialData(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	user := lifecycleUser("pk_lifecycle_list", 0x31)
	first := lifecycleCredential("lifecycle-first")
	second := lifecycleCredential("lifecycle-second")
	firstCreated := time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC)
	secondCreated := firstCreated.Add(2 * time.Minute)
	firstUsed := secondCreated.Add(5 * time.Minute)

	if err := store.CreateCredential(ctx, user, first, firstCreated); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateCredential(ctx, user, second, secondCreated); err != nil {
		t.Fatal(err)
	}

	firstReference := mustLifecycleCredentialReference(t, first.ID)
	secondReference := mustLifecycleCredentialReference(t, second.ID)
	before := mustLifecycleSummaries(t, store, user.UID)
	assertLifecycleSummaryTime(t, before[firstReference], firstReference, firstCreated, firstCreated)
	assertLifecycleSummaryTime(t, before[secondReference], secondReference, secondCreated, secondCreated)

	loaded, err := store.FindCredential(ctx, first.ID, user.UserHandle)
	if err != nil {
		t.Fatal(err)
	}
	updated := loaded.Credential
	updated.Authenticator.SignCount++
	if err := store.UpdateCredential(ctx, first.ID, loaded.Version, updated, firstUsed); err != nil {
		t.Fatal(err)
	}

	after := mustLifecycleSummaries(t, store, user.UID)
	assertLifecycleSummaryTime(t, after[firstReference], firstReference, firstCreated, firstUsed)
	assertLifecycleSummaryTime(t, after[secondReference], secondReference, secondCreated, secondCreated)
}

func TestMemoryCreateCredentialRejectsInvalidStateWithoutMutation(t *testing.T) {
	tests := []struct {
		name       string
		credential webauthn.Credential
		createdAt  time.Time
	}{
		{
			name: "empty credential ID",
			credential: webauthn.Credential{
				PublicKey: []byte{0x04, 0x05, 0x06},
			},
			createdAt: time.Date(2026, 8, 11, 1, 31, 0, 0, time.UTC),
		},
		{
			name:       "zero creation time",
			credential: lifecycleCredential("create-zero-time"),
			createdAt:  time.Time{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryStore()
			user := lifecycleUser("pk_lifecycle_create_invariant", 0x35)
			createdAt := time.Date(2026, 8, 11, 1, 30, 0, 0, time.UTC)
			if err := store.CreateCredential(
				ctx,
				user,
				lifecycleCredential("create-existing"),
				createdAt,
			); err != nil {
				t.Fatal(err)
			}

			before := snapshotLifecycleMemoryStore(store)
			err := store.CreateCredential(ctx, user, test.credential, test.createdAt)
			if !errors.Is(err, ErrCredentialStateInvalid) {
				t.Fatalf("CreateCredential() error = %v, want ErrCredentialStateInvalid", err)
			}
			assertLifecycleMemoryStoreUnchanged(t, store, before)
		})
	}
}

func TestMemoryUpdateCredentialRejectsInvalidStateWithoutMutation(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(webauthn.Credential) webauthn.Credential
		updatedAt func(time.Time) time.Time
	}{
		{
			name: "incoming credential ID replacement",
			mutate: func(credential webauthn.Credential) webauthn.Credential {
				credential.ID = []byte("replacement-credential-id")
				return credential
			},
			updatedAt: func(lastUsedAt time.Time) time.Time {
				return lastUsedAt.Add(time.Minute)
			},
		},
		{
			name:   "zero update time",
			mutate: func(credential webauthn.Credential) webauthn.Credential { return credential },
			updatedAt: func(time.Time) time.Time {
				return time.Time{}
			},
		},
		{
			name:   "backdated update time",
			mutate: func(credential webauthn.Credential) webauthn.Credential { return credential },
			updatedAt: func(lastUsedAt time.Time) time.Time {
				return lastUsedAt.Add(-time.Minute)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryStore()
			user := lifecycleUser("pk_lifecycle_update_invariant", 0x36)
			credential := lifecycleCredential("update-existing")
			createdAt := time.Date(2026, 8, 11, 1, 40, 0, 0, time.UTC)
			lastUsedAt := createdAt.Add(2 * time.Minute)
			if err := store.CreateCredential(ctx, user, credential, createdAt); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.FindCredential(ctx, credential.ID, user.UserHandle)
			if err != nil {
				t.Fatal(err)
			}
			usedCredential := cloneCredential(loaded.Credential)
			usedCredential.Authenticator.SignCount++
			if err := store.UpdateCredential(
				ctx,
				credential.ID,
				loaded.Version,
				usedCredential,
				lastUsedAt,
			); err != nil {
				t.Fatal(err)
			}

			current, err := store.FindCredential(ctx, credential.ID, user.UserHandle)
			if err != nil {
				t.Fatal(err)
			}
			incoming := test.mutate(cloneCredential(current.Credential))
			before := snapshotLifecycleMemoryStore(store)
			err = store.UpdateCredential(
				ctx,
				credential.ID,
				current.Version,
				incoming,
				test.updatedAt(lastUsedAt),
			)
			if !errors.Is(err, ErrCredentialStateInvalid) {
				t.Fatalf("UpdateCredential() error = %v, want ErrCredentialStateInvalid", err)
			}
			assertLifecycleMemoryStoreUnchanged(t, store, before)
		})
	}
}

func TestMemoryRevokeCredentialRejectsInvalidReferenceWithoutMutation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	user := lifecycleUser("pk_lifecycle_invalid_reference", 0x37)
	createdAt := time.Date(2026, 8, 11, 1, 50, 0, 0, time.UTC)
	if err := store.CreateCredential(
		ctx,
		user,
		lifecycleCredential("invalid-reference-existing"),
		createdAt,
	); err != nil {
		t.Fatal(err)
	}

	before := snapshotLifecycleMemoryStore(store)
	err := store.RevokeCredential(
		ctx,
		user.UID,
		CredentialReference("not-a-canonical-reference"),
		createdAt.Add(time.Minute),
	)
	if !errors.Is(err, ErrCredentialReferenceInvalid) {
		t.Fatalf("RevokeCredential() error = %v, want ErrCredentialReferenceInvalid", err)
	}
	assertLifecycleMemoryStoreUnchanged(t, store, before)
}

func TestMemoryRevokeCredentialRejectsInvalidTimeWithoutMutation(t *testing.T) {
	tests := []struct {
		name string
		now  func(time.Time) time.Time
	}{
		{name: "zero time", now: func(time.Time) time.Time { return time.Time{} }},
		{
			name: "before latest credential update",
			now:  func(createdAt time.Time) time.Time { return createdAt },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryStore()
			user := lifecycleUser("pk_lifecycle_revoke_time", 0x38)
			first := lifecycleCredential("revoke-time-first")
			second := lifecycleCredential("revoke-time-second")
			createdAt := time.Date(2026, 8, 11, 1, 55, 0, 0, time.UTC)
			if err := store.CreateCredential(ctx, user, first, createdAt); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateCredential(ctx, user, second, createdAt.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}

			before := snapshotLifecycleMemoryStore(store)
			reference := mustLifecycleCredentialReference(t, first.ID)
			err := store.RevokeCredential(ctx, user.UID, reference, test.now(createdAt))
			if !errors.Is(err, ErrCredentialStateInvalid) {
				t.Fatalf("RevokeCredential() error = %v, want ErrCredentialStateInvalid", err)
			}
			assertLifecycleMemoryStoreUnchanged(t, store, before)
		})
	}
}

func TestMemoryCredentialRevocationRejectsWrongUnknownAndLastCredentialWithoutMutation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	owner := lifecycleUser("pk_lifecycle_owner", 0x41)
	other := lifecycleUser("pk_lifecycle_other", 0x42)
	ownerFirst := lifecycleCredential("owner-first")
	ownerSecond := lifecycleCredential("owner-second")
	otherOnly := lifecycleCredential("other-only")
	now := time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)

	for _, item := range []struct {
		user       *User
		credential webauthn.Credential
	}{
		{owner, ownerFirst},
		{owner, ownerSecond},
		{other, otherOnly},
	} {
		if err := store.CreateCredential(ctx, item.user, item.credential, now); err != nil {
			t.Fatal(err)
		}
	}

	ownerFirstReference := mustLifecycleCredentialReference(t, ownerFirst.ID)
	unknownReference := mustLifecycleCredentialReference(t, []byte("not-registered"))
	otherReference := mustLifecycleCredentialReference(t, otherOnly.ID)

	if err := store.RevokeCredential(ctx, other.UID, ownerFirstReference, now.Add(time.Minute)); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("wrong-owner revoke error = %v, want ErrCredentialNotFound", err)
	}
	if err := store.RevokeCredential(ctx, owner.UID, unknownReference, now.Add(time.Minute)); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("unknown revoke error = %v, want ErrCredentialNotFound", err)
	}
	if err := store.RevokeCredential(ctx, other.UID, otherReference, now.Add(time.Minute)); !errors.Is(err, ErrLastCredential) {
		t.Fatalf("last-credential revoke error = %v, want ErrLastCredential", err)
	}

	if summaries := mustLifecycleSummaries(t, store, owner.UID); len(summaries) != 2 {
		t.Fatalf("owner summaries after rejected revokes = %d, want 2", len(summaries))
	}
	if summaries := mustLifecycleSummaries(t, store, other.UID); len(summaries) != 1 {
		t.Fatalf("other summaries after rejected revokes = %d, want 1", len(summaries))
	}
	if _, err := store.FindCredential(ctx, ownerFirst.ID, owner.UserHandle); err != nil {
		t.Fatalf("wrong-owner revoke changed owner credential: %v", err)
	}
	if _, err := store.FindCredential(ctx, otherOnly.ID, other.UserHandle); err != nil {
		t.Fatalf("last-credential rejection changed remaining credential: %v", err)
	}
}

func TestMemoryCredentialRevocationRejectsAnyDuplicateUserReferenceBeforeMutation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	user := lifecycleUser("pk_lifecycle_duplicate", 0x51)
	first := lifecycleCredential("duplicate-first")
	second := lifecycleCredential("duplicate-second")
	now := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)

	if err := store.CreateCredential(ctx, user, first, now); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateCredential(ctx, user, second, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	userKey := documentID([]byte(user.UID))
	store.mu.Lock()
	storedUser := store.users[userKey]
	storedUser.Credentials = append(storedUser.Credentials, cloneCredential(storedUser.Credentials[0]))
	credentialsBefore := len(store.credentials)
	userCredentialsBefore := len(storedUser.Credentials)
	store.mu.Unlock()

	// Revoke the unique second credential. A duplicate elsewhere in the user
	// record must still make the whole state fail closed.
	secondReference := mustLifecycleCredentialReference(t, second.ID)
	if err := store.RevokeCredential(ctx, user.UID, secondReference, now.Add(2*time.Minute)); !errors.Is(err, ErrCredentialStateInvalid) {
		t.Fatalf("duplicate-state revoke error = %v, want ErrCredentialStateInvalid", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.credentials) != credentialsBefore ||
		len(store.users[userKey].Credentials) != userCredentialsBefore {
		t.Fatalf(
			"duplicate-state rejection mutated store: credentials %d/%d, user credentials %d/%d",
			len(store.credentials), credentialsBefore,
			len(store.users[userKey].Credentials), userCredentialsBefore,
		)
	}
	if _, exists := store.credentials[secondReference.String()]; !exists {
		t.Fatal("duplicate-state rejection deleted the requested credential")
	}
}

func TestMemoryConcurrentCredentialRevocationPreservesExactlyOneCredential(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	user := lifecycleUser("pk_lifecycle_concurrent", 0x61)
	first := lifecycleCredential("concurrent-first")
	second := lifecycleCredential("concurrent-second")
	now := time.Date(2026, 8, 11, 4, 0, 0, 0, time.UTC)

	if err := store.CreateCredential(ctx, user, first, now); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateCredential(ctx, user, second, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	firstReference := mustLifecycleCredentialReference(t, first.ID)
	secondReference := mustLifecycleCredentialReference(t, second.ID)

	type revokeResult struct {
		reference CredentialReference
		err       error
	}
	results := make(chan revokeResult, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for _, reference := range []CredentialReference{firstReference, secondReference} {
		go func(reference CredentialReference) {
			ready.Done()
			<-start
			results <- revokeResult{
				reference: reference,
				err: store.RevokeCredential(
					ctx,
					user.UID,
					reference,
					now.Add(2*time.Minute),
				),
			}
		}(reference)
	}
	ready.Wait()
	close(start)

	var succeeded, preserved CredentialReference
	successCount := 0
	lastCredentialCount := 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			successCount++
			succeeded = result.reference
		case errors.Is(result.err, ErrLastCredential):
			lastCredentialCount++
			preserved = result.reference
		default:
			t.Fatalf("concurrent revoke returned unexpected error for %q: %v", result.reference, result.err)
		}
	}
	if successCount != 1 || lastCredentialCount != 1 {
		t.Fatalf(
			"concurrent revoke results: success=%d ErrLastCredential=%d, want 1/1",
			successCount,
			lastCredentialCount,
		)
	}

	summaries := mustLifecycleSummaries(t, store, user.UID)
	if len(summaries) != 1 {
		t.Fatalf("summaries after concurrent revokes = %d, want 1", len(summaries))
	}
	if _, exists := summaries[preserved]; !exists {
		t.Fatalf("remaining reference = %v, want %q", summaries, preserved)
	}
	if _, exists := summaries[succeeded]; exists {
		t.Fatalf("successfully revoked reference %q remains listed", succeeded)
	}

	credentialByReference := map[CredentialReference]webauthn.Credential{
		firstReference:  first,
		secondReference: second,
	}
	if _, err := store.FindCredential(ctx, credentialByReference[succeeded].ID, user.UserHandle); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("revoked credential lookup error = %v, want ErrCredentialNotFound", err)
	}
	if _, err := store.FindCredential(ctx, credentialByReference[preserved].ID, user.UserHandle); err != nil {
		t.Fatalf("preserved credential lookup error = %v", err)
	}
}

func lifecycleUser(uid string, handleByte byte) *User {
	return &User{
		UID:        uid,
		UserHandle: bytes.Repeat([]byte{handleByte}, 64),
	}
}

func lifecycleCredential(rawID string) webauthn.Credential {
	return webauthn.Credential{
		ID:        []byte(rawID),
		PublicKey: []byte{0x01, 0x02, 0x03},
	}
}

func mustLifecycleCredentialReference(t *testing.T, rawID []byte) CredentialReference {
	t.Helper()
	reference, err := CredentialReferenceForRawID(rawID)
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

func mustLifecycleSummaries(
	t *testing.T,
	store Store,
	uid string,
) map[CredentialReference]CredentialSummary {
	t.Helper()
	summaries, err := store.ListCredentials(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	byReference := make(map[CredentialReference]CredentialSummary, len(summaries))
	for _, summary := range summaries {
		if summary.Reference.String() == "" {
			t.Fatal("credential summary contains an empty reference")
		}
		if _, duplicate := byReference[summary.Reference]; duplicate {
			t.Fatalf("credential summaries contain duplicate reference %q", summary.Reference)
		}
		byReference[summary.Reference] = summary
	}
	return byReference
}

func assertLifecycleSummaryTime(
	t *testing.T,
	summary CredentialSummary,
	wantReference CredentialReference,
	wantCreatedAt, wantLastUsedAt time.Time,
) {
	t.Helper()
	if summary.Reference != wantReference ||
		!summary.CreatedAt.Equal(wantCreatedAt) ||
		!summary.LastUsedAt.Equal(wantLastUsedAt) {
		t.Fatalf(
			"summary = %+v, want reference=%q created=%v last-used=%v",
			summary,
			wantReference,
			wantCreatedAt,
			wantLastUsedAt,
		)
	}
}

type lifecycleMemoryStoreSnapshot struct {
	ceremonies  map[string]Ceremony
	users       map[string]*User
	credentials map[string]memoryCredential
}

func snapshotLifecycleMemoryStore(store *MemoryStore) lifecycleMemoryStoreSnapshot {
	store.mu.Lock()
	defer store.mu.Unlock()

	snapshot := lifecycleMemoryStoreSnapshot{
		ceremonies:  make(map[string]Ceremony, len(store.ceremonies)),
		users:       make(map[string]*User, len(store.users)),
		credentials: make(map[string]memoryCredential, len(store.credentials)),
	}
	for key, ceremony := range store.ceremonies {
		snapshot.ceremonies[key] = cloneCeremony(ceremony)
	}
	for key, user := range store.users {
		snapshot.users[key] = cloneUser(user)
	}
	for key, credential := range store.credentials {
		credential.UserHandle = append([]byte(nil), credential.UserHandle...)
		credential.Credential = cloneCredential(credential.Credential)
		snapshot.credentials[key] = credential
	}
	return snapshot
}

func assertLifecycleMemoryStoreUnchanged(
	t *testing.T,
	store *MemoryStore,
	want lifecycleMemoryStoreSnapshot,
) {
	t.Helper()
	got := snapshotLifecycleMemoryStore(store)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MemoryStore changed after rejected write:\n got: %#v\nwant: %#v", got, want)
	}
}
