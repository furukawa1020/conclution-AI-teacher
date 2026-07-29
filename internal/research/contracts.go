// Package research discovers academic records without treating discovery
// metadata as proof of a claim.
//
// The package deliberately has no arbitrary-URL fetcher. Source implementations
// may contact only their reviewed, fixed API endpoint. A Result can identify
// material worth checking, but callers must obtain and inspect an authorised
// primary source before presenting a claim as verified.
package research

import (
	"context"
	"errors"
	"time"
)

const (
	MaxDOIRunes       = 256
	MaxTopicRunes     = 240
	MaxResults        = 25
	MaxAbstractRunes  = 8_000
	MaxRecentInterval = 31 * 24 * time.Hour
)

var (
	ErrInvalidQuery        = errors.New("research: invalid query")
	ErrSensitiveQuery      = errors.New("research: outbound query may contain sensitive data")
	ErrInvalidSource       = errors.New("research: invalid source")
	ErrInvalidSourceResult = errors.New("research: invalid source result")
	ErrSourceUnavailable   = errors.New("research: source unavailable")
	ErrNotFound            = errors.New("research: record not found")
	ErrRateLimited         = errors.New("research: source rate limited")
	ErrUnexpectedResponse  = errors.New("research: unexpected source response")
	ErrResponseTooLarge    = errors.New("research: source response too large")
	ErrRedirect            = errors.New("research: source redirect refused")
)

type SourceID string

const SourceCrossref SourceID = "crossref"

// MaterialRole prevents source discovery from being mistaken for claim
// verification. There is intentionally no "evidence" role in this MVP.
type MaterialRole string

const RoleDiscoveryMetadata MaterialRole = "discovery_metadata_not_claim_evidence"

type QueryKind string

const (
	QueryDOI         QueryKind = "doi"
	QueryRecentTopic QueryKind = "recent_topic"
)

// Query contains only the minimum text needed by an academic metadata source.
// It must be normalized and screened again immediately before an outbound call.
type Query struct {
	Kind  QueryKind
	DOI   string
	Topic string
	From  time.Time
	Until time.Time
	Limit int
}

type SourceDescriptor struct {
	ID        SourceID     `json:"id"`
	Name      string       `json:"name"`
	Authority string       `json:"authority"`
	Role      MaterialRole `json:"role"`
}

// Source is a typed, read-only discovery adapter.
type Source interface {
	Descriptor() SourceDescriptor
	Search(context.Context, Query) (Result, error)
}

type DatePrecision string

const (
	PrecisionYear      DatePrecision = "year"
	PrecisionMonth     DatePrecision = "month"
	PrecisionDay       DatePrecision = "day"
	PrecisionTimestamp DatePrecision = "timestamp"
)

// NormalizedDate preserves partial publication dates instead of inventing a
// January 1 date when the source supplied only a year.
type NormalizedDate struct {
	Value     string        `json:"value"`
	Precision DatePrecision `json:"precision"`
}

type Author struct {
	Given  string `json:"given,omitempty"`
	Family string `json:"family,omitempty"`
	ORCID  string `json:"orcid,omitempty"`
}

type Update struct {
	DOI     string         `json:"doi,omitempty"`
	Type    string         `json:"type"`
	Updated NormalizedDate `json:"updated,omitempty"`
}

// Record is normalized discovery metadata. AbstractText may still be
// copyrighted; it is plain text only and must not be persisted or redistributed
// without a separate rights decision.
type Record struct {
	CanonicalID       string         `json:"canonical_id"`
	DOI               string         `json:"doi"`
	Title             string         `json:"title,omitempty"`
	Authors           []Author       `json:"authors,omitempty"`
	AbstractText      string         `json:"abstract_text,omitempty"`
	AbstractTruncated bool           `json:"abstract_truncated,omitempty"`
	AbstractRights    string         `json:"abstract_rights"`
	Publisher         string         `json:"publisher,omitempty"`
	ContainerTitle    string         `json:"container_title,omitempty"`
	WorkType          string         `json:"work_type,omitempty"`
	LandingURL        string         `json:"landing_url"`
	MetadataURL       string         `json:"metadata_url"`
	Published         NormalizedDate `json:"published,omitempty"`
	Created           NormalizedDate `json:"created,omitempty"`
	Indexed           NormalizedDate `json:"indexed,omitempty"`
	Updates           []Update       `json:"updates,omitempty"`
}

type Coverage struct {
	From       string `json:"from,omitempty"`
	Until      string `json:"until,omitempty"`
	Returned   int    `json:"returned"`
	Duplicates int    `json:"duplicates"`
}

// Result intentionally omits the raw topic so accidental logs and persistence
// do not copy the user's research subject.
type Result struct {
	Source      SourceDescriptor `json:"source"`
	Role        MaterialRole     `json:"role"`
	QueryKind   QueryKind        `json:"query_kind"`
	RetrievedAt time.Time        `json:"retrieved_at"`
	Coverage    Coverage         `json:"coverage"`
	Records     []Record         `json:"records"`
}

type VerificationStatus string

const (
	// StatusNeedsPrimaryEvidence means discovery succeeded, not that the
	// requested claim is true. It is the only successful status in this MVP.
	StatusNeedsPrimaryEvidence VerificationStatus = "needs_primary_evidence"
)

type Verification struct {
	Status      VerificationStatus `json:"status"`
	Role        MaterialRole       `json:"role"`
	QueryKind   QueryKind          `json:"query_kind"`
	RetrievedAt time.Time          `json:"retrieved_at"`
	Sources     []SourceDescriptor `json:"sources"`
	Records     []Record           `json:"records"`
}

// Verifier is the future-facing contract for Research Verifier. The present
// implementation returns discovery metadata with StatusNeedsPrimaryEvidence;
// it does not make claim-level truth judgments.
type Verifier interface {
	Verify(context.Context, Query) (Verification, error)
}
