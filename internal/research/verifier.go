package research

import (
	"context"
	"net/url"
	"reflect"
	"time"
	"unicode/utf8"
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
		if nilSource(source) {
			return nil, ErrInvalidSource
		}
		descriptor := source.Descriptor()
		if !validSourceDescriptor(descriptor) {
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
			result.QueryKind != query.Kind ||
			result.RetrievedAt.IsZero() {
			return Verification{}, ErrInvalidSourceResult
		}
		for _, record := range result.Records {
			if !validSourceRecord(descriptor, record) {
				return Verification{}, ErrInvalidSourceResult
			}
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

func nilSource(source Source) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func validSourceDescriptor(descriptor SourceDescriptor) bool {
	if descriptor.ID == "" ||
		descriptor.Name == "" ||
		descriptor.Authority == "" ||
		descriptor.Role != RoleDiscoveryMetadata {
		return false
	}
	authority, err := url.Parse(descriptor.Authority)
	return err == nil &&
		authority.Scheme == "https" &&
		authority.Hostname() != "" &&
		authority.User == nil &&
		authority.RawQuery == "" &&
		authority.Fragment == ""
}

func validSourceRecord(descriptor SourceDescriptor, record Record) bool {
	doi, err := NormalizeDOI(record.DOI)
	if err != nil ||
		doi != record.DOI ||
		record.CanonicalID != "doi:"+doi ||
		record.LandingURL != canonicalDOIURL(doi) ||
		record.AbstractRights == "" ||
		!utf8.ValidString(record.AbstractText) ||
		len([]rune(record.AbstractText)) > MaxAbstractRunes {
		return false
	}

	metadataURL, err := url.Parse(record.MetadataURL)
	authority, authorityErr := url.Parse(descriptor.Authority)
	if err != nil ||
		authorityErr != nil ||
		metadataURL.Scheme != "https" ||
		metadataURL.Hostname() == "" ||
		metadataURL.User != nil ||
		metadataURL.Fragment != "" ||
		metadataURL.Hostname() != authority.Hostname() {
		return false
	}
	for _, update := range record.Updates {
		updateDOI, err := NormalizeDOI(update.DOI)
		if err != nil || updateDOI != update.DOI || update.Type == "" {
			return false
		}
	}
	return true
}

var _ Source = (*CrossrefSource)(nil)
var _ Verifier = (*DiscoveryVerifier)(nil)
