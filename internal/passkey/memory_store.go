package passkey

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

type memoryCredential struct {
	UID        string
	UserHandle []byte
	Credential webauthn.Credential
	Version    int64
}

type MemoryStore struct {
	mu          sync.Mutex
	ceremonies  map[string]Ceremony
	users       map[string]*User
	credentials map[string]memoryCredential
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		ceremonies:  make(map[string]Ceremony),
		users:       make(map[string]*User),
		credentials: make(map[string]memoryCredential),
	}
}

func (s *MemoryStore) PutCeremony(_ context.Context, id string, record Ceremony) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := documentID([]byte(id))
	if _, exists := s.ceremonies[key]; exists {
		return ErrCredentialConflict
	}
	s.ceremonies[key] = cloneCeremony(record)
	return nil
}

func (s *MemoryStore) ConsumeCeremony(
	_ context.Context,
	id, purpose string,
	appIDDigest []byte,
	now time.Time,
) (Ceremony, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := documentID([]byte(id))
	record, exists := s.ceremonies[key]
	if !exists {
		return Ceremony{}, ErrCeremonyInvalid
	}
	delete(s.ceremonies, key)
	if !ceremonyMatches(record, purpose, appIDDigest, now) {
		return Ceremony{}, ErrCeremonyInvalid
	}
	return cloneCeremony(record), nil
}

func (s *MemoryStore) LoadUserByUID(_ context.Context, uid string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, exists := s.users[documentID([]byte(uid))]
	if !exists {
		return nil, ErrCredentialNotFound
	}
	return cloneUser(user), nil
}

func (s *MemoryStore) CreateCredential(
	_ context.Context,
	user *User,
	credential webauthn.Credential,
	_ time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	credentialKey := documentID(credential.ID)
	if _, exists := s.credentials[credentialKey]; exists {
		return ErrCredentialConflict
	}
	userKey := documentID([]byte(user.UID))
	stored, exists := s.users[userKey]
	if !exists {
		for _, candidate := range s.users {
			if subtle.ConstantTimeCompare(candidate.UserHandle, user.UserHandle) == 1 {
				return ErrCredentialConflict
			}
		}
		stored = &User{UID: user.UID, UserHandle: append([]byte(nil), user.UserHandle...)}
	} else if subtle.ConstantTimeCompare(stored.UserHandle, user.UserHandle) != 1 {
		return ErrCredentialConflict
	}
	if len(stored.Credentials) >= maxCredentials {
		return ErrCredentialConflict
	}
	stored.Credentials = append(stored.Credentials, cloneCredential(credential))
	s.users[userKey] = stored
	s.credentials[credentialKey] = memoryCredential{
		UID:        user.UID,
		UserHandle: append([]byte(nil), user.UserHandle...),
		Credential: cloneCredential(credential),
		Version:    1,
	}
	return nil
}

func (s *MemoryStore) FindCredential(
	_ context.Context,
	rawID, userHandle []byte,
) (*StoredCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.credentials[documentID(rawID)]
	if !exists || subtle.ConstantTimeCompare(stored.Credential.ID, rawID) != 1 ||
		subtle.ConstantTimeCompare(stored.UserHandle, userHandle) != 1 {
		return nil, ErrCredentialNotFound
	}
	user := s.users[documentID([]byte(stored.UID))]
	if user == nil {
		return nil, ErrCredentialNotFound
	}
	return &StoredCredential{
		User:       cloneUser(user),
		Credential: cloneCredential(stored.Credential),
		Version:    stored.Version,
	}, nil
}

func (s *MemoryStore) UpdateCredential(
	_ context.Context,
	rawID []byte,
	expectedVersion int64,
	credential webauthn.Credential,
	_ time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := documentID(rawID)
	stored, exists := s.credentials[key]
	if !exists || subtle.ConstantTimeCompare(stored.Credential.ID, rawID) != 1 {
		return ErrCredentialNotFound
	}
	if stored.Version != expectedVersion {
		return ErrConcurrentAssertion
	}
	user := s.users[documentID([]byte(stored.UID))]
	if user == nil {
		return ErrCredentialNotFound
	}
	found := false
	for index := range user.Credentials {
		if subtle.ConstantTimeCompare(user.Credentials[index].ID, rawID) == 1 {
			user.Credentials[index] = cloneCredential(credential)
			found = true
			break
		}
	}
	if !found {
		return ErrCredentialNotFound
	}
	stored.Credential = cloneCredential(credential)
	stored.Version++
	s.credentials[key] = stored
	return nil
}

func cloneCeremony(record Ceremony) Ceremony {
	record.AppIDDigest = append([]byte(nil), record.AppIDDigest...)
	record.UserHandle = append([]byte(nil), record.UserHandle...)
	record.SessionJSON = append([]byte(nil), record.SessionJSON...)
	return record
}

func cloneUser(user *User) *User {
	if user == nil {
		return nil
	}
	cloned := &User{
		UID:        user.UID,
		UserHandle: append([]byte(nil), user.UserHandle...),
	}
	for _, credential := range user.Credentials {
		cloned.Credentials = append(cloned.Credentials, cloneCredential(credential))
	}
	return cloned
}

func cloneCredential(credential webauthn.Credential) webauthn.Credential {
	encoded, _ := json.Marshal(credential)
	var cloned webauthn.Credential
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}
