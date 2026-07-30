// Package securityflow is a deterministic reference monitor for privileged
// agent actions. Model output is only an ActionProposal: it cannot construct
// the opaque, one-shot Lease required by an executor.
package securityflow

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/research"
)

const (
	keyBytes              = 32
	nonceBytes            = 16
	maxScopeFieldBytes    = 512
	maxOutstandingRecords = 4_096
)

var (
	ErrInvalidConfig = errors.New("securityflow: invalid configuration")
	ErrDenied        = errors.New("securityflow: capability denied")
)

type Action uint8

const (
	ActionUnknown Action = iota
	ActionCrossrefDiscovery
)

type SourceSet uint16

const (
	SourceCurrentUserSpeech SourceSet = 1 << iota
	SourceAmbientSpeech
	SourcePDF
	SourceConversationState
	SourceModelOutput
	SourceToolOutput
)

const allSources = SourceCurrentUserSpeech |
	SourceAmbientSpeech |
	SourcePDF |
	SourceConversationState |
	SourceModelOutput |
	SourceToolOutput

func (sources SourceSet) Has(source SourceSet) bool {
	return source != 0 && sources&source == source
}

type Decision uint8

const (
	DecisionDeny Decision = iota
	DecisionAllow
)

type ReasonCode uint8

const (
	ReasonInvalid ReasonCode = iota
	ReasonAuthorized
	ReasonInvalidScope
	ReasonInvalidProposal
	ReasonInvalidAuthority
	ReasonArgumentMismatch
	ReasonExpired
	ReasonReplay
	ReasonTampered
	ReasonCapacity
)

type PolicyID uint8

const (
	PolicyUnknown PolicyID = iota
	PolicyPCCMPhase1
)

// DefenseEvent is deliberately finite and content-free. It contains no
// transcript, query, identifier, token, digest, n-gram, or provider detail.
type DefenseEvent struct {
	Policy   PolicyID
	Action   Action
	Decision Decision
	Reason   ReasonCode
	Sources  SourceSet
}

// Scope binds authority to one authenticated user, encrypted conversation and
// server-generated request. Scope values are never copied into grants, leases
// or DefenseEvent.
type Scope struct {
	UID       string
	SessionID string
	RequestID string
}

// Config deliberately has no model or network dependency.
type Config struct {
	Key     []byte
	Policy  PolicyID
	MaxTTL  time.Duration
	Now     func() time.Time
	Random  io.Reader
}

// ActionProposal is safe to construct from model output. It carries only a
// keyed argument digest and provenance bits, never the raw research query.
type ActionProposal struct {
	action  Action
	args    [sha256.Size]byte
	sources SourceSet
}

func (ActionProposal) String() string   { return "securityflow.ActionProposal{redacted}" }
func (ActionProposal) GoString() string { return "securityflow.ActionProposal{redacted}" }
func (ActionProposal) MarshalJSON() ([]byte, error) {
	return []byte(`"securityflow.ActionProposal{redacted}"`), nil
}

// CurrentUserSpeech is an opaque, one-shot authority grant. Its fields are
// unexported so JSON/model output cannot manufacture it.
type CurrentUserSpeech struct {
	issuer    [nonceBytes]byte
	nonce     [nonceBytes]byte
	scope     [sha256.Size]byte
	args      [sha256.Size]byte
	policy    [sha256.Size]byte
	action    Action
	issuedAt  int64
	expiresAt int64
	authority SourceSet
	signature [sha256.Size]byte
}

func (CurrentUserSpeech) String() string {
	return "securityflow.CurrentUserSpeech{redacted}"
}
func (CurrentUserSpeech) GoString() string {
	return "securityflow.CurrentUserSpeech{redacted}"
}
func (CurrentUserSpeech) MarshalJSON() ([]byte, error) {
	return []byte(`"securityflow.CurrentUserSpeech{redacted}"`), nil
}

// Lease is an opaque one-shot execution capability.
type Lease struct {
	issuer    [nonceBytes]byte
	nonce     [nonceBytes]byte
	scope     [sha256.Size]byte
	args      [sha256.Size]byte
	policy    [sha256.Size]byte
	action    Action
	sources   SourceSet
	issuedAt  int64
	expiresAt int64
	uses      uint8
	signature [sha256.Size]byte
}

func (Lease) String() string   { return "securityflow.Lease{redacted}" }
func (Lease) GoString() string { return "securityflow.Lease{redacted}" }
func (Lease) MarshalJSON() ([]byte, error) {
	return []byte(`"securityflow.Lease{redacted}"`), nil
}

type Guard struct {
	key           [keyBytes]byte
	issuer        [nonceBytes]byte
	policy        PolicyID
	policyDigest  [sha256.Size]byte
	maxTTL        time.Duration
	now           func() time.Time
	random        io.Reader

	randomMu sync.Mutex
	mu       sync.Mutex
	grants   map[[nonceBytes]byte]int64
	leases   map[[nonceBytes]byte]int64
}

func NewGuard(config Config) (*Guard, error) {
	if len(config.Key) != keyBytes ||
		config.Policy != PolicyPCCMPhase1 ||
		config.MaxTTL <= 0 ||
		config.MaxTTL > time.Minute {
		return nil, ErrInvalidConfig
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	randomSource := config.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	guard := &Guard{
		policy: config.Policy,
		maxTTL: config.MaxTTL,
		now:    now,
		random: randomSource,
		grants: make(map[[nonceBytes]byte]int64),
		leases: make(map[[nonceBytes]byte]int64),
	}
	copy(guard.key[:], config.Key)
	if _, err := io.ReadFull(guard.random, guard.issuer[:]); err != nil {
		return nil, ErrInvalidConfig
	}
	guard.policyDigest = guard.keyedDigest(
		[]byte("policy\x00"),
		[]byte{byte(guard.policy)},
	)
	return guard, nil
}

func (guard *Guard) ProposeCrossref(
	query research.Query,
	sources SourceSet,
) (ActionProposal, DefenseEvent, error) {
	event := guard.event(
		ActionCrossrefDiscovery,
		DecisionDeny,
		ReasonInvalidProposal,
		sources,
	)
	if guard == nil ||
		sources == 0 ||
		sources&^allSources != 0 ||
		!sources.Has(SourceModelOutput) ||
		!sources.Has(SourceCurrentUserSpeech) ||
		sources.Has(SourceAmbientSpeech) {
		return ActionProposal{}, event, ErrDenied
	}
	args, err := guard.queryDigest(query)
	if err != nil {
		return ActionProposal{}, event, ErrDenied
	}
	event.Decision = DecisionAllow
	event.Reason = ReasonAuthorized
	return ActionProposal{
		action:  ActionCrossrefDiscovery,
		args:    args,
		sources: sources,
	}, event, nil
}

// BindCurrentUserSpeechForCrossref is called only after trusted deterministic
// parsing has rebound the proposal to the authenticated, intentional current
// speech. Data from PDF, state, tools or models cannot call this method.
func (guard *Guard) BindCurrentUserSpeechForCrossref(
	scope Scope,
	query research.Query,
	ttl time.Duration,
) (CurrentUserSpeech, DefenseEvent, error) {
	event := guard.event(
		ActionCrossrefDiscovery,
		DecisionDeny,
		ReasonInvalidAuthority,
		SourceCurrentUserSpeech,
	)
	if guard == nil || !validScope(scope) || !guard.validTTL(ttl) {
		event.Reason = ReasonInvalidScope
		return CurrentUserSpeech{}, event, ErrDenied
	}
	args, err := guard.queryDigest(query)
	if err != nil {
		return CurrentUserSpeech{}, event, ErrDenied
	}
	now := guard.now().UTC()
	grant := CurrentUserSpeech{
		issuer:    guard.issuer,
		scope:     guard.scopeDigest(scope),
		args:      args,
		policy:    guard.policyDigest,
		action:    ActionCrossrefDiscovery,
		issuedAt:  now.UnixNano(),
		expiresAt: now.Add(ttl).UnixNano(),
		authority: SourceCurrentUserSpeech,
	}
	if err := guard.readRandom(grant.nonce[:]); err != nil {
		return CurrentUserSpeech{}, event, ErrDenied
	}
	grant.signature = guard.signGrant(grant)

	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.cleanupLocked(now.UnixNano())
	if len(guard.grants)+len(guard.leases) >= maxOutstandingRecords {
		event.Reason = ReasonCapacity
		return CurrentUserSpeech{}, event, ErrDenied
	}
	if _, exists := guard.grants[grant.nonce]; exists {
		event.Reason = ReasonTampered
		return CurrentUserSpeech{}, event, ErrDenied
	}
	if _, exists := guard.leases[grant.nonce]; exists {
		event.Reason = ReasonTampered
		return CurrentUserSpeech{}, event, ErrDenied
	}
	guard.grants[grant.nonce] = grant.expiresAt
	event.Decision = DecisionAllow
	event.Reason = ReasonAuthorized
	return grant, event, nil
}

func (guard *Guard) MintCrossref(
	grant CurrentUserSpeech,
	scope Scope,
	proposal ActionProposal,
	ttl time.Duration,
) (Lease, DefenseEvent, error) {
	event := guard.event(
		ActionCrossrefDiscovery,
		DecisionDeny,
		ReasonInvalidAuthority,
		proposal.sources,
	)
	if guard == nil ||
		!validScope(scope) ||
		!guard.validTTL(ttl) ||
		proposal.action != ActionCrossrefDiscovery ||
		!proposal.sources.Has(SourceModelOutput) ||
		!proposal.sources.Has(SourceCurrentUserSpeech) ||
		proposal.sources.Has(SourceAmbientSpeech) {
		return Lease{}, event, ErrDenied
	}
	nowTime := guard.now().UTC()
	now := nowTime.UnixNano()
	expectedScope := guard.scopeDigest(scope)
	expectedGrantSignature := guard.signGrant(grant)
	switch {
	case grant.issuer != guard.issuer ||
		grant.action != ActionCrossrefDiscovery ||
		grant.authority != SourceCurrentUserSpeech ||
		grant.policy != guard.policyDigest ||
		!hmac.Equal(grant.signature[:], expectedGrantSignature[:]):
		event.Reason = ReasonTampered
		return Lease{}, event, ErrDenied
	case grant.scope != expectedScope:
		event.Reason = ReasonInvalidScope
		return Lease{}, event, ErrDenied
	case grant.args != proposal.args:
		event.Reason = ReasonArgumentMismatch
		return Lease{}, event, ErrDenied
	case now < grant.issuedAt || now >= grant.expiresAt:
		event.Reason = ReasonExpired
		return Lease{}, event, ErrDenied
	}
	requestedExpiry := nowTime.Add(ttl).UnixNano()
	if requestedExpiry > grant.expiresAt {
		event.Reason = ReasonExpired
		return Lease{}, event, ErrDenied
	}

	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.cleanupLocked(now)
	if _, exists := guard.grants[grant.nonce]; !exists {
		event.Reason = ReasonReplay
		return Lease{}, event, ErrDenied
	}
	delete(guard.grants, grant.nonce)
	if len(guard.grants)+len(guard.leases) >= maxOutstandingRecords {
		event.Reason = ReasonCapacity
		return Lease{}, event, ErrDenied
	}

	lease := Lease{
		issuer:    guard.issuer,
		scope:     expectedScope,
		args:      proposal.args,
		policy:    guard.policyDigest,
		action:    ActionCrossrefDiscovery,
		sources:   proposal.sources,
		issuedAt:  now,
		expiresAt: requestedExpiry,
		uses:      1,
	}
	if err := guard.readRandom(lease.nonce[:]); err != nil {
		return Lease{}, event, ErrDenied
	}
	if _, exists := guard.grants[lease.nonce]; exists {
		event.Reason = ReasonTampered
		return Lease{}, event, ErrDenied
	}
	if _, exists := guard.leases[lease.nonce]; exists {
		event.Reason = ReasonTampered
		return Lease{}, event, ErrDenied
	}
	lease.signature = guard.signLease(lease)
	guard.leases[lease.nonce] = lease.expiresAt
	event.Decision = DecisionAllow
	event.Reason = ReasonAuthorized
	return lease, event, nil
}

func (guard *Guard) ConsumeCrossref(
	lease Lease,
	scope Scope,
	proposal ActionProposal,
) (DefenseEvent, error) {
	event := guard.event(
		ActionCrossrefDiscovery,
		DecisionDeny,
		ReasonInvalidAuthority,
		proposal.sources,
	)
	if guard == nil ||
		!validScope(scope) ||
		proposal.action != ActionCrossrefDiscovery ||
		!proposal.sources.Has(SourceModelOutput) ||
		!proposal.sources.Has(SourceCurrentUserSpeech) ||
		proposal.sources.Has(SourceAmbientSpeech) {
		return event, ErrDenied
	}
	now := guard.now().UTC().UnixNano()
	expectedScope := guard.scopeDigest(scope)
	expectedSignature := guard.signLease(lease)
	switch {
	case lease.issuer != guard.issuer ||
		lease.action != ActionCrossrefDiscovery ||
		lease.policy != guard.policyDigest ||
		lease.uses != 1 ||
		!hmac.Equal(lease.signature[:], expectedSignature[:]):
		event.Reason = ReasonTampered
		return event, ErrDenied
	case lease.scope != expectedScope:
		event.Reason = ReasonInvalidScope
		return event, ErrDenied
	case lease.args != proposal.args || lease.sources != proposal.sources:
		event.Reason = ReasonArgumentMismatch
		return event, ErrDenied
	case now < lease.issuedAt || now >= lease.expiresAt:
		event.Reason = ReasonExpired
		return event, ErrDenied
	}

	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.cleanupLocked(now)
	if _, exists := guard.leases[lease.nonce]; !exists {
		event.Reason = ReasonReplay
		return event, ErrDenied
	}
	delete(guard.leases, lease.nonce)
	event.Decision = DecisionAllow
	event.Reason = ReasonAuthorized
	return event, nil
}

func (guard *Guard) validTTL(ttl time.Duration) bool {
	return ttl > 0 && ttl <= guard.maxTTL
}

func (guard *Guard) event(
	action Action,
	decision Decision,
	reason ReasonCode,
	sources SourceSet,
) DefenseEvent {
	event := DefenseEvent{
		Action:   action,
		Decision: decision,
		Reason:   reason,
		Sources:  sources,
	}
	if guard != nil {
		event.Policy = guard.policy
	}
	return event
}

func (guard *Guard) queryDigest(query research.Query) ([sha256.Size]byte, error) {
	normalized, err := research.NormalizeQuery(query)
	if err != nil {
		return [sha256.Size]byte{}, ErrDenied
	}
	var canonical bytes.Buffer
	canonical.WriteString(string(normalized.Kind))
	canonical.WriteByte(0)
	switch normalized.Kind {
	case research.QueryDOI:
		canonical.WriteString(normalized.DOI)
	case research.QueryRecentTopic:
		canonical.WriteString(normalized.Topic)
		canonical.WriteByte(0)
		writeInt64(&canonical, normalized.From.UTC().Unix())
		writeInt64(&canonical, normalized.Until.UTC().Unix())
		writeInt64(&canonical, int64(normalized.Limit))
	default:
		return [sha256.Size]byte{}, ErrDenied
	}
	return guard.keyedDigest([]byte("args\x00"), canonical.Bytes()), nil
}

func (guard *Guard) scopeDigest(scope Scope) [sha256.Size]byte {
	var canonical bytes.Buffer
	writeBoundedString(&canonical, scope.UID)
	writeBoundedString(&canonical, scope.SessionID)
	writeBoundedString(&canonical, scope.RequestID)
	return guard.keyedDigest([]byte("scope\x00"), canonical.Bytes())
}

func (guard *Guard) keyedDigest(parts ...[]byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, guard.key[:])
	for _, part := range parts {
		_, _ = mac.Write(part)
	}
	var result [sha256.Size]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func (guard *Guard) signGrant(grant CurrentUserSpeech) [sha256.Size]byte {
	var envelope bytes.Buffer
	envelope.WriteString("kotae-authority-grant-v1\x00")
	envelope.Write(grant.issuer[:])
	envelope.Write(grant.nonce[:])
	envelope.Write(grant.scope[:])
	envelope.Write(grant.args[:])
	envelope.Write(grant.policy[:])
	envelope.WriteByte(byte(grant.action))
	writeInt64(&envelope, grant.issuedAt)
	writeInt64(&envelope, grant.expiresAt)
	writeInt64(&envelope, int64(grant.authority))
	return guard.keyedDigest(envelope.Bytes())
}

func (guard *Guard) signLease(lease Lease) [sha256.Size]byte {
	var envelope bytes.Buffer
	envelope.WriteString("kotae-capability-lease-v1\x00")
	envelope.Write(lease.issuer[:])
	envelope.Write(lease.nonce[:])
	envelope.Write(lease.scope[:])
	envelope.Write(lease.args[:])
	envelope.Write(lease.policy[:])
	envelope.WriteByte(byte(lease.action))
	writeInt64(&envelope, int64(lease.sources))
	writeInt64(&envelope, lease.issuedAt)
	writeInt64(&envelope, lease.expiresAt)
	envelope.WriteByte(lease.uses)
	return guard.keyedDigest(envelope.Bytes())
}

func (guard *Guard) cleanupLocked(now int64) {
	for nonce, expiresAt := range guard.grants {
		if now >= expiresAt {
			delete(guard.grants, nonce)
		}
	}
	for nonce, expiresAt := range guard.leases {
		if now >= expiresAt {
			delete(guard.leases, nonce)
		}
	}
}

func (guard *Guard) readRandom(destination []byte) error {
	guard.randomMu.Lock()
	defer guard.randomMu.Unlock()
	if _, err := io.ReadFull(guard.random, destination); err != nil {
		return ErrDenied
	}
	return nil
}

func validScope(scope Scope) bool {
	return validScopeField(scope.UID) &&
		validScopeField(scope.SessionID) &&
		validScopeField(scope.RequestID)
}

func validScopeField(value string) bool {
	return value != "" &&
		len(value) <= maxScopeFieldBytes &&
		utf8.ValidString(value) &&
		value == strings.TrimSpace(value)
}

func writeBoundedString(buffer *bytes.Buffer, value string) {
	writeInt64(buffer, int64(len(value)))
	buffer.WriteString(value)
}

func writeInt64(buffer *bytes.Buffer, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	buffer.Write(encoded[:])
}
