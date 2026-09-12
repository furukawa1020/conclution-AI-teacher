package initiativebench

import (
	"strings"
	"testing"
)

func validRuns(t *testing.T, corpus CounterexampleCorpus) []SystemRun {
	t.Helper()
	digest, err := CounterexampleDigest(corpus)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	runs := make([]SystemRun, 0, len(variants))
	for _, variant := range variants {
		outcomes := make([]RunObservation, len(corpus.Fixtures))
		for index, fixture := range corpus.Fixtures {
			outcome := RunObservation{
				FixtureID: fixture.FixtureID, ActualAction: fixture.ExpectedAction,
				TimeToUsefulActionMS: duration(500),
			}
			if isSpokenAction(outcome.ActualAction) {
				outcome.FirstMeaningfulAudioMS = duration(800)
			}
			outcomes[index] = outcome
		}
		runs = append(runs, SystemRun{
			SchemaVersion: RunSchemaVersion, CorpusSHA256: digest,
			SourceCommit: strings.Repeat("b", 40), Variant: variant,
			Observations: outcomes,
		})
	}
	return runs
}

func TestBuildGeneratedObservationsBindsThreeRunsToOneCorpus(t *testing.T) {
	corpus, err := GenerateCounterexamples(20260912, 50)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	combined, err := BuildGeneratedObservations(corpus, validRuns(t, corpus))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(combined) != 150 || combined[0].Variant != VariantReactiveBaseline ||
		combined[50].Variant != VariantAlwaysProactive || combined[100].Variant != VariantProposalControlled {
		t.Fatalf("unexpected combined runs: %#v", combined)
	}
}

func TestBuildGeneratedObservationsRejectsMissingSwappedAndCrossCommitRuns(t *testing.T) {
	corpus, err := GenerateCounterexamples(20260912, 20)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	runs := validRuns(t, corpus)
	if _, err := BuildGeneratedObservations(corpus, runs[:2]); err == nil {
		t.Fatal("missing system run accepted")
	}
	runs = validRuns(t, corpus)
	runs[0], runs[1] = runs[1], runs[0]
	if _, err := BuildGeneratedObservations(corpus, runs); err == nil {
		t.Fatal("swapped system runs accepted")
	}
	runs = validRuns(t, corpus)
	runs[2].SourceCommit = strings.Repeat("c", 40)
	if _, err := BuildGeneratedObservations(corpus, runs); err == nil {
		t.Fatal("cross-commit system run accepted")
	}
}

func TestBuildGeneratedObservationsRejectsReorderedAndMalformedOutcome(t *testing.T) {
	corpus, err := GenerateCounterexamples(20260912, 20)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	runs := validRuns(t, corpus)
	runs[0].Observations[0], runs[0].Observations[1] = runs[0].Observations[1], runs[0].Observations[0]
	if _, err := BuildGeneratedObservations(corpus, runs); err == nil {
		t.Fatal("reordered outcomes accepted")
	}
	runs = validRuns(t, corpus)
	runs[0].Observations[0].ActualAction = ActionAskOne
	runs[0].Observations[0].FirstMeaningfulAudioMS = nil
	if _, err := BuildGeneratedObservations(corpus, runs); err == nil {
		t.Fatal("spoken action without audio boundary accepted")
	}
}

func TestDecodeSystemRunRejectsContentFieldsAndTrailingJSON(t *testing.T) {
	unknown := `{"schema_version":"kotae.mixed-initiative-run.v1","corpus_sha256":"` +
		strings.Repeat("a", 64) + `","source_commit":"` + strings.Repeat("b", 40) +
		`","variant":"reactive_baseline","observations":[],"transcript":"secret"}`
	if _, err := DecodeSystemRun(strings.NewReader(unknown)); err == nil {
		t.Fatal("content field accepted")
	}
	valid := `{"schema_version":"kotae.mixed-initiative-run.v1","corpus_sha256":"` +
		strings.Repeat("a", 64) + `","source_commit":"` + strings.Repeat("b", 40) +
		`","variant":"reactive_baseline","observations":[]}`
	if _, err := DecodeSystemRun(strings.NewReader(valid + `{}`)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
