package conversation

import "github.com/furukawa1020/conclution-ai-teacher/internal/respondent"

// AuditedQARCCues returns the complete, immutable prose vocabulary that QARC
// may speak. The returned slice is a copy. It contains no user, model, or
// session data, so infrastructure may prepare these exact assets before a
// conversation without speculating about the user's answer.
func AuditedQARCCues() []string {
	pairs := [...]struct {
		template respondent.QARCTemplateID
		slot     respondent.QARCCueSlot
	}{
		{respondent.QARCTemplateBoolean, respondent.QARCSlotPolarity},
		{respondent.QARCTemplateChoice, respondent.QARCSlotSelection},
		{respondent.QARCTemplateQuantity, respondent.QARCSlotQuantity},
		{respondent.QARCTemplateState, respondent.QARCSlotState},
		{respondent.QARCTemplateCause, respondent.QARCSlotCause},
		{respondent.QARCTemplatePurpose, respondent.QARCSlotPurpose},
		{respondent.QARCTemplateProcedure, respondent.QARCSlotProcedure},
		{respondent.QARCTemplateDefinition, respondent.QARCSlotDefinition},
		{respondent.QARCTemplateComparison, respondent.QARCSlotComparison},
		{respondent.QARCTemplateEvidence, respondent.QARCSlotEvidence},
		{respondent.QARCTemplateOpen, respondent.QARCSlotPosition},
		{respondent.QARCTemplateNeutral, respondent.QARCSlotNone},
		{respondent.QARCTemplateRelease, respondent.QARCSlotNone},
	}
	cues := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		cue, ok := renderQARCCue(pair.template, pair.slot)
		if ok && cue != "" {
			cues = append(cues, cue)
		}
	}
	return cues
}

// renderQARCCue is the only prose boundary for a QARC decision. Both inputs
// are closed numeric enums; no caller-controlled string enters the renderer.
func renderQARCCue(
	templateID respondent.QARCTemplateID,
	slot respondent.QARCCueSlot,
) (string, bool) {
	switch templateID {
	case respondent.QARCTemplateNone:
		if slot != respondent.QARCSlotNone {
			return "", false
		}
		return "", true
	case respondent.QARCTemplateBoolean:
		if slot != respondent.QARCSlotPolarity {
			return "", false
		}
		return "するか、しないかだけ。", true
	case respondent.QARCTemplateChoice:
		if slot != respondent.QARCSlotSelection {
			return "", false
		}
		return "選ぶものを一つだけ。", true
	case respondent.QARCTemplateQuantity:
		if slot != respondent.QARCSlotQuantity {
			return "", false
		}
		return "数字と単位だけ。", true
	case respondent.QARCTemplateState:
		if slot != respondent.QARCSlotState {
			return "", false
		}
		return "今どうかだけ。", true
	case respondent.QARCTemplateCause:
		if slot != respondent.QARCSlotCause {
			return "", false
		}
		return "理由を一つだけ。", true
	case respondent.QARCTemplatePurpose:
		if slot != respondent.QARCSlotPurpose {
			return "", false
		}
		return "目的を一つだけ。", true
	case respondent.QARCTemplateProcedure:
		if slot != respondent.QARCSlotProcedure {
			return "", false
		}
		return "最初の一歩だけ。", true
	case respondent.QARCTemplateDefinition:
		if slot != respondent.QARCSlotDefinition {
			return "", false
		}
		return "意味だけ。", true
	case respondent.QARCTemplateComparison:
		if slot != respondent.QARCSlotComparison {
			return "", false
		}
		return "違いを一つだけ。", true
	case respondent.QARCTemplateEvidence:
		if slot != respondent.QARCSlotEvidence {
			return "", false
		}
		return "根拠を一つだけ。", true
	case respondent.QARCTemplateOpen:
		if slot != respondent.QARCSlotPosition {
			return "", false
		}
		return "まず、答えをひと言だけ。", true
	case respondent.QARCTemplateNeutral:
		if slot != respondent.QARCSlotNone {
			return "", false
		}
		return "最初のひと言だけで大丈夫です。", true
	case respondent.QARCTemplateRelease:
		if slot != respondent.QARCSlotNone {
			return "", false
		}
		return "大丈夫です。言い直さなくても、そのまま続けられます。", true
	default:
		return "", false
	}
}
