package passkey

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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

func (s *FirestoreStore) CreateCredential(
	ctx context.Context,
	user *User,
	credential webauthn.Credential,
	now time.Time,
) error {
	credentialJSON, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	userRef := s.client.Collection(userCollection).Doc(documentID([]byte(user.UID)))
	handleRef := s.client.Collection(handleCollection).Doc(documentID(user.UserHandle))
	credentialRef := s.client.Collection(credentialCollection).Doc(documentID(credential.ID))

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
			}
		} else {
			if err := userSnapshot.DataTo(&document); err != nil {
				return err
			}
			if document.UID != user.UID || subtle.ConstantTimeCompare(document.UserHandle, user.UserHandle) != 1 {
				return ErrCredentialConflict
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
	credentialJSON, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	credentialRef := s.client.Collection(credentialCollection).Doc(documentID(rawID))
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
			return err
		}
		if stored.Version != expectedVersion {
			return ErrConcurrentAssertion
		}
		var previous webauthn.Credential
		if json.Unmarshal(stored.CredentialJSON, &previous) != nil ||
			subtle.ConstantTimeCompare(previous.ID, rawID) != 1 ||
			subtle.ConstantTimeCompare(credential.ID, rawID) != 1 {
			return ErrCredentialNotFound
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
			return err
		}
		found := false
		for index, encoded := range user.CredentialsJSON {
			var candidate webauthn.Credential
			if json.Unmarshal(encoded, &candidate) == nil && subtle.ConstantTimeCompare(candidate.ID, rawID) == 1 {
				user.CredentialsJSON[index] = credentialJSON
				found = true
				break
			}
		}
		if !found {
			return ErrCredentialNotFound
		}
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

func documentID(raw []byte) string {
	digest := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(digest[:])
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
