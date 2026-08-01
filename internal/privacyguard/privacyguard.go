// Package privacyguard provides a fail-closed boundary before text is sent to
// another managed service. Its detectors reduce exposure but are not exhaustive.
package privacyguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
	dlp "google.golang.org/api/dlp/v2"
	"google.golang.org/api/option"
)

const (
	// MaxInputBytes follows the Sensitive Data Protection direct-content limit.
	MaxInputBytes = 512 * 1024

	DefaultTimeout = 5 * time.Second
)

// defaultInfoTypes is deliberately explicit. Adding provider detectors must
// be reviewed rather than silently changing the production disclosure policy.
var defaultInfoTypes = []string{
	"AUTH_TOKEN",
	"AWS_CREDENTIALS",
	"BASIC_AUTH_HEADER",
	"CREDIT_CARD_DATA",
	"DATE_OF_BIRTH",
	"DRIVERS_LICENSE_NUMBER",
	"EMAIL_ADDRESS",
	"ENCRYPTION_KEY",
	"FINANCIAL_ACCOUNT_NUMBER",
	"GCP_API_KEY",
	"GCP_CREDENTIALS",
	"GENERIC_ID",
	"GEOGRAPHIC_DATA",
	"HTTP_COOKIE",
	"IP_ADDRESS",
	"JAPAN_BANK_ACCOUNT",
	"JAPAN_DRIVERS_LICENSE_NUMBER",
	"JAPAN_INDIVIDUAL_NUMBER",
	"JAPAN_PASSPORT",
	"JSON_WEB_TOKEN",
	"MEDICAL_RECORD_NUMBER",
	"OAUTH_CLIENT_SECRET",
	"OPENAI_API_KEY",
	"PASSPORT",
	"PASSWORD",
	"PERSON_NAME",
	"PHONE_NUMBER",
	"SECURITY_DATA",
	"STORAGE_SIGNED_URL",
	"STREET_ADDRESS",
	"USER_NAME",
	"XSRF_TOKEN",
}

// DefaultInfoTypes returns a copy of the reviewed production detector policy.
func DefaultInfoTypes() []string {
	return append([]string(nil), defaultInfoTypes...)
}

var (
	ErrInvalidConfiguration  = errors.New("privacy guard configuration is invalid")
	ErrInputTooLarge         = errors.New("privacy guard input exceeds the direct-content limit")
	ErrProtectionUnavailable = errors.New("privacy guard protection is unavailable")
	ErrInvalidResponse       = errors.New("privacy guard received an invalid provider response")
)

// Result is safe to pass to a downstream service only when Protect returned a
// nil error. Redacted reports whether either local screening or the managed
// de-identification call changed the text.
type Result struct {
	Text     string
	Redacted bool
}

// Protector is the narrow boundary used before a downstream managed service.
// Callers must not continue with the original text when Protect returns an
// error.
type Protector interface {
	Protect(ctx context.Context, text string) (Result, error)
}

// Config selects one Sensitive Data Protection processing region and an
// explicit, predictable set of infoTypes.
type Config struct {
	ProjectID string
	Location  string
	InfoTypes []string
	Timeout   time.Duration
}

type deidentifyClient interface {
	Deidentify(
		ctx context.Context,
		parent string,
		request *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
	) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error)
}

type googleDLPClient struct {
	service *dlp.Service
}

func (c *googleDLPClient) Deidentify(
	ctx context.Context,
	parent string,
	request *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error) {
	return c.service.Projects.Locations.Content.
		Deidentify(parent, request).
		Context(ctx).
		Do()
}

// DLPProtector screens deterministic high-risk patterns locally, then invokes
// the regional Sensitive Data Protection content.deidentify API.
type DLPProtector struct {
	client    deidentifyClient
	parent    string
	infoTypes []string
	timeout   time.Duration
}

var _ Protector = (*DLPProtector)(nil)

// New constructs a protector that uses the regional endpoint for the selected
// location. The client logger is explicitly discarded so request bodies cannot
// be emitted by API-client debug logging.
func New(ctx context.Context, config Config) (*DLPProtector, error) {
	if ctx == nil {
		return nil, ErrInvalidConfiguration
	}

	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	discardLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError + 1,
	}))
	service, err := dlp.NewService(
		ctx,
		option.WithEndpoint(regionalEndpoint(normalized.Location)),
		option.WithLogger(discardLogger),
	)
	if err != nil {
		// Do not propagate provider errors: they are outside this package's
		// control and could contain request or credential details.
		return nil, ErrProtectionUnavailable
	}

	return protectorWithClient(normalized, &googleDLPClient{service: service})
}

func protectorWithClient(
	config Config,
	client deidentifyClient,
) (*DLPProtector, error) {
	if client == nil {
		return nil, ErrInvalidConfiguration
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	return &DLPProtector{
		client: client,
		parent: fmt.Sprintf(
			"projects/%s/locations/%s",
			normalized.ProjectID,
			normalized.Location,
		),
		infoTypes: append([]string(nil), normalized.InfoTypes...),
		timeout:   normalized.Timeout,
	}, nil
}

// Protect returns no text on any provider, timeout, cancellation, or response
// validation failure. Known deterministic patterns are replaced before the API
// call so those values never enter its request body.
func (p *DLPProtector) Protect(
	ctx context.Context,
	text string,
) (Result, error) {
	if ctx == nil || p == nil || p.client == nil {
		return Result{}, ErrProtectionUnavailable
	}
	if len(text) > MaxInputBytes {
		return Result{}, ErrInputTooLarge
	}
	if text == "" {
		return Result{}, nil
	}

	screened, locallyRedacted := screenDeterministic(text)
	if len(screened) > MaxInputBytes {
		return Result{}, ErrInputTooLarge
	}
	request := p.deidentifyRequest(screened)

	callContext, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	response, err := p.client.Deidentify(callContext, p.parent, request)
	if err != nil {
		return Result{}, safeCallError(callContext)
	}
	if err := callContext.Err(); err != nil {
		return Result{}, safeCallError(callContext)
	}
	if !validResponse(response, screened) {
		return Result{}, ErrInvalidResponse
	}

	protectedText := response.Item.Value
	return Result{
		Text:     protectedText,
		Redacted: locallyRedacted || protectedText != screened,
	}, nil
}

func (p *DLPProtector) deidentifyRequest(
	text string,
) *dlp.GooglePrivacyDlpV2DeidentifyContentRequest {
	infoTypes := make([]*dlp.GooglePrivacyDlpV2InfoType, 0, len(p.infoTypes))
	for _, name := range p.infoTypes {
		infoTypes = append(infoTypes, &dlp.GooglePrivacyDlpV2InfoType{Name: name})
	}

	return &dlp.GooglePrivacyDlpV2DeidentifyContentRequest{
		InspectConfig: &dlp.GooglePrivacyDlpV2InspectConfig{
			InfoTypes:     infoTypes,
			MinLikelihood: "POSSIBLE",
			IncludeQuote:  false,
			// Boolean zero values are normally omitted by the generated client.
			// Force this field into JSON so the privacy setting is explicit.
			ForceSendFields: []string{"IncludeQuote"},
		},
		DeidentifyConfig: &dlp.GooglePrivacyDlpV2DeidentifyConfig{
			InfoTypeTransformations: &dlp.GooglePrivacyDlpV2InfoTypeTransformations{
				Transformations: []*dlp.GooglePrivacyDlpV2InfoTypeTransformation{
					{
						PrimitiveTransformation: &dlp.GooglePrivacyDlpV2PrimitiveTransformation{
							ReplaceWithInfoTypeConfig: &dlp.GooglePrivacyDlpV2ReplaceWithInfoTypeConfig{},
						},
					},
				},
			},
			TransformationErrorHandling: &dlp.GooglePrivacyDlpV2TransformationErrorHandling{
				ThrowError: &dlp.GooglePrivacyDlpV2ThrowError{},
			},
		},
		Item: &dlp.GooglePrivacyDlpV2ContentItem{Value: text},
	}
}

func safeCallError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.Join(ErrProtectionUnavailable, context.DeadlineExceeded)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return errors.Join(ErrProtectionUnavailable, context.Canceled)
	}
	return ErrProtectionUnavailable
}

func validResponse(
	response *dlp.GooglePrivacyDlpV2DeidentifyContentResponse,
	requestText string,
) bool {
	if response == nil || response.Item == nil {
		return false
	}
	if requestText != "" && response.Item.Value == "" {
		return false
	}
	if hasTransformationError(response.Overview) {
		return false
	}

	// A managed response must not reintroduce a deterministic value which was
	// barred from the request. Discard the whole response instead of returning a
	// second locally modified version whose provenance would be ambiguous.
	_, unsafe := screenDeterministic(response.Item.Value)
	return !unsafe
}

func hasTransformationError(
	overview *dlp.GooglePrivacyDlpV2TransformationOverview,
) bool {
	if overview == nil {
		return false
	}
	for _, summary := range overview.TransformationSummaries {
		if summary == nil {
			continue
		}
		for _, result := range summary.Results {
			if result == nil || result.Count == 0 {
				continue
			}
			if result.Code != "SUCCESS" {
				return true
			}
		}
	}
	return false
}

var (
	projectIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	locationPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	infoTypePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
)

func normalizeConfig(config Config) (Config, error) {
	config.ProjectID = strings.TrimSpace(config.ProjectID)
	config.Location = strings.TrimSpace(config.Location)
	if !projectIDPattern.MatchString(config.ProjectID) ||
		!locationPattern.MatchString(config.Location) ||
		config.Location == "global" {
		return Config{}, ErrInvalidConfiguration
	}
	if config.Timeout < 0 {
		return Config{}, ErrInvalidConfiguration
	}
	if config.Timeout == 0 {
		config.Timeout = DefaultTimeout
	}

	unique := make(map[string]struct{}, len(config.InfoTypes))
	infoTypes := make([]string, 0, len(config.InfoTypes))
	for _, rawName := range config.InfoTypes {
		name := strings.TrimSpace(rawName)
		if !infoTypePattern.MatchString(name) {
			return Config{}, ErrInvalidConfiguration
		}
		if _, exists := unique[name]; exists {
			continue
		}
		unique[name] = struct{}{}
		infoTypes = append(infoTypes, name)
	}
	if len(infoTypes) == 0 {
		return Config{}, ErrInvalidConfiguration
	}
	sort.Strings(infoTypes)
	config.InfoTypes = infoTypes
	return config, nil
}

func regionalEndpoint(location string) string {
	return fmt.Sprintf("https://dlp.%s.rep.googleapis.com/", location)
}

const (
	emailReplacement      = "[EMAIL]"
	phoneReplacement      = "[PHONE]"
	longIDReplacement     = "[ID]"
	credentialReplacement = "[SECRET]"
	urlReplacement        = "[URL]"

	credentialLabelPattern = `(?i)(?:\b(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|client[_ -]?secret|password|passwd|pwd|secret|authorization)\b|APIキー|アクセストークン|認証トークン|パスワード|秘密鍵)`
	credentialSeparator    = `\s*(?:[:=：]|は|\bis\b)\s*`
)

var deterministicRules = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{
		regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
		credentialReplacement,
	},
	{
		regexp.MustCompile(`(?i)\b(?:https?|ftp)://[^\s<>"'）)」』】、。]+|\bwww\.[^\s<>"'）)」』】、。]+`),
		urlReplacement,
	},
	{
		regexp.MustCompile(credentialLabelPattern + credentialSeparator + `"[^"\r\n]{4,}"`),
		credentialReplacement,
	},
	{
		regexp.MustCompile(credentialLabelPattern + credentialSeparator + `'[^'\r\n]{4,}'`),
		credentialReplacement,
	},
	{
		regexp.MustCompile(credentialLabelPattern + credentialSeparator + `(?:bearer[ \t]+)?[^\s,;，；、。]{4,}`),
		credentialReplacement,
	},
	{
		regexp.MustCompile(`(?i)\bbearer[ \t]+[A-Za-z0-9._~+/=-]{8,}\b`),
		credentialReplacement,
	},
	{
		regexp.MustCompile(`\b(?:AIza[0-9A-Za-z_-]{35}|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9_-]{16,}|xox[baprs]-[A-Za-z0-9-]{10,})\b`),
		credentialReplacement,
	},
	{
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
		credentialReplacement,
	},
	{
		regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+`),
		emailReplacement,
	},
}

var (
	phonePattern  = regexp.MustCompile(`(?:\+|＋)?[0-9０-９][-0-9０-９()（） .\t－ー]{6,}[0-9０-９]`)
	longIDPattern = regexp.MustCompile(`\b[A-Za-z0-9][A-Za-z0-9_-]{15,}\b`)
)

func screenDeterministic(text string) (string, bool) {
	text, redacted := normalizeForScreening(text)
	for _, rule := range deterministicRules {
		next := rule.pattern.ReplaceAllString(text, rule.replacement)
		if next != text {
			redacted = true
			text = next
		}
	}

	// Long identifiers must be classified before the deliberately broad phone
	// expression so account IDs do not receive a misleading PHONE placeholder.
	text = longIDPattern.ReplaceAllStringFunc(text, func(candidate string) string {
		if countDigits(candidate) == 0 {
			return candidate
		}
		redacted = true
		return longIDReplacement
	})

	text = phonePattern.ReplaceAllStringFunc(text, func(candidate string) string {
		if countDigits(candidate) < 10 {
			return candidate
		}
		redacted = true
		return phoneReplacement
	})
	return text, redacted
}

// normalizeForScreening closes common Unicode evasion paths before any regular
// expression or managed detector sees the text. Compatibility variants are
// canonicalized and invisible format controls are removed. A changed value is
// reported as redacted so callers can discard conversation state derived from
// the pre-normalized text.
func normalizeForScreening(text string) (string, bool) {
	normalized := norm.NFKC.String(text)
	changed := normalized != text

	var builder strings.Builder
	builder.Grow(len(normalized))
	for _, character := range normalized {
		if unicode.Is(unicode.Cf, character) {
			changed = true
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String(), changed
}

func countDigits(value string) int {
	count := 0
	for _, character := range value {
		if unicode.IsDigit(character) {
			count++
		}
	}
	return count
}
