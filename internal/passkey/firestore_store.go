package passkey

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/go-webauthn/webauthn/webauthn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	ceremonyCollection   = "passkey_ceremonies_v1"
	userCollection       = "passkey_users_v1"
	handleCollection     = "passkey_handles_v1"
	credentialCollection = "passkey_credentials_v1"
)

type FirestoreStore struct {
	client *firestore.Client
}

type ceremonyDocument struct {
	Purpose     string    `firestore:"purpose"`
	AppIDDigest []byte    `firestore:"appIdDigest"`
	TargetUID   string    `firestore:"targetUid,omitempty"`
	UserHandle  []byte    `firestore:"userHandle,omitempty"`
	SessionJSON []byte    `firestore:"session"`
	ExpiresAt   time.Time `firestore:"expiresAt"`
	CreatedAt   time.Time `firestore:"createdAt"`
}

type userDocument struct {
	UID             string    `firestore:"uid"`
	UserHandle      []byte    `firestore:"userHandle"`
	CredentialsJSON [][]byte  `firestore:"credentials"`
	CreatedAt       time.Time `firestore:"createdAt"`
	UpdatedAt       time.Time `firestore:"updatedAt"`
}

type credentialDocument struct {
	UID            string    `firestore:"uid"`
	UserHandle     []byte    `firestore:"userHandle"`
	CredentialJSON []byte    `firestore:"credential"`
	Version        int64     `firestore:"version"`
	CreatedAt      time.Time `firestore:"createdAt"`
	UpdatedAt      time.Time `firestore:"updatedAt"`
}

type handleDocument struct {
	UID       string    `firestore:"uid"`
	CreatedAt time.Time `firestore:"createdAt"`
}

func NewFirestoreStore(client *firestore.Client) (*FirestoreStore, error) {
	if client == nil {
		return nil, errors.New("Firestore client is required")
	}
	return &FirestoreStore{client: client}, nil
}

func (s *FirestoreStore) PutCeremony(ctx context.Context, id string, record Ceremony) error {
	ref := s.client.Collection(ceremonyCollection).Doc(documentID([]byte(id)))
	_, err := ref.Create(ctx, ceremonyDocumentFrom(record))
	return err
}

func (s *FirestoreStore) ConsumeCeremony(
	ctx context.Context,
	id, purpose string,
	appIDDigest []byte,
	now time.Time,
) (record Ceremony, err error) {
	ref := s.client.Collection(ceremonyCollection).Doc(documentID([]byte(id)))
	found := false
	valid := false
	err = s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, getErr := tx.Get(ref)
		if getErr != nil {
			if status.Code(getErr) == codes.NotFound {
				return nil
			}
			return getErr
		}
		found = true
		var document ceremonyDocument
		if getErr := snapshot.DataTo(&document); getErr != nil {
			return getErr
		}
		record = document.toCeremony()
		valid = ceremonyMatches(record, purpose, appIDDigest, now)
		return tx.Delete(ref)
	})
	if err != nil {
		return Ceremony{}, err
	}
	if !found || !valid {
		return Ceremony{}, ErrCeremonyInvalid
	}
	return record, nil
}

func (s *FirestoreStore) LoadUserByUID(ctx context.Context, uid string) (*User, error) {
	ref := s.client.Collection(userCollection).Doc(documentID([]byte(uid)))
	snapshot, err := ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, ErrCredentialNotFound
	}
	if err != nil {
		return nil, err
	}
	var document userDocument
	if err := snapshot.DataTo(&document); err != nil {
		return nil, err
	}
	if document.UID != uid {
		return nil, ErrCredentialNotFound
	}
	return decodeUser(document)
}

func (s *FirestoreStore) ListCredentials(
	ctx context.Context,
	uid string,
) (summaries []CredentialSummary, err error) {
	userRef := s.client.Collection(userCollection).Doc(documentID([]byte(uid)))
	err = s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		// RunTransaction may invoke the callback more than once. Never retain a
		// partial result from a prior attempt.
		summaries = nil

		userSnapshot, getErr := tx.Get(userRef)
		if status.Code(getErr) == codes.NotFound {
			return ErrCredentialNotFound
		}
		if getErr != nil {
			return getErr
		}
		var user userDocument
		if err := userSnapshot.DataTo(&user); err != nil {
			return credentialStateInvalid("decode passkey user document", err)
		}
		if user.UID != uid {
			return ErrCredentialNotFound
		}
		credentials, references, err := decodeLifecycleCredentials(user)
		if err != nil {
			return err
		}

		credentialRefs := lifecycleCredentialDocumentRefs(s.client, references)
		credentialSnapshots, getErr := tx.GetAll(credentialRefs)
		if getErr != nil {
			return getErr
		}
		if len(credentialSnapshots) != len(references) {
			return credentialStateInvalid("credential index result count is inconsistent", nil)
		}

		attemptSummaries := make([]CredentialSummary, 0, len(references))
		for index, snapshot := range credentialSnapshots {
			if snapshot == nil || !snapshot.Exists() {
				return credentialStateInvalid("credential index document is missing", nil)
			}
			var document credentialDocument
			if err := snapshot.DataTo(&document); err != nil {
				return credentialStateInvalid("decode credential index document", err)
			}
			summary, err := validateLifecycleCredentialDocument(
				document,
				user,
				references[index],
				credentials[index].ID,
				ErrCredentialStateInvalid,
			)
			if err != nil {
				return err
			}
			attemptSummaries = append(attemptSummaries, summary)
		}
		summaries = attemptSummaries
		return nil
	}, firestore.ReadOnly)
	if err != nil {
		return nil, normalizeStoreError(err)
	}
	return summaries, nil
}

func (s *FirestoreStore) CreateCredential(
	ctx context.Context,
	user *User,
	credential webauthn.Credential,
	now time.Time,
) error {
	if err := validateCredentialCreation(user, credential, now); err != nil {
		return err
	}
	now = now.UTC()
	credentialJSON, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	reference, err := CredentialReferenceForRawID(credential.ID)
	if err != nil {
		return ErrCredentialStateInvalid
	}
	userRef := s.client.Collection(userCollection).Doc(documentID([]byte(user.UID)))
	handleRef := s.client.Collection(handleCollection).Doc(documentID(user.UserHandle))
	credentialRef := s.client.Collection(credentialCollection).Doc(reference.String())

	return normalizeStoreError(s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		userSnapshot, userErr := tx.Get(userRef)
		newUser := status.Code(userErr) == codes.NotFound
		if userErr != nil && !newUser {
			return userErr
		}
		credentialSnapshot, credentialErr := tx.Get(credentialRef)
		if credentialErr == nil && credentialSnapshot.Exists() {
			return ErrCredentialConflict
		}
		if credentialErr != nil && status.Code(credentialErr) != codes.NotFound {
			return credentialErr
		}

		var document userDocument
		if newUser {
			handleSnapshot, handleErr := tx.Get(handleRef)
			if handleErr == nil && handleSnapshot.Exists() {
				var reservation handleDocument
				if err := handleSnapshot.DataTo(&reservation); err != nil || reservation.UID != user.UID {
					return ErrCredentialConflict
				}
			}
			if handleErr != nil && status.Code(handleErr) != codes.NotFound {
				return handleErr
			}
			document = userDocument{
				UID:        user.UID,
				UserHandle: append([]byte(nil), user.UserHandle...),
				CreatedAt:  now,
				UpdatedAt:  now,
			}
		} else {
			if err := userSnapshot.DataTo(&document); err != nil {
				return credentialStateInvalid("decode passkey user document", err)
			}
			if document.UID != user.UID || subtle.ConstantTimeCompare(document.UserHandle, user.UserHandle) != 1 {
				return ErrCredentialConflict
			}
			credentials, references, err := decodeLifecycleCredentials(document)
			if err != nil {
				return err
			}
			credentialSnapshots, getErr := tx.GetAll(
				lifecycleCredentialDocumentRefs(s.client, references),
			)
			if getErr != nil {
				return getErr
			}
			if err := validateLifecycleCredentialSnapshots(
				credentialSnapshots,
				document,
				credentials,
				references,
				now,
			); err != nil {
				return err
			}
			for _, candidate := range references {
				if candidate == reference {
					return ErrCredentialConflict
				}
			}
			if err := validateLifecycleTimeProgression(document.CreatedAt, document.UpdatedAt, now); err != nil {
				return err
			}
		}
		if len(document.CredentialsJSON) >= maxCredentials {
			return ErrCredentialConflict
		}
		document.CredentialsJSON = append(document.CredentialsJSON, credentialJSON)
		document.UpdatedAt = now
		if err := tx.Set(userRef, document); err != nil {
			return err
		}
		if newUser {
			if err := tx.Set(handleRef, handleDocument{UID: user.UID, CreatedAt: now}); err != nil {
				return err
			}
		}
		return tx.Set(credentialRef, credentialDocument{
			UID:            user.UID,
			UserHandle:     append([]byte(nil), user.UserHandle...),
			CredentialJSON: credentialJSON,
			Version:        1,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}))
}

func (s *FirestoreStore) FindCredential(
	ctx context.Context,
	rawID, userHandle []byte,
) (*StoredCredential, error) {
	credentialRef := s.client.Collection(credentialCollection).Doc(documentID(rawID))
	snapshot, err := credentialRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, ErrCredentialNotFound
	}
	if err != nil {
		return nil, err
	}
	var document credentialDocument
	if err := snapshot.DataTo(&document); err != nil {
		return nil, err
	}
	var credential webauthn.Credential
	if json.Unmarshal(document.CredentialJSON, &credential) != nil ||
		subtle.ConstantTimeCompare(credential.ID, rawID) != 1 ||
		subtle.ConstantTimeCompare(document.UserHandle, userHandle) != 1 {
		return nil, ErrCredentialNotFound
	}
	user, err := s.LoadUserByUID(ctx, document.UID)
	if err != nil {
		return nil, err
	}
	return &StoredCredential{
		User:       user,
		Credential: credential,
		Version:    document.Version,
	}, nil
}

func (s *FirestoreStore) UpdateCredential(
	ctx context.Context,
	rawID []byte,
	expectedVersion int64,
	credential webauthn.Credential,
	now time.Time,
) error {
	if err := validateCredentialUpdate(rawID, credential, now); err != nil || expectedVersion < 1 {
		return ErrCredentialStateInvalid
	}
	now = now.UTC()
	credentialJSON, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	reference, err := CredentialReferenceForRawID(rawID)
	if err != nil {
		return ErrCredentialStateInvalid
	}
	credentialRef := s.client.Collection(credentialCollection).Doc(reference.String())
	return normalizeStoreError(s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		credentialSnapshot, getErr := tx.Get(credentialRef)
		if status.Code(getErr) == codes.NotFound {
			return ErrCredentialNotFound
		}
		if getErr != nil {
			return getErr
		}
		var stored credentialDocument
		if err := credentialSnapshot.DataTo(&stored); err != nil {
			return credentialStateInvalid("decode credential index document", err)
		}
		if stored.UID == "" || len(stored.UserHandle) == 0 || stored.Version < 1 {
			return ErrCredentialStateInvalid
		}
		userRef := s.client.Collection(userCollection).Doc(documentID([]byte(stored.UID)))
		userSnapshot, userErr := tx.Get(userRef)
		if status.Code(userErr) == codes.NotFound {
			return ErrCredentialNotFound
		}
		if userErr != nil {
			return userErr
		}
		var user userDocument
		if err := userSnapshot.DataTo(&user); err != nil {
			return credentialStateInvalid("decode passkey user document", err)
		}
		if user.UID != stored.UID ||
			subtle.ConstantTimeCompare(user.UserHandle, stored.UserHandle) != 1 {
			return ErrCredentialStateInvalid
		}
		credentials, references, err := decodeLifecycleCredentials(user)
		if err != nil {
			return err
		}
		targetIndex := -1
		for index, candidate := range references {
			if candidate == reference {
				if targetIndex >= 0 {
					return ErrCredentialStateInvalid
				}
				targetIndex = index
			}
		}
		if targetIndex < 0 {
			return ErrCredentialStateInvalid
		}
		credentialSnapshots, getErr := tx.GetAll(
			lifecycleCredentialDocumentRefs(s.client, references),
		)
		if getErr != nil {
			return getErr
		}
		if err := validateLifecycleCredentialSnapshots(
			credentialSnapshots,
			user,
			credentials,
			references,
			now,
		); err != nil {
			return err
		}
		if err := validateLifecycleTimeProgression(user.CreatedAt, user.UpdatedAt, now); err != nil {
			return err
		}
		if stored.Version != expectedVersion {
			return ErrConcurrentAssertion
		}

		user.CredentialsJSON[targetIndex] = credentialJSON
		user.UpdatedAt = now
		stored.CredentialJSON = credentialJSON
		stored.Version++
		stored.UpdatedAt = now
		if err := tx.Set(userRef, user); err != nil {
			return err
		}
		return tx.Set(credentialRef, stored)
	}))
}

func (s *FirestoreStore) RevokeCredential(
	ctx context.Context,
	uid string,
	reference CredentialReference,
	now time.Time,
) error {
	canonicalReference, err := ParseCredentialReference(reference.String())
	if err != nil || canonicalReference != reference {
		return ErrCredentialReferenceInvalid
	}
	if now.IsZero() {
		return ErrCredentialStateInvalid
	}
	now = now.UTC()
	userRef := s.client.Collection(userCollection).Doc(documentID([]byte(uid)))

	return normalizeStoreError(s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		// Read and lock the shared user document first. Concurrent revocations
		// then retry against the same final-credential invariant.
		userSnapshot, getErr := tx.Get(userRef)
		if status.Code(getErr) == codes.NotFound {
			return ErrCredentialNotFound
		}
		if getErr != nil {
			return getErr
		}
		var user userDocument
		if err := userSnapshot.DataTo(&user); err != nil {
			return credentialStateInvalid("decode passkey user document", err)
		}
		if user.UID != uid {
			return ErrCredentialNotFound
		}
		credentials, references, err := decodeLifecycleCredentials(user)
		if err != nil {
			return err
		}
		targetIndex := -1
		for index, candidate := range references {
			if candidate == canonicalReference {
				if targetIndex >= 0 {
					return ErrCredentialStateInvalid
				}
				targetIndex = index
			}
		}
		if targetIndex < 0 {
			// A reference belonging to another account and an unknown reference
			// are intentionally indistinguishable.
			return ErrCredentialNotFound
		}

		// Validate every index referenced by the user, including non-target
		// indexes, before deciding whether any mutation is allowed.
		credentialSnapshots, getErr := tx.GetAll(lifecycleCredentialDocumentRefs(s.client, references))
		if getErr != nil {
			return getErr
		}
		if len(credentialSnapshots) != len(references) {
			return credentialStateInvalid("credential index result count is inconsistent", nil)
		}
		for index, snapshot := range credentialSnapshots {
			isTarget := index == targetIndex
			if snapshot == nil || !snapshot.Exists() {
				if isTarget {
					return ErrCredentialNotFound
				}
				return credentialStateInvalid("non-target credential index document is missing", nil)
			}
			var document credentialDocument
			if err := snapshot.DataTo(&document); err != nil {
				return credentialStateInvalid("decode credential index document", err)
			}
			ownershipError := ErrCredentialStateInvalid
			if isTarget {
				ownershipError = ErrCredentialNotFound
			}
			if _, err := validateLifecycleCredentialDocument(
				document,
				user,
				references[index],
				credentials[index].ID,
				ownershipError,
			); err != nil {
				return err
			}
			if err := validateCredentialTimeProgression(
				references[index],
				document.CreatedAt,
				document.UpdatedAt,
				now,
			); err != nil {
				return err
			}
		}
		if err := validateLifecycleTimeProgression(user.CreatedAt, user.UpdatedAt, now); err != nil {
			return err
		}

		// All transaction reads and state validation happen before this guard.
		if len(references) == 1 {
			return ErrLastCredential
		}

		remaining := make([][]byte, 0, len(user.CredentialsJSON)-1)
		remaining = append(remaining, user.CredentialsJSON[:targetIndex]...)
		remaining = append(remaining, user.CredentialsJSON[targetIndex+1:]...)
		user.CredentialsJSON = remaining
		user.UpdatedAt = now
		if err := tx.Set(userRef, user); err != nil {
			return err
		}
		credentialRef := s.client.Collection(credentialCollection).Doc(canonicalReference.String())
		return tx.Delete(credentialRef)
	}))
}

func ceremonyDocumentFrom(record Ceremony) ceremonyDocument {
	return ceremonyDocument{
		Purpose:     record.Purpose,
		AppIDDigest: append([]byte(nil), record.AppIDDigest...),
		TargetUID:   record.TargetUID,
		UserHandle:  append([]byte(nil), record.UserHandle...),
		SessionJSON: append([]byte(nil), record.SessionJSON...),
		ExpiresAt:   record.ExpiresAt,
		CreatedAt:   record.CreatedAt,
	}
}

func (document ceremonyDocument) toCeremony() Ceremony {
	return Ceremony{
		Purpose:     document.Purpose,
		AppIDDigest: append([]byte(nil), document.AppIDDigest...),
		TargetUID:   document.TargetUID,
		UserHandle:  append([]byte(nil), document.UserHandle...),
		SessionJSON: append([]byte(nil), document.SessionJSON...),
		ExpiresAt:   document.ExpiresAt,
		CreatedAt:   document.CreatedAt,
	}
}

func decodeUser(document userDocument) (*User, error) {
	user := &User{UID: document.UID, UserHandle: append([]byte(nil), document.UserHandle...)}
	for _, encoded := range document.CredentialsJSON {
		var credential webauthn.Credential
		if err := json.Unmarshal(encoded, &credential); err != nil {
			return nil, fmt.Errorf("decode stored passkey credential: %w", err)
		}
		user.Credentials = append(user.Credentials, credential)
	}
	return user, nil
}

func decodeLifecycleCredentials(
	document userDocument,
) ([]webauthn.Credential, []CredentialReference, error) {
	if document.UID == "" || len(document.UserHandle) == 0 {
		return nil, nil, credentialStateInvalid("passkey user identity is incomplete", nil)
	}
	if err := validateLifecycleTimeProgression(
		document.CreatedAt,
		document.UpdatedAt,
		document.UpdatedAt,
	); err != nil {
		return nil, nil, err
	}
	credentials := make([]webauthn.Credential, len(document.CredentialsJSON))
	for index, encoded := range document.CredentialsJSON {
		if err := json.Unmarshal(encoded, &credentials[index]); err != nil {
			return nil, nil, credentialStateInvalid("decode stored passkey credential", err)
		}
	}
	references, err := credentialReferences(credentials)
	if err != nil {
		return nil, nil, err
	}
	return credentials, references, nil
}

func lifecycleCredentialDocumentRefs(
	client *firestore.Client,
	references []CredentialReference,
) []*firestore.DocumentRef {
	refs := make([]*firestore.DocumentRef, len(references))
	for index, reference := range references {
		// The reference already is the canonical SHA-256 document ID. Passing it
		// through documentID would address a different, double-hashed document.
		refs[index] = client.Collection(credentialCollection).Doc(reference.String())
	}
	return refs
}

func validateLifecycleCredentialSnapshots(
	snapshots []*firestore.DocumentSnapshot,
	user userDocument,
	credentials []webauthn.Credential,
	references []CredentialReference,
	nextUpdate time.Time,
) error {
	if len(snapshots) != len(references) || len(credentials) != len(references) {
		return credentialStateInvalid("credential index result count is inconsistent", nil)
	}
	for index, snapshot := range snapshots {
		if snapshot == nil || !snapshot.Exists() {
			return credentialStateInvalid("credential index document is missing", nil)
		}
		var document credentialDocument
		if err := snapshot.DataTo(&document); err != nil {
			return credentialStateInvalid("decode credential index document", err)
		}
		if _, err := validateLifecycleCredentialDocument(
			document,
			user,
			references[index],
			credentials[index].ID,
			ErrCredentialStateInvalid,
		); err != nil {
			return err
		}
		if err := validateCredentialTimeProgression(
			references[index],
			document.CreatedAt,
			document.UpdatedAt,
			nextUpdate,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateLifecycleCredentialDocument(
	document credentialDocument,
	user userDocument,
	reference CredentialReference,
	expectedRawID []byte,
	ownershipError error,
) (CredentialSummary, error) {
	if document.UID != user.UID ||
		subtle.ConstantTimeCompare(document.UserHandle, user.UserHandle) != 1 {
		if ownershipError == nil {
			ownershipError = ErrCredentialStateInvalid
		}
		return CredentialSummary{}, ownershipError
	}
	if document.Version < 1 {
		return CredentialSummary{}, credentialStateInvalid("credential index version is invalid", nil)
	}
	var credential webauthn.Credential
	if err := json.Unmarshal(document.CredentialJSON, &credential); err != nil {
		return CredentialSummary{}, credentialStateInvalid("decode credential index payload", err)
	}
	actualReference, err := CredentialReferenceForRawID(credential.ID)
	if err != nil || actualReference != reference ||
		subtle.ConstantTimeCompare(credential.ID, expectedRawID) != 1 {
		return CredentialSummary{}, credentialStateInvalid("credential index raw ID is inconsistent", err)
	}
	summary, err := newCredentialSummary(reference, document.CreatedAt, document.UpdatedAt)
	if err != nil {
		return CredentialSummary{}, err
	}
	if document.CreatedAt.Before(user.CreatedAt) || document.UpdatedAt.After(user.UpdatedAt) {
		return CredentialSummary{}, credentialStateInvalid("credential and user timestamps are inconsistent", nil)
	}
	return summary, nil
}

func credentialStateInvalid(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrCredentialStateInvalid, message)
	}
	return fmt.Errorf("%w: %s: %v", ErrCredentialStateInvalid, message, cause)
}
