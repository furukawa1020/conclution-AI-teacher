package answercontract

import (
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	nonPropositionalFillerPattern = regexp.MustCompile(
		`(?i)^(?:えっ?と+|ええと+|えー+と+|あの+|あのー+|うー+ん|んー+|まあ+|その+|なんというか|um+|uh+|erm+)$`,
	)
	arabicNumberPattern = regexp.MustCompile(
		`[0-9０-９]+([.．][0-9０-９]+)?(円|人|件|日|時間|分|秒|年|月|週|%|％|個|回|倍|GB|MB|KB|キロ|メートル|cm|mm)?`,
	)
	kanjiNumberWithUnitPattern = regexp.MustCompile(
		`[一二三四五六七八九十百千万]+(円|人|件|日|時間|分|秒|年|月|週|個|回|倍)`,
	)
	latinAnchorPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9._-]*`)
	labelAnchorPattern = regexp.MustCompile(
		`[A-Za-zＡ-Ｚａ-ｚ0-9０-９ァ-ヶー]{1,24}(案|社|方式|プラン|モデル|版)`,
	)
	quotedAnchorPattern    = regexp.MustCompile(`「([^」]{1,40})」`)
	conditionClausePattern = regexp.MustCompile(
		`([^\s、。,.!?！？]{1,30})(なら|であれば|の場合|のとき|時だけ)`,
	)
	causalClausePattern = regexp.MustCompile(
		`([^\s、。,.!?！？]{1,40})(から|ため|ので)`,
	)
)

// Validate checks the structural LAC contract without treating it as an
// authoritative audit of any candidate answer.
func Validate(contract Contract) error {
	_, _, err := validateAndNormalize(contract)
	return err
}

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
		topConfidence < LowTopHypothesisThreshold ||
		entropy >= HighEntropyThreshold

	targetSlot, _ := TargetSlot(question.Operator)
	targetFilled := containsSlot(commitment.FilledSlots, targetSlot)
	commitmentAnchored := commitment.PositionClass == PositionAbsent ||
		containsNormalized(originalAnswer, commitment.FirstCommitment)
	commitmentFronted := commitment.PositionClass == PositionFirst &&
		startsWithCommitmentIgnoringFillers(
			originalAnswer,
			commitment.FirstCommitment,
		)
	targetSatisfied := targetFilled &&
		commitment.FillsTarget &&
		coverage == 1 &&
		commitmentAnchored &&
		commitmentFronted &&
		commitment.Issue == IssueNone

	meaningPreserved := preservesMeaning(
		originalAnswer,
		repair.ReconstructedAnswer,
		commitment.FirstCommitment,
		commitment.Calibration,
		question.Operator,
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

	repairFront := repair.MinimalAnswer
	if commitment.PositionClass == PositionLater ||
		commitment.Issue == IssueBackgroundFirst {
		repairFront = commitment.FirstCommitment
	}
	repairFronted := repair.RepairGain == 0 ||
		startsWithNormalized(repair.ReconstructedAnswer, repairFront)
	repairAccepted := coverage == 1 &&
		!blockingIssue(commitment.Issue) &&
		meaningPreserved &&
		repairFronted &&
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
	shouldRestructure := !ambiguous &&
		coverage == 1 &&
		highGainRepair &&
		repairAccepted

	outcome := OutcomeKeep
	switch {
	case ambiguous:
		outcome = OutcomeClarify
	case !commitmentAnchored:
		outcome = OutcomeReject
	case commitment.PositionClass == PositionFirst && !commitmentFronted:
		outcome = OutcomeReject
	case blockingIssue(commitment.Issue):
		outcome = OutcomeReject
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

func startsWithNormalized(container, value string) bool {
	container = collapseSpace(container)
	value = collapseSpace(value)
	return value != "" && strings.HasPrefix(container, value)
}

// startsWithCommitmentIgnoringFillers permits only whole, fixed
// non-propositional filler clauses before the answer. Reasons, conditions,
// uncertainty, and mixed-content clauses remain substantive.
func startsWithCommitmentIgnoringFillers(container, value string) bool {
	if startsWithNormalized(container, value) {
		return true
	}
	containerClauses := semanticClauses(container)
	valueClauses := semanticClauses(value)
	if len(containerClauses) == 0 || len(valueClauses) == 0 {
		return false
	}
	firstSubstantive := 0
	for firstSubstantive < len(containerClauses) &&
		nonPropositionalFillerPattern.MatchString(containerClauses[firstSubstantive]) {
		firstSubstantive++
	}
	if firstSubstantive == 0 ||
		len(containerClauses)-firstSubstantive < len(valueClauses) {
		return false
	}
	for index, clause := range valueClauses {
		candidate := containerClauses[firstSubstantive+index]
		if index == len(valueClauses)-1 {
			return strings.HasPrefix(candidate, clause)
		}
		if candidate != clause {
			return false
		}
	}
	return false
}

func containsNormalized(container, value string) bool {
	container = collapseSpace(container)
	value = collapseSpace(value)
	return value != "" && strings.Contains(container, value)
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
	if question.Operator == OperatorQuantity &&
		!containsSlot(required, SlotUnit) {
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

func preservesMeaning(
	original,
	reconstructed,
	firstCommitment string,
	calibration Calibration,
	operator Operator,
) bool {
	original = collapseSpace(original)
	reconstructed = collapseSpace(reconstructed)
	if reconstructed == "" {
		return original == ""
	}
	// A repair may move exactly the clause containing A to the front while
	// preserving the relative order of every other clause. Arbitrary
	// permutations can change pronoun and discourse references even when the
	// clause multiset is identical. No confidence score can override this
	// deterministic floor.
	if !stableCommitmentFrontMove(
		original,
		reconstructed,
		firstCommitment,
	) {
		return false
	}
	if hasCondition(original) != hasCondition(reconstructed) {
		return false
	}
	if !sameStringSet(
		extractClauses(conditionClausePattern, original),
		extractClauses(conditionClausePattern, reconstructed),
	) {
		return false
	}
	if hasCausalClaim(original) != hasCausalClaim(reconstructed) {
		return false
	}
	if !sameStringSet(
		extractClauses(causalClausePattern, original),
		extractClauses(causalClausePattern, reconstructed),
	) {
		return false
	}
	if operator == OperatorBoolean &&
		polarityClass(original) != polarityClass(reconstructed) {
		return false
	}
	if !sameStringSet(protectedFacts(original), protectedFacts(reconstructed)) {
		return false
	}
	originalUncertainty := uncertaintyLevel(original)
	if originalUncertainty == 0 {
		originalUncertainty = calibrationUncertainty(calibration)
	}
	return originalUncertainty == uncertaintyLevel(reconstructed)
}

func stableCommitmentFrontMove(original, reconstructed, commitment string) bool {
	originalClauses := semanticClauses(original)
	reconstructedClauses := semanticClauses(reconstructed)
	commitmentClauses := semanticClauses(commitment)
	if len(originalClauses) == 0 ||
		len(commitmentClauses) == 0 ||
		len(commitmentClauses) > len(originalClauses) ||
		len(originalClauses) != len(reconstructedClauses) {
		return false
	}
	commitmentStart := -1
	for start := 0; start <= len(originalClauses)-len(commitmentClauses); start++ {
		matches := true
		for offset, clause := range commitmentClauses {
			if originalClauses[start+offset] != clause {
				matches = false
				break
			}
		}
		if matches {
			if commitmentStart >= 0 {
				return false
			}
			commitmentStart = start
		}
	}
	if commitmentStart < 0 {
		return false
	}
	for offset, clause := range commitmentClauses {
		if reconstructedClauses[offset] != clause {
			return false
		}
	}
	commitmentEnd := commitmentStart + len(commitmentClauses)
	reconstructedIndex := len(commitmentClauses)
	for originalIndex, clause := range originalClauses {
		if originalIndex >= commitmentStart && originalIndex < commitmentEnd {
			continue
		}
		if reconstructedClauses[reconstructedIndex] != clause {
			return false
		}
		reconstructedIndex++
	}
	return true
}

func semanticClauses(value string) []string {
	value = collapseSpace(value)
	result := make([]string, 0, 4)
	var current strings.Builder
	flush := func() {
		clause := collapseSpace(current.String())
		current.Reset()
		if clause != "" {
			result = append(result, clause)
		}
	}
	for _, currentRune := range value {
		switch currentRune {
		case '。', '、', '，', ',', '．', '.', '！', '!', '？', '?',
			'；', ';', '\n', '\r':
			flush()
		default:
			current.WriteRune(currentRune)
		}
	}
	flush()
	return result
}

func hasCausalClaim(value string) bool {
	for _, marker := range []string{
		"から", "ため", "ので", "原因", "理由", "により", "によって",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func polarityClass(value string) int {
	negative := containsAny(value, []string{
		"いいえ", "反対", "できない", "できません", "不可能", "しない",
		"しません", "行かない", "行きません", "参加しない", "ありません",
		"不要です", "却下", "中止します", "禁止します", "断定できません",
	})
	affirmative := containsAny(value, []string{
		"はい", "賛成", "できます", "できる", "採用します", "採用する",
		"実施します", "実施する", "行きます", "参加します", "必要です",
		"有効です", "改善します", "改善する",
	})
	switch {
	case negative && affirmative:
		return 2
	case negative:
		return -1
	case affirmative:
		return 1
	default:
		return 0
	}
}

func protectedFacts(value string) map[string]struct{} {
	normalized := normalizeDigits(value)
	result := make(map[string]struct{})
	for _, pattern := range []*regexp.Regexp{
		arabicNumberPattern,
		kanjiNumberWithUnitPattern,
		latinAnchorPattern,
		labelAnchorPattern,
	} {
		for _, match := range pattern.FindAllString(normalized, -1) {
			result[match] = struct{}{}
		}
	}
	for _, match := range quotedAnchorPattern.FindAllStringSubmatch(normalized, -1) {
		if len(match) >= 2 {
			result["quoted:"+match[1]] = struct{}{}
		}
	}
	return result
}

func extractClauses(pattern *regexp.Regexp, value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, match := range pattern.FindAllStringSubmatch(value, -1) {
		if len(match) >= 2 {
			result[collapseSpace(match[1])] = struct{}{}
		}
	}
	return result
}

func sameStringSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, exists := right[value]; !exists {
			return false
		}
	}
	return true
}

func containsAny(value string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func normalizeDigits(value string) string {
	return strings.NewReplacer(
		"０", "0", "１", "1", "２", "2", "３", "3", "４", "4",
		"５", "5", "６", "6", "７", "7", "８", "8", "９", "9", "．", ".",
	).Replace(value)
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
		SlotPurpose, SlotPosition, SlotUnit, SlotCondition, SlotUncertainty,
		SlotScope:
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
