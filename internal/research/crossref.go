package research

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	CrossrefAPIHost            = "api.crossref.org"
	CrossrefRequestTimeout     = 8 * time.Second
	CrossrefResponseLimitBytes = 2 * 1024 * 1024

	maxTitleRunes       = 1_000
	maxAuthorsPerRecord = 100
)

type CrossrefOptions struct {
	// UserAgent should identify the application. It must never contain a user
	// identifier or current-turn text.
	UserAgent string
	// ContactEmail is an optional service contact used for Crossref's polite
	// pool. It must be an operational address, never the end user's address.
	ContactEmail string
	// Transport exists for controlled infrastructure and fake-HTTP tests. It
	// cannot change the fixed request scheme or host.
	Transport http.RoundTripper
}

type CrossrefSource struct {
	client       *http.Client
	userAgent    string
	contactEmail string
	now          func() time.Time
}

func NewCrossrefSource(options CrossrefOptions) (*CrossrefSource, error) {
	userAgent := strings.TrimSpace(options.UserAgent)
	if userAgent == "" {
		userAgent = "KOTAE-ResearchVerifier/0.1"
	}
	if !safeHeaderValue(userAgent, 200) {
		return nil, ErrInvalidSource
	}

	contactEmail := strings.TrimSpace(options.ContactEmail)
	if contactEmail != "" &&
		(emailPattern.FindString(contactEmail) != contactEmail ||
			!safeHeaderValue(contactEmail, 254)) {
		return nil, ErrInvalidSource
	}

	transport := options.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &CrossrefSource{
		client: &http.Client{
			Transport: transport,
			Timeout:   CrossrefRequestTimeout,
			CheckRedirect: func(
				_ *http.Request,
				_ []*http.Request,
			) error {
				return ErrRedirect
			},
		},
		userAgent:    userAgent,
		contactEmail: contactEmail,
		now:          time.Now,
	}, nil
}

func (s *CrossrefSource) Descriptor() SourceDescriptor {
	return SourceDescriptor{
		ID:        SourceCrossref,
		Name:      "Crossref",
		Authority: "https://" + CrossrefAPIHost,
		Role:      RoleDiscoveryMetadata,
	}
}

func (s *CrossrefSource) Search(
	ctx context.Context,
	query Query,
) (Result, error) {
	if s == nil || s.client == nil || s.now == nil {
		return Result{}, ErrInvalidSource
	}
	query, err := NormalizeQuery(query)
	if err != nil {
		return Result{}, err
	}

	endpoint, err := crossrefEndpoint(query, s.contactEmail)
	if err != nil {
		return Result{}, err
	}
	requestContext, cancel := context.WithTimeout(ctx, CrossrefRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		endpoint.String(),
		nil,
	)
	if err != nil {
		return Result{}, ErrInvalidQuery
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", s.userAgent)

	payload, err := s.readResponse(request)
	if err != nil {
		return Result{}, err
	}

	records, err := decodeCrossref(query.Kind, payload)
	if err != nil {
		return Result{}, err
	}
	if query.Kind == QueryDOI &&
		(len(records) != 1 || records[0].DOI != query.DOI) {
		return Result{}, ErrUnexpectedResponse
	}
	records, duplicates := deduplicateRecords(records)
	result := Result{
		Source:      s.Descriptor(),
		Role:        RoleDiscoveryMetadata,
		QueryKind:   query.Kind,
		RetrievedAt: s.now().UTC(),
		Coverage: Coverage{
			Returned:   len(records),
			Duplicates: duplicates,
		},
		Records: records,
	}
	if query.Kind == QueryRecentTopic {
		result.Coverage.From = query.From.Format(time.DateOnly)
		result.Coverage.Until = query.Until.Format(time.DateOnly)
	}
	return result, nil
}

func crossrefEndpoint(query Query, contactEmail string) (*url.URL, error) {
	endpoint := &url.URL{
		Scheme: "https",
		Host:   CrossrefAPIHost,
	}
	parameters := make(url.Values)
	if contactEmail != "" {
		parameters.Set("mailto", contactEmail)
	}

	switch query.Kind {
	case QueryDOI:
		endpoint.Path = "/works/" + query.DOI
	case QueryRecentTopic:
		endpoint.Path = "/works"
		parameters.Set("query.bibliographic", query.Topic)
		parameters.Set(
			"filter",
			"from-index-date:"+query.From.Format(time.DateOnly)+
				",until-index-date:"+query.Until.Format(time.DateOnly),
		)
		parameters.Set("sort", "indexed")
		parameters.Set("order", "desc")
		parameters.Set("rows", strconv.Itoa(query.Limit))
	default:
		return nil, ErrInvalidQuery
	}
	endpoint.RawQuery = parameters.Encode()

	if endpoint.Scheme != "https" ||
		endpoint.Host != CrossrefAPIHost ||
		endpoint.User != nil {
		return nil, ErrInvalidSource
	}
	return endpoint, nil
}

func (s *CrossrefSource) readResponse(request *http.Request) ([]byte, error) {
	if request.URL.Scheme != "https" ||
		request.URL.Host != CrossrefAPIHost ||
		request.URL.User != nil {
		return nil, ErrInvalidSource
	}

	response, err := s.client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		switch {
		case request.Context().Err() != nil:
			return nil, request.Context().Err()
		case errors.Is(err, ErrRedirect):
			return nil, ErrRedirect
		default:
			// The underlying URL error can contain the raw research topic, so
			// it is deliberately not wrapped or returned.
			return nil, ErrSourceUnavailable
		}
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusTooManyRequests:
		return nil, ErrRateLimited
	default:
		return nil, ErrUnexpectedResponse
	}

	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return nil, ErrUnexpectedResponse
	}
	if response.ContentLength > CrossrefResponseLimitBytes {
		return nil, ErrResponseTooLarge
	}

	payload, err := io.ReadAll(io.LimitReader(
		response.Body,
		CrossrefResponseLimitBytes+1,
	))
	if err != nil {
		return nil, ErrSourceUnavailable
	}
	if len(payload) > CrossrefResponseLimitBytes {
		return nil, ErrResponseTooLarge
	}
	return payload, nil
}

type crossrefEnvelope struct {
	Status  string          `json:"status"`
	Message json.RawMessage `json:"message"`
}

type crossrefList struct {
	Items []crossrefWork `json:"items"`
}

type crossrefWork struct {
	DOI             string           `json:"DOI"`
	Title           []string         `json:"title"`
	Abstract        string           `json:"abstract"`
	Author          []crossrefAuthor `json:"author"`
	Publisher       string           `json:"publisher"`
	ContainerTitle  []string         `json:"container-title"`
	Type            string           `json:"type"`
	Published       crossrefDate     `json:"published"`
	PublishedPrint  crossrefDate     `json:"published-print"`
	PublishedOnline crossrefDate     `json:"published-online"`
	Created         crossrefDate     `json:"created"`
	Indexed         crossrefDate     `json:"indexed"`
	UpdateTo        []crossrefUpdate `json:"update-to"`
}

type crossrefAuthor struct {
	Given  string `json:"given"`
	Family string `json:"family"`
	ORCID  string `json:"ORCID"`
}

type crossrefDate struct {
	DateParts [][]int `json:"date-parts"`
	DateTime  string  `json:"date-time"`
}

type crossrefUpdate struct {
	DOI     string       `json:"DOI"`
	Type    string       `json:"type"`
	Updated crossrefDate `json:"updated"`
}

func decodeCrossref(kind QueryKind, payload []byte) ([]Record, error) {
	var envelope crossrefEnvelope
	if len(payload) == 0 ||
		json.Unmarshal(payload, &envelope) != nil ||
		envelope.Status != "ok" ||
		len(envelope.Message) == 0 {
		return nil, ErrUnexpectedResponse
	}

	var works []crossrefWork
	switch kind {
	case QueryDOI:
		var work crossrefWork
		if json.Unmarshal(envelope.Message, &work) != nil {
			return nil, ErrUnexpectedResponse
		}
		works = []crossrefWork{work}
	case QueryRecentTopic:
		var list crossrefList
		if json.Unmarshal(envelope.Message, &list) != nil {
			return nil, ErrUnexpectedResponse
		}
		works = list.Items
	default:
		return nil, ErrInvalidQuery
	}

	records := make([]Record, 0, len(works))
	for _, work := range works {
		record, ok := normalizeCrossrefWork(work)
		if ok {
			records = append(records, record)
		}
	}
	if kind == QueryDOI && len(records) != 1 {
		return nil, ErrUnexpectedResponse
	}
	return records, nil
}

func normalizeCrossrefWork(work crossrefWork) (Record, bool) {
	doi, err := NormalizeDOI(work.DOI)
	if err != nil {
		return Record{}, false
	}

	title := ""
	if len(work.Title) > 0 {
		title = safeSourceText(work.Title[0], maxTitleRunes)
	}
	abstractText, abstractTruncated := plainTextAbstract(work.Abstract)

	authors := make([]Author, 0, min(len(work.Author), maxAuthorsPerRecord))
	for _, sourceAuthor := range work.Author {
		if len(authors) == maxAuthorsPerRecord {
			break
		}
		author := Author{
			Given:  safeSourceText(sourceAuthor.Given, 200),
			Family: safeSourceText(sourceAuthor.Family, 200),
			ORCID:  normalizeORCID(sourceAuthor.ORCID),
		}
		if author.Given == "" && author.Family == "" && author.ORCID == "" {
			continue
		}
		authors = append(authors, author)
	}

	containerTitle := ""
	if len(work.ContainerTitle) > 0 {
		containerTitle = safeSourceText(work.ContainerTitle[0], 500)
	}
	published, _ := preferredPublicationDate(work)
	created, _ := normalizedCrossrefTimestamp(work.Created)
	indexed, _ := normalizedCrossrefTimestamp(work.Indexed)

	updates := make([]Update, 0, len(work.UpdateTo))
	seenUpdates := make(map[string]struct{}, len(work.UpdateTo))
	for _, sourceUpdate := range work.UpdateTo {
		updateDOI, err := NormalizeDOI(sourceUpdate.DOI)
		if err != nil {
			continue
		}
		updateType := safeSourceText(sourceUpdate.Type, 100)
		key := updateDOI + "\x00" + updateType
		if _, duplicate := seenUpdates[key]; duplicate {
			continue
		}
		seenUpdates[key] = struct{}{}
		updated, _ := normalizedCrossrefTimestamp(sourceUpdate.Updated)
		updates = append(updates, Update{
			DOI:     updateDOI,
			Type:    updateType,
			Updated: updated,
		})
	}

	return Record{
		CanonicalID:       "doi:" + doi,
		DOI:               doi,
		Title:             title,
		Authors:           authors,
		AbstractText:      abstractText,
		AbstractTruncated: abstractTruncated,
		AbstractRights:    "unknown_may_be_copyrighted",
		Publisher:         safeSourceText(work.Publisher, 500),
		ContainerTitle:    containerTitle,
		WorkType:          safeSourceText(work.Type, 100),
		LandingURL:        canonicalDOIURL(doi),
		MetadataURL:       crossrefMetadataURL(doi),
		Published:         published,
		Created:           created,
		Indexed:           indexed,
		Updates:           updates,
	}, true
}

func preferredPublicationDate(work crossrefWork) (NormalizedDate, bool) {
	for _, candidate := range []crossrefDate{
		work.Published,
		work.PublishedOnline,
		work.PublishedPrint,
	} {
		if normalized, ok := normalizedCrossrefPartialDate(candidate); ok {
			return normalized, true
		}
	}
	return NormalizedDate{}, false
}

func normalizedCrossrefPartialDate(value crossrefDate) (NormalizedDate, bool) {
	if len(value.DateParts) > 0 {
		if normalized, ok := normalizedDateParts(value.DateParts[0]); ok {
			return normalized, true
		}
	}
	return normalizedCrossrefTimestamp(value)
}

func normalizedCrossrefTimestamp(value crossrefDate) (NormalizedDate, bool) {
	if strings.TrimSpace(value.DateTime) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value.DateTime)
		if err == nil {
			return NormalizedDate{
				Value:     parsed.UTC().Format(time.RFC3339Nano),
				Precision: PrecisionTimestamp,
			}, true
		}
	}
	if len(value.DateParts) > 0 {
		return normalizedDateParts(value.DateParts[0])
	}
	return NormalizedDate{}, false
}

func crossrefMetadataURL(doi string) string {
	return (&url.URL{
		Scheme: "https",
		Host:   CrossrefAPIHost,
		Path:   "/works/" + doi,
	}).String()
}

func safeSourceText(value string, maxRunes int) string {
	text, _ := plainTextAbstract(value)
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return text
}

func deduplicateRecords(records []Record) ([]Record, int) {
	result := make([]Record, 0, len(records))
	indexByID := make(map[string]int, len(records))
	duplicates := 0
	for _, record := range records {
		if record.CanonicalID == "" || record.DOI == "" {
			continue
		}
		if index, duplicate := indexByID[record.CanonicalID]; duplicate {
			result[index] = mergeRecord(result[index], record)
			duplicates++
			continue
		}
		indexByID[record.CanonicalID] = len(result)
		result = append(result, record)
	}
	return result, duplicates
}

func mergeRecord(current Record, candidate Record) Record {
	if current.Title == "" {
		current.Title = candidate.Title
	}
	if len(current.Authors) == 0 {
		current.Authors = candidate.Authors
	}
	if len([]rune(candidate.AbstractText)) > len([]rune(current.AbstractText)) {
		current.AbstractText = candidate.AbstractText
		current.AbstractTruncated = candidate.AbstractTruncated
	}
	if current.Publisher == "" {
		current.Publisher = candidate.Publisher
	}
	if current.ContainerTitle == "" {
		current.ContainerTitle = candidate.ContainerTitle
	}
	if current.WorkType == "" {
		current.WorkType = candidate.WorkType
	}
	if current.Published.Value == "" {
		current.Published = candidate.Published
	}
	if current.Created.Value == "" {
		current.Created = candidate.Created
	}
	if current.Indexed.Value == "" {
		current.Indexed = candidate.Indexed
	}
	current.Updates = mergeUpdates(current.Updates, candidate.Updates)
	return current
}

func mergeUpdates(current []Update, candidate []Update) []Update {
	seen := make(map[string]struct{}, len(current)+len(candidate))
	result := make([]Update, 0, len(current)+len(candidate))
	for _, update := range append(append([]Update(nil), current...), candidate...) {
		key := update.DOI + "\x00" + update.Type
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, update)
	}
	return result
}

func safeHeaderValue(value string, maxRunes int) bool {
	if value == "" ||
		!utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) || char == '\r' || char == '\n' {
			return false
		}
	}
	return true
}
