package conversation

import "github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"

func answerContractResponseSchema() map[string]any {
	unitNumber := func() map[string]any {
		return map[string]any{"type": "number", "minimum": 0, "maximum": 1}
	}
	slotSchema := func() map[string]any {
		return map[string]any{
			"type": "string",
			"enum": []string{
				string(answercontract.SlotPolarity),
				string(answercontract.SlotSelection),
				string(answercontract.SlotQuantity),
				string(answercontract.SlotState),
				string(answercontract.SlotCause),
				string(answercontract.SlotProcedure),
				string(answercontract.SlotDefinition),
				string(answercontract.SlotComparison),
				string(answercontract.SlotEvidence),
				string(answercontract.SlotPurpose),
				string(answercontract.SlotPosition),
				string(answercontract.SlotUnit),
				string(answercontract.SlotCondition),
				string(answercontract.SlotUncertainty),
				string(answercontract.SlotScope),
			},
		}
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"question_frame", "commitment_front", "counterfactual_repair",
		},
		"properties": map[string]any{
			"question_frame": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required": []string{
					"operator", "subject", "required_slots", "hypotheses",
				},
				"properties": map[string]any{
					"operator": map[string]any{
						"type": "string",
						"enum": []string{
							string(answercontract.OperatorBoolean),
							string(answercontract.OperatorChoice),
							string(answercontract.OperatorQuantity),
							string(answercontract.OperatorState),
							string(answercontract.OperatorCause),
							string(answercontract.OperatorProcedure),
							string(answercontract.OperatorDefinition),
							string(answercontract.OperatorComparison),
							string(answercontract.OperatorEvidence),
							string(answercontract.OperatorPurpose),
							string(answercontract.OperatorOpen),
						},
					},
					"subject": map[string]any{"type": "string"},
					"required_slots": map[string]any{
						"type":     "array",
						"minItems": 1,
						"maxItems": answercontract.MaxRequiredSlots,
						"items":    slotSchema(),
					},
					"hypotheses": map[string]any{
						"type":     "array",
						"minItems": 1,
						"maxItems": answercontract.MaxHypotheses,
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"interpretation", "confidence"},
							"properties": map[string]any{
								"interpretation": map[string]any{"type": "string"},
								"confidence":     unitNumber(),
							},
						},
					},
				},
			},
			"commitment_front": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required": []string{
					"first_commitment", "fills_target", "target_coverage",
					"filled_slots", "position_class", "calibration", "issue",
				},
				"properties": map[string]any{
					"first_commitment": map[string]any{"type": "string"},
					"fills_target":     map[string]any{"type": "boolean"},
					"target_coverage":  unitNumber(),
					"filled_slots": map[string]any{
						"type":     "array",
						"maxItems": answercontract.MaxRequiredSlots,
						"items":    slotSchema(),
					},
					"position_class": map[string]any{
						"type": "string",
						"enum": []string{
							string(answercontract.PositionFirst),
							string(answercontract.PositionLater),
							string(answercontract.PositionAbsent),
						},
					},
					"calibration": map[string]any{
						"type": "string",
						"enum": []string{
							string(answercontract.CalibrationCommitted),
							string(answercontract.CalibrationConditional),
							string(answercontract.CalibrationUncertain),
							string(answercontract.CalibrationAbstain),
						},
					},
					"issue": map[string]any{
						"type": "string",
						"enum": []string{
							string(answercontract.IssueNone),
							string(answercontract.IssueTargetMissing),
							string(answercontract.IssueMissingRequiredSlot),
							string(answercontract.IssueReasonOnly),
							string(answercontract.IssueBackgroundFirst),
							string(answercontract.IssueConditionSeparated),
							string(answercontract.IssueQuestionRestatement),
							string(answercontract.IssueAmbiguousCommitment),
							string(answercontract.IssueAnswerTypeMismatch),
							string(answercontract.IssueUnsupportedCertainty),
							string(answercontract.IssueInsufficientEvidence),
							string(answercontract.IssueContradiction),
							string(answercontract.IssueMeaningChanged),
							string(answercontract.IssueNotEvaluable),
						},
					},
				},
			},
			"counterfactual_repair": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required": []string{
					"minimal_answer", "reconstructed_answer",
					"meaning_preservation_confidence", "repair_gain",
				},
				"properties": map[string]any{
					"minimal_answer":                  map[string]any{"type": "string"},
					"reconstructed_answer":            map[string]any{"type": "string"},
					"meaning_preservation_confidence": unitNumber(),
					"repair_gain":                     unitNumber(),
				},
			},
		},
	}
}
