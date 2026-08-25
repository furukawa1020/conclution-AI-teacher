package passkey

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

type memoryCredential struct {
	UID        string
	UserHandle []byte
	Credential webauthn.Credential
	Version    int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type memoryRecoveryCode struct {
	Digest    [sha256.Size]byte
	ExpiresAt time.Time
	IssuedAt  time.Time
}

type MemoryStore struct {
	mu             sync.Mutex
	ceremonies     map[string]Ceremony
	users          map[string]*User
	credentials    map[string]memoryCredential
	deleted        map[string]time.Time
	recovery       map[string]memoryRecoveryCode
	recoveryByCode map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		ceremonies:     make(map[string]Ceremony),
		users:          make(map[string]*User),
		credentials:    make(map[string]memoryCredential),
		deleted:        make(map[string]time.Time),
		recovery:       make(map[string]memoryRecoveryCode),
		recoveryByCode: make(map[string]string),
	}
}

func (s *MemoryStore) ReplaceRecoveryCode(
	_ context.Context,
	uid string,
	digest [sha256.Size]byte,
	expiresAt, now time.Time,
) error {
	uid = strings.TrimSpace(uid)
	if uid == "" || now.IsZero() || !expiresAt.After(now.UTC()) ||
		expiresAt.After(now.UTC().Add(RecoveryCodeTTL)) {
		return ErrRecoveryCode
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	accountKey := documentID([]byte(uid))
	user := s.users[accountKey]
	if user == nil || user.UID != uid {
		return ErrCredentialNotFound
	}
	if _, err := s.validateCredentialStateLocked(user, now.UTC()); err != nil {
		return err
	}
	codeKey := recoveryCodeDocumentID(digest)
	if owner, exists := s.recoveryByCode[codeKey]; exists && owner != accountKey {
		return ErrCredentialConflict
	}
	if previous, exists := s.recovery[accountKey]; exists {
		delete(s.recoveryByCode, recoveryCodeDocumentID(previous.Digest))
	}
	s.recovery[accountKey] = memoryRecoveryCode{Digest: digest, ExpiresAt: expiresAt.UTC(), IssuedAt: now.UTC()}
	s.recoveryByCode[codeKey] = accountKey
	return nil
}

func (s *MemoryStore) LoadRecoveryUser(
	_ context.Context,
	digest [sha256.Size]byte,
	now time.Time,
) (*User, error) {
	if now.IsZero() {
		return nil, ErrRecoveryCode
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	codeKey := recoveryCodeDocumentID(digest)
	accountKey, exists := s.recoveryByCode[codeKey]
	if !exists {
		return nil, ErrRecoveryCode
	}
	recovery, exists := s.recovery[accountKey]
	if !exists || subtle.ConstantTimeCompare(recovery.Digest[:], digest[:]) != 1 ||
		!recovery.ExpiresAt.After(now) || recovery.IssuedAt.IsZero() ||
		!recovery.ExpiresAt.After(recovery.IssuedAt) {
		return nil, ErrRecoveryCode
	}
	user := s.users[accountKey]
	if user == nil {
		return nil, ErrRecoveryCode
	}
	if _, err := s.validateCredentialStateLocked(user, now); err != nil {
		return nil, ErrRecoveryCode
	}
	return cloneUser(user), nil
}

func (s *MemoryStore) LoadRecoveryUserByHandle(
	_ context.Context,
	userHandle, principalDigest []byte,
	now time.Time,
) (*User, error) {
	if len(userHandle) == 0 || len(principalDigest) != sha256.Size || now.IsZero() {
		return nil, ErrRecoveryCode
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, user := range s.users {
		if subtle.ConstantTimeCompare(user.UserHandle, userHandle) != 1 ||
			subtle.ConstantTimeCompare(digestString(user.UID), principalDigest) != 1 {
			continue
		}
		if _, err := s.validateCredentialStateLocked(user, now.UTC()); err != nil {
			return nil, ErrRecoveryCode
		}
		return cloneUser(user), nil
	}
	return nil, ErrRecoveryCode
}

func (s *MemoryStore) DeleteAccountData(_ context.Context, uid string, now time.Time) error {
	if uid == "" || now.IsZero() {
		return ErrCredentialStateInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := documentID([]byte(uid))
	if _, deleted := s.deleted[key]; deleted {
		return nil
	}
	user := s.users[key]
	if user == nil || user.UID != uid {
		return ErrCredentialNotFound
	}
	references, err := s.validateCredentialStateLocked(user, now.UTC())
	if err != nil {
		return err
	}
	if recovery, exists := s.recovery[key]; exists {
		delete(s.recoveryByCode, recoveryCodeDocumentID(recovery.Digest))
		delete(s.recovery, key)
	}
	for _, reference := range references {
		delete(s.credentials, reference.String())
	}
	delete(s.users, key)
	s.deleted[key] = now.UTC()
	return nil
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

func (s *MemoryStore) ConsumeRegistrationCeremony(
	_ context.Context,
	id string,
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
	if (record.Purpose != registrationUse && record.Purpose != recoveryRegistrationUse) ||
		!ceremonyMatches(record, record.Purpose, appIDDigest, now) {
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
	now time.Time,
) error {
	if err := validateCredentialCreation(user, credential, now); err != nil {
		return err
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createCredentialLocked(user, credential, now)
}

func (s *MemoryStore) CreateRecoveryCredential(
	_ context.Context,
	user *User,
	credential webauthn.Credential,
	digest [sha256.Size]byte,
	now time.Time,
) error {
	if err := validateCredentialCreation(user, credential, now); err != nil {
		return ErrRecoveryCode
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	userKey := documentID([]byte(user.UID))
	codeKey := recoveryCodeDocumentID(digest)
	recovery, exists := s.recovery[userKey]
	owner, indexed := s.recoveryByCode[codeKey]
	if !exists || !indexed || owner != userKey ||
		subtle.ConstantTimeCompare(recovery.Digest[:], digest[:]) != 1 ||
		!recovery.ExpiresAt.After(now) || recovery.IssuedAt.IsZero() ||
		!recovery.ExpiresAt.After(recovery.IssuedAt) {
		return ErrRecoveryCode
	}
	if err := s.createCredentialLocked(user, credential, now); err != nil {
		return ErrRecoveryCode
	}
	delete(s.recoveryByCode, codeKey)
	delete(s.recovery, userKey)
	return nil
}

func (s *MemoryStore) createCredentialLocked(
	user *User,
	credential webauthn.Credential,
	now time.Time,
) error {
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
	} else if _, err := s.validateCredentialStateLocked(stored, now); err != nil {
		return err
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
		CreatedAt:  now,
		UpdatedAt:  now,
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
	now time.Time,
) error {
	if err := validateCredentialUpdate(rawID, credential, now); err != nil {
		return err
	}
	now = now.UTC()
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
	if user.UID != stored.UID ||
		subtle.ConstantTimeCompare(user.UserHandle, stored.UserHandle) != 1 {
		return ErrCredentialStateInvalid
	}
	references, err := s.validateCredentialStateLocked(user, now)
	if err != nil {
		return err
	}
	targetIndex := -1
	targetReference := credentialReference(rawID)
	for index, reference := range references {
		if reference == targetReference {
			targetIndex = index
		}
	}
	if targetIndex < 0 {
		return ErrCredentialNotFound
	}
	user.Credentials[targetIndex] = cloneCredential(credential)
	stored.Credential = cloneCredential(credential)
	stored.Version++
	stored.UpdatedAt = now
	s.credentials[key] = stored
	return nil
}

func (s *MemoryStore) ListCredentials(
	_ context.Context,
	uid string,
) ([]CredentialSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user := s.users[documentID([]byte(uid))]
	if user == nil || user.UID != uid {
		return nil, ErrCredentialNotFound
	}
	references, err := s.validateCredentialStateLocked(user, time.Time{})
	if err != nil {
		return nil, err
	}
	summaries := make([]CredentialSummary, 0, len(references))
	for _, reference := range references {
		stored := s.credentials[reference.String()]
		summary, summaryErr := newCredentialSummary(reference, stored.CreatedAt, stored.UpdatedAt)
		if summaryErr != nil {
			return nil, summaryErr
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (s *MemoryStore) RevokeCredential(
	_ context.Context,
	uid string,
	reference CredentialReference,
	now time.Time,
) error {
	parsed, err := ParseCredentialReference(reference.String())
	if err != nil || parsed != reference {
		return ErrCredentialReferenceInvalid
	}
	if now.IsZero() {
		return ErrCredentialStateInvalid
	}
	now = now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	user := s.users[documentID([]byte(uid))]
	if user == nil || user.UID != uid {
		return ErrCredentialNotFound
	}
	references, err := s.validateCredentialStateLocked(user, now)
	if err != nil {
		return err
	}
	targetIndex := -1
	for index, candidate := range references {
		if candidate == parsed {
			targetIndex = index
		}
	}
	if targetIndex < 0 {
		return ErrCredentialNotFound
	}
	if len(references) == 1 {
		return ErrLastCredential
	}

	remaining := make([]webauthn.Credential, 0, len(user.Credentials)-1)
	for index, credential := range user.Credentials {
		if index != targetIndex {
			remaining = append(remaining, cloneCredential(credential))
		}
	}
	user.Credentials = remaining
	delete(s.credentials, parsed.String())
	return nil
}

// validateCredentialStateLocked checks the complete user-to-index relation.
// The caller must hold s.mu. A non-zero nextUpdate additionally enforces that
// the store's logical clock never moves behind any current credential.
func (s *MemoryStore) validateCredentialStateLocked(
	user *User,
	nextUpdate time.Time,
) ([]CredentialReference, error) {
	if user == nil || user.UID == "" || len(user.UserHandle) == 0 {
		return nil, ErrCredentialStateInvalid
	}
	references, err := credentialReferences(user.Credentials)
	if err != nil {
		return nil, err
	}
	for index, reference := range references {
		stored, exists := s.credentials[reference.String()]
		if !exists || stored.UID != user.UID ||
			subtle.ConstantTimeCompare(stored.UserHandle, user.UserHandle) != 1 ||
			subtle.ConstantTimeCompare(stored.Credential.ID, user.Credentials[index].ID) != 1 ||
			credentialReference(stored.Credential.ID) != reference || stored.Version < 1 {
			return nil, ErrCredentialStateInvalid
		}
		if _, summaryErr := newCredentialSummary(reference, stored.CreatedAt, stored.UpdatedAt); summaryErr != nil {
			return nil, summaryErr
		}
		if !nextUpdate.IsZero() && nextUpdate.Before(stored.UpdatedAt) {
			return nil, ErrCredentialStateInvalid
		}
	}
	return references, nil
}

func cloneCeremony(record Ceremony) Ceremony {
	record.AppIDDigest = append([]byte(nil), record.AppIDDigest...)
	record.PrincipalDigest = append([]byte(nil), record.PrincipalDigest...)
	record.RecoveryCodeDigest = append([]byte(nil), record.RecoveryCodeDigest...)
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
