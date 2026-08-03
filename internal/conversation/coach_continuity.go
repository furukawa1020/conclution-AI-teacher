package conversation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
	"golang.org/x/text/unicode/norm"
)

// These expressions are deliberately narrow. Continuity decisions fail closed
// when a current utterance cannot be linked to one exact question and one
// personally-owned target answer.
var (
	coachDecimalAnchorPattern = regexp.MustCompile(
		`^[+-]?[0-9]+(?:\.[0-9]+)?$`,
	)
	coachGenericReporterPattern = regexp.MustCompile(
		`と[\s、,：:]*([^、,。.!！?？;；「」『』【】\[\]\(\)\r\n]+?)[\s、,：:]*` +
			`(?:が|は|も|こそ)`,
	)
)

func authoritativeCoachCommitmentPosition(
	plan modelPlan,
	authoritativeAttempt string,
) respondent.CommitmentPosition {
	target, ok := answercontract.TargetSlot(
		plan.AnswerContract.QuestionFrame.Operator,
	)
	if !ok {
		return respondent.PositionAbsent
	}
	targetSpan := ""
	for _, evidence := range plan.RespondentEvidence {
		if evidence.Slot == target {
			if targetSpan != "" {
				return respondent.PositionAbsent
			}
			targetSpan = evidence.Span
		}
	}
	anchor := strings.ToLower(collapseSpace(norm.NFKC.String(targetSpan)))
	anchor = strings.Trim(anchor, " \t\r\n。．.!！?？、,")
	source := strings.ToLower(
		collapseSpace(norm.NFKC.String(authoritativeAttempt)),
	)
	sourceRunes := []rune(source)
	anchorRunes := []rune(anchor)
	if anchor == "" || len(anchorRunes) > len(sourceRunes) ||
		coachAnswerRetractsAnchor(source, anchor) {
		return respondent.PositionAbsent
	}
	for start := 0; start+len(anchorRunes) <= len(sourceRunes); start++ {
		end := start + len(anchorRunes)
		if !verifiedCoachAssertionAtForSubject(
			sourceRunes,
			anchor,
			anchorRunes,
			start,
			end,
			true,
			plan.AnswerContract.QuestionFrame.Subject,
		) {
			continue
		}
		if coachSubstantiveClausesBefore(
			sourceRunes[:start],
			plan.AnswerContract.QuestionFrame.Subject,
		) > 0 ||
			coachGenericAnswerRelationHasSubstantivePrefix(
				string(sourceRunes[:start]),
				plan.AnswerContract.QuestionFrame.Subject,
			) {
			return respondent.PositionLater
		}
		return respondent.PositionFirst
	}
	return respondent.PositionAbsent
}

func coachProofSpanBound(plan modelPlan, authoritativeAttempt string) bool {
	anchor, _, ok := boundedCoachAnswerFingerprint(
		plan,
		authoritativeAttempt,
		false,
		false,
	)
	source := strings.ToLower(collapseSpace(
		norm.NFKC.String(authoritativeAttempt),
	))
	if !ok || source == "" ||
		utf8.RuneCountInString(anchor)*100 >
			utf8.RuneCountInString(source)*60 {
		return false
	}
	return authoritativeCoachCommitmentPosition(
		plan,
		authoritativeAttempt,
	) == respondent.PositionFirst
}

func coachSubstantiveClausesBefore(prefix []rune, subject string) int {
	count := 0
	var clause strings.Builder
	flush := func() {
		value := strings.TrimSpace(clause.String())
		clause.Reset()
		if value != "" &&
			!coachClauseIsOnlyAnswerLead(value, subject) &&
			substantiveCoachAttempt(value) {
			count++
		}
	}
	for _, current := range prefix {
		switch current {
		case '。', '、', '，', ',', '．', '.', '！', '!', '？', '?',
			'；', ';', '\n', '\r':
			flush()
		default:
			clause.WriteRune(current)
		}
	}
	// The unfinished clause immediately containing A is handled separately;
	// only completed clauses before it establish a background-first answer.
	return count
}

func coachClauseIsOnlyAnswerLead(value string, subject string) bool {
	value = strings.TrimSpace(value)
	if value == "" ||
		coachPrefixIsOnlyFiller(value) ||
		coachPrefixEndsWithSelfTopic(value) {
		return true
	}
	lead, ok := coachStripAnswerRelationPrefix(value, subject)
	return ok && coachQuestionSubjectLocalPrefixAllowed(lead)
}

func coachGenericAnswerRelationHasSubstantivePrefix(
	prefix string,
	subject string,
) bool {
	prefix = strings.TrimRight(prefix, " \t\r\n、,：:")
	lastBoundary := -1
	lastWidth := 0
	for _, boundary := range []string{
		"。", "、", "，", ",", "．", ".", "！", "!", "？", "?",
		"；", ";", "\n", "\r",
	} {
		if position := strings.LastIndex(prefix, boundary); position > lastBoundary {
			lastBoundary = position
			lastWidth = len(boundary)
		}
	}
	local := prefix
	if lastBoundary >= 0 {
		local = prefix[lastBoundary+lastWidth:]
	}
	normalizedSubject := strings.ToLower(
		collapseSpace(norm.NFKC.String(subject)),
	)
	if normalizedSubject != "" {
		for _, relation := range []string{
			"についての答えは", "に関する答えは",
			"ことについての答えは", "についてですが",
			"に関してですが", "のことですが", "ことは",
			"については", "に関しては", "について",
			"に関して", "としては", "への答えは",
			"の答えは", "は",
		} {
			needle := normalizedSubject + relation
			if !strings.HasSuffix(local, needle) {
				continue
			}
			before := strings.TrimRight(
				strings.TrimSuffix(local, needle),
				" \t\r\n、,：:",
			)
			return !coachQuestionSubjectLocalPrefixAllowed(before)
		}
	}
	for _, relation := range []string{
		"自分の答えは", "私の答えは", "答えは", "回答は", "結論は",
		"私たちは", "わたしたちは", "我々は", "われわれは",
		"私は", "わたしは", "自分は", "僕は", "ぼくは",
		"俺は", "おれは",
		"my answer is", "i answer", "i would answer",
	} {
		if !strings.HasSuffix(local, relation) {
			continue
		}
		before := strings.TrimRight(
			strings.TrimSuffix(local, relation),
			" \t\r\n、,：:",
		)
		return !coachQuestionSubjectLocalPrefixAllowed(before)
	}
	return false
}

func deriveCoachContinuityKey(rootKey []byte) []byte {
	mac := hmac.New(sha256.New, rootKey)
	_, _ = mac.Write([]byte("kotae-coach-continuity-v1\x00"))
	return mac.Sum(nil)
}

func (agent *vertexAgent) coachContinuityTag(anchor string) string {
	if agent == nil || len(agent.continuityKey) != sha256.Size || anchor == "" {
		return ""
	}
	mac := hmac.New(sha256.New, agent.continuityKey)
	_, _ = mac.Write([]byte("required-answer-evidence-v2\x00"))
	_, _ = mac.Write([]byte(anchor))
	full := mac.Sum(nil)
	tag := base64.RawURLEncoding.EncodeToString(full[:coachContinuityTagBytes])
	wipe(full)
	return tag
}

func (agent *vertexAgent) coachQuestionContinuityTag(anchor string) string {
	if agent == nil || len(agent.continuityKey) != sha256.Size || anchor == "" {
		return ""
	}
	mac := hmac.New(sha256.New, agent.continuityKey)
	_, _ = mac.Write([]byte("reported-question-subject-anchor\x00"))
	_, _ = mac.Write([]byte(anchor))
	full := mac.Sum(nil)
	tag := base64.RawURLEncoding.EncodeToString(full[:coachContinuityTagBytes])
	wipe(full)
	return tag
}

func (agent *vertexAgent) coachQuestionInstanceTag(
	sessionID string,
	anchor string,
) string {
	if agent == nil || len(agent.continuityKey) != sha256.Size ||
		!validSessionID(sessionID) || anchor == "" {
		return ""
	}
	mac := hmac.New(sha256.New, agent.continuityKey)
	_, _ = mac.Write([]byte("reported-question-instance-anchor-v1\x00"))
	_, _ = mac.Write([]byte(sessionID))
	_, _ = mac.Write([]byte("\x00"))
	_, _ = mac.Write([]byte(anchor))
	full := mac.Sum(nil)
	tag := base64.RawURLEncoding.EncodeToString(full[:coachContinuityTagBytes])
	wipe(full)
	return tag
}

func (agent *vertexAgent) nativeCoachScopeTag(scopeID string) string {
	if agent == nil || len(agent.continuityKey) != sha256.Size || scopeID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, agent.continuityKey)
	_, _ = mac.Write([]byte("native-explicit-coach-scope-v1\x00"))
	_, _ = mac.Write([]byte(scopeID))
	full := mac.Sum(nil)
	tag := base64.RawURLEncoding.EncodeToString(full[:coachContinuityTagBytes])
	wipe(full)
	return tag
}

// utteranceLinksCoachQuestionTag proves that the current turn uses the same
// bounded question subject as the grammatical topic of the exact target A.
// It does not accept a mere mention of the subject. Candidate work is bounded
// to a small number of fixed surface relations and 24-rune subject suffixes.
func (agent *vertexAgent) utteranceLinksCoachQuestionTag(
	tag string,
	answerAnchor string,
	utterance string,
) (string, bool) {
	if agent == nil || tag == "" || answerAnchor == "" || utterance == "" {
		return "", false
	}
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	answerAnchor = strings.ToLower(collapseSpace(norm.NFKC.String(answerAnchor)))
	const maxRelations = 32
	relationsSeen := 0
	for _, relation := range []string{
		"についての答えは",
		"に関する答えは",
		"ことについての答えは",
		"についてですが",
		"に関してですが",
		"のことですが",
		"ことは",
		"については",
		"に関しては",
		"について",
		"に関して",
		"としては",
		"への答えは",
		"の答えは",
		"は",
	} {
		searchFrom := 0
		for relationsSeen < maxRelations && searchFrom < len(source) {
			relative := strings.Index(source[searchFrom:], relation)
			if relative < 0 {
				break
			}
			relationStart := searchFrom + relative
			relationEnd := relationStart + len(relation)
			relationsSeen++
			searchFrom = relationEnd
			if coachTextPositionInsideQuote(source, relationStart) {
				continue
			}

			after := strings.TrimLeft(
				source[relationEnd:],
				" \t\r\n、,：:",
			)
			negated := false
			for _, prefix := range []string{
				"関係なく", "無関係", "対象外", "さておき",
				"ではなく", "じゃなく", "でなく",
			} {
				if strings.HasPrefix(after, prefix) {
					negated = true
					break
				}
			}
			if negated || !hasExactCoachAnchorPrefix(after, answerAnchor) {
				continue
			}

			before := []rune(source[:relationStart])
			first := len(before) - 24
			if first < 0 {
				first = 0
			}
			for start := first; start < len(before); start++ {
				candidate := collapseSpace(string(before[start:]))
				candidateStart := len(string(before[:start]))
				candidateRunes := []rune(candidate)
				if len(candidateRunes) == 0 ||
					len(candidateRunes) > 24 ||
					len(strings.Fields(candidate)) > 4 {
					continue
				}
				validCandidate := true
				for _, current := range candidateRunes {
					if !unicode.IsLetter(current) &&
						!unicode.IsNumber(current) &&
						!unicode.IsSpace(current) &&
						current != '-' {
						validCandidate = false
						break
					}
				}
				if validCandidate &&
					hmac.Equal(
						[]byte(tag),
						[]byte(agent.coachQuestionContinuityTag(candidate)),
					) &&
					coachQuestionRelationStartsAuthoritativeAnswer(
						source,
						candidate,
						candidateStart,
						relationStart,
						relation,
					) {
					return candidate, true
				}
			}
		}
	}
	return "", false
}

func coachQuestionRelationStartsAuthoritativeAnswer(
	source string,
	subject string,
	subjectStart int,
	relationStart int,
	relation string,
) bool {
	if subject == "" || relation == "" ||
		subjectStart < 0 || relationStart <= subjectStart ||
		relationStart+len(relation) > len(source) ||
		source[relationStart:relationStart+len(relation)] != relation ||
		collapseSpace(source[subjectStart:relationStart]) != subject ||
		coachTextPositionInsideQuote(source, subjectStart) ||
		coachTextPositionInsideQuote(source, relationStart) {
		return false
	}
	if coachReportedQuestionEndOutsideQuote(
		source[relationStart+len(relation):],
	) >= 0 {
		return false
	}
	if coachQuestionSubjectPrefixAllowed(source[:subjectStart]) {
		return true
	}

	// A reported-question prefix may be removed only for this exact subject
	// occurrence. Re-searching subject+relation from the beginning would let an
	// earlier first-party occurrence authorize a later third-party occurrence.
	_, reportEnd, reportOK := coachReportedQuestionSpanForSubjectBefore(
		source,
		subject,
		subjectStart,
	)
	if !reportOK {
		return false
	}
	localPrefix := strings.TrimLeft(
		source[reportEnd:subjectStart],
		" \t\r\n、,：:。.!！?？;；",
	)
	return coachQuestionSubjectLocalPrefixAllowed(localPrefix)
}

func coachQuestionSubjectPrefixAllowed(prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	if coachQuestionSubjectLocalPrefixAllowed(prefix) {
		return true
	}
	lastBoundary := -1
	lastWidth := 0
	for _, boundary := range []string{
		"。", ".", "！", "!", "？", "?", "；", ";",
	} {
		if position := strings.LastIndex(prefix, boundary); position > lastBoundary {
			lastBoundary = position
			lastWidth = len(boundary)
		}
	}
	return lastBoundary >= 0 &&
		coachQuestionSubjectLocalPrefixAllowed(
			prefix[lastBoundary+lastWidth:],
		)
}

func coachQuestionSubjectLocalPrefixAllowed(prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	for _, self := range []string{
		"私は", "わたしは", "自分は", "僕は", "ぼくは",
		"my answer is", "i answer", "i would answer",
	} {
		if strings.HasPrefix(prefix, self) {
			prefix = strings.TrimLeft(
				prefix[len(self):],
				" \t\r\n、,：:",
			)
			break
		}
	}
	return coachPrefixIsOnlyFiller(prefix)
}

func coachReportedQuestionOwnAnswerLinked(
	utterance string,
	questionSubject string,
	answerAnchor string,
) bool {
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	questionSubject = strings.ToLower(
		collapseSpace(norm.NFKC.String(questionSubject)),
	)
	answerAnchor = strings.ToLower(collapseSpace(norm.NFKC.String(answerAnchor)))
	sourceRunes := []rune(source)
	anchorRunes := []rune(answerAnchor)
	if questionSubject == "" || answerAnchor == "" ||
		len(anchorRunes) == 0 || len(anchorRunes) > len(sourceRunes) {
		return false
	}
	for start := 0; start+len(anchorRunes) <= len(sourceRunes); start++ {
		startByte := len(string(sourceRunes[:start]))
		_, reportEnd, reportOK := coachReportedQuestionSpanForSubjectBefore(
			source,
			questionSubject,
			startByte,
		)
		if !reportOK ||
			coachReportedQuestionEndOutsideQuote(
				source[reportEnd:startByte],
			) >= 0 ||
			coachReportedQuestionAnswerContextEscapes(
				source[reportEnd:startByte],
			) {
			continue
		}
		end := start + len(anchorRunes)
		endByte := len(string(sourceRunes[:end]))
		if coachReportedQuestionEndOutsideQuote(source[endByte:]) >= 0 {
			continue
		}
		if !verifiedCoachAssertionAtForSubject(
			sourceRunes,
			answerAnchor,
			anchorRunes,
			start,
			end,
			true,
			questionSubject,
		) {
			continue
		}
		clauseStart := 0
		for index := start - 1; index >= 0; index-- {
			switch sourceRunes[index] {
			case '。', '.', '！', '!', '？', '?', '；', ';':
				clauseStart = index + 1
				index = -1
			}
		}
		localPrefix := string(sourceRunes[clauseStart:start])
		if coachGenericAnswerRelationHasSubstantivePrefix(
			localPrefix,
			questionSubject,
		) {
			continue
		}
		if coachLocalAnswerRelationBefore(localPrefix) ||
			(coachAnchorFollowedByOwnAnswerReport(sourceRunes, end) &&
				coachOwnAttemptMarkerBetween(source, reportEnd, startByte)) {
			return true
		}
	}
	return false
}

func coachReportedQuestionIsLatestFocus(
	utterance string,
	questionSubject string,
) bool {
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	questionSubject = strings.ToLower(
		collapseSpace(norm.NFKC.String(questionSubject)),
	)
	_, reportEnd, ok := coachReportedQuestionSpanForSubjectBefore(
		source,
		questionSubject,
		len(source),
	)
	return ok &&
		coachReportedQuestionEndOutsideQuote(source[reportEnd:]) < 0
}

func coachReportedQuestionAnswerContextEscapes(span string) bool {
	for _, marker := range []string{
		"まだ考え", "考え中", "答えがまとまらない",
		"答えません", "答えない", "答えられない",
		"回答しません", "回答しない", "未定",
		"決めていない", "決まっていない",
		"別の話", "別件", "関係なく", "無関係",
		"さておき", "対象外",
		"still thinking", "not answer", "cannot answer",
		"can't answer", "undecided", "unrelated", "another topic",
	} {
		if strings.Contains(span, marker) {
			return true
		}
	}
	return false
}

func coachAnchorFollowedByOwnAnswerReport(
	sourceRunes []rune,
	end int,
) bool {
	if end < 0 || end > len(sourceRunes) {
		return false
	}
	remainder := strings.TrimLeft(
		string(sourceRunes[end:]),
		" \t\r\n、,：:",
	)
	for _, marker := range []string{
		"と答え", "と回答", "と伝え", "と返答",
		" is my answer", " was my answer",
	} {
		if strings.HasPrefix(remainder, marker) {
			return true
		}
	}
	return false
}

func coachOwnAttemptMarkerBetween(
	source string,
	start int,
	end int,
) bool {
	if start < 0 || end <= start || end > len(source) {
		return false
	}
	span := source[start:end]
	for _, marker := range []string{
		"私は", "わたしは", "自分は", "僕は", "ぼくは",
		"私たちは", "わたしたちは", "我々は", "われわれは",
		"私の答え", "自分の答え", "私たちの答え", "我々の答え",
		"i answer", "i answered", "i would answer",
		"my answer", "we answer", "we answered", "our answer",
	} {
		searchFrom := 0
		for searchFrom < len(span) {
			relative := strings.Index(span[searchFrom:], marker)
			if relative < 0 {
				break
			}
			position := start + searchFrom + relative
			if !coachTextPositionInsideQuote(source, position) {
				return true
			}
			searchFrom += relative + len(marker)
		}
	}
	return false
}

func coachLocalAnswerRelationBefore(prefix string) bool {
	prefix = strings.TrimRight(prefix, " \t\r\n、,：:")
	lastBoundary := -1
	lastWidth := 0
	for _, boundary := range []string{
		"。", "、", "，", ",", "．", ".", "！", "!", "？", "?",
		"；", ";", "\n", "\r",
	} {
		if position := strings.LastIndex(prefix, boundary); position > lastBoundary {
			lastBoundary = position
			lastWidth = len(boundary)
		}
	}
	local := prefix
	if lastBoundary >= 0 {
		local = prefix[lastBoundary+lastWidth:]
	}
	local = strings.TrimSpace(local)
	for _, relation := range []string{
		"自分の答えは", "私の答えは", "私たちの答えは",
		"答えは", "回答は", "結論は", "目的は", "理由は",
		"選択は", "数量は", "数字は", "状態は", "定義は",
		"違いは", "根拠は", "手順は",
		"my answer is", "our answer is",
	} {
		if !strings.HasSuffix(local, relation) {
			continue
		}
		before := strings.TrimRight(
			strings.TrimSuffix(local, relation),
			" \t\r\n、,：:",
		)
		return coachQuestionSubjectLocalPrefixAllowed(before)
	}
	return false
}

// boundedCoachContinuityAnchor is used only to authorize a new foreground
// scope whose reported question and answer share one utterance. The returned
// subject is never retained. It rejects whole-turn copies, identifiers,
// credentials, and common PII-labelled fields.
func boundedCoachContinuityAnchor(subject string, utterance string) (string, bool) {
	anchor := strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	anchorRunes := []rune(anchor)
	sourceRunes := []rune(source)
	if len(anchorRunes) < 1 || len(anchorRunes) > 24 ||
		len(sourceRunes) == 0 ||
		!containsExactReportedCoachQuestionSubject(source, anchor) ||
		len(strings.Fields(anchor)) > 4 ||
		len(anchorRunes)*100 > len(sourceRunes)*45 ||
		containsSensitiveStateText(anchor) {
		return "", false
	}
	for _, current := range anchorRunes {
		if !unicode.IsLetter(current) && !unicode.IsNumber(current) &&
			!unicode.IsSpace(current) && current != '-' {
			return "", false
		}
	}
	for _, sensitive := range []string{
		"氏名", "名前", "住所", "電話", "メール", "生年月日", "マイナンバー",
		"口座", "カード番号", "社員番号", "顧客番号", "患者番号",
		"password", "secret", "token", "api key", "account number",
		"phone", "address", "email", "full name", "date of birth",
	} {
		if strings.Contains(anchor, sensitive) {
			return "", false
		}
	}
	for _, generic := range []string{
		"この質問", "その質問", "今の質問", "質問内容", "質問の答え",
		"このこと", "そのこと", "今のこと", "答え", "回答", "内容",
		"理由", "目的", "状態", "手順", "定義", "根拠", "選択",
		"the question", "this question", "that question", "the answer",
	} {
		if anchor == generic {
			return "", false
		}
	}
	return anchor, true
}

// boundedCoachPlanQuestionAnchor screens a planner's transient question
// subject without requiring it to be repeated in the current utterance. It is
// accepted only after its HMAC matches the stored question tag; the raw value
// is never persisted. This lets a later bare answer such as "東京です" bind to
// the already authenticated question while keeping model guesses powerless.
func boundedCoachPlanQuestionAnchor(subject string) (string, bool) {
	anchor := strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	anchorRunes := []rune(anchor)
	if len(anchorRunes) < 1 || len(anchorRunes) > 24 ||
		len(strings.Fields(anchor)) > 4 ||
		containsSensitiveStateText(anchor) {
		return "", false
	}
	for _, current := range anchorRunes {
		if !unicode.IsLetter(current) && !unicode.IsNumber(current) &&
			!unicode.IsSpace(current) && current != '-' {
			return "", false
		}
	}
	for _, sensitive := range []string{
		"氏名", "名前", "住所", "電話", "メール", "生年月日", "マイナンバー",
		"口座", "カード番号", "社員番号", "顧客番号", "患者番号",
		"password", "secret", "token", "api key", "account number",
		"phone", "address", "email", "full name", "date of birth",
	} {
		if strings.Contains(anchor, sensitive) {
			return "", false
		}
	}
	for _, generic := range []string{
		"この質問", "その質問", "今の質問", "質問内容", "質問の答え",
		"このこと", "そのこと", "今のこと", "答え", "回答", "内容",
		"理由", "目的", "状態", "手順", "定義", "根拠", "選択",
		"the question", "this question", "that question", "the answer",
	} {
		if anchor == generic {
			return "", false
		}
	}
	return anchor, true
}

func boundedCoachContinuityAnchorForPlan(
	subject string,
	utterance string,
) (string, bool) {
	if anchor, ok := boundedCoachContinuityAnchor(subject, utterance); ok {
		return anchor, true
	}
	normalized := strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	runes := []rune(normalized)
	// Japanese model subjects commonly add a short domain modifier (for
	// example 導入目的) while the reported question says only 目的. Bind the
	// longest exact suffix that is itself a screened reported-question subject.
	for start := 1; start+2 <= len(runes); start++ {
		candidate := string(runes[start:])
		if anchor, ok := boundedCoachContinuityAnchor(candidate, utterance); ok {
			return anchor, true
		}
	}
	return "", false
}

func boundedReportedCoachQuestionInstanceAnchor(
	utterance string,
	questionSubject string,
) (string, bool) {
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	questionSubject = strings.ToLower(
		collapseSpace(norm.NFKC.String(questionSubject)),
	)
	start, end, ok := coachReportedQuestionSpanForSubjectBefore(
		source,
		questionSubject,
		len(source),
	)
	if !ok || start < 0 || end <= start || end > len(source) ||
		coachReportedQuestionEndOutsideQuote(source[end:]) >= 0 {
		return "", false
	}
	anchor := strings.TrimSpace(source[start:end])
	if anchor == "" || utf8.RuneCountInString(anchor) > 320 {
		return "", false
	}
	// The raw reported-question span is used only as keyed HMAC input. It is
	// never stored, logged, spoken, or sent back to either model.
	return anchor, true
}

func boundedReportedCoachQuestionOperator(
	questionSpan string,
) (answercontract.Operator, bool) {
	questionSpan = strings.ToLower(
		collapseSpace(norm.NFKC.String(questionSpan)),
	)
	if questionSpan == "" {
		return "", false
	}
	containsAny := func(signals ...string) bool {
		for _, signal := range signals {
			if strings.Contains(questionSpan, signal) {
				return true
			}
		}
		return false
	}
	candidates := make([]answercontract.Operator, 0, 2)
	add := func(operator answercontract.Operator, matched bool) {
		if matched {
			candidates = append(candidates, operator)
		}
	}
	add(answercontract.OperatorChoice,
		containsAny("どちら", "どっち", "どれ", "どの案", "which"))
	add(answercontract.OperatorQuantity,
		containsAny("いくつ", "何個", "何人", "何件", "何回", "何日", "何時間", "どのくらい", "どれくらい", "how many", "how much"))
	add(answercontract.OperatorPurpose,
		containsAny("何のため", "目的", "what for"))
	add(answercontract.OperatorCause,
		containsAny("なぜ", "どうして", "理由", "原因", "why"))
	add(answercontract.OperatorProcedure,
		containsAny("どうやって", "どのように", "手順", "進め方", "how do", "how should"))
	add(answercontract.OperatorComparison,
		containsAny("違い", "比べ", "比較", "difference", "compare"))
	add(answercontract.OperatorEvidence,
		containsAny("根拠", "証拠", "エビデンス", "evidence"))
	add(answercontract.OperatorState,
		containsAny("どうなって", "どんな状態", "状況", "状態は", "現在の状態", "現在の状況"))
	add(answercontract.OperatorDefinition,
		containsAny("とは", "定義", "何ですか", "what is"))
	add(answercontract.OperatorOpen,
		containsAny("何を", "何が", "誰", "いつ", "どこ", "どう考え", "どう思", "教えて", "what", "who", "when", "where"))
	add(answercontract.OperatorBoolean,
		containsAny("できますか", "できるか", "ありますか", "あるか", "しますか", "するか", "でしょうか", "ですか", "かどうか", "can you", "do you", "is it", "are you"))
	if len(candidates) != 1 {
		return "", false
	}
	return candidates[0], true
}

func reportedCoachQuestionPresent(utterance string) bool {
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	end := coachReportedQuestionEndOutsideQuote(source)
	return end > 0 && end <= len(source)
}

func boundedCoachPlanQuestionAnchors(subject string) []string {
	normalized := strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	runes := []rune(normalized)
	result := make([]string, 0, len(runes))
	for start := 0; start+2 <= len(runes); start++ {
		if candidate, ok := boundedCoachPlanQuestionAnchor(
			string(runes[start:]),
		); ok {
			result = append(result, candidate)
		}
	}
	return result
}

func containsExactReportedCoachQuestionSubject(
	source string,
	anchor string,
) bool {
	_, ok := exactReportedCoachQuestionSubjectStart(source, anchor)
	return ok
}

func exactReportedCoachQuestionSubjectStart(
	source string,
	anchor string,
) (int, bool) {
	sourceRunes := []rune(source)
	anchorRunes := []rune(anchor)
	if len(anchorRunes) == 0 || len(anchorRunes) > len(sourceRunes) {
		return -1, false
	}
	const maxOccurrences = 32
	seen := 0
	for start := 0; start+len(anchorRunes) <= len(sourceRunes); start++ {
		if string(sourceRunes[start:start+len(anchorRunes)]) != anchor {
			continue
		}
		seen++
		if seen > maxOccurrences {
			return -1, false
		}
		startByte := len(string(sourceRunes[:start]))
		if !exactCoachQuestionSubjectAt(
			sourceRunes,
			anchorRunes,
			start,
		) && (!coachReportedQuestionSubjectLeadAllowed(source[:startByte]) ||
			!coachQuestionSubjectRightBoundedAt(
				sourceRunes,
				anchorRunes,
				start,
			)) {
			continue
		}
		end := start + len(anchorRunes)
		windowEnd := end + 96
		if windowEnd > len(sourceRunes) {
			windowEnd = len(sourceRunes)
		}
		tail := string(sourceRunes[end:windowEnd])
		directTail := strings.TrimLeft(
			tail,
			" \t\r\n」』”’】〉》）)］]〕、,：:",
		)
		if !coachRunePositionInsideQuote(sourceRunes, start) {
			for _, relation := range []string{
				"を聞かれ", "について聞かれ", "に関して聞かれ",
				"のことを聞かれ", "を質問され", "について質問され",
				"に関して質問され", "を尋ねられ", "について尋ねられ",
				"に関して尋ねられ", "のことを尋ねられ",
				"を問われ", "について問われ", "に関して問われ",
				"のことを問われ", "について質問を受け",
				"に関して質問を受け", "についての質問を受け",
				"という質問を受け", "との質問を受け", "の質問を受け",
			} {
				if strings.HasPrefix(directTail, relation) {
					return len(string(sourceRunes[:start])), true
				}
			}
		}
		reportIndex := -1
		for _, marker := range []string{
			"聞かれ", "質問され", "尋ねられ", "問われ", "質問を受け",
		} {
			searchFrom := 0
			for searchFrom < len(tail) {
				relative := strings.Index(tail[searchFrom:], marker)
				if relative < 0 {
					break
				}
				index := searchFrom + relative
				position := end +
					utf8.RuneCountInString(tail[:index])
				if !coachRunePositionInsideQuote(sourceRunes, position) &&
					(reportIndex < 0 || index < reportIndex) {
					reportIndex = index
					break
				}
				searchFrom = index + len(marker)
			}
		}
		if reportIndex >= 0 &&
			utf8.RuneCountInString(tail[:reportIndex]) <= 32 {
			localStart := start - 16
			if localStart < 0 {
				localStart = 0
			}
			for index := start - 1; index >= localStart; index-- {
				switch sourceRunes[index] {
				case '。', '.', '！', '!', '？', '?', '；', ';':
					localStart = index + 1
					index = localStart - 1
				}
			}
			localQuestion := string(sourceRunes[localStart:end]) +
				tail[:reportIndex]
			questionShaped := false
			for _, marker := range []string{
				"何", "なに", "どこ", "いつ", "誰", "なぜ", "どう",
				"どれ", "ですか", "ますか", "か?", "か？", "?", "？",
			} {
				if strings.Contains(localQuestion, marker) {
					questionShaped = true
					break
				}
			}
			metaBoundary := false
			for _, forbidden := range []string{
				"。", "!", "！", "と書いて", "とある", "記載",
				"引用", "述べ", "指示", "だけ", "別のこと",
			} {
				if strings.Contains(localQuestion, forbidden) {
					metaBoundary = true
					break
				}
			}
			if questionShaped && !metaBoundary {
				return len(string(sourceRunes[:start])), true
			}
		}
	}
	return -1, false
}

func coachReportedQuestionSpanForSubjectBefore(
	source string,
	subject string,
	before int,
) (int, int, bool) {
	if source == "" || subject == "" ||
		before < 0 || before > len(source) {
		return -1, -1, false
	}
	sourceRunes := []rune(source)
	subjectRunes := []rune(subject)
	if len(subjectRunes) == 0 || len(subjectRunes) > len(sourceRunes) {
		return -1, -1, false
	}
	bestStart := -1
	bestEnd := -1
	const maxOccurrences = 32
	seen := 0
	for start := 0; start+len(subjectRunes) <= len(sourceRunes); start++ {
		if string(sourceRunes[start:start+len(subjectRunes)]) != subject {
			continue
		}
		seen++
		if seen > maxOccurrences {
			return -1, -1, false
		}
		startByte := len(string(sourceRunes[:start]))
		if startByte >= before {
			continue
		}
		exactOccurrence := exactCoachQuestionSubjectAt(
			sourceRunes,
			subjectRunes,
			start,
		)
		if !exactOccurrence &&
			(!coachReportedQuestionSubjectLeadAllowed(source[:startByte]) ||
				!coachQuestionSubjectRightBoundedAt(
					sourceRunes,
					subjectRunes,
					start,
				)) {
			continue
		}
		if coachRunePositionInsideQuote(
			sourceRunes,
			start,
		) {
			continue
		}
		windowEndRune := start + len(subjectRunes) + 128
		if windowEndRune > len(sourceRunes) {
			windowEndRune = len(sourceRunes)
		}
		windowEnd := len(string(sourceRunes[:windowEndRune]))
		if windowEnd > before {
			windowEnd = before
		}
		contextStartRune := start - 16
		if contextStartRune < 0 {
			contextStartRune = 0
		}
		for index := start - 1; index >= contextStartRune; index-- {
			switch sourceRunes[index] {
			case '。', '.', '！', '!', '？', '?', '；', ';':
				contextStartRune = index + 1
				index = contextStartRune - 1
			}
		}
		contextStart := len(string(sourceRunes[:contextStartRune]))
		window := source[contextStart:windowEnd]
		localStart, localOK := exactReportedCoachQuestionSubjectStart(
			window,
			subject,
		)
		if !localOK || localStart != startByte-contextStart {
			continue
		}
		localEnd := coachReportedQuestionEndOutsideQuote(
			source[startByte:windowEnd],
		)
		if localEnd <= 0 {
			continue
		}
		reportEnd := startByte + localEnd
		if reportEnd <= before && reportEnd > bestEnd {
			bestStart = startByte
			bestEnd = reportEnd
		}
	}
	return bestStart, bestEnd, bestEnd >= 0
}

func exactCoachQuestionSubjectAt(
	sourceRunes []rune,
	anchorRunes []rune,
	start int,
) bool {
	if len(anchorRunes) == 0 || start < 0 ||
		start+len(anchorRunes) > len(sourceRunes) ||
		string(sourceRunes[start:start+len(anchorRunes)]) !=
			string(anchorRunes) {
		return false
	}
	leftBounded := start == 0 ||
		!unicode.IsLetter(sourceRunes[start-1]) &&
			!unicode.IsNumber(sourceRunes[start-1])
	if !leftBounded {
		switch sourceRunes[start-1] {
		case 'は', 'が', 'を', 'に', 'と', 'で', 'も', 'へ', 'の':
			localStart := start - 12
			if localStart < 0 {
				localStart = 0
			}
			localPrefix := string(sourceRunes[localStart:start])
			for _, marker := range []string{
				"何", "なに", "どこ", "いつ", "誰", "なぜ",
				"どう", "どれ", "?", "？",
			} {
				if strings.Contains(localPrefix, marker) {
					leftBounded = true
					break
				}
			}
		}
		if !leftBounded {
			return false
		}
	}
	return coachQuestionSubjectRightBoundedAt(
		sourceRunes,
		anchorRunes,
		start,
	)
}

func coachQuestionSubjectRightBoundedAt(
	sourceRunes []rune,
	anchorRunes []rune,
	start int,
) bool {
	if len(anchorRunes) == 0 || start < 0 ||
		start+len(anchorRunes) > len(sourceRunes) ||
		string(sourceRunes[start:start+len(anchorRunes)]) !=
			string(anchorRunes) {
		return false
	}
	end := start + len(anchorRunes)
	if end == len(sourceRunes) ||
		!unicode.IsLetter(sourceRunes[end]) &&
			!unicode.IsNumber(sourceRunes[end]) {
		return true
	}
	remainder := string(sourceRunes[end:])
	for _, relation := range []string{
		"について", "に関して", "とは", "の答え", "こと",
		"の", "は", "を", "が",
	} {
		if strings.HasPrefix(remainder, relation) {
			return true
		}
	}
	return false
}

// boundedCoachAnswerAnchor returns the exact target evidence A from the
// person's current answer. Only its HMAC is retained. Whole-turn copies are
// rejected while establishing a frame from a reported question plus answer,
// and low-entropy/generic answers cannot become cross-turn proof.
func boundedCoachAnswerAnchor(
	plan modelPlan,
	utterance string,
	rejectHighOverlap bool,
) (string, bool) {
	anchor, _, ok := boundedCoachAnswerFingerprint(
		plan,
		utterance,
		rejectHighOverlap,
		rejectHighOverlap,
	)
	return anchor, ok
}

// boundedCoachAnswerFingerprint binds the target A and every required slot to
// one domain-separated HMAC input. This prevents a same-number restatement
// from silently changing its unit, condition, uncertainty, or scope. Raw
// evidence remains current-turn data and is never stored.
func boundedCoachAnswerFingerprint(
	plan modelPlan,
	utterance string,
	rejectHighOverlap bool,
	allowLaterClause bool,
) (string, string, bool) {
	target, ok := answercontract.TargetSlot(plan.AnswerContract.QuestionFrame.Operator)
	if !ok || plan.RespondentStage != "restructure" ||
		len(plan.RespondentProtected) != 0 {
		return "", "", false
	}

	required := append(
		[]answercontract.RequiredSlot(nil),
		plan.AnswerContract.QuestionFrame.RequiredSlots...,
	)
	if len(required) == 0 || len(required) > answercontract.MaxRequiredSlots {
		return "", "", false
	}
	sort.Slice(required, func(left int, right int) bool {
		return required[left] < required[right]
	})
	for index := 1; index < len(required); index++ {
		if required[index] == required[index-1] {
			return "", "", false
		}
	}
	if plan.AnswerContract.QuestionFrame.Operator ==
		answercontract.OperatorQuantity {
		hasUnit := false
		for _, slot := range required {
			if slot == answercontract.SlotUnit {
				hasUnit = true
				break
			}
		}
		if !hasUnit {
			return "", "", false
		}
	}

	evidenceBySlot := make(
		map[answercontract.RequiredSlot]string,
		len(plan.RespondentEvidence),
	)
	for _, evidence := range plan.RespondentEvidence {
		if _, duplicate := evidenceBySlot[evidence.Slot]; duplicate {
			return "", "", false
		}
		evidenceBySlot[evidence.Slot] = evidence.Span
	}

	targetAnchor := ""
	parts := make([]string, 0, len(required)*2)
	anchorsBySlot := make(
		map[answercontract.RequiredSlot]string,
		len(required),
	)
	for _, slot := range required {
		span, exists := evidenceBySlot[slot]
		if !exists {
			return "", "", false
		}
		strictBoundary := slot == target ||
			slot != answercontract.SlotUnit
		anchor, anchorOK := boundedCoachEvidenceAnchor(
			plan,
			utterance,
			span,
			rejectHighOverlap && slot == target,
			slot == target,
			allowLaterClause && slot == target,
			strictBoundary,
		)
		if !anchorOK {
			return "", "", false
		}
		if slot == target {
			targetAnchor = anchor
		}
		anchorsBySlot[slot] = anchor
		parts = append(parts, string(slot), anchor)
	}
	if targetAnchor == "" {
		return "", "", false
	}
	if _, requiresUnit := anchorsBySlot[answercontract.SlotUnit]; requiresUnit &&
		!coachQuantityUnitTupleAttached(
			target,
			utterance,
			anchorsBySlot,
		) {
		return "", "", false
	}
	return targetAnchor, strings.Join(parts, "\x00"), true
}

func boundedCoachTargetCandidate(
	plan modelPlan,
	utterance string,
) (string, bool) {
	target, ok := answercontract.TargetSlot(
		plan.AnswerContract.QuestionFrame.Operator,
	)
	if !ok {
		return "", false
	}
	span := ""
	for _, evidence := range plan.RespondentEvidence {
		if evidence.Slot != target {
			continue
		}
		if span != "" {
			return "", false
		}
		span = evidence.Span
	}
	anchor := strings.ToLower(collapseSpace(norm.NFKC.String(span)))
	anchor = strings.Trim(anchor, " \t\r\n。．.!！?？、,")
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	anchorRunes := []rune(anchor)
	if len(anchorRunes) < 1 || len(anchorRunes) > 64 ||
		len(strings.Fields(anchor)) > 8 ||
		containsSensitiveStateText(anchor) ||
		!containsExactCoachAnchor(source, anchor) {
		return "", false
	}
	return anchor, true
}

func coachQuantityUnitTupleAttached(
	target answercontract.RequiredSlot,
	utterance string,
	anchors map[answercontract.RequiredSlot]string,
) bool {
	if target != answercontract.SlotQuantity {
		return false
	}
	quantity := anchors[answercontract.SlotQuantity]
	unit := anchors[answercontract.SlotUnit]
	if quantity == "" || unit == "" {
		return false
	}
	allowedUnit := false
	for _, candidate := range coachQuantityUnits {
		if unit == candidate {
			allowedUnit = true
			break
		}
	}
	if !allowedUnit {
		return false
	}

	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	sourceRunes := []rune(source)
	quantityRunes := []rune(quantity)
	matched := false
	conflicted := false
	const maxOccurrences = 32
	seen := 0
	for start := 0; start+len(quantityRunes) <= len(sourceRunes); start++ {
		if !exactCoachAnchorAt(sourceRunes, quantityRunes, start) {
			continue
		}
		seen++
		if seen > maxOccurrences {
			return false
		}
		remainder := strings.TrimLeft(
			string(sourceRunes[start+len(quantityRunes):]),
			" \t\r\n",
		)
		attached := ""
		for _, candidate := range coachQuantityUnits {
			if strings.HasPrefix(remainder, candidate) {
				attached = candidate
				break
			}
		}
		switch {
		case attached == unit:
			matched = true
		case attached != "":
			conflicted = true
		}
	}
	return matched && !conflicted
}

// Longest units come first so "時間" is not reduced to "時" and "年間" is
// not reduced to "年". The same table is used for numeric token boundaries
// and required-slot attachment.
var coachQuantityUnits = []string{
	"センチメートル", "ミリメートル", "パーセント",
	"キログラム", "メートル", "リットル", "ポイント",
	"ミリ秒", "時間", "週間", "年間", "か月", "ヶ月", "カ月",
	"kg", "km", "gb", "mb", "kb", "cm", "mm", "ms", "ml",
	"キロ", "ドル", "ユーロ", "歳", "名",
	"円", "人", "件", "日", "時", "分", "秒", "年", "月", "週",
	"個", "回", "倍", "台", "冊", "本", "枚",
	"g", "m", "l", "%", "％",
}

func boundedCoachEvidenceAnchor(
	plan modelPlan,
	utterance string,
	span string,
	rejectHighOverlap bool,
	requireAssertion bool,
	allowLaterClause bool,
	strictBoundary bool,
) (string, bool) {
	anchor := strings.ToLower(collapseSpace(norm.NFKC.String(span)))
	anchor = strings.Trim(anchor, " \t\r\n。．.!！?？、,")
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	attemptSource := strings.ToLower(
		collapseSpace(norm.NFKC.String(plan.AnswerAttempt)),
	)
	anchorRunes := []rune(anchor)
	sourceRunes := []rune(source)
	if len(anchorRunes) < 1 || len(anchorRunes) > 64 ||
		len(sourceRunes) == 0 ||
		len(strings.Fields(anchor)) > 8 ||
		(rejectHighOverlap && len(anchorRunes)*100 > len(sourceRunes)*60) ||
		containsSensitiveStateText(anchor) {
		return "", false
	}
	if requireAssertion {
		if !containsVerifiedCoachAssertion(
			source,
			anchor,
			allowLaterClause,
			plan.AnswerContract.QuestionFrame.Subject,
		) || !containsVerifiedCoachAssertion(
			attemptSource,
			anchor,
			allowLaterClause,
			plan.AnswerContract.QuestionFrame.Subject,
		) ||
			containsRetractedCoachAnchorOccurrence(source, anchor) ||
			containsRetractedCoachAnchorOccurrence(attemptSource, anchor) {
			return "", false
		}
	} else if strictBoundary {
		if !containsExactCoachAnchor(source, anchor) ||
			!containsExactCoachAnchor(attemptSource, anchor) ||
			containsRetractedCoachAnchorOccurrence(source, anchor) ||
			containsRetractedCoachAnchorOccurrence(attemptSource, anchor) {
			return "", false
		}
	} else if !containsCoachEvidenceOutsideQuote(source, anchor) ||
		!containsCoachEvidenceOutsideQuote(attemptSource, anchor) {
		return "", false
	}
	decimalAnchor := coachDecimalAnchorPattern.MatchString(anchor)
	for _, current := range anchorRunes {
		if !unicode.IsLetter(current) && !unicode.IsNumber(current) &&
			!unicode.IsSpace(current) && current != '-' &&
			!(current == '%' && anchor == "%") &&
			!((current == '.' || current == '+') && decimalAnchor) {
			return "", false
		}
	}
	for _, sensitive := range []string{
		"氏名", "名前", "住所", "電話", "メール", "生年月日", "マイナンバー",
		"口座", "カード番号", "社員番号", "顧客番号", "患者番号",
		"password", "secret", "token", "api key", "account number",
		"phone", "address", "email", "full name", "date of birth",
	} {
		if strings.Contains(anchor, sensitive) {
			return "", false
		}
	}
	return anchor, true
}

func containsCoachEvidenceOutsideQuote(source string, evidence string) bool {
	if source == "" || evidence == "" {
		return false
	}
	const maxOccurrences = 32
	searchFrom := 0
	for seen := 0; seen < maxOccurrences && searchFrom < len(source); seen++ {
		relative := strings.Index(source[searchFrom:], evidence)
		if relative < 0 {
			return false
		}
		start := searchFrom + relative
		if !coachTextPositionInsideQuote(source, start) &&
			!coachSpanContainsQuoteDelimiter([]rune(evidence)) {
			return true
		}
		searchFrom = start + len(evidence)
	}
	return false
}

func containsVerifiedCoachAssertion(
	source string,
	anchor string,
	allowLaterClause bool,
	subject string,
) bool {
	if source == "" || anchor == "" {
		return false
	}
	sourceRunes := []rune(source)
	anchorRunes := []rune(anchor)
	if len(anchorRunes) == 0 || len(anchorRunes) > len(sourceRunes) {
		return false
	}
	if coachAnswerRetractsAnchor(source, anchor) {
		return false
	}
	for start := 0; start+len(anchorRunes) <= len(sourceRunes); start++ {
		end := start + len(anchorRunes)
		if verifiedCoachAssertionAtForSubject(
			sourceRunes,
			anchor,
			anchorRunes,
			start,
			end,
			allowLaterClause,
			subject,
		) {
			return true
		}
	}
	return false
}

func verifiedCoachAssertionAt(
	sourceRunes []rune,
	anchor string,
	anchorRunes []rune,
	start int,
	end int,
	allowLaterClause bool,
) bool {
	return verifiedCoachAssertionAtForSubject(
		sourceRunes,
		anchor,
		anchorRunes,
		start,
		end,
		allowLaterClause,
		"",
	)
}

func verifiedCoachAssertionAtForSubject(
	sourceRunes []rune,
	anchor string,
	anchorRunes []rune,
	start int,
	end int,
	allowLaterClause bool,
	subject string,
) bool {
	if !exactCoachAnchorAt(sourceRunes, anchorRunes, start) ||
		coachAnchorOccurrenceAttributedToOther(
			sourceRunes,
			start,
			end,
			subject,
		) {
		return false
	}
	prefix := string(sourceRunes[:start])
	if coachProxyAnswerLeadOwnedByOther(prefix, subject) {
		return false
	}
	for _, marker := range []string{
		"前の文", "前の文章", "引用", "例文", "記載", "書いて",
		"表示", "指示", "次の通り", "次のとおり",
		"the text", "the quote", "quoted", "example text",
	} {
		if strings.Contains(prefix, marker) {
			return false
		}
	}
	for _, marker := range []string{
		"自分の答えは", "私の答えは", "答えは", "回答は", "結論は",
		"目的は", "理由は", "選択は", "数量は", "数字は",
		"状態は", "定義は", "違いは", "根拠は", "手順は",
		"my answer is", "i answer", "i would answer",
	} {
		if strings.HasPrefix(anchor, marker) {
			return true
		}
	}
	if coachPrefixIsOnlyFiller(prefix) {
		return true
	}
	if coachPrefixIsCorrectionLead(prefix) {
		return true
	}
	if allowLaterClause {
		lastBoundary := -1
		lastBoundaryWidth := 0
		for _, boundary := range []string{"。", ".", "!", "！"} {
			if position := strings.LastIndex(prefix, boundary); position > lastBoundary {
				lastBoundary = position
				lastBoundaryWidth = len(boundary)
			}
		}
		if lastBoundary >= 0 &&
			(coachPrefixIsOnlyFiller(
				prefix[lastBoundary+lastBoundaryWidth:],
			) ||
				coachPrefixIsCorrectionLead(
					prefix[lastBoundary+lastBoundaryWidth:],
				)) {
			return true
		}
	}
	trimmedPrefix := strings.TrimRight(
		prefix,
		" \t\r\n、,：:",
	)
	normalizedSubject := strings.ToLower(
		collapseSpace(norm.NFKC.String(subject)),
	)
	if normalizedSubject != "" {
		for _, relation := range []string{
			"についての答えは", "に関する答えは",
			"ことについての答えは", "についてですが",
			"に関してですが", "のことですが", "ことは",
			"については", "に関しては", "について",
			"に関して", "としては", "への答えは",
			"の答えは", "は",
		} {
			needle := normalizedSubject + relation
			if strings.HasSuffix(trimmedPrefix, needle) &&
				coachQuestionSubjectPrefixAllowed(
					trimmedPrefix[:len(trimmedPrefix)-len(needle)],
				) {
				return true
			}
		}
	}
	if coachPrefixEndsWithSelfTopic(trimmedPrefix) {
		return true
	}
	for _, marker := range []string{
		"自分の答えは", "私の答えは", "答えは", "回答は", "結論は",
		"目的は", "理由は", "選択は", "数量は", "数字は",
		"状態は", "定義は", "違いは", "根拠は", "手順は",
		"ことは", "私は", "自分は",
		"my answer is", "i answer", "i would answer",
	} {
		if strings.HasSuffix(trimmedPrefix, marker) {
			return true
		}
	}
	remainder := string(sourceRunes[end:])
	reportedOwnAnswer := false
	for _, marker := range []string{
		"と答え", "と回答", "と伝え", "と返答",
		" is my answer", " was my answer",
	} {
		if strings.HasPrefix(remainder, marker) {
			reportedOwnAnswer = true
			break
		}
	}
	if !reportedOwnAnswer {
		return false
	}
	for _, selfMarker := range []string{
		"私は", "自分は", "自分の答え", "私の答え",
		"i answered", "i said",
	} {
		if strings.Contains(trimmedPrefix, selfMarker) {
			return true
		}
	}
	return false
}

func coachAnchorOccurrenceAttributedToOther(
	sourceRunes []rune,
	start int,
	end int,
	subject string,
) bool {
	if start < 0 || end < start || end > len(sourceRunes) {
		return true
	}
	clauseStart := 0
	for index := start - 1; index >= 0; index-- {
		switch sourceRunes[index] {
		case '。', '.', '！', '!', '？', '?', '；', ';':
			clauseStart = index + 1
			index = -1
		}
	}
	prefix := strings.ToLower(string(sourceRunes[clauseStart:start]))
	if coachClauseAttributedToOther(prefix) ||
		coachProxyAnswerLeadOwnedByOther(prefix, subject) ||
		coachPossessiveAnswerOwnedByOther(prefix, subject) ||
		coachClauseTopicalActorOwnedByOther(prefix, subject) {
		return true
	}
	for _, marker := range []string{
		"chatgptの答え", "chatgptの回答", "aiの答え", "aiの回答",
		"モデルの答え", "モデルの回答", "彼の答え", "彼の回答",
		"彼女の答え", "彼女の回答", "前任者の答え", "前任者の回答",
		"上司の答え", "上司の回答", "同僚の答え", "同僚の回答",
		"他人の答え", "他人の回答", "第三者の答え", "第三者の回答",
		"彼によると", "彼によれば", "彼女によると", "彼女によれば",
		"前任者によると", "前任者によれば", "上司によると", "上司によれば",
		"同僚によると", "同僚によれば", "chatgptによると",
		"chatgptによれば", "aiによると", "aiによれば",
		"モデルによると", "モデルによれば",
		"assistant's answer", "chatgpt's answer", "the ai's answer",
		"his answer", "her answer", "their answer",
		"according to ",
	} {
		if strings.Contains(prefix, marker) {
			return true
		}
	}

	clauseEnd := len(sourceRunes)
	for index := end; index < len(sourceRunes); index++ {
		switch sourceRunes[index] {
		case '。', '.', '！', '!', '？', '?', '；', ';':
			clauseEnd = index
			index = len(sourceRunes)
		}
	}
	suffix := strings.ToLower(string(sourceRunes[end:clauseEnd]))
	if coachGenericReporterOwnsSuffix(suffix) ||
		coachNominalizedAnswerOwnedByOther(suffix, subject) {
		return true
	}
	if coachFollowingAnaphoricAnswerOwnedByOther(
		sourceRunes,
		end,
		subject,
	) {
		return true
	}
	for _, marker := range []string{
		"と彼が答え", "と彼女が答え", "と前任者が答え",
		"と上司が答え", "と同僚が答え", "と他人が答え",
		"と第三者が答え", "とchatgptが答え", "とaiが答え",
		"とモデルが答え", "と彼が言", "と彼女が言",
		"と前任者が言", "と上司が言", "と同僚が言",
		"とchatgptが言", "とaiが言", "とモデルが言",
		" according to chatgpt", " according to the ai",
		" he answered", " she answered", " they answered",
	} {
		if strings.Contains(suffix, marker) {
			return true
		}
	}
	return false
}

func coachClauseAttributedToOther(prefix string) bool {
	for _, marker := range []string{"によると", "によれば"} {
		searchFrom := 0
		for searchFrom < len(prefix) {
			relative := strings.Index(prefix[searchFrom:], marker)
			if relative < 0 {
				break
			}
			position := searchFrom + relative
			actorPrefix := strings.TrimRight(
				prefix[:position],
				" \t\r\n、,：:。.!！?？;；",
			)
			if !coachTextEndsWithSelfActor(actorPrefix) {
				return true
			}
			searchFrom = position + len(marker)
		}
	}
	return false
}

type coachAnswerOwner uint8

const (
	coachOwnerNone coachAnswerOwner = iota
	coachOwnerSelf
	coachOwnerOther
	coachOwnerDisowned
)

func coachLatestReporterOwner(source string) (coachAnswerOwner, int) {
	clauseStart := 0
	latestOwner := coachOwnerNone
	latestPosition := -1
	for position, current := range source {
		switch current {
		case '。', '.', '！', '!', '？', '?', '；', ';':
			if coachTextPositionInsideQuote(source, position) {
				continue
			}
			if owner := coachReporterClauseOwner(
				source[clauseStart:position],
			); owner != coachOwnerNone {
				latestOwner = owner
				latestPosition = position
			}
			clauseStart = position + len(string(current))
		}
	}
	if clauseStart <= len(source) {
		if owner := coachReporterClauseOwner(
			source[clauseStart:],
		); owner != coachOwnerNone {
			latestOwner = owner
			latestPosition = clauseStart
		}
	}
	return latestOwner, latestPosition
}

func coachReporterClauseOwner(clause string) coachAnswerOwner {
	clause = strings.ToLower(strings.TrimSpace(clause))
	polarity, predicatePosition, disownalPosition :=
		coachReportPredicatePolarity(clause)
	if polarity == coachOwnerNone || predicatePosition < 0 {
		return coachOwnerNone
	}
	actor, actorOK := coachReporterActorBeforePredicate(
		clause,
		predicatePosition,
	)
	if !actorOK {
		return coachOwnerNone
	}
	self := coachTextEndsWithSelfActor(actor) ||
		coachTextEndsWithEmbeddedSelfActor(actor)
	if self && disownalPosition >= 0 {
		disowningActor, disowningActorOK :=
			coachReporterActorBeforePredicate(
				clause,
				disownalPosition,
			)
		if disowningActorOK &&
			(coachTextEndsWithSelfActor(disowningActor) ||
				coachTextEndsWithEmbeddedSelfActor(disowningActor)) {
			return coachOwnerDisowned
		}
	}
	if polarity == coachOwnerDisowned {
		if self {
			return coachOwnerDisowned
		}
		return coachOwnerNone
	}
	if self {
		for _, proxy := range []string{
			"の答え", "の回答", "の結論", "の見解",
			"の意見", "の発言", "の返答", "の返事",
			"に代わって", "の代わり", "代理",
		} {
			if strings.Contains(clause[:predicatePosition], proxy) {
				return coachOwnerDisowned
			}
		}
		return coachOwnerSelf
	}
	if actor == "" {
		return coachOwnerNone
	}
	return coachOwnerOther
}

func coachReporterActorBeforePredicate(
	clause string,
	predicatePosition int,
) (string, bool) {
	if predicatePosition <= 0 || predicatePosition > len(clause) {
		return "", false
	}
	particlePosition := -1
	for _, particle := range []string{"が", "は", "も", "こそ"} {
		searchFrom := 0
		for searchFrom < predicatePosition {
			relative := strings.Index(
				clause[searchFrom:predicatePosition],
				particle,
			)
			if relative < 0 {
				break
			}
			position := searchFrom + relative
			if !coachTextPositionInsideQuote(clause, position) &&
				position > particlePosition {
				particlePosition = position
			}
			searchFrom = position + len(particle)
		}
	}
	if particlePosition < 0 {
		return "", false
	}
	actorStart := 0
	for position, current := range clause[:particlePosition] {
		switch current {
		case '、', ',', '：', ':':
			if !coachTextPositionInsideQuote(clause, position) {
				actorStart = position + len(string(current))
			}
		}
	}
	actor := strings.TrimSpace(clause[actorStart:particlePosition])
	if actor == "" {
		return "", false
	}
	return actor, true
}

func coachReportPredicatePolarity(
	clause string,
) (coachAnswerOwner, int, int) {
	bestPosition := -1
	bestPolarity := coachOwnerNone
	latestDisownal := -1
	for _, candidate := range []struct {
		polarity coachAnswerOwner
		markers  []string
	}{
		{
			polarity: coachOwnerDisowned,
			markers: []string{
				"答えません", "答えない", "答えなかった",
				"答えていません", "答えていない",
				"答えていなかった",
				"答えたわけではありません", "答えたわけではない",
				"回答しません", "回答しない", "回答しなかった",
				"回答していません", "回答していない",
				"回答していなかった",
				"返答しません", "返答しない", "返答しなかった",
				"返答していません", "返答していない",
				"言いません", "言わない", "言わなかった",
				"言っていません", "言っていない",
				"話しません", "話さない", "話さなかった",
				"話していません", "話していない",
				"説明しません", "説明しない", "説明しなかった",
				"説明していません", "説明していない",
				"主張しません", "主張しない", "主張しなかった",
				"主張していません", "主張していない",
				"伝えません", "伝えない", "伝えなかった",
				"伝えていません", "伝えていない",
				"述べません", "述べない", "述べなかった",
				"述べていません", "述べていない",
				"発言しません", "発言しない", "発言しなかった",
				"発言していません", "発言していない",
				"答弁しません", "答弁しない", "答弁しなかった",
				"答弁していません", "答弁していない",
				"語りません", "語らない", "語らなかった",
				"語っていません", "語っていない",
			},
		},
		{
			polarity: coachOwnerSelf,
			markers: []string{
				"答えました", "答えます", "答えた", "答える",
				"答えています", "答えていました", "答えている", "答えていた",
				"回答しました", "回答します", "回答した", "回答する",
				"回答しています", "回答していました", "回答している", "回答していた",
				"返答しました", "返答します", "返答した", "返答する",
				"返答しています", "返答していました", "返答している", "返答していた",
				"言いました", "言います", "言った", "言う",
				"言っています", "言っていました", "言っている", "言っていた",
				"話しました", "話します", "話した", "話す",
				"話しています", "話していました", "話している", "話していた",
				"説明しました", "説明します", "説明した", "説明する",
				"説明しています", "説明していました", "説明している", "説明していた",
				"主張しました", "主張します", "主張した", "主張する",
				"主張しています", "主張していました", "主張している", "主張していた",
				"伝えました", "伝えます", "伝えた", "伝える",
				"伝えています", "伝えていました", "伝えている", "伝えていた",
				"紹介しました", "紹介します", "紹介した", "紹介する",
				"読みました", "読みます", "読んだ", "読む",
				"述べました", "述べます", "述べた", "述べる",
				"述べています", "述べていました", "述べている", "述べていた",
				"発言しました", "発言します", "発言した", "発言する",
				"発言しています", "発言していました", "発言している", "発言していた",
				"答弁しました", "答弁します", "答弁した", "答弁する",
				"答弁しています", "答弁していました", "答弁している", "答弁していた",
				"語りました", "語ります", "語った", "語る",
				"語っています", "語っていました", "語っている", "語っていた",
			},
		},
	} {
		for _, marker := range candidate.markers {
			searchFrom := 0
			for searchFrom < len(clause) {
				relative := strings.Index(clause[searchFrom:], marker)
				if relative < 0 {
					break
				}
				position := searchFrom + relative
				if !coachTextPositionInsideQuote(clause, position) {
					if candidate.polarity == coachOwnerDisowned &&
						position > latestDisownal {
						latestDisownal = position
					}
					if position > bestPosition {
						bestPosition = position
						bestPolarity = candidate.polarity
					}
				}
				searchFrom = position + len(marker)
			}
		}
	}
	return bestPolarity, bestPosition, latestDisownal
}

func coachProxyAnswerLeadOwnedByOther(prefix string, subject string) bool {
	prefix = strings.TrimSpace(prefix)
	subject = strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	for _, marker := range []string{
		"に代わって答え", "に代わって回答", "に代わって返答",
		"の代わりに答え", "の代わりに回答", "の代わりに返答",
		"代理で答え", "代理で回答", "代理で返答",
	} {
		if strings.Contains(prefix, marker) {
			return true
		}
	}
	latestExternal := -1
	latestExplicitSelf := -1
	if reporterOwner, position := coachLatestReporterOwner(prefix); position >= 0 {
		switch reporterOwner {
		case coachOwnerSelf:
			latestExplicitSelf = position
		case coachOwnerDisowned, coachOwnerOther:
			latestExternal = position
		}
	}
	for _, relation := range []string{
		"の答え", "の回答", "の結論", "の見解",
		"の意見", "の発言", "の返答", "の返事",
		"の主張", "の説明",
	} {
		searchFrom := 0
		for searchFrom < len(prefix) {
			relative := strings.Index(prefix[searchFrom:], relation)
			if relative < 0 {
				break
			}
			position := searchFrom + relative
			ownerPrefix := strings.TrimRight(
				prefix[:position],
				" \t\r\n、,：:。.!！?？;；",
			)
			ownerStart := 0
			for index, current := range ownerPrefix {
				switch current {
				case '。', '.', '！', '!', '？', '?', '；', ';',
					'、', ',', '：', ':':
					ownerStart = index + len(string(current))
				}
			}
			owner := strings.TrimSpace(ownerPrefix[ownerStart:])
			switch {
			case coachTextEndsWithSelfActor(owner) ||
				coachTextEndsWithEmbeddedSelfActor(owner):
				afterRelation := strings.TrimSpace(
					prefix[position+len(relation):],
				)
				if coachOwnerRelationNegated(afterRelation) ||
					coachTextPositionInsideQuote(prefix, position) {
					if position > latestExternal {
						latestExternal = position
					}
					break
				}
				// A self-owner can supersede an earlier external report only
				// when the relation immediately governs the current A.
				directRelation := strings.Trim(
					afterRelation,
					" \t\r\n、,：:",
				)
				if (directRelation == "としては" ||
					directRelation == "は" ||
					directRelation == "が") &&
					position > latestExplicitSelf {
					latestExplicitSelf = position
				}
			case subject != "" && owner == subject &&
				coachSubjectCanOwnConceptualAnswer(subject):
				// The exact question subject is a relation, not evidence that
				// the person owns an earlier third-party answer.
			default:
				if position > latestExternal {
					latestExternal = position
				}
			}
			searchFrom = position + len(relation)
		}
	}
	return latestExternal >= 0 && latestExplicitSelf < latestExternal
}

func coachPossessiveAnswerOwnedByOther(
	prefix string,
	subject string,
) bool {
	prefix = strings.TrimSpace(prefix)
	subject = strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	for _, relation := range []string{
		"の答えは", "の回答は", "の結論は", "の見解は",
	} {
		if !strings.HasSuffix(prefix, relation) {
			continue
		}
		owner := strings.TrimSpace(strings.TrimSuffix(prefix, relation))
		if coachTextEndsWithSelfActor(owner) ||
			subject != "" && owner == subject &&
				coachSubjectCanOwnConceptualAnswer(subject) {
			return false
		}
		return true
	}
	return false
}

func coachTextEndsWithSelfActor(source string) bool {
	source = strings.TrimSpace(source)
	for _, actor := range []string{
		"私", "わたし", "自分", "僕", "ぼく", "俺", "おれ",
		"私たち", "わたしたち", "我々", "われわれ",
		"me", "myself", "i", "we", "us", "ourselves",
	} {
		for _, ending := range []string{
			"", "自身", "で", "自身で", "として", "自身として",
		} {
			if source == actor+ending {
				return true
			}
		}
	}
	return false
}

func coachSubjectLooksLikeAnsweringActor(subject string) bool {
	subject = strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	subject = strings.Trim(subject, " \t\r\n、,：:。.!！?？;；")
	switch subject {
	case "彼", "彼女", "彼ら", "彼女ら", "あの人", "その人",
		"他人", "第三者", "前任者", "上司", "同僚", "先生",
		"教師", "教授", "部長", "課長", "担当者", "回答者",
		"chatgpt", "ai", "the ai", "assistant", "model", "モデル":
		return true
	}
	for _, ending := range []string{
		"さん", "氏", "君", "くん", "ちゃん", "様", "先生",
		"教授", "部長", "課長", "担当者", "回答者",
	} {
		if strings.HasSuffix(subject, ending) {
			return true
		}
	}
	return false
}

func coachSubjectCanOwnConceptualAnswer(subject string) bool {
	subject = strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	subject = strings.Trim(subject, " \t\r\n、,：:。.!！?？;；")
	if subject == "" || coachSubjectLooksLikeAnsweringActor(subject) {
		return false
	}
	for _, ending := range []string{
		"問題", "質問", "課題", "テーマ", "論点", "対象", "内容",
		"目的", "理由", "選択", "数量", "数字", "状態", "定義",
		"違い", "根拠", "手順", "方針", "計画", "方法", "結果",
		"結論", "首都", "期限", "納期", "価格", "費用",
	} {
		if strings.HasSuffix(subject, ending) {
			return true
		}
	}
	return false
}

func coachOwnerRelationNegated(afterRelation string) bool {
	afterRelation = strings.TrimLeft(
		afterRelation,
		" \t\r\n、,：:",
	)
	for _, negative := range []string{
		"ではありません", "じゃありません",
		"ではない", "じゃない", "でない",
		"ではなく", "じゃなく", "でなく",
		"はありません", "はない", "はなく",
		"がありません", "がない", "がなく",
	} {
		if strings.HasPrefix(afterRelation, negative) {
			return true
		}
	}
	return false
}

func coachClauseTopicalActorOwnedByOther(
	prefix string,
	subject string,
) bool {
	prefix = strings.TrimSpace(prefix)
	subject = strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	lead, _ := coachStripAnswerRelationPrefix(prefix, subject)
	segments := strings.FieldsFunc(
		lead,
		func(current rune) bool {
			switch current {
			case '、', ',', '：', ':':
				return true
			default:
				return false
			}
		},
	)
	selfOwnerFound := false
segmentsLoop:
	for segmentIndex := len(segments) - 1; segmentIndex >= 0; segmentIndex-- {
		segment := strings.TrimSpace(segments[segmentIndex])
		switch segment {
		case "", "おはよう", "おはようございます",
			"やっぱり", "やっぱ", "やはり", "いや", "いえ",
			"正しくは", "本当は", "訂正":
			continue
		}
		actorFound := false
		for _, particle := range []string{"こそ", "は", "が", "も"} {
			if !strings.HasSuffix(segment, particle) {
				continue
			}
			actorFound = true
			actor := strings.TrimSpace(strings.TrimSuffix(segment, particle))
			if coachTextEndsWithSelfActor(actor) ||
				coachTextEndsWithEmbeddedSelfActor(actor) {
				selfOwnerFound = true
				continue segmentsLoop
			}
			switch actor {
			case "まず", "今", "いま", "ここ", "それで", "では",
				"これ", "それ", "理由", "背景", "根拠", "条件", "結果":
				continue segmentsLoop
			}
			if selfOwnerFound {
				// A nearer explicit self topic owns A. An earlier bare actor
				// topic may provide contrast, but arbitrary earlier prose is
				// still inspected below and fails closed.
				continue segmentsLoop
			}
			return true
		}
		if !actorFound {
			if coachTextEndsWithSelfActor(segment) {
				selfOwnerFound = true
				continue
			}
			if coachSegmentBeginsWithSelfTopicAndBackground(segment) {
				// The closest explicit topic is still the person even when
				// they put a short background clause before "答えは A".
				// Inspect only this nearest comma-delimited segment so an
				// earlier self mention cannot authorize a later third party.
				selfOwnerFound = true
				continue
			}
			if coachExplicitExternalActorWithoutParticle(segment) {
				return true
			}
			if coachTopicalPrefixSegmentAllowedWithoutActor(
				segment,
				subject,
			) {
				continue
			}
			if selfOwnerFound &&
				coachReportedQuestionEndOutsideQuote(segment) >= 0 {
				continue
			}
			return true
		}
	}
	return false
}

func coachSegmentBeginsWithSelfTopicAndBackground(segment string) bool {
	segment = strings.TrimSpace(segment)
	for _, topic := range []string{
		"私たちは", "わたしたちは", "我々は", "われわれは",
		"私は", "わたしは", "自分は", "僕は", "ぼくは",
		"俺は", "おれは",
	} {
		if !strings.HasPrefix(segment, topic) {
			continue
		}
		remainder := strings.TrimSpace(strings.TrimPrefix(segment, topic))
		if remainder == "" {
			return true
		}
		return coachBoundedBackgroundLead(remainder)
	}
	return false
}

func coachBoundedBackgroundLead(segment string) bool {
	segment = strings.TrimSpace(segment)
	for {
		removed := false
		for _, filler := range []string{
			"まず", "先に", "最初に", "ちょっと", "少し",
		} {
			if !strings.HasPrefix(segment, filler) {
				continue
			}
			segment = strings.TrimSpace(strings.TrimPrefix(segment, filler))
			removed = true
			break
		}
		if !removed {
			break
		}
	}
	for _, exact := range []string{
		"理由を先に説明して", "背景を先に説明して",
		"前置きを先に説明して", "状況を先に説明して",
	} {
		if segment == exact {
			return true
		}
	}
	for _, label := range []string{
		"理由は", "背景は", "根拠は", "条件は",
		"状況は", "経緯は", "事情は",
	} {
		if !strings.HasPrefix(segment, label) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(segment, label))
		if value == "" ||
			(!strings.HasSuffix(value, "です") &&
				!strings.HasSuffix(value, "でした")) {
			return false
		}
		for _, proxy := range []string{
			"の答え", "の回答", "の結論", "の見解",
			"による", "によれば", "によると",
			"に代わって", "の代わり", "代理",
		} {
			if strings.Contains(value, proxy) {
				return false
			}
		}
		return true
	}
	for _, ending := range []string{
		"れば", "けば", "せば", "えば", "めば", "べば", "てば",
	} {
		if !strings.HasSuffix(segment, ending) {
			continue
		}
		for _, report := range []string{
			"答え", "回答", "返答", "発言", "主張",
			"言い", "話し", "説明", "紹介", "読", "引用", "伝え",
		} {
			if strings.Contains(segment, report) {
				return false
			}
		}
		return true
	}
	for _, action := range []string{
		"背景を説明", "理由を説明", "前置きを説明",
		"状況を説明", "経緯を説明", "事情を説明",
		"考えを整理", "頭を整理",
	} {
		for _, ending := range []string{
			"してから", "したので", "したため",
		} {
			if segment == action+ending {
				return true
			}
		}
	}
	return false
}

func coachTextEndsWithEmbeddedSelfActor(source string) bool {
	source = strings.TrimSpace(source)
	for _, actor := range []string{
		"私", "わたし", "自分", "僕", "ぼく", "俺", "おれ",
		"私たち", "わたしたち", "我々", "われわれ",
	} {
		if !strings.HasSuffix(source, actor) {
			continue
		}
		lead := strings.TrimSpace(strings.TrimSuffix(source, actor))
		for _, ending := range []string{
			"です", "でした", "ます", "ました", "ません",
			"する", "した", "して", "すると", "から", "ため", "ので",
		} {
			if strings.HasSuffix(lead, ending) {
				return true
			}
		}
	}
	return false
}

func coachPrefixEndsWithSelfTopic(prefix string) bool {
	segments := strings.FieldsFunc(
		prefix,
		func(current rune) bool {
			switch current {
			case '、', ',', '：', ':':
				return true
			default:
				return false
			}
		},
	)
	if len(segments) == 0 {
		return false
	}
	segment := strings.TrimSpace(segments[len(segments)-1])
	for _, actor := range []string{
		"私たち", "わたしたち", "我々", "われわれ",
		"私", "わたし", "自分", "僕", "ぼく", "俺", "おれ",
	} {
		for _, particle := range []string{
			"としては", "自身としては", "自身は", "こそ", "は", "が", "も",
		} {
			if segment == actor+particle {
				return true
			}
		}
	}
	return false
}

func coachStripAnswerRelationPrefix(
	prefix string,
	subject string,
) (string, bool) {
	prefix = strings.TrimRight(prefix, " \t\r\n、,：:")
	subject = strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	if subject != "" {
		for _, relation := range []string{
			"についての答えは", "に関する答えは",
			"ことについての答えは", "についてですが",
			"に関してですが", "のことですが", "ことは",
			"については", "に関しては", "について",
			"に関して", "としては", "への答えは",
			"の答えは", "は",
		} {
			needle := subject + relation
			if strings.HasSuffix(prefix, needle) {
				return strings.TrimRight(
					strings.TrimSuffix(prefix, needle),
					" \t\r\n、,：:",
				), true
			}
		}
	}
	for _, relation := range []string{
		"自分の答えは", "私の答えは", "私たちの答えは",
		"我々の答えは", "答えは", "回答は", "結論は",
		"my answer is", "our answer is",
	} {
		if strings.HasSuffix(prefix, relation) {
			return strings.TrimRight(
				strings.TrimSuffix(prefix, relation),
				" \t\r\n、,：:",
			), true
		}
	}
	for _, relation := range []string{
		"目的は", "理由は", "選択は", "数量は", "数字は",
		"状態は", "定義は", "違いは", "根拠は", "手順は",
	} {
		if prefix == relation {
			return "", true
		}
	}
	return prefix, false
}

func coachTopicalPrefixSegmentAllowedWithoutActor(
	segment string,
	subject string,
) bool {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return true
	}
	for _, allowed := range []string{
		"まず", "今", "いま", "ここ", "それで", "では",
		"これ", "それ", "おはよう", "おはようございます",
		"答え", "回答", "結論", "目的", "理由", "選択",
		"数量", "数字", "状態", "定義", "違い", "根拠", "手順",
	} {
		if segment == allowed {
			return true
		}
	}
	for _, actor := range []string{
		"私", "わたし", "自分", "僕", "ぼく", "俺", "おれ",
		"私たち", "わたしたち", "我々", "われわれ",
	} {
		if !strings.HasPrefix(segment, actor) {
			continue
		}
		switch strings.TrimSpace(strings.TrimPrefix(segment, actor)) {
		case "", "自身", "なら", "ならば", "だって", "こそ",
			"として", "の場合", "の答え", "の回答", "の結論":
			return true
		}
	}
	if coachBoundedBackgroundLead(segment) {
		return true
	}
	if subject == "" {
		return false
	}
	for _, relation := range []string{
		"", "について", "に関して", "の答え", "の回答", "の結論",
	} {
		if segment == subject+relation {
			return true
		}
	}
	if strings.HasSuffix(segment, subject) {
		for _, ambiguousActorLead := range []string{
			"なら" + subject,
			"だって" + subject,
			"こそ" + subject,
			"自身" + subject,
		} {
			if strings.Contains(segment, ambiguousActorLead) {
				return false
			}
		}
		return true
	}
	return false
}

func coachExplicitExternalActorWithoutParticle(segment string) bool {
	segment = strings.TrimSpace(segment)
	for _, ending := range []string{
		"さん自身", "氏自身", "くん自身", "君自身", "ちゃん自身",
		"彼自身", "彼女自身", "彼ら自身", "彼女ら自身",
		"上司自身", "同僚自身", "前任者自身", "先生自身",
		"教授自身", "部長自身", "課長自身",
		"chatgpt自身", "ai自身", "モデル自身",
	} {
		if strings.HasSuffix(segment, ending) {
			return true
		}
	}
	return false
}

func coachFollowingAnaphoricAnswerOwnedByOther(
	sourceRunes []rune,
	anchorEnd int,
	subject string,
) bool {
	if anchorEnd < 0 || anchorEnd > len(sourceRunes) {
		return true
	}
	clauseStart := -1
	clauseEnd := len(sourceRunes)
scanFollowingClause:
	for position := anchorEnd; position < len(sourceRunes); position++ {
		switch sourceRunes[position] {
		case '。', '.', '！', '!', '？', '?', '；', ';':
			if coachRunePositionInsideQuote(sourceRunes, position) {
				continue
			}
			if clauseStart < 0 {
				clauseStart = position + 1
				continue
			}
			if strings.TrimSpace(
				string(sourceRunes[clauseStart:position]),
			) == "" {
				clauseStart = position + 1
				continue
			}
			clauseEnd = position
			break scanFollowingClause
		}
	}
	if clauseStart < 0 || clauseStart >= clauseEnd {
		return false
	}
	clause := strings.ToLower(strings.TrimSpace(
		string(sourceRunes[clauseStart:clauseEnd]),
	))
	originalClause := clause
	for {
		stripped := false
		for _, discourseLead := range []string{
			"ちなみに", "なお", "補足すると", "参考までに",
		} {
			if !strings.HasPrefix(clause, discourseLead) {
				continue
			}
			clause = strings.TrimLeft(
				clause[len(discourseLead):],
				" \t\r\n、,：:",
			)
			stripped = true
			break
		}
		if !stripped {
			break
		}
	}
	if clause == originalClause {
		for position, current := range clause {
			switch current {
			case '、', ',', '：', ':':
				if coachTextPositionInsideQuote(clause, position) {
					continue
				}
				prefix := strings.TrimSpace(clause[:position])
				if prefix != "" &&
					utf8.RuneCountInString(prefix) <= 32 {
					clause = strings.TrimLeft(
						clause[position+len(string(current)):],
						" \t\r\n、,：:",
					)
				}
				break
			}
			if clause != originalClause {
				break
			}
		}
	}
	remainder := ""
	for _, lead := range []string{
		"これは", "それは", "今のは", "いまのは",
		"この答えは", "その答えは", "この回答は", "その回答は",
	} {
		if strings.HasPrefix(clause, lead) {
			remainder = strings.TrimSpace(strings.TrimPrefix(clause, lead))
			break
		}
	}
	if remainder == "" {
		return false
	}
	subject = strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	latestOwner := coachOwnerNone
	latestOwnerPosition := -1
	for _, relation := range []string{
		"の答え", "の回答", "の結論", "の見解",
		"の意見", "の発言", "の返答", "の返事",
		"の主張", "の説明",
	} {
		searchFrom := 0
		for searchFrom < len(remainder) {
			relative := strings.Index(remainder[searchFrom:], relation)
			if relative < 0 {
				break
			}
			position := searchFrom + relative
			if position == 0 {
				searchFrom = position + len(relation)
				continue
			}
			ownerPrefix := remainder[:position]
			ownerStart := 0
			for _, boundary := range []string{
				"、", ",", "：", ":", "ではなく", "じゃなく", "でなく",
				"または", "そして",
			} {
				if candidate := strings.LastIndex(
					ownerPrefix,
					boundary,
				); candidate >= 0 &&
					candidate+len(boundary) > ownerStart {
					ownerStart = candidate + len(boundary)
				}
			}
			owner := strings.TrimSpace(ownerPrefix[ownerStart:])
			afterRelation := remainder[position+len(relation):]
			eventOwner := coachOwnerNone
			switch {
			case coachTextEndsWithSelfActor(owner) ||
				coachTextEndsWithEmbeddedSelfActor(owner):
				if coachOwnerRelationNegated(afterRelation) {
					eventOwner = coachOwnerDisowned
				} else {
					eventOwner = coachOwnerSelf
				}
			case coachOwnerRelationNegated(afterRelation):
				// A negated external attribution does not transfer ownership.
			case subject != "" && owner == subject &&
				coachSubjectCanOwnConceptualAnswer(subject):
				// A conceptual question relation is not a third-party owner.
			case owner != "":
				eventOwner = coachOwnerOther
			}
			if eventOwner != coachOwnerNone &&
				position > latestOwnerPosition {
				latestOwner = eventOwner
				latestOwnerPosition = position
			}
			searchFrom = position + len(relation)
		}
	}
	return latestOwner == coachOwnerOther ||
		latestOwner == coachOwnerDisowned
}

func coachGenericReporterOwnsSuffix(suffix string) bool {
	matches := coachGenericReporterPattern.FindAllStringSubmatchIndex(
		suffix,
		-1,
	)
	for _, match := range matches {
		if len(match) < 4 || match[2] < 0 || match[3] < match[2] {
			continue
		}
		actor := strings.TrimSpace(suffix[match[2]:match[3]])
		remainder := strings.TrimLeft(
			suffix[match[1]:],
			" \t\r\n、,：:",
		)
		polarity, _, disownalPosition :=
			coachReportPredicatePolarity(remainder)
		if polarity == coachOwnerNone {
			continue
		}
		if coachTextEndsWithSelfActor(actor) {
			if polarity == coachOwnerDisowned ||
				disownalPosition >= 0 {
				return true
			}
			continue
		}
		switch actor {
		case "いうこと", "いう点", "いうもの", "いうの", "いう意味",
			"するの", "考えるの":
			continue
		case "条件", "結果", "場合", "数値", "状態":
			continue
		}
		return true
	}
	return false
}

func coachNominalizedAnswerOwnedByOther(
	suffix string,
	subject string,
) bool {
	subject = strings.ToLower(collapseSpace(norm.NFKC.String(subject)))
	for _, relation := range []string{
		"の答え", "の回答", "の結論", "の見解",
	} {
		searchFrom := 0
		for searchFrom < len(suffix) {
			relative := strings.Index(suffix[searchFrom:], relation)
			if relative < 0 {
				break
			}
			position := searchFrom + relative
			prefix := suffix[:position]
			ownerStart := -1
			for _, nominalizer := range []string{
				"というのが", "ということが", "というのは",
			} {
				if candidate := strings.LastIndex(
					prefix,
					nominalizer,
				); candidate >= 0 &&
					candidate+len(nominalizer) > ownerStart {
					ownerStart = candidate + len(nominalizer)
				}
			}
			if ownerStart >= 0 {
				owner := strings.TrimSpace(prefix[ownerStart:])
				afterRelation := suffix[position+len(relation):]
				if coachTextEndsWithSelfActor(owner) {
					if coachOwnerRelationNegated(afterRelation) {
						return true
					}
				} else if owner != "" &&
					(subject == "" ||
						owner != subject ||
						!coachSubjectCanOwnConceptualAnswer(subject)) {
					return true
				}
			}
			searchFrom = position + len(relation)
		}
	}
	return false
}

func coachAnswerRetractsAnchor(source string, anchor string) bool {
	if source == "" || anchor == "" {
		return true
	}
	source = strings.ToLower(source)
	anchor = strings.ToLower(anchor)
	sourceRunes := []rune(source)
	anchorRunes := []rune(anchor)
	if len(anchorRunes) == 0 || len(anchorRunes) > len(sourceRunes) {
		return true
	}
	earliestAnchorEndByte := -1
	for start := 0; start+len(anchorRunes) <= len(sourceRunes); start++ {
		end := start + len(anchorRunes)
		if string(sourceRunes[start:end]) == anchor &&
			!coachRunePositionInsideQuote(sourceRunes, start) {
			earliestAnchorEndByte = len(string(sourceRunes[:end]))
			break
		}
	}
	if earliestAnchorEndByte < 0 {
		return true
	}
	latestCorrection := -1
	for _, marker := range []string{
		"でも訂正", "訂正します", "訂正すると", "訂正して", "訂正",
		"撤回します", "撤回して", "取り消します", "取り消して",
		"正しくは", "本当は", "今のはなし", "今のなし",
		"言い間違えました", "言い間違いでした",
		"間違いでした", "誤りでした", "違いました", "違います",
		"じゃなかった", "ではなかった",
		"やっぱり", "やっぱ", "やはり", "いえ", "いや",
		"私の答えではない", "私の答えではありません",
		"私の回答ではない", "私の回答ではありません",
		"自分の答えではない", "自分の答えではありません",
		"自分の回答ではない", "自分の回答ではありません",
		"私はそう答えない", "私はそう答えません",
		"そうは答えない", "そうは答えません",
		"but i retract", "i retract", "correction:", "actually, no",
		"is not my answer", "was not my answer",
	} {
		searchFrom := earliestAnchorEndByte
		for searchFrom < len(source) {
			relative := strings.Index(source[searchFrom:], marker)
			if relative < 0 {
				break
			}
			position := searchFrom + relative
			if coachCorrectionMarkerAt(source, position, marker) &&
				position > latestCorrection {
				latestCorrection = position
			}
			searchFrom = position + len(marker)
		}
	}
	if latestCorrection < 0 {
		return false
	}

	// A final, personally owned assertion of the same exact A supersedes an
	// earlier correction. This is intentionally occurrence-bound: a quote or a
	// third party repeating A cannot re-establish the person's answer.
	for start := 0; start+len(anchorRunes) <= len(sourceRunes); start++ {
		startByte := len(string(sourceRunes[:start]))
		if startByte <= latestCorrection {
			continue
		}
		end := start + len(anchorRunes)
		if verifiedCoachAssertionAt(
			sourceRunes,
			anchor,
			anchorRunes,
			start,
			end,
			true,
		) {
			return false
		}
	}
	return true
}

func coachPrefixIsCorrectionLead(prefix string) bool {
	prefix = strings.Trim(prefix, " \t\r\n、,：:。.!！?？;；")
	for _, lead := range []string{
		"やっぱり", "やっぱ", "やはり", "いや", "いえ",
		"訂正すると", "訂正して", "訂正", "正しくは", "本当は",
		"actually", "correction",
	} {
		if prefix == lead {
			return true
		}
	}
	return false
}

func coachCorrectionMarkerAt(
	source string,
	position int,
	marker string,
) bool {
	if position < 0 || position+len(marker) > len(source) ||
		source[position:position+len(marker)] != marker ||
		coachTextPositionInsideQuote(source, position) {
		return false
	}
	remainder := source[position:]
	if (strings.HasPrefix(marker, "訂正") &&
		strings.HasPrefix(remainder, "訂正しません")) ||
		(strings.HasPrefix(marker, "撤回") &&
			strings.HasPrefix(remainder, "撤回しません")) ||
		(strings.HasPrefix(marker, "取り消") &&
			strings.HasPrefix(remainder, "取り消しません")) {
		return false
	}
	if marker == "訂正" {
		after := remainder[len(marker):]
		if after == "" ||
			!strings.ContainsRune(
				" \t\r\n、,：:。.!！?？;；",
				[]rune(after)[0],
			) ||
			strings.Trim(
				after,
				" \t\r\n、,：:。.!！?？;；",
			) == "" {
			return false
		}
	}
	switch marker {
	case "やっぱり", "やっぱ", "やはり", "いえ", "いや":
		prefix := strings.TrimRight(
			source[:position],
			" \t\r\n",
		)
		if prefix == "" {
			return true
		}
		for _, boundary := range []string{
			"。", ".", "！", "!", "？", "?", "；", ";", "、", ",", "：", ":",
		} {
			if strings.HasSuffix(prefix, boundary) {
				return true
			}
		}
		for _, assertionTail := range []string{
			"です", "でした", "だ", "だった", "ます", "ました",
		} {
			if strings.HasSuffix(prefix, assertionTail) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// authoritativeCoachAttemptText returns the person's actual answer view used
// by both the deterministic gate and the independent critic. It never trusts
// the planner-selected AnswerAttempt as the whole answer. The only removable
// prefix is a locally verified report of the question itself; reasons,
// examples, proxy ownership, and corrections remain visible.
func authoritativeCoachAttemptText(plan modelPlan, utterance string) string {
	return authoritativeCoachAttemptTextWithPolicy(plan, utterance, true)
}

func authoritativeCoachAttemptTextWithPolicy(
	plan modelPlan,
	utterance string,
	allowReportedQuestionPrefix bool,
) string {
	return authoritativeCoachAttemptTextForSubject(
		plan.AnswerContract.QuestionFrame.Subject,
		utterance,
		allowReportedQuestionPrefix,
	)
}

func authoritativeCoachAttemptTextForSubject(
	subject string,
	utterance string,
	allowReportedQuestionPrefix bool,
) string {
	source := collapseSpace(norm.NFKC.String(utterance))
	if source == "" || !allowReportedQuestionPrefix {
		return source
	}
	questionAnchor, ok := boundedCoachContinuityAnchor(subject, source)
	if !ok {
		return source
	}
	lower := strings.ToLower(source)
	// Lower-casing can change byte length for uncommon Unicode forms. Offsets
	// are used to slice the original normalized utterance, so fail closed
	// instead of risking removal of a different span.
	if len(lower) != len(source) {
		return source
	}
	questionStart, reportEnd, ok :=
		coachReportedQuestionSpanForSubjectBefore(
			lower,
			questionAnchor,
			len(lower),
		)
	if !ok ||
		!coachReportedQuestionLeadPrefixAllowed(source[:questionStart]) {
		return source
	}
	if reportEnd < 0 || reportEnd >= len(source) {
		return source
	}
	remainder := strings.TrimLeft(
		source[reportEnd:],
		" \t\r\n、,：:。.!！;；",
	)
	if remainder == "" {
		return source
	}
	return remainder
}

func coachReportedQuestionLeadPrefixAllowed(prefix string) bool {
	if coachPrefixIsOnlyFiller(prefix) {
		return true
	}
	lastBoundary := -1
	lastWidth := 0
	for _, boundary := range []string{
		"。", ".", "！", "!", "；", ";", "、", ",",
	} {
		if position := strings.LastIndex(prefix, boundary); position > lastBoundary {
			lastBoundary = position
			lastWidth = len(boundary)
		}
	}
	local := prefix
	if lastBoundary >= 0 {
		if !coachPrefixIsOnlyFiller(prefix[:lastBoundary]) {
			return false
		}
		local = prefix[lastBoundary+lastWidth:]
	}
	local = strings.TrimSpace(local)
	if local == "" {
		return false
	}
	for _, forbidden := range []string{
		"理由", "背景", "根拠", "説明", "先に", "結論",
		"答え", "回答", "というと", "言うと",
		"reason", "background", "first",
	} {
		if strings.Contains(local, forbidden) {
			return false
		}
	}
	if len([]rune(local)) <= 24 {
		for _, reporterLead := range []string{
			"上司に", "同僚に", "顧客に", "先生に", "面接官に",
			"相手に", "先方に", "チームに", "取引先に",
			"さんに", "氏に", "教授に", "部長に", "課長に",
		} {
			if strings.HasSuffix(local, reporterLead) {
				return true
			}
		}
	}
	for _, interrogative := range []string{
		"何", "なに", "どこ", "いつ", "誰", "だれ",
		"なぜ", "どう", "どれ", "どっち", "どちら",
		"what", "why", "where", "when", "who", "how", "which",
	} {
		if strings.Contains(strings.ToLower(local), interrogative) {
			return true
		}
	}
	return false
}

func coachReportedQuestionSubjectLeadAllowed(prefix string) bool {
	lastBoundary := -1
	lastWidth := 0
	for _, boundary := range []string{
		"。", ".", "！", "!", "？", "?", "；", ";",
	} {
		if position := strings.LastIndex(prefix, boundary); position > lastBoundary {
			lastBoundary = position
			lastWidth = len(boundary)
		}
	}
	local := prefix
	if lastBoundary >= 0 {
		local = prefix[lastBoundary+lastWidth:]
	}
	local = strings.TrimSpace(local)
	for _, repeatLead := range []string{
		"もう一度", "改めて", "あらためて", "再度",
	} {
		if local == repeatLead {
			return true
		}
	}
	return coachReportedQuestionLeadPrefixAllowed(local)
}

func coachReportedQuestionEndOutsideQuote(source string) int {
	if source == "" {
		return -1
	}
	lower := strings.ToLower(source)
	markers := []string{
		"について聞かれました", "に関して聞かれました",
		"のことを聞かれました", "について質問されました",
		"に関して質問されました", "について尋ねられました",
		"に関して尋ねられました", "のことを尋ねられました",
		"について問われました", "に関して問われました",
		"のことを問われました", "と尋ねられました",
		"って尋ねられました", "と聞かれました", "って聞かれました",
		"と質問されました", "って質問されました",
		"を聞かれました", "を質問されました", "を尋ねられました",
		"と問われました", "って問われました",
		"について聞かれた", "に関して聞かれた",
		"のことを聞かれた", "について質問された",
		"に関して質問された", "について尋ねられた",
		"に関して尋ねられた", "のことを尋ねられた",
		"について問われた", "に関して問われた",
		"のことを問われた", "と尋ねられた", "って尋ねられた",
		"と聞かれた", "って聞かれた",
		"と質問された", "って質問された",
		"を聞かれた", "を質問された", "を尋ねられた",
		"と問われた", "って問われた",
		"について聞かれ", "に関して聞かれ", "のことを聞かれ",
		"について質問され", "に関して質問され",
		"について尋ねられ", "に関して尋ねられ",
		"のことを尋ねられ", "について問われ", "に関して問われ",
		"のことを問われ", "と尋ねられ", "って尋ねられ",
		"と聞かれ", "って聞かれ", "と質問され", "って質問され",
		"を聞かれ", "を質問され", "を尋ねられ",
		"と問われ", "って問われ",
		"を問われました", "を問われた", "を問われ",
		"質問を受けました", "質問を受けた", "質問を受け",
		"訊かれました", "訊かれた", "訊かれ",
		"聞かれました", "聞かれた", "聞かれ",
		"尋ねられました", "尋ねられた", "尋ねられ",
		"問われました", "問われた", "問われ",
		"was asked", "asked me",
	}
	bestStart := -1
	bestEnd := -1
	for _, marker := range markers {
		searchFrom := 0
		for searchFrom < len(lower) {
			relative := strings.Index(lower[searchFrom:], marker)
			if relative < 0 {
				break
			}
			start := searchFrom + relative
			if !coachTextPositionInsideQuote(lower, start) &&
				(bestStart < 0 || start < bestStart ||
					start == bestStart && start+len(marker) > bestEnd) {
				bestStart = start
				bestEnd = start + len(marker)
			}
			searchFrom = start + len(marker)
		}
	}
	return bestEnd
}

func coachDirectQuestionEndOutsideQuote(source string) int {
	lower := strings.ToLower(collapseSpace(norm.NFKC.String(source)))
	for position := 0; position < len(lower); {
		nextASCII := strings.Index(lower[position:], "?")
		nextFull := strings.Index(lower[position:], "？")
		next := -1
		width := 0
		switch {
		case nextASCII >= 0 && (nextFull < 0 || nextASCII < nextFull):
			next = position + nextASCII
			width = len("?")
		case nextFull >= 0:
			next = position + nextFull
			width = len("？")
		default:
			return -1
		}
		if !coachTextPositionInsideQuote(lower, next) &&
			!coachQuestionMarkLocallyReported(lower, next+width) &&
			shouldRecoverOutsideCoach(lower[:next+width]) {
			return next + width
		}
		position = next + width
	}
	return -1
}

func coachQuestionMarkLocallyReported(source string, questionEnd int) bool {
	if questionEnd < 0 || questionEnd > len(source) {
		return true
	}
	clauseEnd := len(source)
scanClause:
	for position, current := range source[questionEnd:] {
		switch current {
		case '。', '.', '！', '!', '；', ';':
			absolute := questionEnd + position
			if !coachTextPositionInsideQuote(source, absolute) {
				clauseEnd = absolute
				break scanClause
			}
		}
	}
	local := strings.TrimSpace(source[questionEnd:clauseEnd])
	if local == "" {
		return false
	}
	local = strings.TrimLeft(local, " \t\r\n、,：:")
	lower := strings.ToLower(local)
	englishReport := strings.Contains(lower, "was asked") ||
		strings.Contains(lower, "asked me")
	japaneseConnector := strings.HasPrefix(local, "って") ||
		strings.HasPrefix(local, "を")
	if strings.HasPrefix(local, "と") {
		lexicalLead := false
		for _, lead := range []string{
			"ところ", "とりあえず", "ともかく", "とにかく",
			"とても", "とはいえ", "とは言え", "ときに",
		} {
			if strings.HasPrefix(local, lead) {
				lexicalLead = true
				break
			}
		}
		japaneseConnector = japaneseConnector || !lexicalLead
	}
	if !japaneseConnector && !englishReport {
		return false
	}
	reportEnd := coachReportedQuestionEndOutsideQuote(local)
	return reportEnd >= 0
}

func coachNewQuestionEndOutsideQuote(source string) int {
	if end := coachReportedQuestionEndOutsideQuote(source); end >= 0 {
		return end
	}
	return coachDirectQuestionEndOutsideQuote(source)
}

func coachPrefixIsOnlyFiller(prefix string) bool {
	prefix = strings.Trim(prefix, " \t\r\n、,：:。.!！?？")
	for prefix != "" {
		matched := false
		for _, filler := range []string{
			"えっと", "あの", "うーん", "まあ", "その",
			"well", "um", "uh",
		} {
			if prefix == filler {
				return true
			}
			if strings.HasPrefix(prefix, filler) {
				next := strings.TrimLeft(
					prefix[len(filler):],
					" \t\r\n、,：:",
				)
				if next != prefix[len(filler):] {
					prefix = next
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// containsExactCoachAnchor rejects a model-selected substring that is merely
// embedded in a longer word or number (A in AI, 2 in 2026). Japanese answer
// particles, copulas, reporting forms, and common counters remain valid so
// one-rune and one-number answers can still be spoken naturally.
func containsExactCoachAnchor(source string, anchor string) bool {
	if source == "" || anchor == "" {
		return false
	}
	sourceRunes := []rune(source)
	anchorRunes := []rune(anchor)
	if len(anchorRunes) == 0 || len(anchorRunes) > len(sourceRunes) {
		return false
	}
	for start := 0; start+len(anchorRunes) <= len(sourceRunes); start++ {
		if exactCoachAnchorAt(sourceRunes, anchorRunes, start) {
			return true
		}
	}
	return false
}

func hasExactCoachAnchorPrefix(source string, anchor string) bool {
	if source == "" || anchor == "" {
		return false
	}
	return exactCoachAnchorAt([]rune(source), []rune(anchor), 0)
}

func exactCoachAnchorAt(sourceRunes []rune, anchorRunes []rune, start int) bool {
	if len(anchorRunes) == 0 || start < 0 ||
		start+len(anchorRunes) > len(sourceRunes) {
		return false
	}
	end := start + len(anchorRunes)
	if string(sourceRunes[start:end]) != string(anchorRunes) ||
		coachRunePositionInsideQuote(sourceRunes, start) {
		return false
	}

	numericAnchor := coachDecimalAnchorPattern.MatchString(
		string(anchorRunes),
	)
	if !coachAnchorLeftBounded(sourceRunes, start) {
		return false
	}

	remainder := string(sourceRunes[end:])
	if coachAnchorRemainderRetractsOrQuestions(remainder) {
		return false
	}
	rightBounded := end == len(sourceRunes) ||
		!unicode.IsLetter(sourceRunes[end]) &&
			!unicode.IsNumber(sourceRunes[end])
	if !rightBounded {
		for _, suffix := range []string{
			"です", "でした", "である", "だった", "だ",
			"と答え", "と回答", "と言", "と伝え", "と思",
			"とする", "と考え", "という", "って",
		} {
			if strings.HasPrefix(remainder, suffix) {
				rightBounded = true
				break
			}
		}
		if numericAnchor && !rightBounded {
			for _, counter := range coachQuantityUnits {
				if strings.HasPrefix(remainder, counter) {
					tail := strings.TrimPrefix(remainder, counter)
					rightBounded = coachAnchorCounterTailBoundary(tail)
					if rightBounded {
						break
					}
				}
			}
		}
	}
	return rightBounded
}

func coachAnchorLeftBounded(sourceRunes []rune, start int) bool {
	if start < 0 || start > len(sourceRunes) {
		return false
	}
	if start == 0 ||
		!unicode.IsLetter(sourceRunes[start-1]) &&
			!unicode.IsNumber(sourceRunes[start-1]) {
		return true
	}
	prefix := strings.TrimRight(
		string(sourceRunes[:start]),
		" \t\r\n",
	)
	for _, lead := range []string{
		"やっぱり", "やっぱ", "やはり", "いや", "いえ",
		"正しくは", "本当は", "訂正",
	} {
		if strings.HasSuffix(prefix, lead) &&
			coachCorrectionMarkerAt(
				prefix,
				len(prefix)-len(lead),
				lead,
			) {
			return true
		}
	}
	switch sourceRunes[start-1] {
	case 'は', 'が', 'を', 'に', 'と', 'で', 'も', 'へ':
		relationPrefix := string(sourceRunes[:start-1])
		if sourceRunes[start-1] == 'は' {
			for _, actor := range []string{
				"私", "わたし", "自分", "僕", "ぼく", "俺", "おれ",
				"私たち", "わたしたち", "我々", "われわれ",
			} {
				if strings.HasSuffix(relationPrefix, actor) {
					return true
				}
			}
		}
		for _, suffix := range []string{
			"答え", "回答", "結論", "目的", "理由", "選択",
			"数量", "数字", "状態", "定義", "違い", "根拠", "手順",
			"こと",
		} {
			if strings.HasSuffix(relationPrefix, suffix) {
				return true
			}
		}
	}
	return false
}

func containsRetractedCoachAnchorOccurrence(
	source string,
	anchor string,
) bool {
	sourceRunes := []rune(source)
	anchorRunes := []rune(anchor)
	if len(anchorRunes) == 0 || len(anchorRunes) > len(sourceRunes) {
		return true
	}
	for start := 0; start+len(anchorRunes) <= len(sourceRunes); start++ {
		end := start + len(anchorRunes)
		if string(sourceRunes[start:end]) != anchor ||
			coachRunePositionInsideQuote(sourceRunes, start) ||
			!coachAnchorLeftBounded(sourceRunes, start) {
			continue
		}
		if coachAnchorRemainderRetractsOrQuestions(
			string(sourceRunes[end:]),
		) {
			return true
		}
	}
	return false
}

func coachAnchorRemainderRetractsOrQuestions(remainder string) bool {
	remainder = strings.TrimLeft(
		remainder,
		" \t\r\n、,：:。.!！",
	)
	for _, prefix := range []string{
		"?", "？", "ですか", "でしたか", "でしょうか", "なのか",
		"ではなく", "じゃなく", "でなく", "ではない", "じゃない",
		"ではありません", "is not", "was not", "not ",
	} {
		if strings.HasPrefix(remainder, prefix) {
			return true
		}
	}
	return false
}

func coachAnchorCounterTailBoundary(tail string) bool {
	if tail == "" || coachAnchorRemainderRetractsOrQuestions(tail) {
		return tail == ""
	}
	tailRunes := []rune(tail)
	if len(tailRunes) == 0 ||
		!unicode.IsLetter(tailRunes[0]) &&
			!unicode.IsNumber(tailRunes[0]) {
		return true
	}
	for _, suffix := range []string{
		"です", "でした", "である", "だった", "だ",
		"と答え", "と回答", "と言", "と伝え", "と思",
		"とする", "と考え", "という", "って",
	} {
		if strings.HasPrefix(tail, suffix) {
			return true
		}
	}
	return false
}

func coachTextPositionInsideQuote(source string, byteIndex int) bool {
	if byteIndex < 0 || byteIndex > len(source) {
		return true
	}
	return coachRunePositionInsideQuote(
		[]rune(source),
		utf8.RuneCountInString(source[:byteIndex]),
	)
}

func coachRunePositionInsideQuote(source []rune, position int) bool {
	if position < 0 || position > len(source) {
		return true
	}
	const maxPairedQuoteDepth = 16
	type quoteDepth struct {
		value    int
		overflow bool
	}
	open := func(depth *quoteDepth) {
		if depth.value >= maxPairedQuoteDepth {
			depth.overflow = true
			return
		}
		depth.value++
	}
	close := func(depth *quoteDepth) {
		if depth.overflow {
			return
		}
		if depth.value > 0 {
			depth.value--
		}
	}
	inside := func(depth quoteDepth) bool {
		return depth.value > 0 || depth.overflow
	}

	var japanese, japaneseDouble, lowDouble, germanDouble quoteDepth
	var germanSingle, curlyDouble, curlySingle quoteDepth
	var guillemet, singleGuillemet quoteDepth
	var lenticular, angle, doubleAngle, round, square quoteDepth
	var fullSquare, tortoise quoteDepth
	var asciiDouble, fullwidthDouble, asciiSingle, fullwidthSingle, backtick bool
	unknownQuote := false
	for index, current := range source[:position] {
		switch current {
		case '「':
			open(&japanese)
		case '」':
			close(&japanese)
		case '『':
			open(&japaneseDouble)
		case '』':
			close(&japaneseDouble)
		case '〝':
			open(&lowDouble)
		case '〟':
			close(&lowDouble)
		case '„':
			open(&germanDouble)
		case '‟':
			close(&germanDouble)
		case '‚':
			open(&germanSingle)
		case '‛':
			close(&germanSingle)
		case '«':
			open(&guillemet)
		case '»':
			close(&guillemet)
		case '‹':
			open(&singleGuillemet)
		case '›':
			close(&singleGuillemet)
		case '“':
			if inside(germanDouble) {
				close(&germanDouble)
			} else {
				open(&curlyDouble)
			}
		case '”':
			if inside(germanDouble) {
				close(&germanDouble)
			} else {
				close(&curlyDouble)
			}
		case '‘':
			if inside(germanSingle) {
				close(&germanSingle)
			} else {
				open(&curlySingle)
			}
		case '’':
			if inside(germanSingle) {
				close(&germanSingle)
			} else {
				close(&curlySingle)
			}
		case '【':
			open(&lenticular)
		case '】':
			close(&lenticular)
		case '〈':
			open(&angle)
		case '〉':
			close(&angle)
		case '《':
			open(&doubleAngle)
		case '》':
			close(&doubleAngle)
		case '（', '(':
			open(&round)
		case '）', ')':
			close(&round)
		case '[':
			open(&square)
		case ']':
			close(&square)
		case '［':
			open(&fullSquare)
		case '］':
			close(&fullSquare)
		case '〔':
			open(&tortoise)
		case '〕':
			close(&tortoise)
		case '"':
			asciiDouble = !asciiDouble
		case '＂':
			fullwidthDouble = !fullwidthDouble
		case '\'':
			if index > 0 && index+1 < len(source) &&
				(unicode.IsLetter(source[index-1]) ||
					unicode.IsNumber(source[index-1])) &&
				(unicode.IsLetter(source[index+1]) ||
					unicode.IsNumber(source[index+1])) {
				continue
			}
			asciiSingle = !asciiSingle
		case '＇':
			fullwidthSingle = !fullwidthSingle
		case '`':
			backtick = !backtick
		default:
			if quoteMarks := unicode.Properties["Quotation_Mark"]; quoteMarks != nil && unicode.Is(quoteMarks, current) {
				// Unknown quotation marks fail closed from their first use.
				// This avoids treating text inside a newly introduced Unicode
				// quote style as the person's own assertion.
				unknownQuote = true
			}
		}
	}
	return inside(japanese) || inside(japaneseDouble) || inside(lowDouble) ||
		inside(germanDouble) || inside(germanSingle) ||
		inside(curlyDouble) || inside(curlySingle) ||
		inside(guillemet) || inside(singleGuillemet) ||
		inside(lenticular) || inside(angle) || inside(doubleAngle) ||
		inside(round) || inside(square) || inside(fullSquare) ||
		inside(tortoise) ||
		asciiDouble || fullwidthDouble ||
		asciiSingle || fullwidthSingle || backtick || unknownQuote
}

func (agent *vertexAgent) coachAttemptContinuity(
	frame PendingAnswerFrame,
	plan modelPlan,
	utterance string,
) (bool, bool) {
	if agent == nil || !frame.Active || plan.RespondentStage != "restructure" {
		return false, false
	}
	if coachNewQuestionEndOutsideQuote(utterance) >= 0 {
		// Every coaching phase is scoped to its earlier question. A newly
		// reported question must be handled as a new focus, including while
		// the person is answering the one bounded expansion question.
		return false, false
	}
	if frame.Phase == respondent.CoachPhaseExpanding &&
		explicitExpansionReferenceInUtterance(
			authoritativeCoachOperator(frame),
			plan.AnswerAttempt,
			utterance,
		) {
		return true, true
	}
	if frame.ContinuityTag == "" {
		return false, false
	}
	if frame.QuestionContinuityTag != "" {
		anchor, ok := boundedCoachTargetCandidate(plan, utterance)
		if !ok {
			return false, false
		}
		_, linked := agent.utteranceLinksCoachQuestionTag(
			frame.QuestionContinuityTag,
			anchor,
			utterance,
		)
		if !linked &&
			coachUtteranceHasNamedAnswerSubject(utterance, anchor) {
			return false, false
		}
	}
	plan = agent.coachVerificationPlanForTurn(frame, plan, utterance)
	_, fingerprint, ok := boundedCoachAnswerFingerprint(
		plan,
		utterance,
		false,
		false,
	)
	if !ok || !hmac.Equal(
		[]byte(frame.ContinuityTag),
		[]byte(agent.coachContinuityTag(fingerprint)),
	) {
		return false, false
	}
	return true, true
}

func coachUtteranceHasNamedAnswerSubject(
	utterance string,
	anchor string,
) bool {
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	anchor = strings.ToLower(collapseSpace(norm.NFKC.String(anchor)))
	if source == "" || anchor == "" {
		return true
	}
	searchFrom := 0
	for searchFrom < len(source) {
		relative := strings.Index(source[searchFrom:], anchor)
		if relative < 0 {
			break
		}
		position := searchFrom + relative
		if coachTextPositionInsideQuote(source, position) {
			searchFrom = position + len(anchor)
			continue
		}
		clauseStart := 0
		for index, current := range source[:position] {
			switch current {
			case '。', '.', '！', '!', '？', '?', '；', ';':
				if !coachTextPositionInsideQuote(source, index) {
					clauseStart = index + len(string(current))
				}
			}
		}
		prefix := strings.TrimRight(
			source[clauseStart:position],
			" \t\r\n、,：:",
		)
		softSegments := strings.FieldsFunc(
			source[clauseStart:position],
			func(current rune) bool {
				switch current {
				case '、', ',', '：', ':':
					return true
				default:
					return false
				}
			},
		)
		for segmentIndex := 0; segmentIndex+1 < len(softSegments); segmentIndex++ {
			if coachSegmentHasNamedAnswerSubject(
				softSegments[segmentIndex],
			) {
				return true
			}
		}
		for _, relation := range []string{
			"についての答えは", "に関する答えは",
			"ことについての答えは", "についてですが",
			"に関してですが", "のことですが", "については",
			"に関しては", "について", "に関して",
			"への答えは", "の答えは", "の回答は",
			"の結論は", "の見解は", "目的は", "答えは",
			"回答は", "結論は", "ことは", "としては", "は",
		} {
			if !strings.HasSuffix(prefix, relation) {
				continue
			}
			ownerLead := strings.TrimSpace(
				strings.TrimSuffix(prefix, relation),
			)
			ownerStart := 0
			for index, current := range ownerLead {
				switch current {
				case '、', ',', '：', ':':
					ownerStart = index + len(string(current))
				}
			}
			owner := strings.TrimSpace(ownerLead[ownerStart:])
			if owner == "" ||
				coachTextEndsWithSelfActor(owner) ||
				coachTextEndsWithEmbeddedSelfActor(owner) {
				break
			}
			for _, filler := range []string{
				"まず", "今", "いま", "ここ", "それで", "では",
				"理由", "背景", "根拠", "条件", "結果",
			} {
				if owner == filler {
					owner = ""
					break
				}
			}
			if owner != "" {
				return true
			}
			break
		}
		searchFrom = position + len(anchor)
	}
	return false
}

func coachSegmentHasNamedAnswerSubject(segment string) bool {
	segment = strings.TrimSpace(segment)
	for _, relation := range []string{
		"についての答えは", "に関する答えは",
		"ことについての答えは", "についてですが",
		"に関してですが", "のことですが", "については",
		"に関しては", "について", "に関して", "とは",
		"への答えは", "の答えは", "の回答は",
		"の結論は", "の見解は", "目的は", "答えは",
		"回答は", "結論は", "ことは", "としては", "は",
	} {
		if !strings.HasSuffix(segment, relation) {
			continue
		}
		owner := strings.TrimSpace(strings.TrimSuffix(segment, relation))
		if owner == "" ||
			coachTextEndsWithSelfActor(owner) ||
			coachTextEndsWithEmbeddedSelfActor(owner) {
			return false
		}
		switch owner {
		case "まず", "今", "いま", "ここ", "それで", "では",
			"理由", "背景", "根拠", "条件", "結果":
			return false
		default:
			return true
		}
	}
	return false
}

func (agent *vertexAgent) coachVerificationPlanForTurn(
	frame PendingAnswerFrame,
	plan modelPlan,
	utterance string,
) modelPlan {
	if agent == nil || frame.QuestionContinuityTag == "" {
		return plan
	}
	anchor, ok := boundedCoachTargetCandidate(plan, utterance)
	if !ok {
		return plan
	}
	questionSubject, linked := agent.utteranceLinksCoachQuestionTag(
		frame.QuestionContinuityTag,
		anchor,
		utterance,
	)
	if linked {
		plan.AnswerContract.QuestionFrame.Subject = questionSubject
	} else {
		// A model-selected raw subject has no authority once a question HMAC
		// exists. Only a current-turn HMAC match may replace the fixed label.
		plan.AnswerContract.QuestionFrame.Subject = frame.Subject
	}
	return plan
}

// bindFirstCoachAnswer binds the first A the person actually says after an
// awaiting-answer prompt. The raw A is never retained. If that first attempt
// puts A later, the tag lets only the same A authorize the requested
// restatement on the next turn.
func (agent *vertexAgent) bindFirstCoachAnswer(
	frame PendingAnswerFrame,
	plan modelPlan,
	utterance string,
) PendingAnswerFrame {
	bound, _ := agent.bindFirstCoachAnswerForTurn(frame, plan, utterance)
	return bound
}

// bindFirstCoachAnswerForTurn also returns the current-turn verification plan
// whose subject is the raw, HMAC-matched question subject. That subject exists
// only on this stack while the just-bound A is checked; PendingAnswerFrame
// retains only the canonical operator subject and non-reversible tags.
func (agent *vertexAgent) bindFirstCoachAnswerForTurn(
	frame PendingAnswerFrame,
	plan modelPlan,
	utterance string,
) (PendingAnswerFrame, modelPlan) {
	if agent == nil ||
		!frame.Active ||
		frame.Phase != respondent.CoachPhaseAwaitingAnswer ||
		frame.ContinuityTag != "" ||
		plan.RespondentStage != "restructure" {
		return frame, plan
	}
	if coachNewQuestionEndOutsideQuote(utterance) >= 0 {
		// A stored subject HMAC identifies the earlier question, not a later
		// instance that happens to use the same words. Keep the old commitment
		// unadvanced and let the current reported question establish a fresh
		// scope on a subsequent intentional turn.
		return frame, plan
	}
	anchor, ok := boundedCoachTargetCandidate(plan, utterance)
	if !ok {
		return frame, plan
	}
	questionSubject, questionLinked := agent.utteranceLinksCoachQuestionTag(
		frame.QuestionContinuityTag,
		anchor,
		utterance,
	)
	if !questionLinked && frame.QuestionContinuityTag != "" &&
		!coachUtteranceHasNamedAnswerSubject(utterance, anchor) {
		for _, plannedSubject := range boundedCoachPlanQuestionAnchors(
			plan.AnswerContract.QuestionFrame.Subject,
		) {
			if hmac.Equal(
				[]byte(frame.QuestionContinuityTag),
				[]byte(agent.coachQuestionContinuityTag(plannedSubject)),
			) {
				questionSubject = plannedSubject
				questionLinked = true
				break
			}
		}
		if !questionLinked {
			// The active authenticated frame itself supplies question authority.
			// The generic subject path is limited to a bare, personally-owned A;
			// the fingerprint gate below still rejects quotations, proxies,
			// corrections, retractions, and unrelated named subjects.
			questionSubject = frame.Subject
			questionLinked = true
		}
	}
	if !questionLinked &&
		frame.QuestionContinuityTag == "" &&
		explicitReportedQuestionAndOwnAttempt(utterance) {
		currentQuestionAnchor, currentQuestionOK :=
			boundedCoachContinuityAnchor(
				plan.AnswerContract.QuestionFrame.Subject,
				utterance,
			)
		if currentQuestionOK {
			questionSubject, questionLinked =
				agent.utteranceLinksCoachQuestionTag(
					agent.coachQuestionContinuityTag(currentQuestionAnchor),
					anchor,
					utterance,
				)
		}
	}
	if !questionLinked {
		return frame, plan
	}
	verificationPlan := plan
	verificationPlan.AnswerContract.QuestionFrame.Subject = questionSubject
	verifiedAnchor, fingerprint, ok := boundedCoachAnswerFingerprint(
		verificationPlan,
		utterance,
		false,
		true,
	)
	if !ok || verifiedAnchor != anchor {
		return frame, plan
	}
	frame.ContinuityTag = agent.coachContinuityTag(fingerprint)
	return frame, verificationPlan
}

func explicitExpansionReference(
	operator respondent.Operator,
	answer string,
) bool {
	answer = strings.ToLower(collapseSpace(norm.NFKC.String(answer)))
	for _, prefix := range coachExpansionPrefixes(operator) {
		if strings.HasPrefix(answer, prefix) {
			remainder := strings.TrimLeft(
				answer[len(prefix):],
				" \t\r\n、,：:。.!！?？",
			)
			for _, current := range remainder {
				if unicode.IsLetter(current) || unicode.IsNumber(current) {
					return true
				}
			}
		}
	}
	return false
}

func coachExpansionPrefixes(operator respondent.Operator) []string {
	switch operator {
	case respondent.OperatorEvidence:
		return []string{
			"その根拠は", "それを支える根拠は",
			"the evidence for that answer is",
		}
	case respondent.OperatorState:
		return []string{
			"その最初の一歩は",
			"the first step for that answer is",
		}
	default:
		return []string{
			"その理由は", "それを支える理由は",
			"the reason for that answer is",
		}
	}
}

func explicitExpansionReferenceInUtterance(
	operator respondent.Operator,
	answer string,
	utterance string,
) bool {
	answer = strings.ToLower(collapseSpace(norm.NFKC.String(answer)))
	source := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	if !explicitExpansionReference(operator, answer) ||
		source == "" ||
		coachSpanContainsQuoteDelimiter([]rune(answer)) {
		return false
	}
	const maxOccurrences = 32
	searchFrom := 0
	for seen := 0; seen < maxOccurrences && searchFrom < len(source); seen++ {
		relative := strings.Index(source[searchFrom:], answer)
		if relative < 0 {
			return false
		}
		start := searchFrom + relative
		if !coachTextPositionInsideQuote(source, start) {
			return true
		}
		searchFrom = start + len(answer)
	}
	return false
}

func coachSpanContainsQuoteDelimiter(span []rune) bool {
	for index, current := range span {
		switch current {
		case '「', '」', '『', '』', '〝', '〟', '„', '‟', '‚', '‛',
			'“', '”', '‘', '’',
			'«', '»', '‹', '›',
			'【', '】', '〈', '〉', '《', '》', '（', '）',
			'(', ')', '[', ']', '［', '］', '〔', '〕',
			'"', '＂', '＇', '`':
			return true
		case '\'':
			if index > 0 && index+1 < len(span) &&
				(unicode.IsLetter(span[index-1]) ||
					unicode.IsNumber(span[index-1])) &&
				(unicode.IsLetter(span[index+1]) ||
					unicode.IsNumber(span[index+1])) {
				continue
			}
			return true
		default:
			if quoteMarks := unicode.Properties["Quotation_Mark"]; quoteMarks != nil && unicode.Is(quoteMarks, current) {
				return true
			}
		}
	}
	return false
}

func explicitReportedQuestionAndOwnAttempt(utterance string) bool {
	lower := strings.ToLower(collapseSpace(norm.NFKC.String(utterance)))
	reportEnd := coachReportedQuestionEndOutsideQuote(lower)
	if reportEnd < 0 || reportEnd >= len(lower) {
		return false
	}
	afterReport := lower[reportEnd:]
	for _, signal := range []string{
		"私は", "わたしは", "自分は", "僕は", "ぼくは",
		"私の答えは", "自分の答えは", "答えとして", "回答として",
		"my answer is", "i answered", "i replied", "i would answer",
	} {
		searchFrom := 0
		for searchFrom < len(afterReport) {
			relative := strings.Index(afterReport[searchFrom:], signal)
			if relative < 0 {
				break
			}
			start := reportEnd + searchFrom + relative
			if !coachTextPositionInsideQuote(lower, start) {
				return true
			}
			searchFrom += relative + len(signal)
		}
	}
	return false
}

func foregroundCanStartCoach(turn VoiceTurn, plan modelPlan) bool {
	if !turn.Foreground || !turn.Ambient || turn.PDF != nil ||
		plan.AssistanceTarget != "respondent" ||
		plan.RespondentStage != "restructure" ||
		plan.ResearchAction != "none" ||
		!explicitReportedQuestionAndOwnAttempt(turn.Utterance) {
		return false
	}
	_, ok := boundedCoachContinuityAnchor(
		plan.AnswerContract.QuestionFrame.Subject,
		turn.Utterance,
	)
	return ok
}
