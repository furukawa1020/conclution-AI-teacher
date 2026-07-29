package research

import (
	"encoding/base64"
	"errors"
	"strconv"
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
		"10.1234/a,10.5678/b",
		"10.1234/public,cancel",
		"10.1234/public;cancel",
		"10.1234/ａｂｃ",
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
		"contact alice＠example.org about the paper",
		"contact alice@exаmple.org about the paper",
		"John\u200bSmith depression",
		"alice%40example.org%ZZ",
		"alice&#64;example.org",
		"YWxpY2VAZXhhbXBsZS5vcmc=",
		"YWxpY2VA ZXhhbXBsZS5vcmc=",
		"YWxpY2VAZXhhbXBsZS5vcmcA",
		"YWxpY2VAZXhhbXBsZS5vcmf_",
		"Sm9obiBTbWl0aCBkZXByZXNzaW9uAAAAAA",
		"WVd4cFkyVkFaWGhoYlhCc1pTNXZjbWM",
		"WVd4cFkyVkFaWGhoYlhCc1pTNXZjbWM9AA",
		"YWxpY2VAZXhhbXBsZS5vcmc%3D",
		"YWxpY2VAZXhhbXBsZS5vcmc&#61;",
		"password%3Dhunter2%ZZ",
		"著者の電話は 090-1234-5678",
		"ship results to 100-0001",
		"送付先は〒 100-0001",
		"password = hunter2",
		"password hunter2",
		"client secret hunter2",
		"api key hunter2",
		"api key abc",
		"client secret abc",
		"api key 秘密値",
		"password は 機密値です",
		"Authorization: Bearer abcdefghijklmnop",
		"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signatureABC",
		"key sk-abcdefghijklmnopqrstuvwxyz",
		"card 4111 1111 1111 1111",
		"card 4222222222222",
		"30569309025904",
		"phone 2125551212",
		"phone-6561234567",
		"fax 6561234567",
		"6561234567",
		"patient12345678 depression",
		"patient no 12345678",
		"患者番号12345678",
		"MFWGSY3FIBSXQYLNOBWGKLTPOJTQ",
		"mfwgsy3fibsxqylnobwgkltpojtq",
		"MFWGSY3FIBSXQYLNOBWGKLTPOJTQAAAAAAAAAAAA",
		"YWxpY2VAZXhhbXBsZS5vcmcAAAAAAAAAAA",
		"-----BEGIN PRIVATE KEY----- abc",
		"田中太郎のうつ病",
		"田中太郎",
		"小鳥遊光",
		"小鳥遊-光",
		"小鳥遊光の研究",
		"小鳥遊量子",
		"小鳥遊光の量子計算",
		"田中太郎 ADHD",
		"田中 太郎 ADHD",
		"田中・太郎 ADHD",
		"John Smith depression",
		"John Smith",
		"john smith depression",
		"José García depression",
		"xavier zane",
		"Xavier Quantum",
		"Xavier Modell",
		"xavier quill quantum computing",
		"xavier/quill quantum computing",
		"xаvier quill quantum computing",
		"turing quantum computing systems",
		"量子計算田中太郎",
		"量子計算田中太́郎",
		"cаncel",
		"cαncel",
		"xavier quill research",
		"Xavier Quill research",
		"山田はな ADHD",
		"私の症状と治療",
		"patient named Alice and depression",
	}
	for index, topic := range tests {
		index, topic := index, topic
		t.Run(strconv.Itoa(index), func(t *testing.T) {
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

func TestDeeplyNestedReversibleEncodingFailsClosed(t *testing.T) {
	t.Parallel()

	topic := "alice@example.org"
	for range maxSensitiveDecodeDepth + 1 {
		topic = base64.RawStdEncoding.EncodeToString([]byte(topic))
	}
	_, err := NewRecentTopicQuery(
		topic,
		time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
		5,
	)
	if !errors.Is(err, ErrSensitiveQuery) {
		t.Fatalf("error = %v; want ErrSensitiveQuery", err)
	}
}

func TestSensitivePayloadEmbeddedInDOIIsRejected(t *testing.T) {
	t.Parallel()
	for _, rawDOI := range []string{
		"10.1234/alice@example.org",
		"10.1234/4111－1111－1111－1111",
		"10.1234/4111−1111−1111−1111",
		"10.1234/٤١١١-١١١١-١١١١-١١١١",
		"10.1234/alice%40example.org",
		"10.1234/alice%2540example.org",
		"10.1234/password%3Dhunter2",
		"10.1234/password-hunter2",
		"10.1234/client-secret-hunter2",
		"10.1234/api-key-abc",
		"10.1234/MFWGSY3FIBSXQYLNOBWGKLTPOJTQ",
		"10.1234/mfwgsy3fibsxqylnobwgkltpojtq",
		"10.1234/MFWGSY3FIBSXQYLNOBWGKLTPOJTQAAAAAAAAAAAA",
		"10.1234/YWxpY2VAZXhhbXBsZS5vcmcAAAAAAAAAAA",
		"10.1234/6561234567",
		"10.1234/phone-6561234567",
		"10.1234/john-smith-depression",
		"10.1234/田中太郎-ADHD",
		"10.1234/田中-太郎-adhd",
		"10.1234/田中太郎.ADHD",
		"10.1234/john.smith.adhd",
		"10.1234/Xavier.Quill.depression",
		"10.1234/(john.smith.adhd)",
		"10.1234/john:smith:adhd",
		"10.1234/sk-abcdefghijklmnopqrstuvwxyz",
		"10.1234/eyJabcdefgh.eyJijklmnop.eyJqrstuvwx",
		"10.1234/YWxpY2VAZXhhbXBsZS5vcmc=",
		"10.1234/YWxpY2VAZXhhbXBsZS5vcmcA",
		"10.1234/YWxpY2VAZXhhbXBsZS5vcmf_",
		"10.1234/Sm9obiBTbWl0aCBkZXByZXNzaW9uAAAAAA",
		"10.1234/foo.MDkwLTEyMzQtNTY3OA==",
		"10.1234/foo.MTAwLTAwMDE=",
	} {
		if _, err := NewDOIQuery(rawDOI); !errors.Is(err, ErrSensitiveQuery) {
			t.Errorf("NewDOIQuery(%q) error = %v; want ErrSensitiveQuery", rawDOI, err)
		}
	}
}

func TestResearchTermsAreNotMistakenForActualSecrets(t *testing.T) {
	t.Parallel()

	for _, topic := range []string{
		"API key rotation methods",
		"client secret management",
		"password hashing methods",
		"Password Spraying Detection",
		"API Key Watermarking",
		"PII removal in language models",
		"telephone fraud detection",
		"OpenAI GPT-5.6 evaluation",
		"Japan 2020-01-01 to 2024-01-01",
		"量子エラー訂正",
		"作業記憶",
		"睡眠",
		"Quantum Computing",
		"Graph Neural Networks",
		"retrieval augmented generation",
		"telephone fraud detection",
		"Artificial Intelligence",
		"Natural Language Processing",
		"Data Science",
		"Software Engineering",
		"Human Computer Interaction",
		"Microplastic Pollution",
		"Protein Structure Prediction",
		"Base64 encoding methods",
		"post quantum cryptography",
		"homomorphic encryption",
		"diffusion models",
		"拡散モデル",
		"大規模言語モデル",
		"Neuromorphic Photonics",
		"Mechanochemical Recycling",
		"Spatial Omics",
		"patient outcomes research",
		"patient safety research",
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

func TestBenignOpaqueDOIsAreNotMistakenForEncodedPII(t *testing.T) {
	t.Parallel()

	for _, rawDOI := range []string{
		"10.1234/YW5hbHlzaXM",
		"10.1234/AbCdEfGhIjKlMnOpQrStUvWx",
		"10.1038/s41586-020-2649-2",
	} {
		if _, err := NewDOIQuery(rawDOI); err != nil {
			t.Errorf("NewDOIQuery(%q) rejected: %v", rawDOI, err)
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
			_, err := NewRecentTopicQuery(
				"quantum computing",
				from,
				test.until,
				test.limit,
			)
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("error = %v; want ErrInvalidQuery", err)
			}
		})
	}
}

func TestRecentTopicRejectsClauseBoundariesAndWithdrawal(t *testing.T) {
	t.Parallel()

	for _, topic := range []string{
		"量子、いや、やめて",
		"quantum computing, cancel",
		"quantum computing; scratch that",
		"量子エラー訂正を検索しないで",
		"YWxpY/2VAZXhhbXBsZS5vcmc=",
		"YW:0:xp:0:Y2:0:VA:0:ZX:0:hh:0:bX:0:Bs:0:ZS:0:5v:0:cm:0:c=",
		"YW.api.xp.api.Y2.api.VA.api.ZX.api.hh.api.bX.api.Bs.api.ZS.api.5v.api.cm.api.c",
	} {
		_, err := NewRecentTopicQuery(
			topic,
			time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
			5,
		)
		if !errors.Is(err, ErrInvalidQuery) {
			t.Errorf(
				"NewRecentTopicQuery(%q) error = %v; want ErrInvalidQuery",
				topic,
				err,
			)
		}
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
