package research

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeDOI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bare DOI",
			input: " 10.1234/ABC.Def ",
			want:  "10.1234/abc.def",
		},
		{
			name:  "doi prefix",
			input: "DOI: 10.55555/Some(Thing)",
			want:  "10.55555/some(thing)",
		},
		{
			name:  "canonical resolver URL",
			input: "https://doi.org/10.1000%2FMiXeD",
			want:  "10.1000/mixed",
		},
		{
			name:  "legacy resolver URL",
			input: "http://dx.doi.org/10.1000/Legacy",
			want:  "10.1000/legacy",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeDOI(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("NormalizeDOI() = %q; want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeDOIRejectsRequestConfusion(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"",
		"not-a-doi",
		"10.1/short-prefix",
		"10.1234/has space",
		"10.1234/fragment#value",
		"10.1234/query?value=1",
		"10.1234/back\\slash",
		"https://evil.example/10.1234/work",
		"https://user@doi.org/10.1234/work",
		"https://doi.org:444/10.1234/work",
		"https://doi.org/10.1234/work?download=1",
	} {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if _, err := NormalizeDOI(input); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("NormalizeDOI(%q) error = %v; want ErrInvalidQuery", input, err)
			}
		})
	}
}

func TestRecentTopicQueryIsMinimalAndUTCDateBounded(t *testing.T) {
	t.Parallel()

	jst := time.FixedZone("JST", 9*60*60)
	query, err := NewRecentTopicQuery(
		"  retrieval   augmented\n generation  ",
		time.Date(2026, 7, 1, 23, 30, 0, 0, jst),
		time.Date(2026, 7, 8, 9, 0, 0, 0, jst),
		7,
	)
	if err != nil {
		t.Fatal(err)
	}
	if query.Topic != "retrieval augmented generation" {
		t.Fatalf("topic = %q", query.Topic)
	}
	if got := query.From.Format(time.RFC3339); got != "2026-07-01T00:00:00Z" {
		t.Fatalf("from = %s", got)
	}
	if got := query.Until.Format(time.RFC3339); got != "2026-07-08T00:00:00Z" {
		t.Fatalf("until = %s", got)
	}
	if query.Limit != 7 || query.DOI != "" {
		t.Fatalf("query not minimal: %+v", query)
	}
}

func TestSensitiveTopicIsRejectedBeforeOutboundUse(t *testing.T) {
	t.Parallel()

	tests := []string{
		"contact alice@example.org about the paper",
		"著者の電話は 090-1234-5678",
		"ship results to 100-0001",
		"password = hunter2",
		"Authorization: Bearer abcdefghijklmnop",
		"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signatureABC",
		"key sk-abcdefghijklmnopqrstuvwxyz",
		"card 4111 1111 1111 1111",
		"-----BEGIN PRIVATE KEY----- abc",
	}
	for _, topic := range tests {
		topic := topic
		t.Run(topic[:min(16, len(topic))], func(t *testing.T) {
			t.Parallel()
			_, err := NewRecentTopicQuery(
				topic,
				time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
				5,
			)
			if !errors.Is(err, ErrSensitiveQuery) {
				t.Fatalf("error = %v; want ErrSensitiveQuery", err)
			}
			if strings.Contains(err.Error(), topic) {
				t.Fatal("sensitive value was copied into the error")
			}
		})
	}
}

func TestResearchTermsAreNotMistakenForActualSecrets(t *testing.T) {
	t.Parallel()

	for _, topic := range []string{
		"API key rotation methods",
		"PII removal in language models",
		"telephone fraud detection",
		"OpenAI GPT-5.6 evaluation",
		"Japan 2020-01-01 to 2024-01-01",
	} {
		if _, err := NewRecentTopicQuery(
			topic,
			time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
			5,
		); err != nil {
			t.Fatalf("topic %q rejected: %v", topic, err)
		}
	}
}

func TestRecentTopicWindowAndLimitAreBounded(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		until time.Time
		limit int
	}{
		{
			name:  "too old",
			until: from.Add(MaxRecentInterval + 24*time.Hour),
			limit: 5,
		},
		{name: "zero limit", until: from, limit: 0},
		{name: "too many", until: from, limit: MaxResults + 1},
		{name: "reversed", until: from.Add(-24 * time.Hour), limit: 5},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewRecentTopicQuery("safe topic", from, test.until, test.limit)
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("error = %v; want ErrInvalidQuery", err)
			}
		})
	}
}

func TestPlainTextAbstractRemovesMarkupAndExecutableContent(t *testing.T) {
	t.Parallel()

	raw := `<jats:p>First &amp; second.</jats:p>` +
		`<script>steal()</script><style>.hidden{}</style>` +
		`<div>Last&nbsp;line.</div><!-- ignored -->`
	got, truncated := plainTextAbstract(raw)
	if truncated {
		t.Fatal("short abstract was truncated")
	}
	if got != "First & second. Last line." {
		t.Fatalf("plainTextAbstract() = %q", got)
	}
	if strings.Contains(got, "steal") || strings.Contains(got, "hidden") {
		t.Fatalf("executable content survived: %q", got)
	}
}

func TestPlainTextAbstractHasARuneLimit(t *testing.T) {
	t.Parallel()

	got, truncated := plainTextAbstract(
		"<p>" + strings.Repeat("界", MaxAbstractRunes+20) + "</p>",
	)
	if !truncated || len([]rune(got)) != MaxAbstractRunes {
		t.Fatalf("truncated = %v, runes = %d", truncated, len([]rune(got)))
	}
}

func TestNormalizedDatePartsPreservesPrecisionAndCalendarValidity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		parts     []int
		want      NormalizedDate
		wantValid bool
	}{
		{
			parts: []int{2026},
			want: NormalizedDate{
				Value:     "2026",
				Precision: PrecisionYear,
			},
			wantValid: true,
		},
		{
			parts: []int{2026, 7},
			want: NormalizedDate{
				Value:     "2026-07",
				Precision: PrecisionMonth,
			},
			wantValid: true,
		},
		{
			parts: []int{2024, 2, 29},
			want: NormalizedDate{
				Value:     "2024-02-29",
				Precision: PrecisionDay,
			},
			wantValid: true,
		},
		{parts: []int{2026, 2, 29}},
		{parts: []int{2026, 13}},
	}
	for _, test := range tests {
		got, valid := normalizedDateParts(test.parts)
		if valid != test.wantValid || got != test.want {
			t.Fatalf(
				"normalizedDateParts(%v) = (%+v, %v); want (%+v, %v)",
				test.parts,
				got,
				valid,
				test.want,
				test.wantValid,
			)
		}
	}
}
