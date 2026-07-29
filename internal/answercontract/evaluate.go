package answercontract

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// Evaluate validates a current-turn LAC and derives all scores independently
// from its discrete evidence. originalAnswer is used only during this call to
// enforce the meaning-preservation invariant and is not retained.
func Evaluate(contract Contract, originalAnswer string) (Assessment, error) {
	normalized, coverage, err := validateAndNormalize(contract)
	if err != nil {
		return Assessment{}, err
	}
	question := normalized.QuestionFrame
	commitment := normalized.CommitmentFront
	repair := normalized.CounterfactualRepair

	gap := hypothesisGap(question.Hypotheses)
	entropy := hypothesisEntropy(question.Hypotheses)
	topConfidence := question.Hypotheses[0].Confidence
	ambiguous := (len(question.Hypotheses) > 1 && gap <= HypothesisGapThreshold) ||
		(entropy >= HighEntropyThreshold && topConfidence < LowTopHypothesisThreshold)

	targetSlot, _ := TargetSlot(question.Operator)
	targetFilled := containsSlot(commitment.FilledSlots, targetSlot)
	targetSatisfied := targetFilled &&
		commitment.FillsTarget &&
		coverage >= TargetCoverageThreshold &&
		commitment.PositionClass != PositionAbsent

	meaningPreserved := preservesMeaning(
		originalAnswer,
		repair.ReconstructedAnswer,
		commitment.Calibration,
	)
	if repair.RepairGain == 0 &&
		collapseSpace(repair.ReconstructedAnswer) != collapseSpace(originalAnswer) {
		meaningPreserved = false
	}
	if repair.MinimalAnswer != "" &&
		!strings.Contains(
			collapseSpace(repair.ReconstructedAnswer),
			collapseSpace(repair.MinimalAnswer),
		) {
		meaningPreserved = false
	}
	if !targetFilled &&
		repair.MeaningPreservationConfidence < InferredCommitmentThreshold {
		meaningPreserved = false
	}

	repairAccepted := meaningPreserved &&
		repair.MeaningPreservationConfidence >= MeaningPreservationThreshold
	meaningScore := 0.0
	if repairAccepted {
		meaningScore = repair.MeaningPreservationConfidence
	}

	repairWanted := !targetSatisfied ||
		commitment.PositionClass == PositionLater ||
		repairableIssue(commitment.Issue)
	highGainRepair := repairWanted &&
		repair.RepairGain >= RepairGainThreshold
	shouldRestructure := !ambiguous && highGainRepair && repairAccepted

	outcome := OutcomeKeep
	switch {
	case ambiguous:
		outcome = OutcomeClarify
	case shouldRestructure:
		outcome = OutcomeRestructure
	case highGainRepair && !repairAccepted:
		outcome = OutcomeReject
	case !targetSatisfied && (coverage < 0.20 ||
		commitment.Issue == IssueAnswerTypeMismatch ||
		commitment.Issue == IssueQuestionRestatement ||
		commitment.Issue == IssueNotEvaluable):
		outcome = OutcomeReject
	case !targetSatisfied:
		outcome = OutcomeClarify
	case blockingIssue(commitment.Issue):
		outcome = OutcomeReject
	}

	return Assessment{
		Metrics: Metrics{
			HypothesisGap:           roundScore(gap),
			HypothesisEntropy:       roundScore(entropy),
			TargetSlotCoverage:      roundScore(coverage),
			CommitmentFrontPosition: commitment.PositionClass,
			MeaningPreservation:     roundScore(meaningScore),
		},
		Outcome:             outcome,
		Ambiguous:           ambiguous,
		TargetSatisfied:     targetSatisfied,
		RepairAccepted:      repairAccepted,
		ReconstructedAnswer: repair.ReconstructedAnswer,
	}, nil
}

func validateAndNormalize(contract Contract) (Contract, float64, error) {
	question := &contract.QuestionFrame
	commitment := &contract.CommitmentFront
	repair := &contract.CounterfactualRepair

	question.Subject = collapseSpace(question.Subject)
	if _, ok := TargetSlot(question.Operator); !ok ||
		!boundedText(question.Subject, MaxSubjectRunes, true) {
		return Contract{}, 0, ErrInvalidContract
	}
	if len(question.RequiredSlots) == 0 ||
		len(question.RequiredSlots) > MaxRequiredSlots {
		return Contract{}, 0, ErrInvalidContract
	}
	required, err := uniqueSlots(question.RequiredSlots)
	if err != nil {
		return Contract{}, 0, err
	}
	question.RequiredSlots = required
	targetSlot, _ := TargetSlot(question.Operator)
	if !containsSlot(required, targetSlot) {
		return Contract{}, 0, ErrInvalidContract
	}

	if len(question.Hypotheses) == 0 || len(question.Hypotheses) > MaxHypotheses {
		return Contract{}, 0, ErrInvalidContract
	}
	hypothesisNames := make(map[string]struct{}, len(question.Hypotheses))
	confidenceSum := 0.0
	previousConfidence := 2.0
	for index := range question.Hypotheses {
		hypothesis := &question.Hypotheses[index]
		hypothesis.Interpretation = collapseSpace(hypothesis.Interpretation)
		if !boundedText(hypothesis.Interpretation, MaxHypothesisRunes, true) ||
			!unitInterval(hypothesis.Confidence) ||
			hypothesis.Confidence <= 0 ||
			hypothesis.Confidence > previousConfidence+1e-9 {
			return Contract{}, 0, ErrInvalidContract
		}
		if _, duplicate := hypothesisNames[hypothesis.Interpretation]; duplicate {
			return Contract{}, 0, ErrInvalidContract
		}
		hypothesisNames[hypothesis.Interpretation] = struct{}{}
		previousConfidence = hypothesis.Confidence
		confidenceSum += hypothesis.Confidence
	}
	if confidenceSum > 1.000001 {
		return Contract{}, 0, ErrInvalidContract
	}

	commitment.FirstCommitment = collapseSpace(commitment.FirstCommitment)
	if !boundedText(
		commitment.FirstCommitment,
		MaxFirstCommitmentRunes,
		commitment.PositionClass != PositionAbsent,
	) ||
		!validPosition(commitment.PositionClass) ||
		!validCalibration(commitment.Calibration) ||
		!validIssue(commitment.Issue) ||
		!unitInterval(commitment.TargetCoverage) {
		return Contract{}, 0, ErrInvalidContract
	}
	if commitment.PositionClass == PositionAbsent &&
		commitment.FirstCommitment != "" {
		return Contract{}, 0, ErrInvalidContract
	}
	filled, err := uniqueSlots(commitment.FilledSlots)
	if err != nil {
		return Contract{}, 0, err
	}
	for _, slot := range filled {
		if !containsSlot(required, slot) {
			return Contract{}, 0, ErrInvalidContract
		}
	}
	commitment.FilledSlots = filled
	coverage := float64(len(filled)) / float64(len(required))
	computedFillsTarget := containsSlot(filled, targetSlot)
	if math.Abs(commitment.TargetCoverage-coverage) > CoverageTolerance ||
		commitment.FillsTarget != computedFillsTarget ||
		(commitment.PositionClass == PositionAbsent && commitment.FillsTarget) ||
		(commitment.Issue == IssueNone && coverage < 1) {
		return Contract{}, 0, ErrInvalidContract
	}

	repair.MinimalAnswer = collapseSpace(repair.MinimalAnswer)
	repair.ReconstructedAnswer = collapseSpace(repair.ReconstructedAnswer)
	if !boundedText(repair.MinimalAnswer, MaxMinimalAnswerRunes, false) ||
		!boundedText(repair.ReconstructedAnswer, MaxReconstructedRunes, false) ||
		!unitInterval(repair.MeaningPreservationConfidence) ||
		!unitInterval(repair.RepairGain) {
		return Contract{}, 0, ErrInvalidContract
	}
	if repair.RepairGain > 0 &&
		(repair.MinimalAnswer == "" || repair.ReconstructedAnswer == "") {
		return Contract{}, 0, ErrInvalidContract
	}
	return contract, coverage, nil
}

func hypothesisGap(hypotheses []Hypothesis) float64 {
	if len(hypotheses) < 2 {
		return 1
	}
	return hypotheses[0].Confidence - hypotheses[1].Confidence
}

func hypothesisEntropy(hypotheses []Hypothesis) float64 {
	probabilities := make([]float64, 0, len(hypotheses)+1)
	sum := 0.0
	for _, hypothesis := range hypotheses {
		probabilities = append(probabilities, hypothesis.Confidence)
		sum += hypothesis.Confidence
	}
	if residual := 1 - sum; residual > 1e-9 {
		probabilities = append(probabilities, residual)
	}
	if len(probabilities) <= 1 {
		return 0
	}
	entropy := 0.0
	for _, probability := range probabilities {
		if probability > 0 {
			entropy -= probability * math.Log(probability)
		}
	}
	return entropy / math.Log(float64(len(probabilities)))
}

func preservesMeaning(original, reconstructed string, calibration Calibration) bool {
	original = collapseSpace(original)
	reconstructed = collapseSpace(reconstructed)
	if reconstructed == "" {
		return original == ""
	}
	if hasCondition(original) != hasCondition(reconstructed) {
		return false
	}
	originalUncertainty := uncertaintyLevel(original)
	if originalUncertainty == 0 {
		originalUncertainty = calibrationUncertainty(calibration)
	}
	return originalUncertainty == uncertaintyLevel(reconstructed)
}

func hasCondition(value string) bool {
	for _, marker := range []string{
		"なら", "であれば", "場合", "とき", "時だけ", "限り", "ただし",
		"を除き", "を除く", "前提", "次第", "平日", "休日", "通常", "障害時",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func uncertaintyLevel(value string) int {
	for _, marker := range []string{
		"わかりません", "分かりません", "不明です", "判断できません",
		"答えられません", "確認できません", "情報がありません",
	} {
		if strings.Contains(value, marker) {
			return 3
		}
	}
	for _, marker := range []string{
		"断定できません", "確信がありません", "不確実", "判断が難しい",
		"まだ不明", "未確認",
	} {
		if strings.Contains(value, marker) {
			return 2
		}
	}
	for _, marker := range []string{
		"かもしれ", "可能性", "と思います", "おそらく", "たぶん",
		"推定", "見込み", "約", "くらい", "程度",
	} {
		if strings.Contains(value, marker) {
			return 1
		}
	}
	return 0
}

func calibrationUncertainty(calibration Calibration) int {
	switch calibration {
	case CalibrationConditional:
		return 1
	case CalibrationUncertain:
		return 2
	case CalibrationAbstain:
		return 3
	default:
		return 0
	}
}

func repairableIssue(issue Issue) bool {
	switch issue {
	case IssueReasonOnly, IssueBackgroundFirst, IssueConditionSeparated,
		IssueAmbiguousCommitment, IssueMissingRequiredSlot,
		IssueInsufficientEvidence:
		return true
	default:
		return false
	}
}

func blockingIssue(issue Issue) bool {
	switch issue {
	case IssueQuestionRestatement, IssueAnswerTypeMismatch,
		IssueUnsupportedCertainty, IssueContradiction,
		IssueMeaningChanged, IssueNotEvaluable:
		return true
	default:
		return false
	}
}

func uniqueSlots(slots []RequiredSlot) ([]RequiredSlot, error) {
	result := make([]RequiredSlot, 0, len(slots))
	seen := make(map[RequiredSlot]struct{}, len(slots))
	for _, slot := range slots {
		if !validSlot(slot) {
			return nil, ErrInvalidContract
		}
		if _, duplicate := seen[slot]; duplicate {
			return nil, ErrInvalidContract
		}
		seen[slot] = struct{}{}
		result = append(result, slot)
	}
	return result, nil
}

func validSlot(slot RequiredSlot) bool {
	switch slot {
	case SlotPolarity, SlotSelection, SlotQuantity, SlotState, SlotCause,
		SlotProcedure, SlotDefinition, SlotComparison, SlotEvidence,
		SlotPosition, SlotUnit, SlotCondition, SlotUncertainty, SlotScope:
		return true
	default:
		return false
	}
}

func validPosition(position PositionClass) bool {
	return position == PositionFirst ||
		position == PositionLater ||
		position == PositionAbsent
}

func validCalibration(calibration Calibration) bool {
	return calibration == CalibrationCommitted ||
		calibration == CalibrationConditional ||
		calibration == CalibrationUncertain ||
		calibration == CalibrationAbstain
}

func validIssue(issue Issue) bool {
	switch issue {
	case IssueNone, IssueTargetMissing, IssueMissingRequiredSlot,
		IssueReasonOnly, IssueBackgroundFirst, IssueConditionSeparated,
		IssueQuestionRestatement, IssueAmbiguousCommitment,
		IssueAnswerTypeMismatch, IssueUnsupportedCertainty,
		IssueInsufficientEvidence, IssueContradiction,
		IssueMeaningChanged, IssueNotEvaluable:
		return true
	default:
		return false
	}
}

func containsSlot(slots []RequiredSlot, target RequiredSlot) bool {
	for _, slot := range slots {
		if slot == target {
			return true
		}
	}
	return false
}

func boundedText(value string, maximum int, required bool) bool {
	if !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > maximum {
		return false
	}
	return !required || value != ""
}

func unitInterval(value float64) bool {
	return !math.IsNaN(value) &&
		!math.IsInf(value, 0) &&
		value >= 0 &&
		value <= 1
}

func collapseSpace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func roundScore(value float64) float64 {
	return math.Round(value*1_000) / 1_000
}

func invalidField(name string) error {
	return fmt.Errorf("%w: %s", ErrInvalidContract, name)
}
