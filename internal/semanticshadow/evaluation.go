package semanticshadow

import (
	"encoding/json"
	"errors"
)

const (
	RelationDirect      = "direct"
	RelationRestatement = "restatement"
	RelationUnresolved  = "unresolved"
	RelationConflict    = "conflict"
)

// Comparison is a finite, content-free description of a disagreement between
// the current QBA proof and a shadow graph relation. No transcript, answer, or
// free-form explanation can be represented by this type.
type Comparison string

const (
	ComparisonMatch                   Comparison = "match"
	ComparisonDirectToRestatement     Comparison = "direct_to_restatement"
	ComparisonDirectToUnresolved      Comparison = "direct_to_unresolved"
	ComparisonDirectToConflict        Comparison = "direct_to_conflict"
	ComparisonRestatementToDirect     Comparison = "restatement_to_direct"
	ComparisonRestatementToUnresolved Comparison = "restatement_to_unresolved"
	ComparisonRestatementToConflict   Comparison = "restatement_to_conflict"
	ComparisonUnresolvedToDirect      Comparison = "unresolved_to_direct"
	ComparisonUnresolvedToRestatement Comparison = "unresolved_to_restatement"
	ComparisonUnresolvedToConflict    Comparison = "unresolved_to_conflict"
	ComparisonConflictToDirect        Comparison = "conflict_to_direct"
	ComparisonConflictToRestatement   Comparison = "conflict_to_restatement"
	ComparisonConflictToUnresolved    Comparison = "conflict_to_unresolved"
)

// CompareRelations rejects unknown values instead of turning them into a
// label. This keeps metrics cardinality finite and prevents content from being
// smuggled into logs or traces as a purported relation.
func CompareRelations(current, shadow string) (Comparison, error) {
	if !validRelation(current) || !validRelation(shadow) {
		return "", errors.New("semantic shadow comparison contains an unknown relation")
	}
	if current == shadow {
		return ComparisonMatch, nil
	}
	pairs := map[[2]string]Comparison{
		{RelationDirect, RelationRestatement}:     ComparisonDirectToRestatement,
		{RelationDirect, RelationUnresolved}:      ComparisonDirectToUnresolved,
		{RelationDirect, RelationConflict}:        ComparisonDirectToConflict,
		{RelationRestatement, RelationDirect}:     ComparisonRestatementToDirect,
		{RelationRestatement, RelationUnresolved}: ComparisonRestatementToUnresolved,
		{RelationRestatement, RelationConflict}:   ComparisonRestatementToConflict,
		{RelationUnresolved, RelationDirect}:      ComparisonUnresolvedToDirect,
		{RelationUnresolved, RelationRestatement}: ComparisonUnresolvedToRestatement,
		{RelationUnresolved, RelationConflict}:    ComparisonUnresolvedToConflict,
		{RelationConflict, RelationDirect}:        ComparisonConflictToDirect,
		{RelationConflict, RelationRestatement}:   ComparisonConflictToRestatement,
		{RelationConflict, RelationUnresolved}:    ComparisonConflictToUnresolved,
	}
	return pairs[[2]string{current, shadow}], nil
}

func validRelation(value string) bool {
	return oneOf(value, RelationDirect, RelationRestatement, RelationUnresolved, RelationConflict)
}

// EvaluationSummary is deliberately a closed schema instead of a map. Its
// canonical JSON is safe for offline aggregate artifacts because neither keys
// nor values can carry user content.
type EvaluationSummary struct {
	Total                   uint64 `json:"total"`
	Match                   uint64 `json:"match"`
	DirectToRestatement     uint64 `json:"directToRestatement"`
	DirectToUnresolved      uint64 `json:"directToUnresolved"`
	DirectToConflict        uint64 `json:"directToConflict"`
	RestatementToDirect     uint64 `json:"restatementToDirect"`
	RestatementToUnresolved uint64 `json:"restatementToUnresolved"`
	RestatementToConflict   uint64 `json:"restatementToConflict"`
	UnresolvedToDirect      uint64 `json:"unresolvedToDirect"`
	UnresolvedToRestatement uint64 `json:"unresolvedToRestatement"`
	UnresolvedToConflict    uint64 `json:"unresolvedToConflict"`
	ConflictToDirect        uint64 `json:"conflictToDirect"`
	ConflictToRestatement   uint64 `json:"conflictToRestatement"`
	ConflictToUnresolved    uint64 `json:"conflictToUnresolved"`
}

// Add records exactly one accepted finite comparison. On error it leaves the
// summary unchanged, making unknown relation handling fail closed.
func (s *EvaluationSummary) Add(current, shadow string) error {
	if s == nil {
		return errors.New("semantic shadow evaluation summary is required")
	}
	comparison, err := CompareRelations(current, shadow)
	if err != nil {
		return err
	}
	s.Total++
	s.increment(comparison)
	return nil
}

func (s *EvaluationSummary) increment(comparison Comparison) {
	switch comparison {
	case ComparisonMatch:
		s.Match++
	case ComparisonDirectToRestatement:
		s.DirectToRestatement++
	case ComparisonDirectToUnresolved:
		s.DirectToUnresolved++
	case ComparisonDirectToConflict:
		s.DirectToConflict++
	case ComparisonRestatementToDirect:
		s.RestatementToDirect++
	case ComparisonRestatementToUnresolved:
		s.RestatementToUnresolved++
	case ComparisonRestatementToConflict:
		s.RestatementToConflict++
	case ComparisonUnresolvedToDirect:
		s.UnresolvedToDirect++
	case ComparisonUnresolvedToRestatement:
		s.UnresolvedToRestatement++
	case ComparisonUnresolvedToConflict:
		s.UnresolvedToConflict++
	case ComparisonConflictToDirect:
		s.ConflictToDirect++
	case ComparisonConflictToRestatement:
		s.ConflictToRestatement++
	case ComparisonConflictToUnresolved:
		s.ConflictToUnresolved++
	}
}

func (s EvaluationSummary) CanonicalJSON() ([]byte, error) {
	return json.Marshal(s)
}
