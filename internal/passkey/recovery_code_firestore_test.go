package passkey

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFirestoreRecoveryCodeReissueAndAccountDeletionAreAtomic(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := firestore.NewClient(ctx, firestoreLifecycleProjectID(t), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		firestoreLifecycleCleanup(t, context.Background(), client)
		_ = client.Close()
	})
	store, err := NewFirestoreStore(client)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC)
	uid := "firestore-recovery-user"
	user := firestoreLifecycleUser(uid, 0x29)
	if err := store.CreateCredential(ctx, user, firestoreLifecycleCredential("recovery-primary"), now); err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{
		RPID:        "kotae-ai.web.app",
		Origin:      "https://kotae-ai.web.app",
		Store:       store,
		TokenMinter: &recordingMinter{},
		Now:         func() time.Time { return now.Add(time.Minute) },
		Random: bytes.NewReader(append(
			append(
				bytes.Repeat([]byte{0x61}, recoveryCodeBytes),
				bytes.Repeat([]byte{0x62}, recoveryCodeBytes)...,
			),
			bytes.Repeat([]byte{0x63}, 32)...,
		)),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.IssueRecoveryCode(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.IssueRecoveryCode(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	service.registrations = &recordingRegistrationCeremonies{}
	begin, err := service.BeginRecoveryRegistration(ctx, "firebase-app-id", second.Code)
	if err != nil || begin.CeremonyID == "" || begin.Options == nil {
		t.Fatalf("begin=%+v err=%v", begin, err)
	}
	firstDigest, _ := recoveryCodeDigest(first.Code)
	secondDigest, _ := recoveryCodeDigest(second.Code)
	if _, err := client.Collection(recoveryCodeCollection).Doc(recoveryCodeDocumentID(firstDigest)).Get(ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("old recovery code index err=%v", err)
	}
	accountKey := documentID([]byte(uid))
	accountSnapshot, err := client.Collection(recoveryAccountCollection).Doc(accountKey).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	indexSnapshot, err := client.Collection(recoveryCodeCollection).Doc(recoveryCodeDocumentID(secondDigest)).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ceremonySnapshot, err := client.Collection(ceremonyCollection).Doc(documentID([]byte(begin.CeremonyID))).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	serialized := fmt.Sprint(accountSnapshot.Data(), indexSnapshot.Data(), ceremonySnapshot.Data())
	for _, forbidden := range []string{uid, first.Code, second.Code} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("recovery documents exposed forbidden value %q", forbidden)
		}
	}
	if err := store.DeleteAccountData(ctx, uid, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []*firestore.DocumentRef{
		client.Collection(recoveryAccountCollection).Doc(accountKey),
		client.Collection(recoveryCodeCollection).Doc(recoveryCodeDocumentID(secondDigest)),
	} {
		if _, err := ref.Get(ctx); status.Code(err) != codes.NotFound {
			t.Fatalf("account deletion retained %s: %v", ref.Path, err)
		}
	}
}

func TestFirestoreExpiredRecoveryIndexTTLGapDoesNotBlockReissue(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := firestore.NewClient(ctx, firestoreLifecycleProjectID(t), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		firestoreLifecycleCleanup(t, context.Background(), client)
		_ = client.Close()
	})
	store, err := NewFirestoreStore(client)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)
	uid := "firestore-expired-recovery-user"
	if err := store.CreateCredential(ctx, firestoreLifecycleUser(uid, 0x39), firestoreLifecycleCredential("expired-recovery-primary"), now); err != nil {
		t.Fatal(err)
	}
	oldDigest := sha256.Sum256([]byte("expired recovery code"))
	accountKey := documentID([]byte(uid))
	expired := recoveryAccountDocument{
		SchemaVersion: 1,
		CodeDigest:    append([]byte(nil), oldDigest[:]...),
		IssuedAt:      now.Add(-RecoveryCodeTTL),
		ExpiresAt:     now,
	}
	if _, err := client.Collection(recoveryAccountCollection).Doc(accountKey).Set(ctx, expired); err != nil {
		t.Fatal(err)
	}
	newDigest := sha256.Sum256([]byte("replacement recovery code"))
	if err := store.ReplaceRecoveryCode(ctx, uid, newDigest, now.Add(time.Minute).Add(RecoveryCodeTTL), now.Add(time.Minute)); err != nil {
		t.Fatalf("replace after TTL removed the expired index: %v", err)
	}
	if _, err := client.Collection(recoveryCodeCollection).Doc(recoveryCodeDocumentID(newDigest)).Get(ctx); err != nil {
		t.Fatal(err)
	}
}
