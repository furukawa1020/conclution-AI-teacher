package conversation

import (
	"reflect"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestRenderQARCCueClosedGrammar(t *testing.T) {
	tests := []struct {
		name       string
		templateID respondent.QARCTemplateID
		slot       respondent.QARCCueSlot
		want       string
	}{
		{"none", respondent.QARCTemplateNone, respondent.QARCSlotNone, ""},
		{"boolean", respondent.QARCTemplateBoolean, respondent.QARCSlotPolarity, "するか、しないかだけ。"},
		{"choice", respondent.QARCTemplateChoice, respondent.QARCSlotSelection, "選ぶものを一つだけ。"},
		{"quantity", respondent.QARCTemplateQuantity, respondent.QARCSlotQuantity, "数字と単位だけ。"},
		{"state", respondent.QARCTemplateState, respondent.QARCSlotState, "今どうかだけ。"},
		{"cause", respondent.QARCTemplateCause, respondent.QARCSlotCause, "理由を一つだけ。"},
		{"purpose", respondent.QARCTemplatePurpose, respondent.QARCSlotPurpose, "目的を一つだけ。"},
		{"procedure", respondent.QARCTemplateProcedure, respondent.QARCSlotProcedure, "最初の一歩だけ。"},
		{"definition", respondent.QARCTemplateDefinition, respondent.QARCSlotDefinition, "意味だけ。"},
		{"comparison", respondent.QARCTemplateComparison, respondent.QARCSlotComparison, "違いを一つだけ。"},
		{"evidence", respondent.QARCTemplateEvidence, respondent.QARCSlotEvidence, "根拠を一つだけ。"},
		{"open", respondent.QARCTemplateOpen, respondent.QARCSlotPosition, "まず、答えをひと言だけ。"},
		{"neutral", respondent.QARCTemplateNeutral, respondent.QARCSlotNone, "最初のひと言だけで大丈夫です。"},
		{"release", respondent.QARCTemplateRelease, respondent.QARCSlotNone, "大丈夫です。言い直さなくても、そのまま続けられます。"},
	}
	seen := make(map[string]bool, len(tests))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := renderQARCCue(test.templateID, test.slot)
			if !ok || got != test.want {
				t.Fatalf("got=%q ok=%t want=%q", got, ok, test.want)
			}
			if test.want != "" && seen[got] {
				t.Fatalf("duplicate cue %q", got)
			}
			seen[got] = true
		})
	}
}

func TestRenderQARCCueRejectsEveryInvalidPair(t *testing.T) {
	valid := map[[2]uint8]bool{
		{uint8(respondent.QARCTemplateNone), uint8(respondent.QARCSlotNone)}:             true,
		{uint8(respondent.QARCTemplateBoolean), uint8(respondent.QARCSlotPolarity)}:      true,
		{uint8(respondent.QARCTemplateChoice), uint8(respondent.QARCSlotSelection)}:      true,
		{uint8(respondent.QARCTemplateQuantity), uint8(respondent.QARCSlotQuantity)}:     true,
		{uint8(respondent.QARCTemplateState), uint8(respondent.QARCSlotState)}:           true,
		{uint8(respondent.QARCTemplateCause), uint8(respondent.QARCSlotCause)}:           true,
		{uint8(respondent.QARCTemplatePurpose), uint8(respondent.QARCSlotPurpose)}:       true,
		{uint8(respondent.QARCTemplateProcedure), uint8(respondent.QARCSlotProcedure)}:   true,
		{uint8(respondent.QARCTemplateDefinition), uint8(respondent.QARCSlotDefinition)}: true,
		{uint8(respondent.QARCTemplateComparison), uint8(respondent.QARCSlotComparison)}: true,
		{uint8(respondent.QARCTemplateEvidence), uint8(respondent.QARCSlotEvidence)}:     true,
		{uint8(respondent.QARCTemplateOpen), uint8(respondent.QARCSlotPosition)}:         true,
		{uint8(respondent.QARCTemplateNeutral), uint8(respondent.QARCSlotNone)}:          true,
		{uint8(respondent.QARCTemplateRelease), uint8(respondent.QARCSlotNone)}:          true,
	}
	for templateValue := 0; templateValue <= 255; templateValue++ {
		for slotValue := 0; slotValue <= 255; slotValue++ {
			templateID := respondent.QARCTemplateID(templateValue)
			slot := respondent.QARCCueSlot(slotValue)
			_, ok := renderQARCCue(templateID, slot)
			want := valid[[2]uint8{uint8(templateID), uint8(slot)}]
			if ok != want {
				t.Fatalf(
					"template=%d slot=%d ok=%t want=%t",
					templateID,
					slot,
					ok,
					want,
				)
			}
		}
	}
}

func TestRenderQARCCueAcceptsOnlyClosedEnums(t *testing.T) {
	typ := reflect.TypeOf(renderQARCCue)
	if typ.NumIn() != 2 || typ.NumOut() != 2 ||
		typ.In(0) != reflect.TypeOf(respondent.QARCTemplateID(0)) ||
		typ.In(1) != reflect.TypeOf(respondent.QARCCueSlot(0)) ||
		typ.Out(0).Kind() != reflect.String ||
		typ.Out(1).Kind() != reflect.Bool {
		t.Fatalf("renderer boundary changed: %s", typ)
	}
}
