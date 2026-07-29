package conversation

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/research"
)

type fakeResearchVerifier struct {
	verification research.Verification
	err          error
	calls        []research.Query
}

type cancelingResearchVerifier struct {
	calls int
}

func (fake *cancelingResearchVerifier) Verify(
	ctx context.Context,
	_ research.Query,
) (research.Verification, error) {
	fake.calls++
	<-ctx.Done()
	return research.Verification{}, ctx.Err()
}

func (fake *fakeResearchVerifier) Verify(
	_ context.Context,
	query research.Query,
) (research.Verification, error) {
	fake.calls = append(fake.calls, query)
	return fake.verification, fake.err
}

func TestAgentResearchRecentPapersMapsBoundedDiscoveryMetadata(t *testing.T) {
	const (
		utterance = "外部検索で「量子エラー訂正」の最新論文を探してください"
		topic     = "量子エラー訂正"
	)
	plan := recentPapersPlan(topic)
	generator := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodePlan(t, plan)},
	}}
	records := make([]research.Record, 0, MaxResearchRecords+2)
	for index := 1; index <= MaxResearchRecords+2; index++ {
		value := strconv.Itoa(index)
		doi := "10.1000/paper-" + value
		records = append(records, research.Record{
			CanonicalID:    "doi:" + doi,
			DOI:            doi,
			Title:          "論文候補 " + value,
			LandingURL:     "https://doi.org/" + doi,
			MetadataURL:    "https://api.crossref.org/works/" + doi,
			AbstractRights: "unknown_may_be_copyrighted",
			Published: research.NormalizedDate{
				Value:     "2026-07-" + leftPadTwo(index),
				Precision: research.PrecisionDay,
			},
			AbstractText: "会話結果へ出してはいけない抄録 " + value,
		})
	}
	verifier := &fakeResearchVerifier{
		verification: research.Verification{
			Status:      research.StatusNeedsPrimaryEvidence,
			Role:        research.RoleDiscoveryMetadata,
			QueryKind:   research.QueryRecentTopic,
			RetrievedAt: time.Date(2026, time.July, 29, 15, 30, 0, 0, time.UTC),
			Sources:     []research.SourceDescriptor{crossrefDiscoverySource()},
			Records:     records,
		},
	}
	agent := newTestAgent(t, generator)
	agent.research = verifier
	now := time.Date(2026, time.July, 29, 15, 30, 0, 0, time.UTC)
	agent.now = func() time.Time { return now }

	result, err := agent.Process(context.Background(), "uid", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     utterance,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if result.ResearchStatus != string(research.StatusNeedsPrimaryEvidence) {
		t.Fatalf("research status = %q", result.ResearchStatus)
	}
	if result.Route != "research-discovery-precision" {
		t.Fatalf("route = %q", result.Route)
	}
	const wantReply = "Crossrefの索引日が指定期間内の書誌候補を5件見つけました。内容や主張はまだ検証していません。"
	if result.SpokenReply != wantReply {
		t.Fatalf("spoken reply = %q", result.SpokenReply)
	}
	if len(result.ResearchRecords) != MaxResearchRecords {
		t.Fatalf("research records = %d", len(result.ResearchRecords))
	}
	for index, record := range result.ResearchRecords {
		value := strconv.Itoa(index + 1)
		if record.Title != "論文候補 "+value ||
			record.DOI != "10.1000/paper-"+value ||
			record.URL != "https://doi.org/10.1000/paper-"+value ||
			record.Published != "2026-07-"+leftPadTwo(index+1) ||
			record.Source != "Crossref" {
			t.Fatalf("record %d = %#v", index, record)
		}
	}
	if strings.Contains(result.SpokenReply, "検証済み") {
		t.Fatalf("reply overstates verification: %q", result.SpokenReply)
	}
	if len(verifier.calls) != 1 {
		t.Fatalf("verifier calls = %d", len(verifier.calls))
	}
	query := verifier.calls[0]
	if query.Kind != research.QueryRecentTopic ||
		query.Topic != topic ||
		query.Limit != MaxResearchRecords ||
		!query.From.Equal(time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC)) ||
		!query.Until.Equal(time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("outbound query = %#v", query)
	}
}

func TestAgentResearchUnavailableReturnsNoRecords(t *testing.T) {
	const topic = "量子エラー訂正"
	plan := recentPapersPlan(topic)
	generator := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodePlan(t, plan)},
	}}
	verifier := &fakeResearchVerifier{err: research.ErrSourceUnavailable}
	agent := newTestAgent(t, generator)
	agent.research = verifier
	agent.now = func() time.Time {
		return time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC)
	}

	result, err := agent.Process(context.Background(), "uid", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     japaneseRecentRequest(topic),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if result.ResearchStatus != "unavailable" {
		t.Fatalf("research status = %q", result.ResearchStatus)
	}
	if len(result.ResearchRecords) != 0 {
		t.Fatalf("research records = %#v", result.ResearchRecords)
	}
	if result.Route != "research-unavailable-precision" {
		t.Fatalf("route = %q", result.Route)
	}
	const wantReply = "論文候補の取得先に接続できませんでした。内容や主張は検証していません。"
	if result.SpokenReply != wantReply {
		t.Fatalf("spoken reply = %q", result.SpokenReply)
	}
	if len(verifier.calls) != 1 {
		t.Fatalf("verifier calls = %d", len(verifier.calls))
	}
}

func TestAgentResearchRejectsMismatchedVerifierProvenance(t *testing.T) {
	const topic = "量子エラー訂正"
	plan := recentPapersPlan(topic)
	generator := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodePlan(t, plan)},
	}}
	now := time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC)
	verifier := &fakeResearchVerifier{
		verification: research.Verification{
			Status:      research.StatusNeedsPrimaryEvidence,
			Role:        research.RoleDiscoveryMetadata,
			QueryKind:   research.QueryDOI,
			RetrievedAt: now,
			Sources: []research.SourceDescriptor{{
				ID:        research.SourceCrossref,
				Name:      "Unreviewed proxy",
				Authority: "https://example.com",
				Role:      research.RoleDiscoveryMetadata,
			}},
		},
	}
	agent := newTestAgent(t, generator)
	agent.research = verifier
	agent.now = func() time.Time { return now }

	result, err := agent.Process(context.Background(), "uid", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     japaneseRecentRequest(topic),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.ResearchStatus != "unavailable" ||
		len(result.ResearchRecords) != 0 {
		t.Fatalf("mismatched provenance escaped: %#v", result)
	}
}

func TestAgentResearchQueryRejectedBeforeVerifier(t *testing.T) {
	tests := []struct {
		name      string
		utterance string
		query     string
		ambient   bool
	}{
		{
			name:      "not an exact utterance span",
			utterance: japaneseRecentRequest("量子エラー訂正"),
			query:     "誤り耐性量子計算",
		},
		{
			name:      "sensitive exact span",
			utterance: japaneseRecentRequest("alice@example.com"),
			query:     "alice@example.com",
		},
		{
			name:      "ambient capture has no outbound authority",
			utterance: japaneseRecentRequest("量子エラー訂正"),
			query:     "量子エラー訂正",
			ambient:   true,
		},
		{
			name:      "negated search request",
			utterance: "量子エラー訂正の最新論文は検索しないで",
			query:     "量子エラー訂正",
		},
		{
			name:      "negative desire is not consent",
			utterance: "量子エラー訂正の最新論文を探してほしくない",
			query:     "量子エラー訂正",
		},
		{
			name:      "negative lookup is not consent",
			utterance: "量子エラー訂正の論文を調べてほしくない",
			query:     "量子エラー訂正",
		},
		{
			name:      "later cancellation withdraws consent",
			utterance: japaneseRecentRequest("量子エラー訂正") + "。でもやっぱりやめて。",
			query:     "量子エラー訂正",
		},
		{
			name: "english cancellation withdraws consent",
			utterance: `Use Crossref to find papers on "quantum error correction". ` +
				"Actually, cancel that.",
			query: "quantum error correction",
		},
		{
			name:      "unlisted Japanese cancellation withdraws consent",
			utterance: japaneseRecentRequest("量子エラー訂正") + "。いや、やめて。",
			query:     "量子エラー訂正",
		},
		{
			name: "unlisted English cancellation withdraws consent",
			utterance: `Use Crossref to find papers on "quantum error correction". ` +
				"I changed my mind.",
			query: "quantum error correction",
		},
		{
			name:      "cancellation cannot be swallowed by model query",
			utterance: `Use Crossref to find papers on "quantum computing" scratch that`,
			query:     "quantum computing scratch that",
		},
		{
			name:      "English excluded topic is not authority",
			utterance: "Find papers on quantum error correction but not papers on classical coding",
			query:     "classical coding",
		},
		{
			name:      "topic is not scoped to the request",
			utterance: "田中太郎について話した。最新論文を探して",
			query:     "田中太郎",
		},
		{
			name:      "quoted topic from another sentence is not authority",
			utterance: "「田中太郎」について話した。量子論文を探して",
			query:     "田中太郎",
		},
		{
			name:      "topic before Japanese comma is not authority",
			utterance: "引用は「田中太郎」の論文です、量子エラー訂正の論文を探してください",
			query:     "田中太郎",
		},
		{
			name:      "earlier topic in same clause is not bound to command",
			utterance: "田中太郎の論文について話した 量子エラー訂正の論文を探してください",
			query:     "田中太郎",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := recentPapersPlan(test.query)
			generator := &fakeGenerator{generations: []fakeGeneration{
				{body: encodePlan(t, plan)},
				{body: encodePlan(t, plan)},
			}}
			verifier := &fakeResearchVerifier{}
			agent := newTestAgent(t, generator)
			agent.research = verifier

			_, err := agent.Process(context.Background(), "uid", VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     test.utterance,
				Ambient:       test.ambient,
			})
			if !errors.Is(err, ErrModelOutputInvalid) {
				t.Fatalf("Process error = %v", err)
			}
			if len(verifier.calls) != 0 {
				t.Fatalf("verifier called with %#v", verifier.calls)
			}
		})
	}
}

func TestAgentRejectsRespondentResearchCombinationBeforeVerifier(t *testing.T) {
	const (
		answerAttempt = "目的は評価基準をそろえることです"
		topic         = "量子エラー訂正"
	)
	plan := respondentRestructurePlan(answerAttempt, answerAttempt)
	plan.Domain = "research"
	plan.ResearchAction = "recent_papers"
	plan.ResearchQuery = topic
	plan.InterventionPolicy = "paper_check"
	plan.Intervention.Act = "paper_check"
	generator := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodePlan(t, plan)},
	}}
	verifier := &fakeResearchVerifier{}
	agent := newTestAgent(t, generator)
	agent.research = verifier

	_, err := agent.Process(context.Background(), "uid", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance: answerAttempt + "。" +
			topic + "の最新論文を探して",
	})
	if !errors.Is(err, ErrModelOutputInvalid) {
		t.Fatalf("Process error = %v", err)
	}
	if len(verifier.calls) != 0 {
		t.Fatalf("verifier called with %#v", verifier.calls)
	}
}

func TestAgentDOILookupRequiresExplicitIntentBeforeVerifier(t *testing.T) {
	const doi = "10.1000/example"
	plan := validModelPlan()
	plan.Domain = "research"
	plan.Intent = "verify"
	plan.ResearchAction = "doi_lookup"
	plan.ResearchQuery = doi
	plan.ArgumentStructure = "claim_evidence_limit"
	plan.InterventionPolicy = "paper_check"
	plan.SpokenReply = "DOIを確認します。"
	plan.Intervention.Act = "paper_check"
	generator := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodePlan(t, plan)},
	}}
	verifier := &fakeResearchVerifier{}
	agent := newTestAgent(t, generator)
	agent.research = verifier

	_, err := agent.Process(context.Background(), "uid", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "参考文献のDOIは" + doi + "です",
	})
	if !errors.Is(err, ErrModelOutputInvalid) {
		t.Fatalf("Process error = %v", err)
	}
	if len(verifier.calls) != 0 {
		t.Fatalf("verifier called with %#v", verifier.calls)
	}
}

func TestAuthorizedResearchQueryAcceptsExplicitIntentionalDOI(t *testing.T) {
	const doi = "10.1000/example"
	plan := validModelPlan()
	plan.ResearchAction = "doi_lookup"
	plan.ResearchQuery = doi
	plan.InterventionPolicy = "paper_check"
	plan.Intervention.Act = "paper_check"
	query, err := authorizedResearchQuery(plan, VoiceTurn{
		Utterance: japaneseDOIRequest(doi),
	}, time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("authorizedResearchQuery: %v", err)
	}
	if query.Kind != research.QueryDOI || query.DOI != doi {
		t.Fatalf("query = %#v", query)
	}
}

func TestAuthorizedResearchQueryRejectsAmbiguousOrTruncatedDOI(t *testing.T) {
	tests := []struct {
		name      string
		utterance string
		query     string
	}{
		{
			name: "multiple DOI candidates",
			utterance: "Crossrefで DOI 10.1234/private DOI " +
				"10.1234/public を調べてください",
			query: "10.1234/private",
		},
		{
			name:      "different DOI chosen",
			utterance: japaneseDOIRequest("10.1234/public"),
			query:     "10.1234/private",
		},
		{
			name:      "DOI prefix is not an exact identifier",
			utterance: japaneseDOIRequest("10.1234/public-secret-suffix"),
			query:     "10.1234/public",
		},
		{
			name:      "negative DOI request",
			utterance: "Crossrefで DOI 10.1234/public を調べてほしくない",
			query:     "10.1234/public",
		},
		{
			name:      "command words cannot terminate a longer DOI",
			utterance: "Crossrefで DOI 10.1234/publicを調べてevil を確認してください",
			query:     "10.1234/public",
		},
		{
			name:      "topic words cannot terminate a longer DOI",
			utterance: "Crossrefで DOI 10.1234/publicについて調べて-private を確認してください",
			query:     "10.1234/public",
		},
		{
			name:      "arbitrary resolver host is not explicit DOI syntax",
			utterance: "Crossrefで DOI https://evil-doi.org/10.1234/public を調べてください",
			query:     "10.1234/public",
		},
		{
			name:      "English DOI cancellation is not fixed syntax",
			utterance: "Use Crossref to check DOI 10.1234/public but forget it",
			query:     "10.1234/public",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validModelPlan()
			plan.ResearchAction = "doi_lookup"
			plan.ResearchQuery = test.query
			plan.InterventionPolicy = "paper_check"
			plan.Intervention.Act = "paper_check"

			_, err := authorizedResearchQuery(plan, VoiceTurn{
				Utterance: test.utterance,
			}, time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC))
			if !errors.Is(err, ErrModelOutputInvalid) {
				t.Fatalf("authorizedResearchQuery error = %v", err)
			}
		})
	}
}

func TestAuthorizedResearchQueryAcceptsBoundResearchTopic(t *testing.T) {
	tests := []struct {
		name      string
		utterance string
		query     string
	}{
		{
			name:      "Japanese topic",
			utterance: japaneseRecentRequest("量子エラー訂正"),
			query:     "量子エラー訂正",
		},
		{
			name:      "quoted Japanese topic",
			utterance: "Crossrefで「作業記憶」の研究を検索してください",
			query:     "作業記憶",
		},
		{
			name:      "English topic",
			utterance: `Use Crossref to find recent papers on "quantum error correction"`,
			query:     "quantum error correction",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := recentPapersPlan(test.query)
			query, err := authorizedResearchQuery(plan, VoiceTurn{
				Utterance: test.utterance,
			}, time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatalf("authorizedResearchQuery: %v", err)
			}
			if query.Kind != research.QueryRecentTopic ||
				query.Topic != test.query {
				t.Fatalf("query = %#v", query)
			}
		})
	}
}

func TestAgentDOILookupBindsVerifierResultToRequestedDOI(t *testing.T) {
	const requestedDOI = "10.1234/requested"
	now := time.Date(2026, time.July, 29, 15, 30, 0, 0, time.UTC)
	tests := []struct {
		name    string
		records []research.Record
	}{
		{
			name:    "mismatched DOI",
			records: []research.Record{testResearchRecord("10.1234/different")},
		},
		{
			name: "multiple records",
			records: []research.Record{
				testResearchRecord(requestedDOI),
				testResearchRecord("10.1234/extra"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validModelPlan()
			plan.Domain = "research"
			plan.Intent = "verify"
			plan.ResearchAction = "doi_lookup"
			plan.ResearchQuery = requestedDOI
			plan.ArgumentStructure = "claim_evidence_limit"
			plan.InterventionPolicy = "paper_check"
			plan.Intervention.Act = "paper_check"
			generator := &fakeGenerator{generations: []fakeGeneration{
				{body: encodePlan(t, plan)},
				{body: encodePlan(t, plan)},
			}}
			verifier := &fakeResearchVerifier{
				verification: research.Verification{
					Status:      research.StatusNeedsPrimaryEvidence,
					Role:        research.RoleDiscoveryMetadata,
					QueryKind:   research.QueryDOI,
					RetrievedAt: now,
					Sources:     []research.SourceDescriptor{crossrefDiscoverySource()},
					Records:     test.records,
				},
			}
			agent := newTestAgent(t, generator)
			agent.research = verifier
			agent.now = func() time.Time { return now }

			result, err := agent.Process(context.Background(), "uid", VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     japaneseDOIRequest(requestedDOI),
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if result.ResearchStatus != "unavailable" ||
				len(result.ResearchRecords) != 0 {
				t.Fatalf("unbound DOI result escaped: %#v", result)
			}
		})
	}
}

func TestAgentResearchPropagatesParentCancellation(t *testing.T) {
	const topic = "量子エラー訂正"
	plan := recentPapersPlan(topic)
	generator := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		{body: encodePlan(t, plan)},
	}}
	verifier := &cancelingResearchVerifier{}
	agent := newTestAgent(t, generator)
	agent.research = verifier
	agent.now = func() time.Time {
		return time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := agent.Process(ctx, "uid", VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     japaneseRecentRequest(topic),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Process error = %v; want context.Canceled", err)
	}
	if verifier.calls != 1 {
		t.Fatalf("verifier calls = %d", verifier.calls)
	}
}

func recentPapersPlan(topic string) modelPlan {
	plan := validModelPlan()
	plan.Domain = "research"
	plan.Intent = "verify"
	plan.ResearchAction = "recent_papers"
	plan.ResearchQuery = topic
	plan.ArgumentStructure = "claim_evidence_limit"
	plan.InterventionPolicy = "paper_check"
	plan.SpokenReply = "論文候補の書誌情報を確認します。"
	plan.Intervention.Act = "paper_check"
	return plan
}

func leftPadTwo(value int) string {
	if value < 10 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

func japaneseRecentRequest(topic string) string {
	return "外部検索で「" + topic + "」の最新論文を探してください"
}

func japaneseDOIRequest(doi string) string {
	return "Crossrefで DOI " + doi + " を調べてください"
}

func testResearchRecord(doi string) research.Record {
	return research.Record{
		CanonicalID:    "doi:" + doi,
		DOI:            doi,
		Title:          "書誌候補",
		LandingURL:     "https://doi.org/" + doi,
		MetadataURL:    "https://api.crossref.org/works/" + doi,
		AbstractRights: "unknown_may_be_copyrighted",
		Published: research.NormalizedDate{
			Value:     "2026-07-29",
			Precision: research.PrecisionDay,
		},
	}
}
