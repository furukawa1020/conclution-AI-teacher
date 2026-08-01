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

type fakeStrictInspector struct {
	calls int
	texts []string
	check func(context.Context, string) (Inspection, error)
}

func (inspector *fakeStrictInspector) Inspect(
	ctx context.Context,
	text string,
) (Inspection, error) {
	inspector.calls++
	inspector.texts = append(inspector.texts, text)
	if inspector.check == nil {
		return Inspection{}, nil
	}
	return inspector.check(ctx, text)
}

func TestHighConfidenceDetectorCanonicalizesObfuscation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
	}{
		{"NFKC email", "連絡先は ａｌｉｃｅ＠ｅｘａｍｐｌｅ．ｃｏｍ"},
		{"zero width phone", "電話は090\u200b-1234\u2060-5678"},
		{"address", "住所は東京都千代田区千代田1番地"},
		{"postal address", "〒100-0001"},
		{"name-ish", "氏名：田中太郎"},
		{"zero width JWT", "eyJabcdefghijk.abcde\u200bfghijkl.abcdefghijk"},
		{"credential", "APIキー：sk-abcdefghijklmnop1234"},
		{"private key", "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if !HasHighConfidenceFinding(test.text) {
				t.Fatalf("detector missed %s", test.name)
			}
		})
	}
}

func TestHighConfidenceDetectorAllowsOrdinaryConversation(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"今日は少し疲れたけれど散歩は楽しかった",
		"日本の首都はどこですか",
		"結論から話す練習をしたい",
	} {
		if HasHighConfidenceFinding(text) {
			t.Fatalf("ordinary text was blocked")
		}
	}
}

func TestStrictBoundaryNeverSendsLocalFindingToManagedInspector(t *testing.T) {
	t.Parallel()
	inspector := &fakeStrictInspector{check: func(
		context.Context,
		string,
	) (Inspection, error) {
		return Inspection{Status: InspectionClear}, nil
	}}
	boundary, err := NewStrictBoundary(inspector, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := boundary.Check(context.Background(), "alice@example.com"); !errors.Is(err, ErrProtectionUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if inspector.calls != 0 {
		t.Fatalf("local finding reached managed inspector: %d", inspector.calls)
	}
}

func TestStrictBoundaryFailsClosedForFindingUnknownFailureAndTimeout(t *testing.T) {
	t.Parallel()
	const secret = "provider echoed alice@example.com"
	tests := []struct {
		name    string
		timeout time.Duration
		check   func(context.Context, string) (Inspection, error)
	}{
		{
			name: "finding",
			check: func(context.Context, string) (Inspection, error) {
				return Inspection{Status: InspectionFinding}, nil
			},
		},
		{
			name: "unknown",
			check: func(context.Context, string) (Inspection, error) {
				return Inspection{Status: InspectionUnknown}, nil
			},
		},
		{
			name: "failure",
			check: func(context.Context, string) (Inspection, error) {
				return Inspection{}, errors.New(secret)
			},
		},
		{
			name:    "timeout",
			timeout: 5 * time.Millisecond,
			check: func(ctx context.Context, _ string) (Inspection, error) {
				<-ctx.Done()
				return Inspection{}, ctx.Err()
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			inspector := &fakeStrictInspector{check: test.check}
			boundary, err := NewStrictBoundary(inspector, test.timeout)
			if err != nil {
				t.Fatal(err)
			}
			err = boundary.Check(context.Background(), "ordinary sentence")
			if !errors.Is(err, ErrProtectionUnavailable) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), secret) || inspector.calls != 1 {
				t.Fatalf("unsafe failure: error=%q calls=%d", err, inspector.calls)
			}
		})
	}
}

func TestStrictBoundaryAllowsOnlyExplicitClear(t *testing.T) {
	t.Parallel()
	inspector := &fakeStrictInspector{check: func(
		context.Context,
		string,
	) (Inspection, error) {
		return Inspection{Status: InspectionClear}, nil
	}}
	boundary, err := NewStrictBoundary(inspector, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := boundary.Check(context.Background(), "ordinary sentence"); err != nil {
		t.Fatal(err)
	}
}

func TestStrictResearchRequestCanonicalization(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"Cross\u200brefで最新論文を探して",
		"DOI 10.1000/example を確認して",
		"外部検索でテーマは量子計算の最新論文を探して",
		"クロスレフで『量子計算』のプレプリントを探して",
		"please search the web",
		"find recent papers about cognition",
	} {
		if !IsResearchRequest(text) {
			t.Fatalf("research request was not blocked")
		}
	}
	if IsResearchRequest("最近どうしていますか") {
		t.Fatal("ordinary conversation was classified as research")
	}
}

type fakeGoogleInspectClient struct {
	call func(
		context.Context,
		string,
		*dlp.GooglePrivacyDlpV2InspectContentRequest,
	) (*dlp.GooglePrivacyDlpV2InspectContentResponse, error)
}

func (client fakeGoogleInspectClient) Inspect(
	ctx context.Context,
	parent string,
	request *dlp.GooglePrivacyDlpV2InspectContentRequest,
) (*dlp.GooglePrivacyDlpV2InspectContentResponse, error) {
	return client.call(ctx, parent, request)
}

func TestGoogleDLPInspectorUsesRegionalQuoteFreeInspect(t *testing.T) {
	t.Parallel()
	client := fakeGoogleInspectClient{call: func(
		_ context.Context,
		parent string,
		request *dlp.GooglePrivacyDlpV2InspectContentRequest,
	) (*dlp.GooglePrivacyDlpV2InspectContentResponse, error) {
		if parent != "projects/kotae-security/locations/asia-northeast1" {
			t.Fatalf("parent = %q", parent)
		}
		if request == nil || request.Item == nil ||
			request.Item.Value != "ordinary sentence" ||
			request.InspectConfig == nil ||
			request.InspectConfig.IncludeQuote ||
			request.InspectConfig.MinLikelihood != "POSSIBLE" {
			t.Fatalf("request = %#v", request)
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"includeQuote":false`) {
			t.Fatalf("quote setting is not explicit: %s", encoded)
		}
		return &dlp.GooglePrivacyDlpV2InspectContentResponse{
			Result: &dlp.GooglePrivacyDlpV2InspectResult{},
		}, nil
	}}
	inspector := &GoogleDLPInspector{
		client:    client,
		parent:    "projects/kotae-security/locations/asia-northeast1",
		infoTypes: []string{"PERSON_NAME", "STREET_ADDRESS"},
	}
	inspection, err := inspector.Inspect(context.Background(), "ordinary sentence")
	if err != nil || inspection.Status != InspectionClear {
		t.Fatalf("inspection=%#v error=%v", inspection, err)
	}
}

func TestGoogleDLPInspectorFindingsAndUnknownResponsesFailClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		response *dlp.GooglePrivacyDlpV2InspectContentResponse
		finding  bool
	}{
		{"finding", &dlp.GooglePrivacyDlpV2InspectContentResponse{
			Result: &dlp.GooglePrivacyDlpV2InspectResult{
				Findings: []*dlp.GooglePrivacyDlpV2Finding{{}},
			},
		}, true},
		{"truncated", &dlp.GooglePrivacyDlpV2InspectContentResponse{
			Result: &dlp.GooglePrivacyDlpV2InspectResult{FindingsTruncated: true},
		}, true},
		{"nil", nil, false},
		{"missing result", &dlp.GooglePrivacyDlpV2InspectContentResponse{}, false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			inspector := &GoogleDLPInspector{
				client: fakeGoogleInspectClient{call: func(
					context.Context,
					string,
					*dlp.GooglePrivacyDlpV2InspectContentRequest,
				) (*dlp.GooglePrivacyDlpV2InspectContentResponse, error) {
					return test.response, nil
				}},
				parent:    "projects/project/locations/asia-northeast1",
				infoTypes: []string{"PERSON_NAME"},
			}
			inspection, err := inspector.Inspect(context.Background(), "text")
			if test.finding {
				if err != nil || inspection.Status != InspectionFinding {
					t.Fatalf("inspection=%#v error=%v", inspection, err)
				}
				return
			}
			if !errors.Is(err, ErrProtectionUnavailable) ||
				inspection.Status != InspectionUnknown {
				t.Fatalf("inspection=%#v error=%v", inspection, err)
			}
		})
	}
}
