package passkey

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/go-webauthn/webauthn/webauthn"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const firestoreLifecycleRetryCollection = "passkey_lifecycle_retry_tests"

func TestFirestoreCredentialLifecycleEmulator(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set; skipping Firestore credential lifecycle integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	projectID := firestoreLifecycleProjectID(t)
	client, err := firestore.NewClient(ctx, projectID, option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		firestoreLifecycleCleanup(t, cleanupCtx, client)
		if err := client.Close(); err != nil {
			t.Errorf("close Firestore emulator client: %v", err)
		}
	})

	store, err := NewFirestoreStore(client)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("list preserves registration order timestamps and direct canonical references", func(t *testing.T) {
		user := firestoreLifecycleUser("pk_firestore_lifecycle_list", 0x71)
		first := firestoreLifecycleCredential("firestore-list-first")
		second := firestoreLifecycleCredential("firestore-list-second")
		firstCreated := time.Date(2026, 8, 11, 5, 0, 0, 123000000, time.UTC)
		secondCreated := firstCreated.Add(2 * time.Minute)
		firstUsed := secondCreated.Add(4 * time.Minute)

		if err := store.CreateCredential(ctx, user, first, firstCreated); err != nil {
			t.Fatal(err)
		}
		if err := store.CreateCredential(ctx, user, second, secondCreated); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.FindCredential(ctx, first.ID, user.UserHandle)
		if err != nil {
			t.Fatal(err)
		}
		updated := loaded.Credential
		updated.Authenticator.SignCount++
		if err := store.UpdateCredential(ctx, first.ID, loaded.Version, updated, firstUsed); err != nil {
			t.Fatal(err)
		}

		firstReference := firestoreLifecycleReference(t, first.ID)
		secondReference := firestoreLifecycleReference(t, second.ID)
		summaries, err := store.ListCredentials(ctx, user.UID)
		if err != nil {
			t.Fatal(err)
		}
		want := []CredentialSummary{
			{Reference: firstReference, CreatedAt: firstCreated, LastUsedAt: firstUsed},
			{Reference: secondReference, CreatedAt: secondCreated, LastUsedAt: secondCreated},
		}
		if !reflect.DeepEqual(summaries, want) {
			t.Fatalf("ListCredentials() = %+v, want registration-order summaries %+v", summaries, want)
		}

		for _, reference := range []CredentialReference{firstReference, secondReference} {
			snapshot, err := client.Collection(credentialCollection).Doc(reference.String()).Get(ctx)
			if err != nil {
				t.Fatalf("canonical credential document %q: %v", reference, err)
			}
			if snapshot.Ref.ID != reference.String() {
				t.Fatalf("credential document ID = %q, want direct reference %q", snapshot.Ref.ID, reference)
			}
			doubleHashedID := documentID([]byte(reference.String()))
			if doubleHashedID == reference.String() {
				t.Fatalf("test vector unexpectedly has identical direct and double-hashed IDs: %q", reference)
			}
			if _, err := client.Collection(credentialCollection).Doc(doubleHashedID).Get(ctx); status.Code(err) != codes.NotFound {
				t.Fatalf("double-hashed credential document %q error = %v, want NotFound", doubleHashedID, err)
			}
		}
	})

	for _, operation := range []string{"create", "update"} {
		operation := operation
		for _, damage := range []string{"missing", "corrupt"} {
			damage := damage
			t.Run(operation+" rejects a "+damage+" non-target index", func(t *testing.T) {
				user := firestoreLifecycleUser(
					"pk_firestore_"+operation+"_"+damage,
					map[string]byte{"missing": 0x77, "corrupt": 0x78}[damage],
				)
				first := firestoreLifecycleCredential(operation + "-first")
				second := firestoreLifecycleCredential(operation + "-second")
				createdAt := time.Date(2026, 8, 11, 5, 30, 0, 0, time.UTC)
				if err := store.CreateCredential(ctx, user, first, createdAt); err != nil {
					t.Fatal(err)
				}
				if err := store.CreateCredential(ctx, user, second, createdAt.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				storedFirst, err := store.FindCredential(ctx, first.ID, user.UserHandle)
				if err != nil {
					t.Fatal(err)
				}
				secondReference := firestoreLifecycleReference(t, second.ID)
				secondRef := client.Collection(credentialCollection).Doc(secondReference.String())
				switch damage {
				case "missing":
					if _, err := secondRef.Delete(ctx); err != nil {
						t.Fatal(err)
					}
				case "corrupt":
					document := firestoreLifecycleCredentialDocument(t, ctx, secondRef)
					document.UID = "pk_foreign_owner"
					if _, err := secondRef.Set(ctx, document); err != nil {
						t.Fatal(err)
					}
				}

				userRef := client.Collection(userCollection).Doc(documentID([]byte(user.UID)))
				beforeUser := firestoreLifecycleUserDocument(t, ctx, userRef)
				switch operation {
				case "create":
					third := firestoreLifecycleCredential(operation + "-third")
					err = store.CreateCredential(ctx, user, third, createdAt.Add(2*time.Minute))
					if !errors.Is(err, ErrCredentialStateInvalid) {
						t.Fatalf("CreateCredential() error = %v, want ErrCredentialStateInvalid", err)
					}
					thirdReference := firestoreLifecycleReference(t, third.ID)
					if _, err := client.Collection(credentialCollection).Doc(thirdReference.String()).Get(ctx); status.Code(err) != codes.NotFound {
						t.Fatalf("rejected create wrote a credential index: %v", err)
					}
				case "update":
					updated := storedFirst.Credential
					updated.Authenticator.SignCount++
					err = store.UpdateCredential(
						ctx, first.ID, storedFirst.Version, updated,
						createdAt.Add(2*time.Minute),
					)
					if !errors.Is(err, ErrCredentialStateInvalid) {
						t.Fatalf("UpdateCredential() error = %v, want ErrCredentialStateInvalid", err)
					}
				}
				if got := firestoreLifecycleUserDocument(t, ctx, userRef); !reflect.DeepEqual(got, beforeUser) {
					t.Fatalf("rejected %s mutated user: got %#v, want %#v", operation, got, beforeUser)
				}
			})
		}
	}

	for _, damage := range []string{"missing", "corrupt"} {
		damage := damage
		t.Run("revoke fails closed when a non-target index is "+damage, func(t *testing.T) {
			user := firestoreLifecycleUser("pk_firestore_non_target_"+damage, map[string]byte{"missing": 0x72, "corrupt": 0x73}[damage])
			credentials := []webauthn.Credential{
				firestoreLifecycleCredential("firestore-" + damage + "-target"),
				firestoreLifecycleCredential("firestore-" + damage + "-healthy"),
				firestoreLifecycleCredential("firestore-" + damage + "-damaged"),
			}
			createdAt := time.Date(2026, 8, 11, 6, 0, 0, 0, time.UTC)
			for index, credential := range credentials {
				if err := store.CreateCredential(ctx, user, credential, createdAt.Add(time.Duration(index)*time.Minute)); err != nil {
					t.Fatal(err)
				}
			}

			targetReference := firestoreLifecycleReference(t, credentials[0].ID)
			damagedReference := firestoreLifecycleReference(t, credentials[2].ID)
			damagedRef := client.Collection(credentialCollection).Doc(damagedReference.String())
			switch damage {
			case "missing":
				if _, err := damagedRef.Delete(ctx); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				document := firestoreLifecycleCredentialDocument(t, ctx, damagedRef)
				document.CredentialJSON, err = json.Marshal(firestoreLifecycleCredential("different-index-raw-id"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := damagedRef.Set(ctx, document); err != nil {
					t.Fatal(err)
				}
			}

			userRef := client.Collection(userCollection).Doc(documentID([]byte(user.UID)))
			targetRef := client.Collection(credentialCollection).Doc(targetReference.String())
			beforeUser := firestoreLifecycleUserDocument(t, ctx, userRef)
			beforeTarget := firestoreLifecycleCredentialDocument(t, ctx, targetRef)
			var beforeDamaged credentialDocument
			if damage == "corrupt" {
				beforeDamaged = firestoreLifecycleCredentialDocument(t, ctx, damagedRef)
			}

			err := store.RevokeCredential(ctx, user.UID, targetReference, createdAt.Add(10*time.Minute))
			if !errors.Is(err, ErrCredentialStateInvalid) {
				t.Fatalf("RevokeCredential() error = %v, want ErrCredentialStateInvalid", err)
			}
			afterUser := firestoreLifecycleUserDocument(t, ctx, userRef)
			afterTarget := firestoreLifecycleCredentialDocument(t, ctx, targetRef)
			if !reflect.DeepEqual(afterUser, beforeUser) || !reflect.DeepEqual(afterTarget, beforeTarget) {
				t.Fatalf("failed-closed revoke mutated user or target index:\nuser before=%#v\nuser after=%#v\ntarget before=%#v\ntarget after=%#v", beforeUser, afterUser, beforeTarget, afterTarget)
			}
			if damage == "missing" {
				if _, err := damagedRef.Get(ctx); status.Code(err) != codes.NotFound {
					t.Fatalf("missing non-target index was recreated: %v", err)
				}
			} else {
				afterDamaged := firestoreLifecycleCredentialDocument(t, ctx, damagedRef)
				if !reflect.DeepEqual(afterDamaged, beforeDamaged) {
					t.Fatalf("corrupt non-target index changed after rejected revoke:\n before=%#v\n after=%#v", beforeDamaged, afterDamaged)
				}
			}
		})
	}

	t.Run("wrong owner unknown last and invalid reference are rejected without mutation", func(t *testing.T) {
		owner := firestoreLifecycleUser("pk_firestore_revoke_owner", 0x74)
		other := firestoreLifecycleUser("pk_firestore_revoke_other", 0x75)
		ownerFirst := firestoreLifecycleCredential("firestore-owner-first")
		ownerSecond := firestoreLifecycleCredential("firestore-owner-second")
		otherOnly := firestoreLifecycleCredential("firestore-other-only")
		createdAt := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
		for _, item := range []struct {
			user       *User
			credential webauthn.Credential
		}{
			{owner, ownerFirst},
			{owner, ownerSecond},
			{other, otherOnly},
		} {
			if err := store.CreateCredential(ctx, item.user, item.credential, createdAt); err != nil {
				t.Fatal(err)
			}
		}

		ownerUserRef := client.Collection(userCollection).Doc(documentID([]byte(owner.UID)))
		otherUserRef := client.Collection(userCollection).Doc(documentID([]byte(other.UID)))
		ownerFirstReference := firestoreLifecycleReference(t, ownerFirst.ID)
		otherReference := firestoreLifecycleReference(t, otherOnly.ID)
		ownerFirstRef := client.Collection(credentialCollection).Doc(ownerFirstReference.String())
		otherCredentialRef := client.Collection(credentialCollection).Doc(otherReference.String())
		beforeOwner := firestoreLifecycleUserDocument(t, ctx, ownerUserRef)
		beforeOther := firestoreLifecycleUserDocument(t, ctx, otherUserRef)
		beforeOwnerCredential := firestoreLifecycleCredentialDocument(t, ctx, ownerFirstRef)
		beforeOtherCredential := firestoreLifecycleCredentialDocument(t, ctx, otherCredentialRef)

		if err := store.RevokeCredential(ctx, other.UID, ownerFirstReference, createdAt.Add(time.Minute)); !errors.Is(err, ErrCredentialNotFound) {
			t.Fatalf("wrong-owner revoke error = %v, want ErrCredentialNotFound", err)
		}
		unknownReference := firestoreLifecycleReference(t, []byte("firestore-unregistered"))
		if err := store.RevokeCredential(ctx, owner.UID, unknownReference, createdAt.Add(time.Minute)); !errors.Is(err, ErrCredentialNotFound) {
			t.Fatalf("unknown revoke error = %v, want ErrCredentialNotFound", err)
		}
		if err := store.RevokeCredential(ctx, other.UID, otherReference, createdAt.Add(time.Minute)); !errors.Is(err, ErrLastCredential) {
			t.Fatalf("last-credential revoke error = %v, want ErrLastCredential", err)
		}
		if err := store.RevokeCredential(ctx, owner.UID, CredentialReference("invalid-reference"), createdAt.Add(time.Minute)); !errors.Is(err, ErrCredentialReferenceInvalid) {
			t.Fatalf("invalid-reference revoke error = %v, want ErrCredentialReferenceInvalid", err)
		}

		if got := firestoreLifecycleUserDocument(t, ctx, ownerUserRef); !reflect.DeepEqual(got, beforeOwner) {
			t.Fatalf("rejected revoke changed owner user: got %#v, want %#v", got, beforeOwner)
		}
		if got := firestoreLifecycleUserDocument(t, ctx, otherUserRef); !reflect.DeepEqual(got, beforeOther) {
			t.Fatalf("rejected revoke changed other user: got %#v, want %#v", got, beforeOther)
		}
		if got := firestoreLifecycleCredentialDocument(t, ctx, ownerFirstRef); !reflect.DeepEqual(got, beforeOwnerCredential) {
			t.Fatalf("rejected revoke changed owner index: got %#v, want %#v", got, beforeOwnerCredential)
		}
		if got := firestoreLifecycleCredentialDocument(t, ctx, otherCredentialRef); !reflect.DeepEqual(got, beforeOtherCredential) {
			t.Fatalf("rejected revoke changed other index: got %#v, want %#v", got, beforeOtherCredential)
		}
	})

	t.Run("concurrent revocations commit one and preserve the last credential", func(t *testing.T) {
		user := firestoreLifecycleUser("pk_firestore_concurrent", 0x76)
		first := firestoreLifecycleCredential("firestore-concurrent-first")
		second := firestoreLifecycleCredential("firestore-concurrent-second")
		createdAt := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
		if err := store.CreateCredential(ctx, user, first, createdAt); err != nil {
			t.Fatal(err)
		}
		if err := store.CreateCredential(ctx, user, second, createdAt.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		firstReference := firestoreLifecycleReference(t, first.ID)
		secondReference := firestoreLifecycleReference(t, second.ID)

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
					err:       store.RevokeCredential(ctx, user.UID, reference, createdAt.Add(2*time.Minute)),
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
				t.Fatalf("concurrent revoke %q returned unexpected error: %v", result.reference, result.err)
			}
		}
		if successCount != 1 || lastCredentialCount != 1 {
			t.Fatalf("concurrent revoke results success=%d ErrLastCredential=%d, want 1/1", successCount, lastCredentialCount)
		}

		summaries, err := store.ListCredentials(ctx, user.UID)
		if err != nil {
			t.Fatal(err)
		}
		if len(summaries) != 1 || summaries[0].Reference != preserved {
			t.Fatalf("remaining summaries = %+v, want only %q", summaries, preserved)
		}
		if _, err := client.Collection(credentialCollection).Doc(succeeded.String()).Get(ctx); status.Code(err) != codes.NotFound {
			t.Fatalf("successfully revoked direct index %q error = %v, want NotFound", succeeded, err)
		}
		if _, err := client.Collection(credentialCollection).Doc(preserved.String()).Get(ctx); err != nil {
			t.Fatalf("preserved direct index %q: %v", preserved, err)
		}
	})

	t.Run("concurrent additions at the eight credential limit commit only one", func(t *testing.T) {
		user := firestoreLifecycleUser("pk_firestore_concurrent_add", 0x79)
		createdAt := time.Date(2026, 8, 12, 2, 0, 0, 0, time.UTC)
		for index := 0; index < maxCredentials-1; index++ {
			credential := firestoreLifecycleCredential(
				fmt.Sprintf("firestore-concurrent-add-seed-%d", index),
			)
			if err := store.CreateCredential(
				ctx, user, credential, createdAt.Add(time.Duration(index)*time.Second),
			); err != nil {
				t.Fatalf("seed credential %d: %v", index, err)
			}
		}
		candidates := []webauthn.Credential{
			firestoreLifecycleCredential("firestore-concurrent-add-a"),
			firestoreLifecycleCredential("firestore-concurrent-add-b"),
		}
		results := make(chan error, len(candidates))
		start := make(chan struct{})
		var ready sync.WaitGroup
		ready.Add(len(candidates))
		for _, credential := range candidates {
			credential := credential
			go func() {
				ready.Done()
				<-start
				results <- store.CreateCredential(
					ctx, user, credential, createdAt.Add(maxCredentials*time.Second),
				)
			}()
		}
		ready.Wait()
		close(start)
		successCount := 0
		conflictCount := 0
		for range candidates {
			switch err := <-results; {
			case err == nil:
				successCount++
			case errors.Is(err, ErrCredentialConflict):
				conflictCount++
			default:
				t.Fatalf("concurrent add returned unexpected error: %v", err)
			}
		}
		summaries, err := store.ListCredentials(ctx, user.UID)
		if err != nil {
			t.Fatal(err)
		}
		if successCount != 1 || conflictCount != 1 || len(summaries) != maxCredentials {
			t.Fatalf("success=%d conflict=%d credentials=%d", successCount, conflictCount, len(summaries))
		}
	})

	t.Run("SDK retries an aborted emulator transaction", func(t *testing.T) {
		ref := client.Collection(firestoreLifecycleRetryCollection).Doc("forced-abort")
		if _, err := ref.Set(ctx, map[string]any{"value": int64(0)}); err != nil {
			t.Fatal(err)
		}
		var attempts atomic.Int32
		err := client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
			attempt := attempts.Add(1)
			snapshot, err := tx.Get(ref)
			if err != nil {
				return err
			}
			value, err := snapshot.DataAt("value")
			if err != nil {
				return err
			}
			if attempt == 1 {
				// This is an actual emulator-backed attempt: the document was read
				// before the retryable status is returned. RunTransaction rolls it
				// back, begins a retry transaction, and invokes this callback again.
				return status.Error(codes.Aborted, "exercise Firestore SDK transaction retry")
			}
			return tx.Update(ref, []firestore.Update{{Path: "value", Value: value.(int64) + 1}})
		}, firestore.MaxAttempts(3))
		if err != nil {
			t.Fatal(err)
		}
		if got := attempts.Load(); got != 2 {
			t.Fatalf("transaction callback attempts = %d, want 2", got)
		}
		snapshot, err := ref.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		value, err := snapshot.DataAt("value")
		if err != nil {
			t.Fatal(err)
		}
		if value != int64(1) {
			t.Fatalf("retried transaction value = %v, want 1", value)
		}
	})
}

func firestoreLifecycleProjectID(t *testing.T) string {
	t.Helper()
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate unique Firestore emulator project ID: %v", err)
	}
	return "passkey-lifecycle-" + hex.EncodeToString(suffix[:])
}

func firestoreLifecycleCleanup(t *testing.T, ctx context.Context, client *firestore.Client) {
	t.Helper()
	for _, collection := range []string{
		ceremonyCollection,
		userCollection,
		handleCollection,
		credentialCollection,
		deletionCollection,
		recoveryAccountCollection,
		recoveryCodeCollection,
		firestoreLifecycleRetryCollection,
	} {
		iter := client.Collection(collection).Documents(ctx)
		for {
			snapshot, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				iter.Stop()
				t.Errorf("list %s during Firestore emulator cleanup: %v", collection, err)
				break
			}
			if _, err := snapshot.Ref.Delete(ctx); err != nil {
				t.Errorf("delete %s/%s during Firestore emulator cleanup: %v", collection, snapshot.Ref.ID, err)
			}
		}
		iter.Stop()
	}
}

func firestoreLifecycleUser(uid string, handleByte byte) *User {
	return &User{UID: uid, UserHandle: bytes.Repeat([]byte{handleByte}, 64)}
}

func firestoreLifecycleCredential(rawID string) webauthn.Credential {
	return webauthn.Credential{ID: []byte(rawID), PublicKey: []byte{0x01, 0x02, 0x03}}
}

func firestoreLifecycleReference(t *testing.T, rawID []byte) CredentialReference {
	t.Helper()
	reference, err := CredentialReferenceForRawID(rawID)
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

func firestoreLifecycleUserDocument(
	t *testing.T,
	ctx context.Context,
	ref *firestore.DocumentRef,
) userDocument {
	t.Helper()
	snapshot, err := ref.Get(ctx)
	if err != nil {
		t.Fatalf("get Firestore user document %q: %v", ref.ID, err)
	}
	var document userDocument
	if err := snapshot.DataTo(&document); err != nil {
		t.Fatalf("decode Firestore user document %q: %v", ref.ID, err)
	}
	return document
}

func firestoreLifecycleCredentialDocument(
	t *testing.T,
	ctx context.Context,
	ref *firestore.DocumentRef,
) credentialDocument {
	t.Helper()
	snapshot, err := ref.Get(ctx)
	if err != nil {
		t.Fatalf("get Firestore credential document %q: %v", ref.ID, err)
	}
	var document credentialDocument
	if err := snapshot.DataTo(&document); err != nil {
		t.Fatalf("decode Firestore credential document %q: %v", ref.ID, err)
	}
	return document
}
