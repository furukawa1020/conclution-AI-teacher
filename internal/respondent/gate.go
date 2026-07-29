package respondent

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxSubjectRunes      = 240
	maxAnswerRunes       = 8_000
	maxEvidenceRunes     = 320
	maxProtectedRunes    = 160
	maxRequiredSlots     = 8
	maxProtectedSpans    = 16
	maxSlotEvidenceItems = 8
)

var (
	numberPattern = regexp.MustCompile(
		`(?:[+-]?[0-9０-９]+(?:[,，][0-9０-９]{3})*(?:[.．][0-9０-９]+)?(?:\s*(?:円|人|件|日|時間|分|秒|年|月|週|個|回|倍|%|％|GB|MB|KB|キロ|メートル|cm|mm))?|[一二三四五六七八九十百千万億兆]+\s*(?:円|人|件|日|時間|分|秒|年|月|週|個|回|倍))`,
	)
	properContentPattern = regexp.MustCompile(
		`「[^」\r\n]{1,80}」|『[^』\r\n]{1,80}』|"[^"\r\n]{1,80}"|[A-Za-zＡ-Ｚａ-ｚ][A-Za-zＡ-Ｚａ-ｚ0-9０-９._-]{0,31}(?:案|社|方式|モデル|版|規格)?|[ァ-ヶー]{3,}`,
	)
)

var negationMarkers = []string{
	"断定できません",
	"わかりません",
	"分かりません",
	"ではありません",
	"じゃありません",
	"できません",
	"しません",
	"ありません",
	"ではない",
	"じゃない",
	"できない",
	"しない",
	"不可能",
	"反対",
	"禁止",
	"不要",
	"却下",
	"中止",
	"ない",
}

var conditionMarkers = []string{
	"であれば",
	"の場合",
	"時だけ",
	"を除き",
	"を除く",
	"前提で",
	"条件で",
	"ただし",
	"なら",
	"とき",
	"限り",
}

var uncertaintyMarkers = []string{
	"わかりません",
	"分かりません",
	"判断できません",
	"確認できません",
	"情報がありません",
	"断定できません",
	"確信がありません",
	"不確実",
	"未確認",
	"かもしれ",
	"可能性",
	"と思います",
	"おそらく",
	"たぶん",
	"推定",
	"見込み",
	"約",
	"くらい",
	"程度",
}

// Gate validates a proposed answer-first reconstruction without generating or
// inferring missing content. Malformed contracts and meaning-changing
// reconstructions fail closed with OutcomeReject.
func Gate(input Input) Assessment {
	assessment := Assessment{
		Outcome:                    OutcomeReject,
		OriginalCommitmentPosition: PositionAbsent,
		CommitmentPosition:         PositionAbsent,
		Issues:                     []Issue{},
	}

	targetSlot, evidence, valid := validateInput(input)
	if !valid {
		assessment.Issues = []Issue{IssueInvalidContract}
		return assessment
	}

	originalText := collapseSpace(input.Attempt.Text)
	candidateText := collapseSpace(input.Reconstruction)
	effectiveText := originalText
	if candidateText != "" {
		effectiveText = candidateText
	}

	assessment.OriginalTargetCoverage = slotCoverage(
		input.Frame.RequiredSlots,
		evidence,
		originalText,
	)
	assessment.TargetCoverage = slotCoverage(
		input.Frame.RequiredSlots,
		evidence,
		effectiveText,
	)
	assessment.OriginalCommitmentPosition = commitmentPosition(
		originalText,
		evidence[targetSlot],
	)
	assessment.CommitmentPosition = commitmentPosition(
		effectiveText,
		evidence[targetSlot],
	)
	assessment.MeaningPreserved = candidateText == ""

	if candidateText != "" {
		assessment.Issues = preservationIssues(
			originalText,
			candidateText,
			input.Attempt.ProtectedSpans,
		)
		assessment.MeaningPreserved = len(assessment.Issues) == 0
		if !assessment.MeaningPreserved {
			assessment.Outcome = OutcomeReject
			return assessment
		}
	}

	if input.Frame.Ambiguous {
		assessment.Outcome = OutcomeClarify
		assessment.Issues = appendIssue(
			assessment.Issues,
			IssueAmbiguousQuestion,
		)
		return assessment
	}

	if assessment.OriginalTargetCoverage < 1 {
		assessment.Outcome = OutcomeClarify
		assessment.Issues = appendIssue(
			assessment.Issues,
			IssueRequiredSlotMissing,
		)
		if assessment.OriginalCommitmentPosition == PositionAbsent {
			assessment.Issues = appendIssue(
				assessment.Issues,
				IssueCommitmentAbsent,
			)
		}
		return assessment
	}

	if assessment.TargetCoverage < 1 {
		assessment.Outcome = OutcomeReject
		assessment.Issues = appendIssue(
			assessment.Issues,
			IssueRequiredSlotLost,
		)
		return assessment
	}

	if assessment.CommitmentPosition == PositionAbsent {
		assessment.Outcome = OutcomeReject
		assessment.Issues = appendIssue(
			assessment.Issues,
			IssueCommitmentAbsent,
		)
		return assessment
	}

	if assessment.CommitmentPosition != PositionFirst {
		if candidateText == "" {
			assessment.Outcome = OutcomeClarify
			assessment.Issues = appendIssue(
				assessment.Issues,
				IssueReconstructionNeeded,
			)
		} else {
			assessment.Outcome = OutcomeReject
			assessment.Issues = appendIssue(
				assessment.Issues,
				IssueCommitmentNotFirst,
			)
		}
		return assessment
	}

	assessment.TargetSatisfied = true
	if candidateText == "" ||
		(sameClauseOrder(originalText, candidateText) &&
			assessment.OriginalCommitmentPosition == PositionFirst) {
		assessment.Outcome = OutcomeKeep
		return assessment
	}
	assessment.Outcome = OutcomeRestructure
	return assessment
}

func validateInput(input Input) (Slot, map[Slot]string, bool) {
	target, ok := TargetSlot(input.Frame.Operator)
	if !ok ||
		!boundedText(input.Frame.Subject, maxSubjectRunes, true) ||
		!boundedText(input.Attempt.Text, maxAnswerRunes, true) ||
		!boundedText(input.Reconstruction, maxAnswerRunes, false) ||
		len(input.Frame.RequiredSlots) == 0 ||
		len(input.Frame.RequiredSlots) > maxRequiredSlots ||
		len(input.Attempt.SlotEvidence) > maxSlotEvidenceItems ||
		len(input.Attempt.ProtectedSpans) > maxProtectedSpans {
		return "", nil, false
	}

	required := make(map[Slot]struct{}, len(input.Frame.RequiredSlots))
	for _, slot := range input.Frame.RequiredSlots {
		if !validSlot(slot) {
			return "", nil, false
		}
		if _, duplicate := required[slot]; duplicate {
			return "", nil, false
		}
		required[slot] = struct{}{}
	}
	if _, exists := required[target]; !exists {
		return "", nil, false
	}

	originalText := collapseSpace(input.Attempt.Text)
	evidence := make(map[Slot]string, len(input.Attempt.SlotEvidence))
	for _, item := range input.Attempt.SlotEvidence {
		span := collapseSpace(item.Span)
		if _, requiredSlot := required[item.Slot]; !requiredSlot ||
			!boundedText(span, maxEvidenceRunes, true) ||
			len(semanticClauses(span)) != 1 ||
			!strings.Contains(originalText, span) {
			return "", nil, false
		}
		if _, duplicate := evidence[item.Slot]; duplicate {
			return "", nil, false
		}
		evidence[item.Slot] = span
	}

	seenProtected := make(map[string]struct{}, len(input.Attempt.ProtectedSpans))
	for _, value := range input.Attempt.ProtectedSpans {
		value = collapseSpace(value)
		if !boundedText(value, maxProtectedRunes, true) ||
			!strings.Contains(originalText, value) {
			return "", nil, false
		}
		if _, duplicate := seenProtected[value]; duplicate {
			return "", nil, false
		}
		seenProtected[value] = struct{}{}
	}
	return target, evidence, true
}

func validSlot(slot Slot) bool {
	switch slot {
	case SlotPolarity, SlotSelection, SlotQuantity, SlotState, SlotCause,
		SlotPurpose, SlotProcedure, SlotDefinition, SlotComparison,
		SlotEvidence, SlotPosition, SlotUnit, SlotCondition, SlotUncertainty,
		SlotScope:
		return true
	default:
		return false
	}
}

func slotCoverage(required []Slot, evidence map[Slot]string, text string) float64 {
	filled := 0
	for _, slot := range required {
		if span := evidence[slot]; span != "" && strings.Contains(text, span) {
			filled++
		}
	}
	return float64(filled) / float64(len(required))
}

func commitmentPosition(text, targetEvidence string) CommitmentPosition {
	if targetEvidence == "" {
		return PositionAbsent
	}
	clauses := semanticClauses(text)
	for index, clause := range clauses {
		if strings.Contains(clause, targetEvidence) {
			if index == 0 {
				return PositionFirst
			}
			return PositionLater
		}
	}
	return PositionAbsent
}

func preservationIssues(original, candidate string, protected []string) []Issue {
	issues := make([]Issue, 0, 7)
	if !equalBag(numberBag(original), numberBag(candidate)) {
		issues = appendIssue(issues, IssueNumberChanged)
	}
	if !equalBag(markerBag(original, negationMarkers), markerBag(candidate, negationMarkers)) {
		issues = appendIssue(issues, IssueNegationChanged)
	}
	if !equalBag(conditionBag(original), conditionBag(candidate)) {
		issues = appendIssue(issues, IssueConditionChanged)
	}
	if !equalBag(
		markerBag(original, uncertaintyMarkers),
		markerBag(candidate, uncertaintyMarkers),
	) {
		issues = appendIssue(issues, IssueUncertaintyChanged)
	}
	if !equalBag(properContentBag(original), properContentBag(candidate)) {
		issues = appendIssue(issues, IssueProperContentChanged)
	}
	for _, value := range protected {
		value = collapseSpace(value)
		if strings.Count(original, value) != strings.Count(candidate, value) {
			issues = appendIssue(issues, IssueProtectedSpanChanged)
			break
		}
	}
	if !equalBag(clauseBag(original), clauseBag(candidate)) {
		issues = appendIssue(issues, IssueContentChanged)
	}
	return issues
}

func numberBag(value string) map[string]int {
	result := make(map[string]int)
	for _, token := range numberPattern.FindAllString(value, -1) {
		token = normalizeDigits(token)
		token = strings.NewReplacer(
			",", "",
			"，", "",
			" ", "",
			"\t", "",
			"％", "%",
		).Replace(token)
		result[token]++
	}
	return result
}

func properContentBag(value string) map[string]int {
	result := make(map[string]int)
	for _, token := range properContentPattern.FindAllString(value, -1) {
		result[collapseSpace(token)]++
	}
	return result
}

func markerBag(value string, markers []string) map[string]int {
	result := make(map[string]int)
	for _, marker := range markers {
		if count := strings.Count(value, marker); count > 0 {
			result[marker] = count
		}
	}
	return result
}

func conditionBag(value string) map[string]int {
	result := make(map[string]int)
	for _, clause := range semanticClauses(value) {
		for _, marker := range conditionMarkers {
			if strings.Contains(clause, marker) {
				result[clause]++
				break
			}
		}
	}
	return result
}

func clauseBag(value string) map[string]int {
	result := make(map[string]int)
	for _, clause := range semanticClauses(value) {
		result[clause]++
	}
	return result
}

func equalBag(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for value, count := range left {
		if right[value] != count {
			return false
		}
	}
	return true
}

func sameClauseOrder(left, right string) bool {
	leftClauses := semanticClauses(left)
	rightClauses := semanticClauses(right)
	if len(leftClauses) != len(rightClauses) {
		return false
	}
	for index := range leftClauses {
		if leftClauses[index] != rightClauses[index] {
			return false
		}
	}
	return true
}

func semanticClauses(value string) []string {
	value = collapseSpace(value)
	result := make([]string, 0, 4)
	var current strings.Builder
	flush := func() {
		clause := strings.TrimSpace(current.String())
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

func appendIssue(issues []Issue, issue Issue) []Issue {
	for _, existing := range issues {
		if existing == issue {
			return issues
		}
	}
	return append(issues, issue)
}

func boundedText(value string, maximum int, required bool) bool {
	if !utf8.ValidString(value) {
		return false
	}
	value = collapseSpace(value)
	if required && value == "" {
		return false
	}
	return utf8.RuneCountInString(value) <= maximum
}

func collapseSpace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizeDigits(value string) string {
	return strings.NewReplacer(
		"０", "0",
		"１", "1",
		"２", "2",
		"３", "3",
		"４", "4",
		"５", "5",
		"６", "6",
		"７", "7",
		"８", "8",
		"９", "9",
		"．", ".",
	).Replace(value)
}
