package respondent

import (
	"math"
	"slices"
	"testing"
)

func TestGateJapaneseHardCases(t *testing.T) {
	tests := []struct {
		name            string
		input           Input
		wantOutcome     Outcome
		wantPosition    CommitmentPosition
		wantCoverage    float64
		wantMeaning     bool
		wantIssue       Issue
		targetSatisfied bool
	}{
		{
			name: "definition already begins with A",
			input: definitionInput(
				"APIはソフトウェア間の接続規約です。通信方法をそろえます。",
				"",
			),
			wantOutcome:     OutcomeKeep,
			wantPosition:    PositionFirst,
			wantCoverage:    1,
			wantMeaning:     true,
			targetSatisfied: true,
		},
		{
			name: "definition clauses are safely reordered",
			input: definitionInput(
				"通信方法をそろえます。APIはソフトウェア間の接続規約です。",
				"APIはソフトウェア間の接続規約です。通信方法をそろえます。",
			),
			wantOutcome:     OutcomeRestructure,
			wantPosition:    PositionFirst,
			wantCoverage:    1,
			wantMeaning:     true,
			targetSatisfied: true,
		},
		{
			name: "late definition without candidate needs reconstruction",
			input: definitionInput(
				"通信方法をそろえます。APIはソフトウェア間の接続規約です。",
				"",
			),
			wantOutcome:  OutcomeClarify,
			wantPosition: PositionLater,
			wantCoverage: 1,
			wantMeaning:  true,
			wantIssue:    IssueReconstructionNeeded,
		},
		{
			name: "purpose clauses are safely reordered",
			input: purposeInput(
				"判断のばらつきを減らします。目的は評価基準をそろえることです。",
				"目的は評価基準をそろえることです。判断のばらつきを減らします。",
			),
			wantOutcome:     OutcomeRestructure,
			wantPosition:    PositionFirst,
			wantCoverage:    1,
			wantMeaning:     true,
			targetSatisfied: true,
		},
		{
			name: "missing purpose cannot be invented",
			input: Input{
				Frame: QuestionFrame{
					Operator:      OperatorPurpose,
					Subject:       "この評価",
					RequiredSlots: []Slot{SlotPurpose},
				},
				Attempt: AnswerAttempt{
					Text: "判断のばらつきが課題です。",
				},
			},
			wantOutcome:  OutcomeClarify,
			wantPosition: PositionAbsent,
			wantCoverage: 0,
			wantMeaning:  true,
			wantIssue:    IssueRequiredSlotMissing,
		},
		{
			name: "new purpose detail is rejected",
			input: purposeInput(
				"判断のばらつきを減らします。目的は評価基準をそろえることです。",
				"目的は評価基準をそろえることです。判断のばらつきを減らします。監査にも使います。",
			),
			wantOutcome:  OutcomeReject,
			wantPosition: PositionFirst,
			wantCoverage: 1,
			wantMeaning:  false,
			wantIssue:    IssueContentChanged,
		},
		{
			name: "changed number is rejected",
			input: choiceInput(
				"費用は3万円です。A案を選びます。",
				"A案を選びます。費用は4万円です。",
			),
			wantOutcome:  OutcomeReject,
			wantPosition: PositionFirst,
			wantCoverage: 1,
			wantMeaning:  false,
			wantIssue:    IssueNumberChanged,
		},
		{
			name: "removed negation is rejected",
			input: booleanInput(
				"今は採用しません。検証が不足しています。",
				"今は採用します。検証が不足しています。",
				"今は採用しません",
			),
			wantOutcome:  OutcomeReject,
			wantPosition: PositionAbsent,
			wantCoverage: 0,
			wantMeaning:  false,
			wantIssue:    IssueNegationChanged,
		},
		{
			name: "changed condition is rejected",
			input: booleanInput(
				"雨なら中止します。実施には賛成です。",
				"実施には賛成です。雪なら中止します。",
				"実施には賛成です",
			),
			wantOutcome:  OutcomeReject,
			wantPosition: PositionFirst,
			wantCoverage: 1,
			wantMeaning:  false,
			wantIssue:    IssueConditionChanged,
		},
		{
			name: "compound protected meaning survives clause reordering",
			input: Input{
				Frame: QuestionFrame{
					Operator: OperatorBoolean,
					Subject:  "A案の採用",
					RequiredSlots: []Slot{
						SlotPolarity,
						SlotCondition,
						SlotUncertainty,
					},
				},
				Attempt: AnswerAttempt{
					Text: "費用は約3万円です。雨ならA案は採用しません。",
					SlotEvidence: []SlotBinding{
						{Slot: SlotPolarity, Span: "A案は採用しません"},
						{Slot: SlotCondition, Span: "雨ならA案は採用しません"},
						{Slot: SlotUncertainty, Span: "費用は約3万円です"},
					},
				},
				Reconstruction: "雨ならA案は採用しません。費用は約3万円です。",
			},
			wantOutcome:     OutcomeRestructure,
			wantPosition:    PositionFirst,
			wantCoverage:    1,
			wantMeaning:     true,
			targetSatisfied: true,
		},
		{
			name: "removed uncertainty is rejected",
			input: booleanInput(
				"たぶん成功します。小規模試験では改善しました。",
				"成功します。小規模試験では改善しました。",
				"たぶん成功します",
			),
			wantOutcome:  OutcomeReject,
			wantPosition: PositionAbsent,
			wantCoverage: 0,
			wantMeaning:  false,
			wantIssue:    IssueUncertaintyChanged,
		},
		{
			name: "changed proper label is rejected",
			input: choiceInput(
				"費用を抑えられます。A案を選びます。",
				"B案を選びます。費用を抑えられます。",
			),
			wantOutcome:  OutcomeReject,
			wantPosition: PositionAbsent,
			wantCoverage: 0,
			wantMeaning:  false,
			wantIssue:    IssueProperContentChanged,
		},
		{
			name: "caller protected Japanese name cannot be replaced",
			input: Input{
				Frame: QuestionFrame{
					Operator:      OperatorChoice,
					Subject:       "担当者",
					RequiredSlots: []Slot{SlotSelection},
				},
				Attempt: AnswerAttempt{
					Text: "経験があります。担当は田中さんです。",
					SlotEvidence: []SlotBinding{
						{Slot: SlotSelection, Span: "担当は田中さんです"},
					},
					ProtectedSpans: []string{"田中さん"},
				},
				Reconstruction: "担当は佐藤さんです。経験があります。",
			},
			wantOutcome:  OutcomeReject,
			wantPosition: PositionAbsent,
			wantCoverage: 0,
			wantMeaning:  false,
			wantIssue:    IssueProtectedSpanChanged,
		},
		{
			name: "dropping an ordinary reason is rejected",
			input: choiceInput(
				"保守担当が少ないです。A案を選びます。",
				"A案を選びます。",
			),
			wantOutcome:  OutcomeReject,
			wantPosition: PositionFirst,
			wantCoverage: 1,
			wantMeaning:  false,
			wantIssue:    IssueContentChanged,
		},
		{
			name: "safe candidate still must put A first",
			input: choiceInput(
				"保守担当が少ないです。A案を選びます。",
				"保守担当が少ないです。A案を選びます。",
			),
			wantOutcome:  OutcomeReject,
			wantPosition: PositionLater,
			wantCoverage: 1,
			wantMeaning:  true,
			wantIssue:    IssueCommitmentNotFirst,
		},
		{
			name: "ambiguous frame is clarified without guessing",
			input: Input{
				Frame: QuestionFrame{
					Operator:      OperatorDefinition,
					Subject:       "モデル",
					RequiredSlots: []Slot{SlotDefinition},
					Ambiguous:     true,
				},
				Attempt: AnswerAttempt{
					Text: "モデルは評価用の表現です。",
					SlotEvidence: []SlotBinding{
						{Slot: SlotDefinition, Span: "モデルは評価用の表現です"},
					},
				},
			},
			wantOutcome:  OutcomeClarify,
			wantPosition: PositionFirst,
			wantCoverage: 1,
			wantMeaning:  true,
			wantIssue:    IssueAmbiguousQuestion,
		},
		{
			name: "all required slots must be covered",
			input: Input{
				Frame: QuestionFrame{
					Operator:      OperatorDefinition,
					Subject:       "API",
					RequiredSlots: []Slot{SlotDefinition, SlotScope},
				},
				Attempt: AnswerAttempt{
					Text: "APIは接続規約です。",
					SlotEvidence: []SlotBinding{
						{Slot: SlotDefinition, Span: "APIは接続規約です"},
					},
				},
			},
			wantOutcome:  OutcomeClarify,
			wantPosition: PositionFirst,
			wantCoverage: 0.5,
			wantMeaning:  true,
			wantIssue:    IssueRequiredSlotMissing,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Gate(test.input)
			if got.Outcome != test.wantOutcome {
				t.Fatalf("Outcome = %q, want %q; assessment=%+v", got.Outcome, test.wantOutcome, got)
			}
			if got.CommitmentPosition != test.wantPosition {
				t.Fatalf(
					"CommitmentPosition = %q, want %q; assessment=%+v",
					got.CommitmentPosition,
					test.wantPosition,
					got,
				)
			}
			if math.Abs(got.TargetCoverage-test.wantCoverage) > 0.001 {
				t.Fatalf(
					"TargetCoverage = %v, want %v; assessment=%+v",
					got.TargetCoverage,
					test.wantCoverage,
					got,
				)
			}
			if got.MeaningPreserved != test.wantMeaning {
				t.Fatalf(
					"MeaningPreserved = %v, want %v; assessment=%+v",
					got.MeaningPreserved,
					test.wantMeaning,
					got,
				)
			}
			if got.TargetSatisfied != test.targetSatisfied {
				t.Fatalf(
					"TargetSatisfied = %v, want %v; assessment=%+v",
					got.TargetSatisfied,
					test.targetSatisfied,
					got,
				)
			}
			if test.wantIssue != "" && !slices.Contains(got.Issues, test.wantIssue) {
				t.Fatalf("Issues = %v, want %q", got.Issues, test.wantIssue)
			}
		})
	}
}

func TestGateIgnoresNonPropositionalLeadingFillers(t *testing.T) {
	for _, utterance := range []string{
		"えっと。うーん。目的は評価基準をそろえることです。",
		"えっと目的は評価基準をそろえることです。",
	} {
		input := purposeInput(utterance, "")
		input.RequireTargetAtUtteranceFront = true
		assessment := Gate(input)
		if assessment.Outcome != OutcomeKeep ||
			assessment.OriginalCommitmentPosition != PositionFirst ||
			!assessment.TargetSatisfied {
			t.Fatalf("fillers were treated as a competing answer: utterance=%q assessment=%#v", utterance, assessment)
		}
	}
}

func TestGateDoesNotFrontEvidenceInsideTheFirstClause(t *testing.T) {
	for _, utterance := range []string{
		"判断のばらつきを減らしたくて目的は評価基準をそろえることです",
		"判断のばらつきを減らします：目的は評価基準をそろえることです",
		"判断のばらつきを減らします—目的は評価基準をそろえることです",
	} {
		input := purposeInput(utterance, "")
		input.RequireTargetAtUtteranceFront = true
		assessment := Gate(input)
		if assessment.Outcome != OutcomeClarify ||
			assessment.OriginalCommitmentPosition != PositionLater ||
			assessment.TargetSatisfied {
			t.Fatalf("evidence inside first clause was treated as fronted: utterance=%q assessment=%#v", utterance, assessment)
		}
	}
}

func TestStrictCoachGateRequiresTargetEvidenceToIncludeFrontModifiers(t *testing.T) {
	input := Input{
		Frame: QuestionFrame{
			Operator: OperatorBoolean,
			Subject:  "A案の採用",
			RequiredSlots: []Slot{
				SlotPolarity,
				SlotCondition,
			},
		},
		Attempt: AnswerAttempt{
			Text: "雨ならA案は採用しません。",
			SlotEvidence: []SlotBinding{
				{Slot: SlotPolarity, Span: "雨ならA案は採用しません"},
				{Slot: SlotCondition, Span: "雨ならA案は採用しません"},
			},
		},
		RequireTargetAtUtteranceFront: true,
	}
	assessment := Gate(input)
	if assessment.Outcome != OutcomeKeep ||
		assessment.OriginalCommitmentPosition != PositionFirst ||
		!assessment.TargetSatisfied {
		t.Fatalf("target evidence with its front modifier was rejected: %#v", assessment)
	}

	input.Frame.Operator = OperatorPurpose
	input.Frame.Subject = "導入目的"
	input.Frame.RequiredSlots = []Slot{SlotPurpose, SlotCause}
	input.Attempt.Text = "判断のばらつきを減らしたくて目的は評価基準をそろえることです"
	input.Attempt.SlotEvidence = []SlotBinding{
		{Slot: SlotPurpose, Span: "目的は評価基準をそろえることです"},
		{Slot: SlotCause, Span: "判断のばらつきを減らしたくて目的は評価基準をそろえることです"},
	}
	assessment = Gate(input)
	if assessment.OriginalCommitmentPosition != PositionLater ||
		assessment.TargetSatisfied {
		t.Fatalf("a different slot's broad span fronted the target: %#v", assessment)
	}
}

func TestGateRejectsMalformedContracts(t *testing.T) {
	tests := []Input{
		{
			Frame: QuestionFrame{
				Operator:      OperatorPurpose,
				Subject:       "目的",
				RequiredSlots: []Slot{SlotPurpose, SlotPurpose},
			},
			Attempt: AnswerAttempt{Text: "目的は安全性の確認です。"},
		},
		{
			Frame: QuestionFrame{
				Operator:      OperatorDefinition,
				Subject:       "API",
				RequiredSlots: []Slot{SlotPurpose},
			},
			Attempt: AnswerAttempt{Text: "APIは接続規約です。"},
		},
		{
			Frame: QuestionFrame{
				Operator:      OperatorDefinition,
				Subject:       "API",
				RequiredSlots: []Slot{SlotDefinition},
			},
			Attempt: AnswerAttempt{
				Text: "APIは接続規約です。",
				SlotEvidence: []SlotBinding{
					{Slot: SlotDefinition, Span: "原文にない定義"},
				},
			},
		},
	}

	for index, input := range tests {
		got := Gate(input)
		if got.Outcome != OutcomeReject ||
			!slices.Contains(got.Issues, IssueInvalidContract) {
			t.Fatalf("case %d did not fail closed: %+v", index, got)
		}
	}
}

func definitionInput(original, reconstruction string) Input {
	return Input{
		Frame: QuestionFrame{
			Operator:      OperatorDefinition,
			Subject:       "API",
			RequiredSlots: []Slot{SlotDefinition},
		},
		Attempt: AnswerAttempt{
			Text: original,
			SlotEvidence: []SlotBinding{
				{
					Slot: SlotDefinition,
					Span: "APIはソフトウェア間の接続規約です",
				},
			},
		},
		Reconstruction: reconstruction,
	}
}

func purposeInput(original, reconstruction string) Input {
	return Input{
		Frame: QuestionFrame{
			Operator:      OperatorPurpose,
			Subject:       "この評価",
			RequiredSlots: []Slot{SlotPurpose},
		},
		Attempt: AnswerAttempt{
			Text: original,
			SlotEvidence: []SlotBinding{
				{
					Slot: SlotPurpose,
					Span: "目的は評価基準をそろえることです",
				},
			},
		},
		Reconstruction: reconstruction,
	}
}

func choiceInput(original, reconstruction string) Input {
	return Input{
		Frame: QuestionFrame{
			Operator:      OperatorChoice,
			Subject:       "採用案",
			RequiredSlots: []Slot{SlotSelection},
		},
		Attempt: AnswerAttempt{
			Text: original,
			SlotEvidence: []SlotBinding{
				{Slot: SlotSelection, Span: "A案を選びます"},
			},
		},
		Reconstruction: reconstruction,
	}
}

func booleanInput(original, reconstruction, commitment string) Input {
	return Input{
		Frame: QuestionFrame{
			Operator:      OperatorBoolean,
			Subject:       "実施判断",
			RequiredSlots: []Slot{SlotPolarity},
		},
		Attempt: AnswerAttempt{
			Text: original,
			SlotEvidence: []SlotBinding{
				{Slot: SlotPolarity, Span: commitment},
			},
		},
		Reconstruction: reconstruction,
	}
}
