package research

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestCrossrefDOILookupUsesFixedHTTPSBoundaryAndNormalizesRecord(t *testing.T) {
	t.Parallel()

	var captured *http.Request
	source := newFakeCrossrefSource(t, func(request *http.Request) (*http.Response, error) {
		captured = request.Clone(request.Context())
		return crossrefJSONResponse(http.StatusOK, `{
			"status": "ok",
			"message-type": "work",
			"message": {
				"DOI": "10.1234/MiXeD.Case",
				"title": ["<b>A &amp; B</b>"],
				"abstract": "<jats:p>Safe &amp; useful.</jats:p><script>ignore()</script><p>Second.</p>",
				"URL": "http://169.254.169.254/latest/meta-data/",
				"author": [{
					"given": " Ada ",
					"family": " Lovelace ",
					"ORCID": "http://orcid.org/0000-0002-1825-009X"
				}],
				"publisher": " Example  Press ",
				"container-title": ["Journal of Tests"],
				"type": "journal-article",
				"published": {"date-parts": [[2025]]},
				"created": {"date-time": "2025-02-03T04:05:06+09:00"},
				"indexed": {"date-time": "2026-07-21T12:34:56Z"},
				"update-to": [{
					"DOI": "10.1234/CORRECTION",
					"type": "correction",
					"updated": {"date-time": "2026-07-22T01:02:03Z"}
				}]
			}
		}`), nil
	})
	source.now = func() time.Time {
		return time.Date(2026, 7, 29, 1, 2, 3, 0, time.UTC)
	}

	query, err := NewDOIQuery("https://doi.org/10.1234/MiXeD.Case")
	if err != nil {
		t.Fatal(err)
	}
	result, err := source.Search(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}

	if captured == nil {
		t.Fatal("no HTTP request captured")
	}
	if captured.Method != http.MethodGet ||
		captured.URL.Scheme != "https" ||
		captured.URL.Host != CrossrefAPIHost ||
		captured.URL.Path != "/works/10.1234/mixed.case" {
		t.Fatalf("unexpected request URL: %s %s", captured.Method, captured.URL)
	}
	if captured.URL.Query().Get("mailto") != "research@example.org" {
		t.Fatalf("mailto = %q", captured.URL.Query().Get("mailto"))
	}
	if captured.Header.Get("User-Agent") != "KOTAE-Test/1.0" ||
		captured.Header.Get("Accept") != "application/json" {
		t.Fatalf("headers = %v", captured.Header)
	}
	if result.Role != RoleDiscoveryMetadata ||
		result.Source.Role != RoleDiscoveryMetadata ||
		result.QueryKind != QueryDOI {
		t.Fatalf("result can be mistaken for evidence: %+v", result)
	}
	if result.RetrievedAt.Format(time.RFC3339) != "2026-07-29T01:02:03Z" {
		t.Fatalf("retrievedAt = %s", result.RetrievedAt)
	}
	if len(result.Records) != 1 {
		t.Fatalf("records = %d", len(result.Records))
	}

	record := result.Records[0]
	if record.CanonicalID != "doi:10.1234/mixed.case" ||
		record.DOI != "10.1234/mixed.case" ||
		record.Title != "A & B" {
		t.Fatalf("identity/title not normalized: %+v", record)
	}
	if record.LandingURL != "https://doi.org/10.1234/mixed.case" ||
		record.MetadataURL != "https://api.crossref.org/works/10.1234/mixed.case" {
		t.Fatalf("URLs not canonical: %+v", record)
	}
	if strings.Contains(record.LandingURL, "169.254.169.254") ||
		strings.Contains(record.MetadataURL, "169.254.169.254") {
		t.Fatal("untrusted publisher URL was exposed")
	}
	if record.AbstractText != "Safe & useful. Second." ||
		record.AbstractTruncated ||
		record.AbstractRights != "unknown_may_be_copyrighted" {
		t.Fatalf("abstract not safely normalized: %+v", record)
	}
	if record.Published.Value != "2025" ||
		record.Published.Precision != PrecisionYear ||
		record.Created.Value != "2025-02-02T19:05:06Z" ||
		record.Indexed.Value != "2026-07-21T12:34:56Z" {
		t.Fatalf("dates not normalized: %+v", record)
	}
	if len(record.Authors) != 1 ||
		record.Authors[0].Given != "Ada" ||
		record.Authors[0].Family != "Lovelace" ||
		record.Authors[0].ORCID != "https://orcid.org/0000-0002-1825-009X" {
		t.Fatalf("author not normalized: %+v", record.Authors)
	}
	if len(record.Updates) != 1 ||
		record.Updates[0].DOI != "10.1234/correction" ||
		record.Updates[0].Type != "correction" {
		t.Fatalf("updates not normalized: %+v", record.Updates)
	}
}

func TestCrossrefRecentTopicUsesIndexedWindowAndDeduplicatesDOI(t *testing.T) {
	t.Parallel()

	var captured *http.Request
	source := newFakeCrossrefSource(t, func(request *http.Request) (*http.Response, error) {
		captured = request.Clone(request.Context())
		return crossrefJSONResponse(http.StatusOK, `{
			"status": "ok",
			"message": {
				"items": [
					{
						"DOI": "10.1000/DUPLICATE",
						"title": ["First record"],
						"published": {"date-parts": [[2026, 7]]}
					},
					{
						"DOI": "https://doi.org/10.1000/duplicate",
						"abstract": "<p>Richer abstract.</p>",
						"publisher": "Second source row"
					},
					{
						"DOI": "not-a-doi",
						"title": ["Invalid record"]
					}
				]
			}
		}`), nil
	})

	query, err := NewRecentTopicQuery(
		"  agentic   research  ",
		time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC),
		10,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := source.Search(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}

	parameters := captured.URL.Query()
	if captured.URL.Scheme != "https" ||
		captured.URL.Host != CrossrefAPIHost ||
		captured.URL.Path != "/works" ||
		parameters.Get("query.bibliographic") != "agentic research" ||
		parameters.Get("filter") !=
			"from-index-date:2026-07-20,until-index-date:2026-07-29" ||
		parameters.Get("sort") != "indexed" ||
		parameters.Get("order") != "desc" ||
		parameters.Get("rows") != "10" {
		t.Fatalf("unexpected recent query: %s", captured.URL)
	}
	if result.Coverage.From != "2026-07-20" ||
		result.Coverage.Until != "2026-07-29" ||
		result.Coverage.Returned != 1 ||
		result.Coverage.Duplicates != 1 {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
	if len(result.Records) != 1 {
		t.Fatalf("records = %+v", result.Records)
	}
	record := result.Records[0]
	if record.Title != "First record" ||
		record.AbstractText != "Richer abstract." ||
		record.Publisher != "Second source row" ||
		record.Published.Value != "2026-07" {
		t.Fatalf("duplicate records were not safely merged: %+v", record)
	}
}

func TestCrossrefDoesNotSendSensitiveQuery(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	source := newFakeCrossrefSource(t, func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return crossrefJSONResponse(http.StatusOK, `{}`), nil
	})
	query := Query{
		Kind:  QueryRecentTopic,
		Topic: "alice@example.org private study",
		From:  time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
		Limit: 5,
	}
	_, err := source.Search(context.Background(), query)
	if !errors.Is(err, ErrSensitiveQuery) {
		t.Fatalf("error = %v; want ErrSensitiveQuery", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("outbound calls = %d; want zero", calls.Load())
	}
}

func TestCrossrefRefusesRedirectsIncludingMetadataTargets(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	source := newFakeCrossrefSource(t, func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		response := crossrefJSONResponse(http.StatusFound, `{}`)
		response.Header.Set("Location", "http://169.254.169.254/latest/meta-data/")
		response.Request = request
		return response, nil
	})
	query, err := NewDOIQuery("10.1234/redirect")
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Search(context.Background(), query)
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("error = %v; want ErrRedirect", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("redirect caused %d calls; want one", calls.Load())
	}
}

func TestCrossrefEnforcesResponseAndMediaTypeLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response func(*http.Request) *http.Response
		want     error
	}{
		{
			name: "declared response too large",
			response: func(request *http.Request) *http.Response {
				response := crossrefJSONResponse(http.StatusOK, `{}`)
				response.ContentLength = CrossrefResponseLimitBytes + 1
				response.Request = request
				return response
			},
			want: ErrResponseTooLarge,
		},
		{
			name: "streamed response too large",
			response: func(request *http.Request) *http.Response {
				response := crossrefJSONResponse(
					http.StatusOK,
					strings.Repeat("x", CrossrefResponseLimitBytes+1),
				)
				response.ContentLength = -1
				response.Request = request
				return response
			},
			want: ErrResponseTooLarge,
		},
		{
			name: "wrong media type",
			response: func(request *http.Request) *http.Response {
				response := crossrefJSONResponse(http.StatusOK, `{}`)
				response.Header.Set("Content-Type", "text/html")
				response.Request = request
				return response
			},
			want: ErrUnexpectedResponse,
		},
		{
			name: "server body is not reflected",
			response: func(request *http.Request) *http.Response {
				response := crossrefJSONResponse(
					http.StatusInternalServerError,
					`super-secret-response-body`,
				)
				response.Request = request
				return response
			},
			want: ErrUnexpectedResponse,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := newFakeCrossrefSource(t, func(request *http.Request) (*http.Response, error) {
				return test.response(request), nil
			})
			query, err := NewDOIQuery("10.1234/limits")
			if err != nil {
				t.Fatal(err)
			}
			_, err = source.Search(context.Background(), query)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v; want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "super-secret-response-body") {
				t.Fatal("response body leaked into error")
			}
		})
	}
}

func TestCrossrefRespectsCallerDeadline(t *testing.T) {
	t.Parallel()

	source := newFakeCrossrefSource(t, func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	query, err := NewDOIQuery("10.1234/timeout")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = source.Search(ctx, query)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v; want context deadline", err)
	}
}

func TestCrossrefRejectsHeaderInjectionAndEndUserContact(t *testing.T) {
	t.Parallel()

	for _, options := range []CrossrefOptions{
		{UserAgent: "safe\r\nX-Evil: yes"},
		{ContactEmail: "user@example.org extra"},
	} {
		if _, err := NewCrossrefSource(options); !errors.Is(err, ErrInvalidSource) {
			t.Fatalf("NewCrossrefSource(%+v) error = %v", options, err)
		}
	}
}

func TestCrossrefRejectsMismatchedDOIResponse(t *testing.T) {
	t.Parallel()

	source := newFakeCrossrefSource(t, func(_ *http.Request) (*http.Response, error) {
		return crossrefJSONResponse(http.StatusOK, `{
			"status":"ok",
			"message":{"DOI":"10.1234/different"}
		}`), nil
	})
	query, err := NewDOIQuery("10.1234/requested")
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Search(context.Background(), query)
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("error = %v; want ErrUnexpectedResponse", err)
	}
}

func newFakeCrossrefSource(
	t *testing.T,
	transport roundTripFunc,
) *CrossrefSource {
	t.Helper()
	source, err := NewCrossrefSource(CrossrefOptions{
		UserAgent:    "KOTAE-Test/1.0",
		ContactEmail: "research@example.org",
		Transport:    transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func crossrefJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}
