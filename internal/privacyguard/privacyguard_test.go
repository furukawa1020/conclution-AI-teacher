package privacyguard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	dlp "google.golang.org/api/dlp/v2"
)

type fakeDeidentifier struct {
	call func(
		context.Context,
		string,
		*dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
	) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error)
}

func (f fakeDeidentifier) Deidentify(
	ctx context.Context,
	parent string,
	request *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error) {
	return f.call(ctx, parent, request)
}

func testConfig() Config {
	return Config{
		ProjectID: "kotae-security",
		Location:  "asia-northeast1",
		InfoTypes: []string{
			"PHONE_NUMBER",
			"EMAIL_ADDRESS",
			"PERSON_NAME",
			"EMAIL_ADDRESS",
		},
		Timeout: 250 * time.Millisecond,
	}
}

func TestProtectScreensLocallyBeforeExplicitRegionalRequest(t *testing.T) {
	t.Parallel()

	rawValues := []string{
		"alice@example.com",
		"090-1234-5678",
		"customer_ABC123456789012345",
		"very-secret-value",
		"https://example.com/private?q=1",
	}
	input := "email=" + rawValues[0] +
		" phone=" + rawValues[1] +
		" id=" + rawValues[2] +
		" password=\"" + rawValues[3] + "\"" +
		" url=" + rawValues[4]

	var calls int
	client := fakeDeidentifier{call: func(
		ctx context.Context,
		parent string,
		request *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
	) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error) {
		calls++
		if parent != "projects/kotae-security/locations/asia-northeast1" {
			t.Fatalf("parent = %q", parent)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Second {
			t.Fatalf("bounded deadline was not applied: %v, %v", deadline, ok)
		}
		if request == nil || request.Item == nil {
			t.Fatal("content item is missing")
		}
		for _, raw := range rawValues {
			if strings.Contains(request.Item.Value, raw) {
				t.Fatalf("request retained a locally blocked value")
			}
		}
		for _, placeholder := range []string{
			emailReplacement,
			phoneReplacement,
			longIDReplacement,
			credentialReplacement,
			urlReplacement,
		} {
			if !strings.Contains(request.Item.Value, placeholder) {
				t.Fatalf("request does not contain %q: %q", placeholder, request.Item.Value)
			}
		}

		inspect := request.InspectConfig
		if inspect == nil || inspect.IncludeQuote || inspect.MinLikelihood != "POSSIBLE" {
			t.Fatalf("inspect config = %#v", inspect)
		}
		if got := infoTypeNames(inspect.InfoTypes); strings.Join(got, ",") !=
			"EMAIL_ADDRESS,PERSON_NAME,PHONE_NUMBER" {
			t.Fatalf("infoTypes = %v", got)
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"includeQuote":false`) {
			t.Fatalf("includeQuote=false is not explicit: %s", encoded)
		}

		deidentify := request.DeidentifyConfig
		if deidentify == nil ||
			deidentify.TransformationErrorHandling == nil ||
			deidentify.TransformationErrorHandling.ThrowError == nil ||
			deidentify.InfoTypeTransformations == nil ||
			len(deidentify.InfoTypeTransformations.Transformations) != 1 ||
			deidentify.InfoTypeTransformations.Transformations[0].
				PrimitiveTransformation == nil ||
			deidentify.InfoTypeTransformations.Transformations[0].
				PrimitiveTransformation.ReplaceWithInfoTypeConfig == nil {
			t.Fatalf("deidentify config = %#v", deidentify)
		}

		return &dlp.GooglePrivacyDlpV2DeidentifyContentResponse{
			Item: &dlp.GooglePrivacyDlpV2ContentItem{Value: request.Item.Value},
		}, nil
	}}
	protector, err := protectorWithClient(testConfig(), client)
	if err != nil {
		t.Fatal(err)
	}

	result, err := protector.Protect(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d; want 1", calls)
	}
	if !result.Redacted || result.Text == "" {
		t.Fatalf("result = %#v", result)
	}
	for _, raw := range rawValues {
		if strings.Contains(result.Text, raw) {
			t.Fatal("result retained a locally blocked value")
		}
	}
}

func TestProtectReportsManagedTransformation(t *testing.T) {
	t.Parallel()

	client := fakeDeidentifier{call: func(
		_ context.Context,
		_ string,
		request *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
	) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error) {
		if request.Item.Value != "My name is Alice." {
			t.Fatalf("request text = %q", request.Item.Value)
		}
		return &dlp.GooglePrivacyDlpV2DeidentifyContentResponse{
			Item: &dlp.GooglePrivacyDlpV2ContentItem{Value: "My name is PERSON_NAME."},
			Overview: &dlp.GooglePrivacyDlpV2TransformationOverview{
				TransformationSummaries: []*dlp.GooglePrivacyDlpV2TransformationSummary{
					{Results: []*dlp.GooglePrivacyDlpV2SummaryResult{
						{Code: "SUCCESS", Count: 1},
					}},
				},
			},
		}, nil
	}}
	protector, err := protectorWithClient(testConfig(), client)
	if err != nil {
		t.Fatal(err)
	}

	result, err := protector.Protect(context.Background(), "My name is Alice.")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "My name is PERSON_NAME." || !result.Redacted {
		t.Fatalf("result = %#v", result)
	}
}

func TestProtectLeavesSafeUnchangedTextMarkedUnredacted(t *testing.T) {
	t.Parallel()

	client := fakeDeidentifier{call: func(
		_ context.Context,
		_ string,
		request *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
	) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error) {
		return &dlp.GooglePrivacyDlpV2DeidentifyContentResponse{
			Item: &dlp.GooglePrivacyDlpV2ContentItem{Value: request.Item.Value},
		}, nil
	}}
	protector, err := protectorWithClient(testConfig(), client)
	if err != nil {
		t.Fatal(err)
	}

	const input = "今日は研究テーマの構成について話したいです"
	result, err := protector.Protect(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != input || result.Redacted {
		t.Fatalf("result = %#v", result)
	}
}

func TestDeterministicScreeningCoverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		replacement string
	}{
		{"email", "連絡は user+study@example.co.jp へ", emailReplacement},
		{"phone", "電話は +81 (90) 1234-5678", phoneReplacement},
		{"full-width phone", "電話は ０９０－１２３４－５６７８", phoneReplacement},
		{"long ID", "IDは 1234567890123456", longIDReplacement},
		{"uuid", "IDは 550e8400-e29b-41d4-a716-446655440000", longIDReplacement},
		{"labeled credential", "APIキー：abcd-efgh-1234-5678", credentialReplacement},
		{"bearer", "Bearer abcdefghijklmnop1234", credentialReplacement},
		{"known credential", "AI" + "za" + strings.Repeat("A", 35), credentialReplacement},
		{"JWT", "eyJabcdefghijk.abcdefghijkl.abcdefghijk", credentialReplacement},
		{"URL", "https://example.jp/private/path?q=secret", urlReplacement},
		{
			"private key",
			"-----BEGIN PRIVATE KEY-----\nsecret material\n-----END PRIVATE KEY-----",
			credentialReplacement,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, redacted := screenDeterministic(test.input)
			if !redacted || !strings.Contains(got, test.replacement) || got == test.input {
				t.Fatalf("screenDeterministic() = (%q, %v)", got, redacted)
			}
		})
	}
}

func TestProtectFailsClosedOnTimeout(t *testing.T) {
	t.Parallel()

	config := testConfig()
	config.Timeout = 10 * time.Millisecond
	client := fakeDeidentifier{call: func(
		ctx context.Context,
		_ string,
		_ *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
	) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	protector, err := protectorWithClient(config, client)
	if err != nil {
		t.Fatal(err)
	}

	result, err := protector.Protect(context.Background(), "sensitive free text")
	if !errors.Is(err, ErrProtectionUnavailable) ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if result != (Result{}) {
		t.Fatalf("result = %#v; want zero value", result)
	}
}

func TestProtectDoesNotPropagateProviderErrorText(t *testing.T) {
	t.Parallel()

	const raw = "alice@example.com"
	client := fakeDeidentifier{call: func(
		_ context.Context,
		_ string,
		_ *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
	) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error) {
		return nil, errors.New("provider echoed " + raw)
	}}
	protector, err := protectorWithClient(testConfig(), client)
	if err != nil {
		t.Fatal(err)
	}

	result, err := protector.Protect(context.Background(), raw)
	if !errors.Is(err, ErrProtectionUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), raw) {
		t.Fatal("returned error exposed provider text")
	}
	if result != (Result{}) {
		t.Fatalf("result = %#v; want zero value", result)
	}
}

func TestProtectRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response *dlp.GooglePrivacyDlpV2DeidentifyContentResponse
	}{
		{"nil", nil},
		{"missing item", &dlp.GooglePrivacyDlpV2DeidentifyContentResponse{}},
		{
			"empty item",
			&dlp.GooglePrivacyDlpV2DeidentifyContentResponse{
				Item: &dlp.GooglePrivacyDlpV2ContentItem{},
			},
		},
		{
			"unsafe provider output",
			&dlp.GooglePrivacyDlpV2DeidentifyContentResponse{
				Item: &dlp.GooglePrivacyDlpV2ContentItem{Value: "reintroduced@example.com"},
			},
		},
		{
			"transformation error",
			&dlp.GooglePrivacyDlpV2DeidentifyContentResponse{
				Item: &dlp.GooglePrivacyDlpV2ContentItem{Value: "safe output"},
				Overview: &dlp.GooglePrivacyDlpV2TransformationOverview{
					TransformationSummaries: []*dlp.GooglePrivacyDlpV2TransformationSummary{
						{Results: []*dlp.GooglePrivacyDlpV2SummaryResult{
							{Code: "ERROR", Count: 1, Details: "do not expose"},
						}},
					},
				},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := fakeDeidentifier{call: func(
				_ context.Context,
				_ string,
				_ *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
			) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error) {
				return test.response, nil
			}}
			protector, err := protectorWithClient(testConfig(), client)
			if err != nil {
				t.Fatal(err)
			}

			result, err := protector.Protect(context.Background(), "input")
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("error = %v", err)
			}
			if result != (Result{}) {
				t.Fatalf("result = %#v; want zero value", result)
			}
			if strings.Contains(err.Error(), "do not expose") {
				t.Fatal("returned error exposed response details")
			}
		})
	}
}

func TestProtectRejectsOversizeInputBeforeProviderCall(t *testing.T) {
	t.Parallel()

	var calls int
	client := fakeDeidentifier{call: func(
		_ context.Context,
		_ string,
		_ *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
	) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error) {
		calls++
		return nil, nil
	}}
	protector, err := protectorWithClient(testConfig(), client)
	if err != nil {
		t.Fatal(err)
	}

	result, err := protector.Protect(
		context.Background(),
		strings.Repeat("x", MaxInputBytes+1),
	)
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("error = %v", err)
	}
	if calls != 0 || result != (Result{}) {
		t.Fatalf("calls = %d, result = %#v", calls, result)
	}
}

func TestProtectEmptyTextDoesNotCallProvider(t *testing.T) {
	t.Parallel()

	var calls int
	client := fakeDeidentifier{call: func(
		_ context.Context,
		_ string,
		_ *dlp.GooglePrivacyDlpV2DeidentifyContentRequest,
	) (*dlp.GooglePrivacyDlpV2DeidentifyContentResponse, error) {
		calls++
		return nil, nil
	}}
	protector, err := protectorWithClient(testConfig(), client)
	if err != nil {
		t.Fatal(err)
	}

	result, err := protector.Protect(context.Background(), "")
	if err != nil || calls != 0 || result != (Result{}) {
		t.Fatalf("error = %v, calls = %d, result = %#v", err, calls, result)
	}
}

func TestConfigRequiresRegionalLocationAndExplicitInfoTypes(t *testing.T) {
	t.Parallel()

	tests := []Config{
		{},
		{ProjectID: "project", Location: "asia-northeast1"},
		{ProjectID: "project", Location: "global", InfoTypes: []string{"EMAIL_ADDRESS"}},
		{ProjectID: "project/path", Location: "asia-northeast1", InfoTypes: []string{"EMAIL_ADDRESS"}},
		{ProjectID: "project", Location: "asia-northeast1", InfoTypes: []string{"not valid"}},
		{ProjectID: "project", Location: "asia-northeast1", InfoTypes: []string{"EMAIL_ADDRESS"}, Timeout: -1},
	}

	for index, config := range tests {
		if _, err := protectorWithClient(config, fakeDeidentifier{}); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("case %d: error = %v", index, err)
		}
	}
}

func TestRegionalEndpoint(t *testing.T) {
	t.Parallel()

	if got := regionalEndpoint("asia-northeast1"); got !=
		"https://dlp.asia-northeast1.rep.googleapis.com/" {
		t.Fatalf("endpoint = %q", got)
	}
}

func infoTypeNames(infoTypes []*dlp.GooglePrivacyDlpV2InfoType) []string {
	names := make([]string, 0, len(infoTypes))
	for _, infoType := range infoTypes {
		if infoType != nil {
			names = append(names, infoType.Name)
		}
	}
	return names
}
