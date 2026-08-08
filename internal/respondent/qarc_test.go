package respondent

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unicode"
)

func validBoundQARCObservation(operator QARCOperator) QARCObservation {
	return QARCObservation{
		Operator:              operator,
		ScopeBound:            true,
		OperatorConfidence:    0.96,
		EndpointCommitted:     true,
		NewQuestionScope:      true,
		UserOnsetProbability:  0.03,
		IncidentalProbability: 0.10,
		AudioCancelable:       true,
	}
}

func TestQARCSelectsMeaningfulOperatorTemplateForBoundScope(t *testing.T) {
	decision := DecideQARC(validBoundQARCObservation(QARCOperatorCause))
	if decision.Action != QARCOperatorCue ||
		decision.TemplateID != QARCTemplateCause ||
		decision.Slot != QARCSlotCause {
		t.Fatalf("decision=%+v", decision)
	}
	certificate := decision.Certificate
	if certificate.PolicyVersion != QARCPolicyVersion ||
		certificate.Action != decision.Action ||
		certificate.TemplateID != decision.TemplateID ||
		certificate.Slot != decision.Slot ||
		!certificate.NonInterference ||
		!certificate.FloorProtected {
		t.Fatalf("certificate=%+v", certificate)
	}
}

func TestQARCConformalFallbackUsesInvariantTemplateWhenOperatorUncertain(t *testing.T) {
	observation := validBoundQARCObservation(QARCOperatorPurpose)
	observation.OperatorConfidence = 0.41
	decision := DecideQARC(observation)
	if decision.Action != QARCNeutralCue ||
		decision.TemplateID != QARCTemplateNeutral ||
		decision.Slot != QARCSlotNone ||
		decision.Certificate.Reason != QARCReasonOperatorUncertain ||
		!decision.Certificate.NonInterference {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestQARCProtectsThinkingAndUserFloor(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*QARCObservation)
		reason QARCReason
	}{
		{
			name: "hesitation",
			mutate: func(observation *QARCObservation) {
				observation.HesitationOnly = true
			},
			reason: QARCReasonHesitationSpace,
		},
		{
			name: "user onset",
			mutate: func(observation *QARCObservation) {
				observation.UserOnsetProbability = 0.61
			},
			reason: QARCReasonFloorNotClear,
		},
		{
			name: "uncommitted endpoint",
			mutate: func(observation *QARCObservation) {
				observation.EndpointCommitted = false
			},
			reason: QARCReasonFloorNotClear,
		},
		{
			name: "incidental sound",
			mutate: func(observation *QARCObservation) {
				observation.IncidentalProbability = 0.81
			},
			reason: QARCReasonFloorNotClear,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := validBoundQARCObservation(QARCOperatorDefinition)
			test.mutate(&observation)
			decision := DecideQARC(observation)
			if decision.Action != QARCWait ||
				decision.TemplateID != QARCTemplateNone ||
				decision.Certificate.Reason != test.reason ||
				decision.Certificate.WorstCaseRegret != 0 ||
				!decision.Certificate.NonInterference ||
				!decision.Certificate.FloorProtected {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}

func TestQARCCommitProtectedSpeculationComputesWithoutClaimingCommit(t *testing.T) {
	observation := validBoundQARCObservation(QARCOperatorPurpose)
	observation.EndpointCommitted = false
	observation.CommitProtected = true
	decision := DecideQARC(observation)
	if decision.Action != QARCOperatorCue ||
		decision.TemplateID != QARCTemplatePurpose ||
		!decision.Certificate.NonInterference ||
		!decision.Certificate.FloorProtected {
		t.Fatalf("decision=%+v", decision)
	}

	observation.CommitProtected = false
	unprotected := DecideQARC(observation)
	if unprotected.Action != QARCWait ||
		unprotected.TemplateID != QARCTemplateNone ||
		unprotected.Certificate.Reason != QARCReasonFloorNotClear {
		t.Fatalf("unprotected decision=%+v", unprotected)
	}
}

func TestQARCUncancelableAudioCannotGrabFloor(t *testing.T) {
	for operator := QARCOperatorBoolean; operator < qarcOperatorCount; operator++ {
		for _, confidence := range []float64{0, 0.41, 0.65, 1} {
			for _, attempts := range []uint8{0, 1, MaxCoachAttempts, math.MaxUint8} {
				observation := validBoundQARCObservation(operator)
				observation.OperatorConfidence = confidence
				observation.Attempts = attempts
				observation.AudioCancelable = false
				decision := DecideQARC(observation)
				if decision.Action != QARCWait ||
					decision.TemplateID != QARCTemplateNone ||
					decision.Slot != QARCSlotNone ||
					decision.Certificate.Reason != QARCReasonFloorNotClear ||
					!decision.Certificate.NonInterference ||
					!decision.Certificate.FloorProtected {
					t.Fatalf(
						"operator=%d confidence=%f attempts=%d decision=%+v",
						operator,
						confidence,
						attempts,
						decision,
					)
				}
			}
		}
	}
}

func TestQARCInvalidNumericAndOperatorObservationsFailClosed(t *testing.T) {
	base := validBoundQARCObservation(QARCOperatorPurpose)
	base.Attempts = MaxCoachAttempts
	tests := []struct {
		name   string
		mutate func(*QARCObservation)
	}{
		{"unknown operator", func(value *QARCObservation) { value.Operator = QARCOperator(255) }},
		{"operator confidence NaN", func(value *QARCObservation) { value.OperatorConfidence = math.NaN() }},
		{"operator confidence infinity", func(value *QARCObservation) { value.OperatorConfidence = math.Inf(1) }},
		{"operator confidence negative infinity", func(value *QARCObservation) { value.OperatorConfidence = math.Inf(-1) }},
		{"operator confidence negative", func(value *QARCObservation) { value.OperatorConfidence = -0.01 }},
		{"operator confidence above one", func(value *QARCObservation) { value.OperatorConfidence = 1.01 }},
		{"user onset NaN", func(value *QARCObservation) { value.UserOnsetProbability = math.NaN() }},
		{"user onset infinity", func(value *QARCObservation) { value.UserOnsetProbability = math.Inf(1) }},
		{"user onset negative infinity", func(value *QARCObservation) { value.UserOnsetProbability = math.Inf(-1) }},
		{"user onset negative", func(value *QARCObservation) { value.UserOnsetProbability = -0.01 }},
		{"user onset above one", func(value *QARCObservation) { value.UserOnsetProbability = 1.01 }},
		{"incidental NaN", func(value *QARCObservation) { value.IncidentalProbability = math.NaN() }},
		{"incidental infinity", func(value *QARCObservation) { value.IncidentalProbability = math.Inf(1) }},
		{"incidental negative infinity", func(value *QARCObservation) { value.IncidentalProbability = math.Inf(-1) }},
		{"incidental negative", func(value *QARCObservation) { value.IncidentalProbability = -0.01 }},
		{"incidental above one", func(value *QARCObservation) { value.IncidentalProbability = 1.01 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := base
			test.mutate(&observation)
			decision := DecideQARC(observation)
			if decision.Action != QARCWait ||
				decision.TemplateID != QARCTemplateNone ||
				decision.Slot != QARCSlotNone ||
				decision.Certificate.Reason != QARCReasonInvalidObservation ||
				decision.Certificate.WorstCaseRegret != 0 ||
				!decision.Certificate.NonInterference ||
				!decision.Certificate.FloorProtected {
				t.Fatalf("decision=%+v", decision)
			}
			for _, value := range []float64{
				decision.Certificate.WorstCaseUtility,
				decision.Certificate.WorstCaseRegret,
				decision.Certificate.WaitBestCaseUtility,
			} {
				if math.IsNaN(value) || math.IsInf(value, 0) {
					t.Fatalf("non-finite certificate=%+v", decision.Certificate)
				}
			}
			for _, interval := range decision.Belief {
				if math.IsNaN(interval.Lower) || math.IsNaN(interval.Upper) ||
					math.IsInf(interval.Lower, 0) || math.IsInf(interval.Upper, 0) {
					t.Fatalf("non-finite belief=%+v", decision.Belief)
				}
			}
		})
	}
}

func TestQARCGuardOrderingWaitsBeforeRelease(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*QARCObservation)
		action QARCAction
		reason QARCReason
	}{
		{
			name: "unbound scope",
			mutate: func(observation *QARCObservation) {
				observation.ScopeBound = false
			},
			action: QARCWait,
			reason: QARCReasonUnboundScope,
		},
		{
			name: "hesitation",
			mutate: func(observation *QARCObservation) {
				observation.HesitationOnly = true
			},
			action: QARCWait,
			reason: QARCReasonHesitationSpace,
		},
		{
			name: "user onset",
			mutate: func(observation *QARCObservation) {
				observation.UserOnsetProbability = 0.35
			},
			action: QARCWait,
			reason: QARCReasonFloorNotClear,
		},
		{
			name: "incidental sound",
			mutate: func(observation *QARCObservation) {
				observation.IncidentalProbability = 0.9
			},
			action: QARCWait,
			reason: QARCReasonFloorNotClear,
		},
		{
			name:   "clear bound floor",
			mutate: func(*QARCObservation) {},
			action: QARCRelease,
			reason: QARCReasonAttemptBudgetExhausted,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := validBoundQARCObservation(QARCOperatorComparison)
			observation.Attempts = MaxCoachAttempts
			test.mutate(&observation)
			decision := DecideQARC(observation)
			if decision.Action != test.action ||
				decision.Certificate.Reason != test.reason {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}

func TestQARCReleasesWithOnlyAuditedTemplateAtBoundedAttemptLimit(t *testing.T) {
	observation := validBoundQARCObservation(QARCOperatorComparison)
	observation.Attempts = MaxCoachAttempts
	decision := DecideQARC(observation)
	if decision.Action != QARCRelease ||
		decision.TemplateID != QARCTemplateRelease ||
		decision.Slot != QARCSlotNone ||
		decision.Certificate.Reason != QARCReasonAttemptBudgetExhausted ||
		decision.Certificate.WorstCaseRegret != 0 ||
		!decision.Certificate.NonInterference ||
		!decision.Certificate.FloorProtected {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestQARCObservationHasNoAnswerBearingInputChannel(t *testing.T) {
	typeOfObservation := reflect.TypeOf(QARCObservation{})
	for index := 0; index < typeOfObservation.NumField(); index++ {
		field := typeOfObservation.Field(index)
		lowerName := strings.ToLower(field.Name)
		for _, forbidden := range []string{
			"answer", "subject", "transcript", "utterance", "document",
			"candidate", "draft", "span", "text", "prompt", "cue",
		} {
			if strings.Contains(lowerName, forbidden) {
				t.Fatalf("answer-bearing field %q entered QARCObservation", field.Name)
			}
		}
		switch field.Type.Kind() {
		case reflect.String, reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface:
			t.Fatalf("content-capable field %q entered QARCObservation", field.Name)
		}
	}
	if reflect.TypeOf(QARCOperatorInvalid).Kind() != reflect.Uint8 {
		t.Fatalf("QARCOperator kind=%s", reflect.TypeOf(QARCOperatorInvalid).Kind())
	}
	if projected, ok := ProjectQARCOperator(Operator("arbitrary content")); ok || projected != QARCOperatorInvalid {
		t.Fatalf("unknown projection=%d ok=%v", projected, ok)
	}
}

func TestQARCDecisionAndCertificateContainNoProseChannel(t *testing.T) {
	for _, rootType := range []reflect.Type{
		reflect.TypeOf(QARCObservation{}),
		reflect.TypeOf(QARCDecision{}),
		reflect.TypeOf(QARCCertificate{}),
		reflect.TypeOf(QARCBelief{}),
		reflect.TypeOf(ProbabilityInterval{}),
	} {
		assertNoQARCContentCarrier(t, rootType, map[reflect.Type]bool{})
	}
	if _, exists := reflect.TypeOf(QARCDecision{}).FieldByName("Cue"); exists {
		t.Fatal("raw Cue field re-entered QARCDecision")
	}
	field, exists := reflect.TypeOf(QARCDecision{}).FieldByName("TemplateID")
	if !exists || field.Type.Kind() != reflect.Uint8 {
		t.Fatalf("TemplateID field=%+v exists=%v", field, exists)
	}
	for _, enum := range []any{
		QARCPolicyVersion,
		QARCAction(0),
		QARCOperator(0),
		QARCTemplateID(0),
		QARCCueSlot(0),
		QARCReason(0),
	} {
		if reflect.TypeOf(enum).Kind() == reflect.String {
			t.Fatalf("%T is a string channel", enum)
		}
	}
}

func TestQARCPolicySourceContainsNoJapaneseProse(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate QARC test source")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "qarc.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range string(source) {
		if unicode.In(value, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			t.Fatalf("qarc.go contains rendered prose rune %q", value)
		}
	}
}

func assertNoQARCContentCarrier(
	t *testing.T,
	typeOfValue reflect.Type,
	seen map[reflect.Type]bool,
) {
	t.Helper()
	if seen[typeOfValue] {
		return
	}
	seen[typeOfValue] = true
	switch typeOfValue.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface:
		t.Fatalf("content-capable kind %s entered %s", typeOfValue.Kind(), typeOfValue)
	case reflect.Array:
		assertNoQARCContentCarrier(t, typeOfValue.Elem(), seen)
	case reflect.Struct:
		for index := 0; index < typeOfValue.NumField(); index++ {
			assertNoQARCContentCarrier(t, typeOfValue.Field(index).Type, seen)
		}
	}
}

func TestQARCBeliefDefinesFeasibleIntervalSimplex(t *testing.T) {
	observation := validBoundQARCObservation(QARCOperatorOpen)
	observation.OperatorConfidence = 0.72
	observation.HasSubstantiveAttempt = true
	observation.Attempts = 1
	observation.UserOnsetProbability = 0.12
	observation.IncidentalProbability = 0.45
	decision := DecideQARC(observation)
	lowerSum := 0.0
	upperSum := 0.0
	if len(decision.Belief) != len(qarcStates) {
		t.Fatalf("belief states=%d", len(decision.Belief))
	}
	for _, state := range qarcStates {
		interval, ok := decision.Belief.Interval(state)
		if !ok || interval.Lower < 0 || interval.Upper > 1 ||
			interval.Lower > interval.Upper ||
			math.IsNaN(interval.Lower) || math.IsNaN(interval.Upper) {
			t.Fatalf("state=%d interval=%+v ok=%v", state, interval, ok)
		}
		lowerSum += interval.Lower
		upperSum += interval.Upper
	}
	if lowerSum > 1+1e-9 || upperSum < 1-1e-9 {
		t.Fatalf("infeasible lower_sum=%f upper_sum=%f", lowerSum, upperSum)
	}
	if _, ok := decision.Belief.Interval(RetrievalState(255)); ok {
		t.Fatal("unknown retrieval state accepted")
	}
}

func TestQARCFiniteGrammarCoversEveryQuestionOperator(t *testing.T) {
	tests := []struct {
		respondent Operator
		qarc       QARCOperator
		template   QARCTemplateID
		slot       QARCCueSlot
	}{
		{OperatorBoolean, QARCOperatorBoolean, QARCTemplateBoolean, QARCSlotPolarity},
		{OperatorChoice, QARCOperatorChoice, QARCTemplateChoice, QARCSlotSelection},
		{OperatorQuantity, QARCOperatorQuantity, QARCTemplateQuantity, QARCSlotQuantity},
		{OperatorState, QARCOperatorState, QARCTemplateState, QARCSlotState},
		{OperatorCause, QARCOperatorCause, QARCTemplateCause, QARCSlotCause},
		{OperatorPurpose, QARCOperatorPurpose, QARCTemplatePurpose, QARCSlotPurpose},
		{OperatorProcedure, QARCOperatorProcedure, QARCTemplateProcedure, QARCSlotProcedure},
		{OperatorDefinition, QARCOperatorDefinition, QARCTemplateDefinition, QARCSlotDefinition},
		{OperatorComparison, QARCOperatorComparison, QARCTemplateComparison, QARCSlotComparison},
		{OperatorEvidence, QARCOperatorEvidence, QARCTemplateEvidence, QARCSlotEvidence},
		{OperatorOpen, QARCOperatorOpen, QARCTemplateOpen, QARCSlotPosition},
	}
	seenTemplates := map[QARCTemplateID]bool{}
	for _, test := range tests {
		projected, projectedOK := ProjectQARCOperator(test.respondent)
		roundTrip, roundTripOK := test.qarc.respondentOperator()
		templateID, slot, templateOK := qarcOperatorTemplate(test.qarc)
		if !projectedOK || projected != test.qarc ||
			!roundTripOK || roundTrip != test.respondent ||
			!templateOK || templateID != test.template || slot != test.slot ||
			seenTemplates[templateID] ||
			!qarcTemplateIsCompiled(QARCOperatorCue, templateID, slot) {
			t.Fatalf("case=%+v projected=%d template=%d slot=%d", test, projected, templateID, slot)
		}
		seenTemplates[templateID] = true
	}
	if len(seenTemplates) != int(qarcOperatorCount)-1 {
		t.Fatalf("operator templates=%d", len(seenTemplates))
	}
	if qarcTemplateIsCompiled(QARCOperatorCue, QARCTemplateCause, QARCSlotPurpose) ||
		qarcTemplateIsCompiled(QARCOperatorCue, QARCTemplateNeutral, QARCSlotNone) {
		t.Fatal("mismatched template/slot accepted")
	}
}

func TestQARCTemplateGrammarRejectsEveryInvalidTriple(t *testing.T) {
	valid := map[[3]uint8]bool{
		{uint8(QARCWait), uint8(QARCTemplateNone), uint8(QARCSlotNone)}:          true,
		{uint8(QARCNeutralCue), uint8(QARCTemplateNeutral), uint8(QARCSlotNone)}: true,
		{uint8(QARCRelease), uint8(QARCTemplateRelease), uint8(QARCSlotNone)}:    true,
	}
	for operator := QARCOperatorBoolean; operator < qarcOperatorCount; operator++ {
		templateID, slot, ok := qarcOperatorTemplate(operator)
		if !ok {
			t.Fatalf("known operator %d did not compile", operator)
		}
		valid[[3]uint8{
			uint8(QARCOperatorCue),
			uint8(templateID),
			uint8(slot),
		}] = true
	}
	for action := QARCAction(0); action <= qarcActionCount; action++ {
		for templateID := QARCTemplateID(0); templateID <= qarcTemplateCount; templateID++ {
			for slot := QARCCueSlot(0); slot <= qarcSlotCount; slot++ {
				got := qarcTemplateIsCompiled(action, templateID, slot)
				want := valid[[3]uint8{
					uint8(action),
					uint8(templateID),
					uint8(slot),
				}]
				if got != want {
					t.Fatalf(
						"action=%d template=%d slot=%d got=%t want=%t",
						action,
						templateID,
						slot,
						got,
						want,
					)
				}
			}
		}
	}
}

func TestQARCActionLanguageHasNoCompleteAction(t *testing.T) {
	expected := [...]QARCAction{
		QARCWait,
		QARCOperatorCue,
		QARCNeutralCue,
		QARCRelease,
	}
	if qarcActions != expected || qarcActionCount != QARCAction(len(expected)) {
		t.Fatalf("actions=%v count=%d", qarcActions, qarcActionCount)
	}
	for operator := QARCOperatorBoolean; operator < qarcOperatorCount; operator++ {
		for _, attempts := range []uint8{0, MaxCoachAttempts} {
			observation := validBoundQARCObservation(operator)
			observation.Attempts = attempts
			decision := DecideQARC(observation)
			if decision.Action >= qarcActionCount ||
				!qarcActionInSet(decision.Action, expected[:]) {
				t.Fatalf("operator=%d attempts=%d action=%d", operator, attempts, decision.Action)
			}
		}
	}
}

func TestQARCExpectationBoundSolvesIntervalExtrema(t *testing.T) {
	var belief QARCBelief
	var utilities qarcUtility
	belief[RetrievalTargetLost] = ProbabilityInterval{Lower: 0.2, Upper: 0.8}
	belief[RetrievalReady] = ProbabilityInterval{Lower: 0.2, Upper: 0.8}
	utilities[RetrievalTargetLost] = -1
	utilities[RetrievalReady] = 1
	minimum := qarcExpectationBound(belief, utilities, false)
	maximum := qarcExpectationBound(belief, utilities, true)
	if minimum != -0.6 || maximum != 0.6 {
		t.Fatalf("minimum=%f maximum=%f", minimum, maximum)
	}
}

func BenchmarkDecideQARC(b *testing.B) {
	observation := validBoundQARCObservation(QARCOperatorPurpose)
	observation.OperatorConfidence = 0.93
	observation.UserOnsetProbability = 0.04
	observation.IncidentalProbability = 0.25
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		_ = DecideQARC(observation)
	}
}
