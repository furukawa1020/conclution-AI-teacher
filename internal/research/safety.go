package research

import (
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
)

var (
	doiPrefixPattern = regexp.MustCompile(`(?i)^10\.[0-9]{4,9}/`)
	emailPattern     = regexp.MustCompile(
		`(?i)\b[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+\b`,
	)
	credentialAssignmentPattern = regexp.MustCompile(
		`(?i)\b(?:api[_ -]?key|client[_ -]?secret|password|passwd|authorization|bearer|access[_ -]?token|refresh[_ -]?token|private[_ -]?key)\b\s*(?::|=|is|は)\s*\S+`,
	)
	knownSecretPattern = regexp.MustCompile(
		`(?i)(?:\bAIza[0-9A-Za-z_-]{30,}\b|\bsk-[0-9A-Za-z_-]{16,}\b|\bgh[pousr]_[0-9A-Za-z]{20,}\b|\bxox[baprs]-[0-9A-Za-z-]{10,}\b)`,
	)
	jwtPattern = regexp.MustCompile(
		`(?i)\beyj[0-9A-Za-z_-]{8,}\.[0-9A-Za-z_-]{8,}\.[0-9A-Za-z_-]{8,}\b`,
	)
	japanesePhonePattern = regexp.MustCompile(
		`(?:\+81[\s()-]*(?:0[\s()-]*)?[1-9][0-9\s()-]{7,12}[0-9]|(?:^|[^0-9])0[1-9][0-9\s()-]{7,12}[0-9](?:$|[^0-9]))`,
	)
	postalCodePattern = regexp.MustCompile(
		`(?:〒\s*)?\b[0-9]{3}-[0-9]{4}\b`,
	)
	longDigitPattern = regexp.MustCompile(
		`(?:^|[^0-9])[0-9][0-9 -]{10,23}[0-9](?:$|[^0-9])`,
	)
	orcidPattern = regexp.MustCompile(
		`^[0-9]{4}-[0-9]{4}-[0-9]{4}-[0-9]{3}[0-9X]$`,
	)
	personalCasePattern = regexp.MustCompile(
		`[一-龯々ぁ-んァ-ヶー]{2,12}の(?:症状|診断|病気|うつ病|服薬|治療|病歴|ADHD|PTSD)`,
	)
	healthContextPattern = regexp.MustCompile(
		`(?i)(?:ADHD|PTSD|depression|bipolar|schizophrenia|autism|` +
			`diagnosis|diagnosed|symptoms?|うつ病|鬱|統合失調|双極|` +
			`自閉|発達障害|診断|症状|服薬|治療|病歴)`,
	)
	japaneseNameLikePattern = regexp.MustCompile(
		`(?:^|[/\s「『"'（(._・-])[一-龯々ぁ-んァ-ヶー]{2,12}` +
			`(?:さん|氏|先生)?(?:$|[/\s」』"'、,）)._・-])`,
	)
	latinFullNamePattern = regexp.MustCompile(
		`(?:^|[^A-Za-z])(?:` +
			`[A-Z][a-z]{1,30}(?:[-'][A-Z]?[a-z]{1,30})?` +
			`\s+[A-Z][a-z]{1,30}(?:[-'][A-Z]?[a-z]{1,30})?` +
			`|[A-Z]{2,30}\s+[A-Z]{2,30}` +
			`)(?:$|[^A-Za-z])`,
	)
	commonJapaneseNamePattern = regexp.MustCompile(
		`(?:^|[\s「『"'（(])(?:佐藤|鈴木|高橋|田中|伊藤|渡辺|山本|中村|` +
			`小林|加藤|吉田|山田|佐々木|山口|松本|井上|木村|斎藤|清水|` +
			`山崎|森|池田|橋本|阿部|石川|山下|中島|前田|藤田|小川|` +
			`後藤|岡田|長谷川|村上|近藤|石井|坂本|遠藤|青木|藤井|` +
			`西村|福田|太田|三浦|藤原|岡本|松田|中川|中野|原田)` +
			`[一-龯々ぁ-んァ-ヶー]{1,8}(?:$|[\s」』"'、,）)])`,
	)
	commonLatinNamePattern = regexp.MustCompile(
		`(?i)(?:^|[^a-z])(?:john|jane|james|mary|robert|patricia|michael|` +
			`jennifer|william|linda|david|elizabeth|richard|barbara|` +
			`joseph|susan|thomas|jessica|charles|sarah|christopher|` +
			`karen|daniel|nancy|matthew|lisa|anthony|betty|mark|` +
			`sandra|donald|ashley|steven|kimberly|paul|emily|andrew|` +
			`alice|bob)\s+[a-z][a-z'-]{1,30}(?:$|[^a-z])`,
	)
	healthPersonSlugPattern = regexp.MustCompile(
		`(?i)(?:^|[/\s])(?:[\p{L}]{1,30}[-_.・\s]+){1,4}` +
			`(?:adhd|ptsd|depression|bipolar|schizophrenia|autism)` +
			`(?:$|[^\p{L}])`,
	)
	compactJapaneseTopicPattern = regexp.MustCompile(
		`^[一-龯々ぁ-んァ-ヶー]{2,12}$`,
	)
	latinShortPhrasePattern = regexp.MustCompile(
		`(?i)^[\p{L}][\p{L}'-]{1,30}` +
			`(?:\s+[\p{L}][\p{L}'-]{1,30}){1,2}$`,
	)
)

var abstractBoundaryElements = map[string]struct{}{
	"address": {}, "article": {}, "aside": {}, "blockquote": {}, "br": {},
	"dd": {}, "div": {}, "dl": {}, "dt": {}, "figcaption": {}, "figure": {},
	"footer": {}, "h1": {}, "h2": {}, "h3": {}, "h4": {}, "h5": {},
	"h6": {}, "header": {}, "hr": {}, "li": {}, "main": {}, "nav": {},
	"ol": {}, "p": {}, "pre": {}, "section": {}, "table": {}, "td": {},
	"th": {}, "tr": {}, "ul": {},
}

// NewDOIQuery constructs a minimal explicit DOI lookup.
func NewDOIQuery(rawDOI string) (Query, error) {
	query := Query{Kind: QueryDOI, DOI: rawDOI, Limit: 1}
	return NormalizeQuery(query)
}

// NewRecentTopicQuery constructs a bounded Crossref indexed-date search.
func NewRecentTopicQuery(
	topic string,
	from time.Time,
	until time.Time,
	limit int,
) (Query, error) {
	query := Query{
		Kind:  QueryRecentTopic,
		Topic: topic,
		From:  from,
		Until: until,
		Limit: limit,
	}
	return NormalizeQuery(query)
}

// NormalizeQuery validates, minimizes and screens text before any outbound
// request. A topic can still be sensitive in meaning even after this syntactic
// screen; callers must obtain consent for sensitive research domains.
func NormalizeQuery(query Query) (Query, error) {
	switch query.Kind {
	case QueryDOI:
		if query.Topic != "" || !query.From.IsZero() || !query.Until.IsZero() {
			return Query{}, ErrInvalidQuery
		}
		if likelySensitive(query.DOI) {
			return Query{}, ErrSensitiveQuery
		}
		doi, err := NormalizeDOI(query.DOI)
		if err != nil {
			return Query{}, err
		}
		query.DOI = doi
		if likelySensitive(query.DOI) {
			return Query{}, ErrSensitiveQuery
		}
		query.Limit = 1
		return query, nil

	case QueryRecentTopic:
		if query.DOI != "" ||
			!utf8.ValidString(query.Topic) ||
			query.Limit < 1 ||
			query.Limit > MaxResults ||
			query.From.IsZero() ||
			query.Until.IsZero() {
			return Query{}, ErrInvalidQuery
		}
		query.Topic = cleanText(query.Topic)
		if query.Topic == "" ||
			utf8.RuneCountInString(query.Topic) > MaxTopicRunes {
			return Query{}, ErrInvalidQuery
		}
		if likelySensitive(query.Topic) {
			return Query{}, ErrSensitiveQuery
		}
		query.From = startOfUTCDay(query.From)
		query.Until = startOfUTCDay(query.Until)
		if query.Until.Before(query.From) ||
			query.Until.Sub(query.From) > MaxRecentInterval {
			return Query{}, ErrInvalidQuery
		}
		return query, nil

	default:
		return Query{}, ErrInvalidQuery
	}
}

// NormalizeDOI accepts a bare DOI, doi: form, or doi.org URL and returns the
// DOI's case-insensitive canonical form. It intentionally rejects query and
// fragment delimiters so a DOI cannot alter an outbound API request.
func NormalizeDOI(rawDOI string) (string, error) {
	if !utf8.ValidString(rawDOI) {
		return "", ErrInvalidQuery
	}
	value := strings.TrimSpace(rawDOI)
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "doi:"):
		value = strings.TrimSpace(value[len("doi:"):])
		if strings.Contains(value, "%") {
			return "", ErrInvalidQuery
		}
	case strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "http://"):
		parsed, err := url.Parse(value)
		if err != nil ||
			parsed.User != nil ||
			(parsed.Scheme != "https" && parsed.Scheme != "http") ||
			(!strings.EqualFold(parsed.Hostname(), "doi.org") &&
				!strings.EqualFold(parsed.Hostname(), "dx.doi.org")) ||
			parsed.Port() != "" ||
			parsed.RawQuery != "" ||
			parsed.Fragment != "" {
			return "", ErrInvalidQuery
		}
		escapedPath := strings.TrimPrefix(parsed.EscapedPath(), "/")
		value, err = url.PathUnescape(escapedPath)
		if err != nil {
			return "", ErrInvalidQuery
		}
		if strings.Contains(value, "%") {
			return "", ErrInvalidQuery
		}
	default:
		if strings.Contains(value, "%") {
			return "", ErrInvalidQuery
		}
	}

	value = strings.TrimSpace(value)
	if value == "" ||
		utf8.RuneCountInString(value) > MaxDOIRunes ||
		!doiPrefixPattern.MatchString(value) {
		return "", ErrInvalidQuery
	}
	for _, char := range value {
		if unicode.IsSpace(char) ||
			unicode.IsControl(char) ||
			char == '?' ||
			char == '#' ||
			char == '\\' {
			return "", ErrInvalidQuery
		}
	}
	return strings.ToLower(value), nil
}

func likelySensitive(value string) bool {
	if likelySensitiveLiteral(value) {
		return true
	}
	decoded := value
	for depth := 0; depth < 4; depth++ {
		next, err := url.PathUnescape(decoded)
		if err != nil || next == decoded {
			return false
		}
		if likelySensitiveLiteral(next) {
			return true
		}
		decoded = next
	}
	// Excessively nested encoding is not needed for a DOI lookup and is
	// rejected rather than risk sending a reversible secret representation.
	return strings.Contains(decoded, "%")
}

func likelySensitiveLiteral(value string) bool {
	if emailPattern.MatchString(value) ||
		credentialAssignmentPattern.MatchString(value) ||
		knownSecretPattern.MatchString(value) ||
		jwtPattern.MatchString(value) ||
		japanesePhonePattern.MatchString(value) ||
		postalCodePattern.MatchString(value) ||
		strings.Contains(value, "-----BEGIN PRIVATE KEY-----") ||
		strings.Contains(value, "-----BEGIN RSA PRIVATE KEY-----") ||
		likelyPersonalResearchContext(value) {
		return true
	}

	for _, candidate := range longDigitPattern.FindAllString(value, -1) {
		digits := digitsOnly(candidate)
		if len(digits) >= 12 || (len(digits) >= 13 && luhnValid(digits)) {
			return true
		}
	}
	return false
}

func likelyPersonalResearchContext(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"私の", "わたしの", "僕の", "自分の", "患者名", "患者id", "症例番号",
		"カルテ", "病歴", "服薬歴", "氏名", "本名", "住所", "勤務先",
		"学校名", "学籍番号", "生年月日", "マイナンバー",
		"さんの症状", "氏の症状", "先生の症状",
		"my symptoms", "my diagnosis", "my patient", "patient named",
		"medical record", "case id", "case number", "full name",
		"home address", "date of birth", "student id", "employee id",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if personalCasePattern.MatchString(value) {
		return true
	}
	if commonJapaneseNamePattern.MatchString(value) ||
		commonLatinNamePattern.MatchString(value) {
		return true
	}
	if healthContextPattern.MatchString(value) &&
		(japaneseNameLikePattern.MatchString(value) ||
			latinFullNamePattern.MatchString(value) ||
			healthPersonSlugPattern.MatchString(value)) {
		return true
	}
	return likelyStandalonePersonName(value)
}

func likelyStandalonePersonName(value string) bool {
	candidate := strings.Trim(
		strings.TrimSpace(value),
		"「」『』\"'()（）",
	)
	if candidate == "" ||
		strings.ContainsAny(candidate, "/:@") {
		return false
	}

	academicTopic := containsAcademicTopicMarker(candidate)
	if fullName := latinFullNamePattern.FindString(candidate); fullName != "" &&
		(fullName != candidate || !academicTopic) {
		return true
	}
	compact := strings.NewReplacer(
		" ", "",
		"　", "",
		"・", "",
		"-", "",
		".", "",
	).Replace(candidate)
	if compactJapaneseTopicPattern.MatchString(compact) && !academicTopic {
		return true
	}
	return latinShortPhrasePattern.MatchString(candidate) && !academicTopic
}

func containsAcademicTopicMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"理論", "解析", "分析", "学習", "記憶", "認知", "言語", "処理",
		"計算", "量子", "細胞", "遺伝", "疾患", "症候群", "治療", "研究",
		"技術", "手法", "モデル", "システム", "ネットワーク", "アルゴリズム",
		"エラー", "訂正", "評価", "効果", "機構", "化学", "物理", "数学",
		"医学", "教育", "支援", "障害", "環境", "経済", "政策", "材料",
		"電池", "宇宙", "地震", "睡眠", "感染", "免疫", "疼痛",
		"quantum", "comput", "learning", "memory", "language", "model",
		"network", "algorithm", "analysis", "theory", "method", "system",
		"physics", "chemistry", "biology", "medicine", "education",
		"policy", "econom", "material", "battery", "climate", "error",
		"retrieval", "augment", "generation", "cryptograph", "coding",
		"research", "topic", "agentic", "fraud", "detect",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func digitsOnly(value string) string {
	var result strings.Builder
	for _, char := range value {
		if char >= '0' && char <= '9' {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func luhnValid(value string) bool {
	if len(value) < 13 || len(value) > 19 {
		return false
	}
	sum := 0
	parity := len(value) % 2
	for index, char := range value {
		digit := int(char - '0')
		if index%2 == parity {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
	}
	return sum%10 == 0
}

func startOfUTCDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func canonicalDOIURL(doi string) string {
	return (&url.URL{
		Scheme: "https",
		Host:   "doi.org",
		Path:   "/" + doi,
	}).String()
}

func cleanText(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, char := range value {
		switch {
		case unicode.IsControl(char) && !unicode.IsSpace(char):
			continue
		case unicode.IsSpace(char):
			result.WriteByte(' ')
		default:
			result.WriteRune(char)
		}
	}
	return strings.Join(strings.Fields(result.String()), " ")
}

func plainTextAbstract(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", false
	}

	tokenizer := xhtml.NewTokenizer(strings.NewReader(raw))
	var result strings.Builder
	skipDepth := 0
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case xhtml.ErrorToken:
			text := cleanText(html.UnescapeString(result.String()))
			runes := []rune(text)
			if len(runes) > MaxAbstractRunes {
				return string(runes[:MaxAbstractRunes]), true
			}
			return text, false

		case xhtml.StartTagToken:
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
			if skipDepth > 0 {
				skipDepth++
				continue
			}
			if name == "script" ||
				name == "style" ||
				name == "template" ||
				name == "noscript" {
				skipDepth = 1
				continue
			}
			if _, boundary := abstractBoundaryElements[name]; boundary {
				result.WriteByte(' ')
			}

		case xhtml.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			if _, boundary := abstractBoundaryElements[strings.ToLower(string(name))]; boundary {
				result.WriteByte(' ')
			}

		case xhtml.EndTagToken:
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			if _, boundary := abstractBoundaryElements[name]; boundary {
				result.WriteByte(' ')
			}

		case xhtml.TextToken:
			if skipDepth == 0 {
				result.Write(tokenizer.Text())
				result.WriteByte(' ')
			}
		}
	}
}

func normalizeORCID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "https://orcid.org/")
	value = strings.TrimPrefix(value, "http://orcid.org/")
	value = strings.ToUpper(value)
	if !orcidPattern.MatchString(value) {
		return ""
	}
	return "https://orcid.org/" + value
}

func normalizedDateParts(parts []int) (NormalizedDate, bool) {
	if len(parts) < 1 || parts[0] < 1000 || parts[0] > 9999 {
		return NormalizedDate{}, false
	}
	year := parts[0]
	if len(parts) == 1 {
		return NormalizedDate{
			Value:     strconv.Itoa(year),
			Precision: PrecisionYear,
		}, true
	}
	month := parts[1]
	if month < 1 || month > 12 {
		return NormalizedDate{}, false
	}
	if len(parts) == 2 {
		return NormalizedDate{
			Value:     strconv.Itoa(year) + "-" + twoDigits(month),
			Precision: PrecisionMonth,
		}, true
	}
	day := parts[2]
	if day < 1 || day > 31 {
		return NormalizedDate{}, false
	}
	candidate := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if candidate.Year() != year ||
		int(candidate.Month()) != month ||
		candidate.Day() != day {
		return NormalizedDate{}, false
	}
	return NormalizedDate{
		Value: strconv.Itoa(year) + "-" + twoDigits(month) + "-" +
			twoDigits(day),
		Precision: PrecisionDay,
	}, true
}

func twoDigits(value int) string {
	if value < 10 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}
