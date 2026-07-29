package research

import (
	"context"
	"time"
)

// DiscoveryVerifier coordinates reviewed sources while preserving the
// distinction between finding a record and verifying a claim.
type DiscoveryVerifier struct {
	sources []Source
	now     func() time.Time
}

func NewDiscoveryVerifier(sources ...Source) (*DiscoveryVerifier, error) {
	if len(sources) == 0 {
		return nil, ErrInvalidSource
	}
	seen := make(map[SourceID]struct{}, len(sources))
	copied := make([]Source, 0, len(sources))
	for _, source := range sources {
		if source == nil {
			return nil, ErrInvalidSource
		}
		descriptor := source.Descriptor()
		if descriptor.ID == "" ||
			descriptor.Name == "" ||
			descriptor.Authority == "" ||
			descriptor.Role != RoleDiscoveryMetadata {
			return nil, ErrInvalidSource
		}
		if _, duplicate := seen[descriptor.ID]; duplicate {
			return nil, ErrInvalidSource
		}
		seen[descriptor.ID] = struct{}{}
		copied = append(copied, source)
	}
	return &DiscoveryVerifier{
		sources: copied,
		now:     time.Now,
	}, nil
}

func (v *DiscoveryVerifier) Verify(
	ctx context.Context,
	query Query,
) (Verification, error) {
	if v == nil || len(v.sources) == 0 || v.now == nil {
		return Verification{}, ErrInvalidSource
	}
	query, err := NormalizeQuery(query)
	if err != nil {
		return Verification{}, err
	}

	sources := make([]SourceDescriptor, 0, len(v.sources))
	allRecords := make([]Record, 0)
	for _, source := range v.sources {
		if err := ctx.Err(); err != nil {
			return Verification{}, err
		}
		result, err := source.Search(ctx, query)
		if err != nil {
			return Verification{}, err
		}
		descriptor := source.Descriptor()
		if result.Source != descriptor ||
			result.Role != RoleDiscoveryMetadata ||
			result.QueryKind != query.Kind {
			return Verification{}, ErrInvalidSourceResult
		}
		sources = append(sources, descriptor)
		allRecords = append(allRecords, result.Records...)
	}
	records, _ := deduplicateRecords(allRecords)

	return Verification{
		Status:      StatusNeedsPrimaryEvidence,
		Role:        RoleDiscoveryMetadata,
		QueryKind:   query.Kind,
		RetrievedAt: v.now().UTC(),
		Sources:     sources,
		Records:     records,
	}, nil
}

var _ Source = (*CrossrefSource)(nil)
var _ Verifier = (*DiscoveryVerifier)(nil)
