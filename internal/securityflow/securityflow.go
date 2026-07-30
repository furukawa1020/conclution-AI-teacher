// Package securityflow is a deterministic reference monitor for privileged
// agent actions. Model output is only an ActionProposal: it cannot construct
// the opaque, one-shot Lease required by an executor.
package securityflow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
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
	ErrInvalidConfig       = errors.New("securityflow: invalid configuration")
	ErrDenied              = errors.New("securityflow: capability denied")
	ErrExecutorUnavailable = errors.New("securityflow: executor unavailable")
)

type Action uint8

const (
	ActionUnknown Action = iota
	ActionCrossrefDiscovery
)

type SourceSet uint16

const (
	// SourceDeclaredIntentionalAudio means only that an App Check-authenticated
	// application request declared this turn intentional. It is not evidence of
	// speaker identity, liveness, or resistance to replayed audio.
	SourceDeclaredIntentionalAudio SourceSet = 1 << iota
	SourceAmbientSpeech
	SourcePDF
	SourceConversationState
	SourceModelOutput
	SourceToolOutput
)

const allSources = SourceDeclaredIntentionalAudio |
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

// Scope binds authority to one authenticated application principal, encrypted
// conversation and server-generated request. In the current frontend the UID
// is anonymous Firebase Auth, not a verified natural person. Scope values are
// never copied into grants, leases or DefenseEvent.
type Scope struct {
	UID       string
	SessionID string
	RequestID string
}

func (Scope) String() string   { return "securityflow.Scope{redacted}" }
func (Scope) GoString() string { return "securityflow.Scope{redacted}" }
func (Scope) MarshalJSON() ([]byte, error) {
	return []byte(`"securityflow.Scope{redacted}"`), nil
}

// Config deliberately has no model or network dependency.
type Config struct {
	Key         []byte
	Policy      PolicyID
	MaxTTL      time.Duration
	IssuanceTTL time.Duration
	Now         func() time.Time
	Random      io.Reader
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

// DeclaredIntentionalAudioGrant is an opaque, one-shot request authority grant.
// It does not attest speaker identity or audio liveness. Its fields are
// unexported so JSON/model output cannot manufacture it.
type DeclaredIntentionalAudioGrant struct {
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

func (DeclaredIntentionalAudioGrant) String() string {
	return "securityflow.DeclaredIntentionalAudioGrant{redacted}"
}
func (DeclaredIntentionalAudioGrant) GoString() string {
	return "securityflow.DeclaredIntentionalAudioGrant{redacted}"
}
func (DeclaredIntentionalAudioGrant) MarshalJSON() ([]byte, error) {
	return []byte(`"securityflow.DeclaredIntentionalAudioGrant{redacted}"`), nil
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
	key          [keyBytes]byte
	issuer       [nonceBytes]byte
	policy       PolicyID
	policyDigest [sha256.Size]byte
	maxTTL       time.Duration
	issuanceTTL  time.Duration
	now          func() time.Time
	random       io.Reader

	randomMu sync.Mutex
	mu       sync.Mutex
	issued   map[[sha256.Size]byte]int64
	grants   map[[nonceBytes]byte]int64
	leases   map[[nonceBytes]byte]int64
}

func (*Guard) String() string   { return "securityflow.Guard{redacted}" }
func (*Guard) GoString() string { return "securityflow.Guard{redacted}" }
func (*Guard) MarshalJSON() ([]byte, error) {
	return []byte(`"securityflow.Guard{redacted}"`), nil
}

// CrossrefExecutor is the only application boundary that owns a raw research
// verifier. A model proposal cannot reach the verifier unless a matching,
// one-shot Lease is consumed immediately before execution.
type CrossrefExecutor struct {
	guard    *Guard
	verifier research.Verifier
}

func (*CrossrefExecutor) String() string {
	return "securityflow.CrossrefExecutor{redacted}"
}
func (*CrossrefExecutor) GoString() string {
	return "securityflow.CrossrefExecutor{redacted}"
}
func (*CrossrefExecutor) MarshalJSON() ([]byte, error) {
	return []byte(`"securityflow.CrossrefExecutor{redacted}"`), nil
}

func NewGuard(config Config) (*Guard, error) {
	if len(config.Key) != keyBytes ||
		config.Policy != PolicyPCCMPhase1 ||
		config.MaxTTL <= 0 ||
		config.MaxTTL > time.Minute ||
		config.IssuanceTTL <= 0 ||
		config.IssuanceTTL > time.Minute {
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
		policy:      config.Policy,
		maxTTL:      config.MaxTTL,
		issuanceTTL: config.IssuanceTTL,
		now:         now,
		random:      randomSource,
		issued:      make(map[[sha256.Size]byte]int64),
		grants:      make(map[[nonceBytes]byte]int64),
		leases:      make(map[[nonceBytes]byte]int64),
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

func NewCrossrefExecutor(
	guard *Guard,
	verifier research.Verifier,
) (*CrossrefExecutor, error) {
	if guard == nil || nilVerifier(verifier) {
		return nil, ErrInvalidConfig
	}
	return &CrossrefExecutor{
		guard:    guard,
		verifier: verifier,
	}, nil
}

// Verify rebinds the exact normalized query at the final execution boundary,
// consumes the Lease, and only then calls the wrapped verifier. A mismatched
// query does not consume the Lease, so argument substitution cannot turn a
// denial into an accidental external request.
func (executor *CrossrefExecutor) Verify(
	ctx context.Context,
	lease Lease,
	scope Scope,
	proposal ActionProposal,
	query research.Query,
) (research.Verification, DefenseEvent, error) {
	event := DefenseEvent{
		Action:   ActionCrossrefDiscovery,
		Decision: DecisionDeny,
		Reason:   ReasonInvalidAuthority,
		Sources:  proposal.sources,
	}
	if executor == nil || executor.guard == nil ||
		nilVerifier(executor.verifier) {
		return research.Verification{}, event, ErrDenied
	}
	event.Policy = executor.guard.policy
	if ctx == nil {
		return research.Verification{}, event, ErrDenied
	}
	normalizedQuery, err := research.NormalizeQuery(query)
	if err != nil {
		event.Reason = ReasonArgumentMismatch
		return research.Verification{}, event, ErrDenied
	}
	queryArgs, err := executor.guard.queryDigest(normalizedQuery)
	if err != nil || proposal.action != ActionCrossrefDiscovery ||
		proposal.args != queryArgs {
		event.Reason = ReasonArgumentMismatch
		return research.Verification{}, event, ErrDenied
	}
	event, err = executor.guard.ConsumeCrossref(lease, scope, proposal)
	if err != nil {
		return research.Verification{}, event, ErrDenied
	}
	verification, err := callVerifier(
		executor.verifier,
		ctx,
		normalizedQuery,
	)
	return verification, event, err
}

func callVerifier(
	verifier research.Verifier,
	ctx context.Context,
	query research.Query,
) (verification research.Verification, err error) {
	defer func() {
		if recover() != nil {
			verification = research.Verification{}
			err = ErrExecutorUnavailable
		}
	}()
	verification, err = verifier.Verify(ctx, query)
	if err != nil {
		return research.Verification{}, ErrExecutorUnavailable
	}
	return verification, nil
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
		!sources.Has(SourceDeclaredIntentionalAudio) ||
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

// BindDeclaredIntentionalAudioForCrossref is called after deterministic parsing
// has rebound the proposal to the current nonambient transcript. It establishes
// request authority, not speaker authentication; future speaker/liveness proof
// must be a separate required capability.
func (guard *Guard) BindDeclaredIntentionalAudioForCrossref(
	scope Scope,
	query research.Query,
	ttl time.Duration,
) (DeclaredIntentionalAudioGrant, DefenseEvent, error) {
	event := guard.event(
		ActionCrossrefDiscovery,
		DecisionDeny,
		ReasonInvalidAuthority,
		SourceDeclaredIntentionalAudio,
	)
	if guard == nil || !validScope(scope) || !guard.validTTL(ttl) {
		event.Reason = ReasonInvalidScope
		return DeclaredIntentionalAudioGrant{}, event, ErrDenied
	}
	args, err := guard.queryDigest(query)
	if err != nil {
		return DeclaredIntentionalAudioGrant{}, event, ErrDenied
	}
	now := guard.now().UTC()
	scopeDigest := guard.scopeDigest(scope)
	grant := DeclaredIntentionalAudioGrant{
		issuer:    guard.issuer,
		scope:     scopeDigest,
		args:      args,
		policy:    guard.policyDigest,
		action:    ActionCrossrefDiscovery,
		issuedAt:  now.UnixNano(),
		expiresAt: now.Add(ttl).UnixNano(),
		authority: SourceDeclaredIntentionalAudio,
	}
	if err := guard.readRandom(grant.nonce[:]); err != nil {
		return DeclaredIntentionalAudioGrant{}, event, ErrDenied
	}
	grant.signature = guard.signGrant(grant)

	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.cleanupLocked(now.UnixNano())
	issuance := guard.issuanceDigest(
		scopeDigest,
		ActionCrossrefDiscovery,
	)
	if _, exists := guard.issued[issuance]; exists {
		event.Reason = ReasonReplay
		return DeclaredIntentionalAudioGrant{}, event, ErrDenied
	}
	if guard.outstandingLocked()+2 > maxOutstandingRecords {
		event.Reason = ReasonCapacity
		return DeclaredIntentionalAudioGrant{}, event, ErrDenied
	}
	if _, exists := guard.grants[grant.nonce]; exists {
		event.Reason = ReasonTampered
		return DeclaredIntentionalAudioGrant{}, event, ErrDenied
	}
	if _, exists := guard.leases[grant.nonce]; exists {
		event.Reason = ReasonTampered
		return DeclaredIntentionalAudioGrant{}, event, ErrDenied
	}
	guard.issued[issuance] = now.Add(guard.issuanceTTL).UnixNano()
	guard.grants[grant.nonce] = grant.expiresAt
	event.Decision = DecisionAllow
	event.Reason = ReasonAuthorized
	return grant, event, nil
}

func (guard *Guard) MintCrossref(
	grant DeclaredIntentionalAudioGrant,
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
		!proposal.sources.Has(SourceDeclaredIntentionalAudio) ||
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
		grant.authority != SourceDeclaredIntentionalAudio ||
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
	if guard.outstandingLocked()+1 > maxOutstandingRecords {
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
		!proposal.sources.Has(SourceDeclaredIntentionalAudio) ||
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

func (guard *Guard) issuanceDigest(
	scope [sha256.Size]byte,
	action Action,
) [sha256.Size]byte {
	return guard.keyedDigest(
		[]byte("issuance\x00"),
		scope[:],
		[]byte{byte(action)},
	)
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

func (guard *Guard) signGrant(
	grant DeclaredIntentionalAudioGrant,
) [sha256.Size]byte {
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
	for issuance, expiresAt := range guard.issued {
		if now >= expiresAt {
			delete(guard.issued, issuance)
		}
	}
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

func (guard *Guard) outstandingLocked() int {
	return len(guard.issued) + len(guard.grants) + len(guard.leases)
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

func nilVerifier(verifier research.Verifier) bool {
	if verifier == nil {
		return true
	}
	value := reflect.ValueOf(verifier)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
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
