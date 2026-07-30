// Package shadowcut synthesizes and verifies content-free causal cuts over a
// one-sink chain-forest proof IR. A general agent DAG must be decomposed into
// one trusted root-to-outcome chain bundle per sink before it enters this
// package; that compiler and its attestation are intentionally out of scope.
//
// It is deliberately disconnected from the production securityflow monitor:
// this package has no executor, persistence, network, signing, or apply API.
package shadowcut

import "errors"

const (
	// SchemaVersion identifies the fixed-width canonical encoding used by this
	// package.
	SchemaVersion uint16 = 3

	MaxControls        = 16
	MaxTraces          = 1_024
	MaxNodesPerTrace   = 64
	MaxEdgesPerTrace   = 128
	MaxSourcesPerTrace = 16
	MaxSinksPerTrace   = 1
	MaxTraceWeight     = 1_000_000
	maxSearchWork      = 50_000_000
)

var (
	ErrInvalidCorpus    = errors.New("shadowcut: invalid corpus")
	ErrInvalidTrace     = errors.New("shadowcut: invalid trace")
	ErrInvalidCandidate = errors.New("shadowcut: invalid candidate")
	ErrInfeasible       = errors.New("shadowcut: no feasible finite cut")
	ErrVerification     = errors.New("shadowcut: verification failed")
)

// NodeID is a dense, one-based causal event ordinal local to one trace. Edges
// must move from a lower ordinal to a higher ordinal. It is not a request,
// session, user, content, URL, or provider identifier.
type NodeID uint16

type NodeKind uint8

const (
	NodeUnknown NodeKind = iota
	NodeAuthenticatedIntent
	NodeAmbient
	NodePDF
	NodeWeb
	NodeModel
	NodeTool
	NodeMemory
	NodeGrant
	NodeSink
	nodeKindLimit
)

type EdgeKind uint8

const (
	EdgeUnknown EdgeKind = iota
	EdgeQuote
	EdgeRecall
	EdgePropose
	EdgeIssueGrant
	EdgeConsume
	EdgeToolCall
	EdgeToolResult
	EdgeExecute
	EdgeRespond
	edgeKindLimit
)

type Integrity uint8

const (
	IntegrityUnknown Integrity = iota
	IntegrityUntrusted
	IntegrityAuthenticated
	IntegritySystem
	integrityLimit
)

// Authority is a finite capability set, not an identity or content-derived
// score.
type Authority uint16

const (
	AuthorityResearchRead Authority = 1 << iota
	AuthoritySecretRead
	AuthorityExternalWrite
	AuthorityHighMemoryWrite
	AuthorityDestructiveAction
)

const allAuthorities = AuthorityResearchRead |
	AuthoritySecretRead |
	AuthorityExternalWrite |
	AuthorityHighMemoryWrite |
	AuthorityDestructiveAction

type Binding uint8

const (
	BindingNone Binding = iota
	BindingScopeOnly
	BindingExactScopeActionArgs
	bindingLimit
)

// BindingRef is a trace-local, content-free equality witness. It is never a
// UID, request ID, URL, query hash, or provider identifier.
type BindingRef uint16

// Control is a compile-time enforcement point. A model cannot add controls or
// provide executable policy text.
type Control uint8

const (
	ControlUnknown Control = iota
	ControlEvidenceIngress
	ControlPlannerBoundary
	ControlMemoryBoundary
	ControlGrantBoundary
	ControlToolBoundary
	ControlExecutionBoundary
	ControlSpeechBoundary
	ControlExternalEgress
	controlLimit
)

const controlCostCount = 8

var (
	_ [int(controlLimit-1) - controlCostCount]struct{}
	_ [controlCostCount - int(controlLimit-1)]struct{}
)

type ControlMask uint16

const knownControlMask ControlMask = (1 << (controlLimit - 1)) - 1

type TraceClass uint8

const (
	TraceUnknown TraceClass = iota
	TraceAttack
	TraceBenign
	traceClassLimit
)

type behaviorFingerprint [1 + ((1 << (controlLimit - 1)) / 8)]byte

// Node contains finite security labels only.
type Node struct {
	ID         NodeID
	Kind       NodeKind
	Integrity  Integrity
	Authority  Authority
	Requires   Authority
	Binding    Binding
	BindingRef BindingRef
}

// Edge contains finite provenance and enforcement labels only. An edge is
// removed by a candidate when Controls and the candidate mask intersect.
type Edge struct {
	From     NodeID
	To       NodeID
	Kind     EdgeKind
	Controls ControlMask
}

// Trace is a normalized one-sink chain forest supplied by trusted offline
// instrumentation. Independent chains may meet only at the terminal Sink.
// Class and Weight are trusted aggregate labels; they must never be authored
// by a model, retrieved content, or a live user. Sources identify attack
// origins for an attack trace and required normal inputs for a benign trace.
type Trace struct {
	Class   TraceClass
	Weight  uint32
	Nodes   []Node
	Edges   []Edge
	Sources []NodeID
	Sinks   []NodeID
}

// Candidate is a shadow-only policy recommendation. This package intentionally
// provides no method that can apply it to a production monitor.
type Candidate struct {
	Schema   uint16
	Controls ControlMask
}

// Certificate commits to a content-free trace set and the exact finite-space
// objective. It is not a signature and conveys no production authority.
type Certificate struct {
	Schema               uint16
	TraceSetHash         [32]byte
	CandidateHash        [32]byte
	AttackTraceCount     uint32
	BlockedAttackCount   uint32
	BenignWeightTotal    uint64
	BenignWeightRetained uint64
	ControlCount         uint8
	ControlCost          uint32
}

// ControlMaskOf constructs a mask from known finite controls.
func ControlMaskOf(controls ...Control) (ControlMask, error) {
	var mask ControlMask
	for _, control := range controls {
		if control <= ControlUnknown || control >= controlLimit {
			return 0, ErrInvalidCandidate
		}
		mask |= 1 << (control - 1)
	}
	return mask, nil
}

func validControlMask(mask ControlMask) bool {
	return mask&^knownControlMask == 0
}

func controlCost(mask ControlMask) uint32 {
	// Costs are fixed integers to keep synthesis deterministic. They are only
	// a final tie-break after normal-path retention and control count.
	costs := [controlCostCount]uint16{
		1, // evidence ingress
		3, // planner boundary
		2, // memory boundary
		4, // grant boundary
		2, // tool boundary
		5, // execution boundary
		4, // speech boundary
		5, // external egress
	}
	var total uint32
	for index, cost := range costs {
		if mask&(1<<index) != 0 {
			total += uint32(cost)
		}
	}
	return total
}
