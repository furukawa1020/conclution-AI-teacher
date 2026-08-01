package privacyguard

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	dlp "google.golang.org/api/dlp/v2"
)

const DefaultStrictInspectionTimeout = 1500 * time.Millisecond

// InspectionStatus is deliberately tri-state. The zero value is unknown and
// therefore cannot accidentally grant permission to continue.
type InspectionStatus uint8

const (
	InspectionUnknown InspectionStatus = iota
	InspectionClear
	InspectionFinding
)

type Inspection struct {
	Status InspectionStatus
}

// Inspector is the narrow managed sensitive-data inspection boundary used by
// strict cloud minimization. Implementations must not expose inspected text or
// provider response bodies in returned errors.
type Inspector interface {
	Inspect(context.Context, string) (Inspection, error)
}

// StrictBoundary blocks rather than redacts. It is intentionally separate
// from Protector: a strict caller must never continue with modified text after
// either a local or managed finding.
type StrictBoundary struct {
	inspector Inspector
	timeout   time.Duration
}

func NewStrictBoundary(
	inspector Inspector,
	timeout time.Duration,
) (*StrictBoundary, error) {
	if inspector == nil || timeout < 0 {
		return nil, ErrInvalidConfiguration
	}
	if timeout == 0 {
		timeout = DefaultStrictInspectionTimeout
	}
	return &StrictBoundary{inspector: inspector, timeout: timeout}, nil
}

// Check returns nil only after both the local detector and the managed
// inspector positively classify the text as clear. Findings, timeouts, quota
// failures, cancellations and unknown statuses are all fail-closed. No input
// text is included in the returned error.
func (boundary *StrictBoundary) Check(ctx context.Context, text string) error {
	if boundary == nil || boundary.inspector == nil || ctx == nil {
		return ErrProtectionUnavailable
	}
	if HasHighConfidenceFinding(text) {
		return ErrProtectionUnavailable
	}

	inspectCtx, cancel := context.WithTimeout(ctx, boundary.timeout)
	defer cancel()
	inspection, err := boundary.inspector.Inspect(inspectCtx, text)
	if err != nil || inspectCtx.Err() != nil {
		return ErrProtectionUnavailable
	}
	if inspection.Status != InspectionClear {
		return ErrProtectionUnavailable
	}
	return nil
}

var (
	strictEmailPattern = regexp.MustCompile(
		`(?i)[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@` +
			`[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?` +
			`(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+`,
	)
	strictJWTPattern = regexp.MustCompile(
		`(?i)\beyj[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}\b`,
	)
	strictKnownCredentialPattern = regexp.MustCompile(
		`(?i)\b(?:AIza[0-9a-z_-]{30,}|AKIA[0-9A-Z]{16}|` +
			`gh[pousr]_[0-9a-z]{20,}|sk-[0-9a-z_-]{16,}|` +
			`xox[baprs]-[0-9a-z-]{10,})\b`,
	)
	strictLabeledCredentialPattern = regexp.MustCompile(
		`(?i)(?:api[ _-]?key|access[ _-]?token|refresh[ _-]?token|` +
			`client[ _-]?secret|authorization|password|passwd|pwd|` +
			`secret|apiキー|アクセストークン|認証トークン|` +
			`パスワード|秘密鍵)\s*(?:は|[:=：])\s*` +
			`(?:bearer\s+)?[^\s,;、，。]{4,}`,
	)
	strictPrivateKeyPattern = regexp.MustCompile(
		`(?is)-----begin [a-z0-9 ]*private key-----.*?` +
			`-----end [a-z0-9 ]*private key-----`,
	)
	strictPostalAddressPattern = regexp.MustCompile(
		`(?:〒\s*)?[0-9]{3}[-ー−―]?\s*[0-9]{4}`,
	)
	strictLabeledAddressPattern = regexp.MustCompile(
		`(?i)(?:住所|所在地|address)\s*(?:は|[:：])\s*` +
			`[^\s,，。]{4,80}`,
	)
	strictJapaneseAddressPattern = regexp.MustCompile(
		`(?:東京都|北海道|(?:京都|大阪)府|[一-龯々]{2,3}県)` +
			`[一-龯ぁ-んァ-ヶー0-9 -]{1,40}(?:市|区|町|村|丁目|番地)`,
	)
	strictEnglishAddressPattern = regexp.MustCompile(
		`(?i)\b[0-9]{1,6}\s+[a-z][a-z0-9 .'-]{1,50}\s+` +
			`(?:street|st|road|rd|avenue|ave|boulevard|blvd|lane|ln)\b`,
	)
	strictLabeledNamePattern = regexp.MustCompile(
		`(?i)(?:氏名|本名|フルネーム|名前|full[ -]?name|name)` +
			`\s*(?:は|[:：])\s*[\p{L}々・·.' -]{2,48}(?:です)?`,
	)
)

// HasHighConfidenceFinding recognizes deterministic identifiers and
// explicitly labelled identity fields before another managed service sees the
// text. NFKC and removal of Unicode format controls prevent width and
// zero-width characters from splitting otherwise obvious values.
func HasHighConfidenceFinding(text string) bool {
	if !utf8.ValidString(text) {
		return true
	}
	canonical := strictCanonicalText(text)
	if canonical == "" {
		return false
	}
	for _, pattern := range []*regexp.Regexp{
		strictEmailPattern,
		strictJWTPattern,
		strictKnownCredentialPattern,
		strictLabeledCredentialPattern,
		strictPrivateKeyPattern,
		strictPostalAddressPattern,
		strictLabeledAddressPattern,
		strictJapaneseAddressPattern,
		strictEnglishAddressPattern,
		strictLabeledNamePattern,
	} {
		if pattern.MatchString(canonical) {
			return true
		}
	}
	return strictContainsPhone(canonical)
}

func strictCanonicalText(text string) string {
	normalized := norm.NFKC.String(text)
	return strings.Map(func(character rune) rune {
		if unicode.Is(unicode.Cf, character) {
			return -1
		}
		return character
	}, normalized)
}

// IsResearchRequest identifies text that would ask the conversation agent to
// acquire research or web-search authority. Strict mode has no such authority;
// the check therefore runs before the agent, including for obfuscated Unicode
// spellings normalized by strictCanonicalText.
func IsResearchRequest(text string) bool {
	canonical := strings.ToLower(strictCanonicalText(text))
	for _, signal := range []string{
		"crossref",
		"クロスレフ",
		"doi",
		"論文",
		"プレプリント",
		"研究を",
		"研究して",
		"文献",
		"外部検索",
		"ウェブ検索",
		"web検索",
		"最新技術を調べ",
		"search the web",
		"search online",
		"look up online",
		"find papers",
		"find a paper",
		"latest papers",
		"recent papers",
		"latest studies",
		"recent studies",
		"literature search",
		"research online",
		"use crossref",
	} {
		if strings.Contains(canonical, signal) {
			return true
		}
	}
	return false
}

var strictPhoneCandidatePattern = regexp.MustCompile(
	`(?:\+|[0-9])[0-9()（）\[\] -]{8,}[0-9]`,
)

func strictContainsPhone(text string) bool {
	for _, candidate := range strictPhoneCandidatePattern.FindAllString(text, -1) {
		digits := 0
		for _, character := range candidate {
			if unicode.IsDigit(character) {
				digits++
			}
		}
		if digits >= 10 && digits <= 15 &&
			(strings.HasPrefix(candidate, "+") ||
				strings.HasPrefix(candidate, "0")) {
			return true
		}
	}
	return false
}

type googleInspectClient interface {
	Inspect(
		context.Context,
		string,
		*dlp.GooglePrivacyDlpV2InspectContentRequest,
	) (*dlp.GooglePrivacyDlpV2InspectContentResponse, error)
}

type GoogleDLPInspector struct {
	client    googleInspectClient
	parent    string
	infoTypes []string
}

var _ Inspector = (*GoogleDLPInspector)(nil)

// NewGoogleDLPInspector reuses the authenticated, regional client and explicit
// infoType configuration validated by New. This avoids a second credential or
// endpoint configuration path.
func NewGoogleDLPInspector(protector *DLPProtector) (*GoogleDLPInspector, error) {
	if protector == nil || protector.client == nil {
		return nil, ErrInvalidConfiguration
	}
	client, ok := protector.client.(*googleDLPClient)
	if !ok || client.service == nil {
		return nil, ErrInvalidConfiguration
	}
	return &GoogleDLPInspector{
		client:    &googleInspectServiceClient{service: client.service},
		parent:    protector.parent,
		infoTypes: append([]string(nil), protector.infoTypes...),
	}, nil
}

type googleInspectServiceClient struct {
	service *dlp.Service
}

func (client *googleInspectServiceClient) Inspect(
	ctx context.Context,
	parent string,
	request *dlp.GooglePrivacyDlpV2InspectContentRequest,
) (*dlp.GooglePrivacyDlpV2InspectContentResponse, error) {
	return client.service.Projects.Locations.Content.
		Inspect(parent, request).
		Context(ctx).
		Do()
}

func (inspector *GoogleDLPInspector) Inspect(
	ctx context.Context,
	text string,
) (Inspection, error) {
	if inspector == nil || inspector.client == nil || ctx == nil {
		return Inspection{}, ErrProtectionUnavailable
	}
	infoTypes := make([]*dlp.GooglePrivacyDlpV2InfoType, 0, len(inspector.infoTypes))
	for _, name := range inspector.infoTypes {
		infoTypes = append(infoTypes, &dlp.GooglePrivacyDlpV2InfoType{Name: name})
	}
	request := &dlp.GooglePrivacyDlpV2InspectContentRequest{
		InspectConfig: &dlp.GooglePrivacyDlpV2InspectConfig{
			InfoTypes:       infoTypes,
			MinLikelihood:   "POSSIBLE",
			IncludeQuote:    false,
			ForceSendFields: []string{"IncludeQuote"},
		},
		Item: &dlp.GooglePrivacyDlpV2ContentItem{Value: text},
	}
	response, err := inspector.client.Inspect(ctx, inspector.parent, request)
	if err != nil || ctx.Err() != nil || response == nil || response.Result == nil {
		return Inspection{}, ErrProtectionUnavailable
	}
	if len(response.Result.Findings) > 0 || response.Result.FindingsTruncated {
		return Inspection{Status: InspectionFinding}, nil
	}
	return Inspection{Status: InspectionClear}, nil
}
