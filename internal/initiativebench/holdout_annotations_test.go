package initiativebench

import (
	"strings"
	"testing"
)

func annotationSet(items ...AnnotatedHoldoutItem) HoldoutAnnotationSet {
	return HoldoutAnnotationSet{
		SchemaVersion: HoldoutAnnotationSchemaVersion,
		SourceSHA256:  strings.Repeat("d", 64),
		BlindingSeed:  20260913,
		Items:         items,
	}
}

func annotatedItem(id string, choices ...AnnotationChoice) AnnotatedHoldoutItem {
	annotations := make([]RaterAnnotation, len(choices))
	for index, choice := range choices {
		annotations[index] = RaterAnnotation{RaterSlot: uint8(index + 1), Choice: choice}
	}
	return AnnotatedHoldoutItem{FixtureID: id, Situation: SituationQuestionRequired, Annotations: annotations}
}

func TestPerfectHoldoutAgreementProducesExactConsensusAndAlpha(t *testing.T) {
	set := annotationSet(
		annotatedItem("holdout-1", AnnotationChoice(ActionAskOne), AnnotationChoice(ActionAskOne), AnnotationChoice(ActionAskOne)),
		annotatedItem("holdout-2", AnnotationChoice(ActionWait), AnnotationChoice(ActionWait), AnnotationChoice(ActionWait)),
	)
	report, err := EvaluateHoldoutAgreement(set)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.NominalAlphaBPS == nil || *report.NominalAlphaBPS != 10_000 ||
		report.AmbiguousItemCount != 0 || report.Consensus[0].ExpectedAction == nil ||
		*report.Consensus[0].ExpectedAction != ActionAskOne {
		t.Fatalf("unexpected agreement report: %#v", report)
	}
}

func TestTieUnsureAndLowConsensusStayOutsideExpectedAction(t *testing.T) {
	set := annotationSet(
		annotatedItem("holdout-1", AnnotationChoice(ActionWait), AnnotationChoice(ActionAskOne)),
		annotatedItem("holdout-2", AnnotationChoice(ActionWait), AnnotationChoice(ActionWait), ChoiceAmbiguous),
		annotatedItem("holdout-3", AnnotationChoice(ActionWait), AnnotationChoice(ActionWait), AnnotationChoice(ActionAskOne), AnnotationChoice(ActionAskOne)),
	)
	report, err := EvaluateHoldoutAgreement(set)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.AmbiguousItemCount != 3 {
		t.Fatalf("ambiguous cases hidden: %#v", report)
	}
	for _, consensus := range report.Consensus {
		if consensus.ExpectedAction != nil || !consensus.Ambiguous {
			t.Fatalf("ambiguous case received an expected action: %#v", consensus)
		}
	}
}

func TestNominalAlphaReportsSystematicDisagreement(t *testing.T) {
	set := annotationSet(
		annotatedItem("holdout-1", AnnotationChoice(ActionWait), AnnotationChoice(ActionAskOne)),
		annotatedItem("holdout-2", AnnotationChoice(ActionWait), AnnotationChoice(ActionAskOne)),
	)
	report, err := EvaluateHoldoutAgreement(set)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.NominalAlphaBPS == nil || *report.NominalAlphaBPS >= 0 {
		t.Fatalf("systematic disagreement was not exposed: %#v", report.NominalAlphaBPS)
	}
}

func TestHoldoutValidationRejectsIdentityCollisionAndContentSmuggling(t *testing.T) {
	set := annotationSet(annotatedItem("holdout-1", AnnotationChoice(ActionWait), AnnotationChoice(ActionWait)))
	set.Items[0].Annotations[1].RaterSlot = 1
	if err := set.Validate(); err == nil {
		t.Fatal("duplicate anonymous rater slot accepted")
	}
	unknown := `{"schema_version":"kotae.mixed-initiative-holdout-annotations.v1","source_sha256":"` +
		strings.Repeat("d", 64) + `","blinding_seed":1,"items":[],"transcript":"secret"}`
	if _, err := DecodeHoldoutAnnotations(strings.NewReader(unknown)); err == nil {
		t.Fatal("content field accepted")
	}
}

func TestHoldoutAgreementIsDeterministic(t *testing.T) {
	set := annotationSet(annotatedItem("holdout-1", AnnotationChoice(ActionOfferChoice), AnnotationChoice(ActionOfferChoice), AnnotationChoice(ActionWait)))
	first, err := EvaluateHoldoutAgreement(set)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := EvaluateHoldoutAgreement(set)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.NominalAlphaBPS == nil || second.NominalAlphaBPS == nil ||
		*first.NominalAlphaBPS != *second.NominalAlphaBPS || first.Consensus[0].AgreementBPS == nil ||
		*first.Consensus[0].AgreementBPS != 6_667 {
		t.Fatalf("agreement is not deterministic: %#v %#v", first, second)
	}
}
