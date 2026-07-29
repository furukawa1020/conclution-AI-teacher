package answercontract

import (
	"fmt"
	"math"
	"testing"
)

type hardCase struct {
	name          string
	question      string
	answer        string
	operator      Operator
	required      []RequiredSlot
	filled        []RequiredSlot
	hypotheses    []float64
	position      PositionClass
	calibration   Calibration
	issue         Issue
	first         string
	minimal       string
	reconstructed string
	meaning       float64
	gain          float64
	want          Outcome
}

func TestJapaneseAnswerToAnswerInvariantHardCases(t *testing.T) {
	cases := []hardCase{
		{
			name: "boolean direct yes", question: "この案に賛成ですか？", answer: "はい、賛成です。",
			operator: OperatorBoolean, required: slots(SlotPolarity), filled: slots(SlotPolarity),
			hypotheses: []float64{0.99, 0.01}, position: PositionFirst,
			calibration: CalibrationCommitted, issue: IssueNone, first: "賛成です",
			want: OutcomeKeep,
		},
		{
			name: "boolean calibrated yes", question: "成功しますか？", answer: "たぶん成功します。",
			operator: OperatorBoolean, required: slots(SlotPolarity), filled: slots(SlotPolarity),
			hypotheses: []float64{0.88, 0.08, 0.04}, position: PositionFirst,
			calibration: CalibrationConditional, issue: IssueNone, first: "たぶん成功します",
			want: OutcomeKeep,
		},
		{
			name: "negative question ambiguity", question: "会議には行かないんですか？", answer: "はい。",
			operator: OperatorBoolean, required: slots(SlotPolarity), filled: nil,
			hypotheses: []float64{0.58, 0.42}, position: PositionAbsent,
			calibration: CalibrationCommitted, issue: IssueAmbiguousCommitment,
			want: OutcomeClarify,
		},
		{
			name: "double negative ambiguity", question: "公開を禁止しない方針に反対ですか？", answer: "はい。",
			operator: OperatorBoolean, required: slots(SlotPolarity), filled: nil,
			hypotheses: []float64{0.55, 0.45}, position: PositionAbsent,
			calibration: CalibrationCommitted, issue: IssueAmbiguousCommitment,
			want: OutcomeClarify,
		},
		{
			name: "choice direct", question: "A案とB案ならどちらですか？", answer: "A案です。",
			operator: OperatorChoice, required: slots(SlotSelection), filled: slots(SlotSelection),
			hypotheses: []float64{0.97, 0.03}, position: PositionFirst,
			calibration: CalibrationCommitted, issue: IssueNone, first: "A案です",
			want: OutcomeKeep,
		},
		{
			name: "choice after background", question: "A案とB案ならどちらですか？", answer: "費用を考えると、A案です。",
			operator: OperatorChoice, required: slots(SlotSelection), filled: slots(SlotSelection),
			hypotheses: []float64{0.96, 0.04}, position: PositionLater,
			calibration: CalibrationCommitted, issue: IssueBackgroundFirst, first: "A案です",
			minimal: "A案です", reconstructed: "A案です。費用を考えるとそう判断します。",
			meaning: 0.96, gain: 0.28, want: OutcomeRestructure,
		},
		{
			name: "choice interpretations close", question: "速度と価格のどちらを優先しますか？", answer: "両方です。",
			operator: OperatorChoice, required: slots(SlotSelection), filled: nil,
			hypotheses: []float64{0.52, 0.48}, position: PositionAbsent,
			calibration: CalibrationUncertain, issue: IssueAmbiguousCommitment,
			want: OutcomeClarify,
		},
		{
			name: "quantity exact with unit", question: "参加者は何人ですか？", answer: "10人です。",
			operator: OperatorQuantity, required: slots(SlotQuantity, SlotUnit),
			filled: slots(SlotQuantity, SlotUnit), hypotheses: []float64{0.99},
			position: PositionFirst, calibration: CalibrationCommitted,
			issue: IssueNone, first: "10人です", want: OutcomeKeep,
		},
		{
			name: "quantity inferred unit", question: "何分かかりますか？", answer: "だいたい20くらいです。",
			operator: OperatorQuantity, required: slots(SlotQuantity, SlotUnit),
			filled: slots(SlotQuantity), hypotheses: []float64{0.95, 0.05},
			position: PositionFirst, calibration: CalibrationConditional,
			issue: IssueMissingRequiredSlot, first: "だいたい20くらいです",
			minimal: "約20分です", reconstructed: "約20分です",
			meaning: 0.94, gain: 0.25, want: OutcomeReject,
		},
		{
			name: "quantity type mismatch", question: "参加者は何人ですか？", answer: "3時間です。",
			operator: OperatorQuantity, required: slots(SlotQuantity, SlotUnit), filled: nil,
			hypotheses: []float64{0.99}, position: PositionAbsent,
			calibration: CalibrationCommitted, issue: IssueAnswerTypeMismatch,
			meaning: 0.10, want: OutcomeReject,
		},
		{
			name: "cause direct", question: "なぜ延期したのですか？", answer: "予算が足りないからです。",
			operator: OperatorCause, required: slots(SlotCause), filled: slots(SlotCause),
			hypotheses: []float64{0.99}, position: PositionFirst,
			calibration: CalibrationCommitted, issue: IssueNone,
			first: "予算が足りないからです", want: OutcomeKeep,
		},
		{
			name: "reason only implies stance", question: "導入すべきですか？理由も教えてください。", answer: "高すぎるからです。",
			operator: OperatorBoolean, required: slots(SlotPolarity, SlotCause), filled: slots(SlotCause),
			hypotheses: []float64{0.91, 0.09}, position: PositionAbsent,
			calibration: CalibrationCommitted, issue: IssueReasonOnly,
			minimal: "導入には反対です", reconstructed: "導入には反対です。高すぎるからです。",
			meaning: 0.94, gain: 0.34, want: OutcomeReject,
		},
		{
			name: "required reason missing", question: "賛成ですか？理由も教えてください。", answer: "賛成です。",
			operator: OperatorBoolean, required: slots(SlotPolarity, SlotCause), filled: slots(SlotPolarity),
			hypotheses: []float64{0.99}, position: PositionFirst,
			calibration: CalibrationCommitted, issue: IssueMissingRequiredSlot,
			first: "賛成です", minimal: "賛成です", reconstructed: "賛成です",
			meaning: 0.99, gain: 0.05, want: OutcomeClarify,
		},
		{
			name: "circular cause", question: "なぜ実験に失敗したのですか？", answer: "失敗したからです。",
			operator: OperatorCause, required: slots(SlotCause), filled: slots(SlotCause),
			hypotheses: []float64{0.98}, position: PositionFirst,
			calibration: CalibrationCommitted, issue: IssueQuestionRestatement,
			first: "失敗したからです", meaning: 0.20, want: OutcomeReject,
		},
		{
			name: "condition separated but preserved", question: "初心者にも勧めますか？", answer: "おすすめです。初心者なら使えます。",
			operator: OperatorBoolean, required: slots(SlotPolarity, SlotCondition),
			filled: slots(SlotPolarity, SlotCondition), hypotheses: []float64{0.96, 0.04},
			position: PositionFirst, calibration: CalibrationCommitted,
			issue: IssueConditionSeparated, first: "おすすめです",
			minimal: "初心者ならおすすめです", reconstructed: "初心者ならおすすめです",
			meaning: 0.97, gain: 0.30, want: OutcomeRestructure,
		},
		{
			name: "one condition branch missing", question: "平日と休日の受付時間は？", answer: "平日は9時からです。",
			operator: OperatorState, required: slots(SlotState, SlotCondition),
			filled: slots(SlotState), hypotheses: []float64{0.98},
			position: PositionFirst, calibration: CalibrationCommitted,
			issue: IssueMissingRequiredSlot, first: "平日は9時からです",
			minimal: "平日は9時からです", reconstructed: "平日は9時からです",
			meaning: 0.98, gain: 0.03, want: OutcomeClarify,
		},
		{
			name: "ambiguous condition referent", question: "Aの場合とBの場合は？", answer: "その場合だけ停止します。",
			operator: OperatorState, required: slots(SlotState, SlotCondition), filled: nil,
			hypotheses: []float64{0.50, 0.50}, position: PositionAbsent,
			calibration: CalibrationUncertain, issue: IssueAmbiguousCommitment,
			want: OutcomeClarify,
		},
		{
			name: "explicit unknown is valid", question: "原因は何ですか？", answer: "まだわかりません。",
			operator: OperatorCause, required: slots(SlotCause), filled: slots(SlotCause),
			hypotheses: []float64{0.99}, position: PositionFirst,
			calibration: CalibrationAbstain, issue: IssueNone,
			first: "まだわかりません", want: OutcomeKeep,
		},
		{
			name: "vague boolean answer", question: "成功しますか？", answer: "微妙ですね。",
			operator: OperatorBoolean, required: slots(SlotPolarity), filled: nil,
			hypotheses: []float64{0.52, 0.31, 0.17}, position: PositionAbsent,
			calibration: CalibrationUncertain, issue: IssueAmbiguousCommitment,
			want: OutcomeClarify,
		},
		{
			name: "pure nonanswer", question: "対応できますか？", answer: "いい質問ですね。",
			operator: OperatorBoolean, required: slots(SlotPolarity), filled: nil,
			hypotheses: []float64{0.99}, position: PositionAbsent,
			calibration: CalibrationAbstain, issue: IssueNotEvaluable,
			meaning: 0.08, want: OutcomeReject,
		},
		{
			name: "contradictory polarity", question: "賛成ですか？", answer: "はい、反対です。",
			operator: OperatorBoolean, required: slots(SlotPolarity), filled: nil,
			hypotheses: []float64{0.52, 0.48}, position: PositionAbsent,
			calibration: CalibrationUncertain, issue: IssueContradiction,
			want: OutcomeClarify,
		},
		{
			name: "research evidence calibrated repair", question: "効果の根拠は？", answer: "一人には効いた可能性があります。",
			operator: OperatorEvidence, required: slots(SlotEvidence, SlotUncertainty),
			filled: slots(SlotEvidence, SlotUncertainty), hypotheses: []float64{0.96, 0.04},
			position: PositionFirst, calibration: CalibrationConditional,
			issue: IssueInsufficientEvidence, first: "一人には効いた可能性があります",
			minimal:       "一人の事例",
			reconstructed: "一人の事例では効いた可能性がありますが、一般化はできません",
			meaning:       0.95, gain: 0.27, want: OutcomeRestructure,
		},
		{
			name: "research causal claim cannot change certainty", question: "この介入は改善すると言えますか？", answer: "相関があったので改善します。",
			operator: OperatorEvidence, required: slots(SlotEvidence, SlotUncertainty),
			filled: slots(SlotEvidence), hypotheses: []float64{0.91, 0.09},
			position: PositionFirst, calibration: CalibrationCommitted,
			issue: IssueUnsupportedCertainty, first: "改善します",
			minimal:       "因果関係は未確認です",
			reconstructed: "相関はありますが、改善の因果関係は断定できません",
			meaning:       0.92, gain: 0.35, want: OutcomeReject,
		},
		{
			name: "research calibrated reservation", question: "平均が上がったので有効ですか？", answer: "平均は上がりましたが、まだ有効とは断定できません。",
			operator: OperatorEvidence, required: slots(SlotEvidence, SlotUncertainty),
			filled: slots(SlotEvidence, SlotUncertainty), hypotheses: []float64{0.97, 0.03},
			position: PositionFirst, calibration: CalibrationUncertain,
			issue: IssueNone, first: "まだ有効とは断定できません",
			want: OutcomeKeep,
		},
		{
			name: "repair may not add a condition", question: "採用しますか？", answer: "採用します。",
			operator: OperatorBoolean, required: slots(SlotPolarity), filled: slots(SlotPolarity),
			hypotheses: []float64{0.99}, position: PositionLater,
			calibration: CalibrationCommitted, issue: IssueBackgroundFirst, first: "採用します",
			minimal: "採用します", reconstructed: "データが十分なら採用します",
			meaning: 0.99, gain: 0.30, want: OutcomeReject,
		},
		{
			name: "repair may not remove uncertainty", question: "賛成ですか？", answer: "たぶん賛成です。",
			operator: OperatorBoolean, required: slots(SlotPolarity), filled: slots(SlotPolarity),
			hypotheses: []float64{0.90, 0.10}, position: PositionLater,
			calibration: CalibrationConditional, issue: IssueBackgroundFirst, first: "たぶん賛成です",
			minimal: "賛成です", reconstructed: "賛成です",
			meaning: 0.99, gain: 0.30, want: OutcomeReject,
		},
	}

	if len(cases) < 20 {
		t.Fatalf("hard case count = %d, want at least 20", len(cases))
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			contract := contractForCase(test)
			assessment, err := Evaluate(contract, test.answer)
			if err != nil {
				t.Fatalf("Evaluate(%s -> %s): %v", test.question, test.answer, err)
			}
			if assessment.Outcome != test.want {
				t.Fatalf(
					"outcome = %s, want %s; metrics=%+v repairAccepted=%v",
					assessment.Outcome,
					test.want,
					assessment.Metrics,
					assessment.RepairAccepted,
				)
			}
			wantCoverage := float64(len(test.filled)) / float64(len(test.required))
			if math.Abs(assessment.Metrics.TargetSlotCoverage-wantCoverage) > 0.001 {
				t.Fatalf(
					"coverage = %.3f, want %.3f",
					assessment.Metrics.TargetSlotCoverage,
					wantCoverage,
				)
			}
			if assessment.Metrics.CommitmentFrontPosition != test.position {
				t.Fatalf(
					"position = %s, want %s",
					assessment.Metrics.CommitmentFrontPosition,
					test.position,
				)
			}
			if test.want == OutcomeRestructure &&
				(!assessment.RepairAccepted ||
					assessment.ReconstructedAnswer != test.reconstructed) {
				t.Fatalf("safe repair was not accepted: %+v", assessment)
			}
		})
	}
}

func contractForCase(test hardCase) Contract {
	hypotheses := make([]Hypothesis, len(test.hypotheses))
	for index, confidence := range test.hypotheses {
		hypotheses[index] = Hypothesis{
			Interpretation: fmt.Sprintf("interpretation-%d", index+1),
			Confidence:     confidence,
		}
	}
	first := test.first
	if test.position != PositionAbsent && first == "" {
		first = test.answer
	}
	minimal := test.minimal
	reconstructed := test.reconstructed
	meaning := test.meaning
	if test.gain == 0 {
		if minimal == "" {
			minimal = test.answer
		}
		if reconstructed == "" {
			reconstructed = test.answer
		}
		if meaning == 0 {
			meaning = 0.99
		}
	}
	target, _ := TargetSlot(test.operator)
	return Contract{
		QuestionFrame: QuestionFrame{
			Operator:      test.operator,
			Subject:       test.question,
			RequiredSlots: append([]RequiredSlot(nil), test.required...),
			Hypotheses:    hypotheses,
		},
		CommitmentFront: CommitmentFront{
			FirstCommitment: first,
			FillsTarget:     containsSlot(test.filled, target),
			TargetCoverage:  float64(len(test.filled)) / float64(len(test.required)),
			FilledSlots:     append([]RequiredSlot(nil), test.filled...),
			PositionClass:   test.position,
			Calibration:     test.calibration,
			Issue:           test.issue,
		},
		CounterfactualRepair: CounterfactualRepair{
			MinimalAnswer:                 minimal,
			ReconstructedAnswer:           reconstructed,
			MeaningPreservationConfidence: meaning,
			RepairGain:                    test.gain,
		},
	}
}

func slots(values ...RequiredSlot) []RequiredSlot {
	return values
}
