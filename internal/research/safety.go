package research

import (
	"bytes"
	"encoding/base32"
	"encoding/base64"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
	"golang.org/x/text/unicode/norm"
)

var (
	doiPrefixPattern   = regexp.MustCompile(`(?i)^10\.[0-9]{4,9}/`)
	doiAnywherePattern = regexp.MustCompile(
		`(?i)10\.[0-9]{4,9}/`,
	)
	emailPattern = regexp.MustCompile(
		`(?i)\b[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+\b`,
	)
	credentialAssignmentPattern = regexp.MustCompile(
		`(?i)\b(?:api[_ -]?key|client[_ -]?secret|password|passwd|authorization|bearer|access[_ -]?token|refresh[_ -]?token|private[_ -]?key)\b\s*(?::|=|is|は)\s*\S+`,
	)
	credentialLoosePattern = regexp.MustCompile(
		`(?i)\b(?:api[_ -]?key|client[_ -]?secret|password|passwd|` +
			`authorization|bearer|access[_ -]?token|refresh[_ -]?token|` +
			`private[_ -]?key)\b(?:\s+|[-_.])` +
			`([^\s,;、。！？!?]+)`,
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
	phoneContextPattern = regexp.MustCompile(
		`(?i)(?:phone|telephone|mobile|cell|tel|fax|電話|連絡先)` +
			`(?:\s*[:：=-]\s*|\s+)(?:\+?[0-9][0-9\s().-]{7,16}[0-9])`,
	)
	northAmericanPhonePattern = regexp.MustCompile(
		`(?:^|[^0-9])(?:\+?1[\s().-]*)?` +
			`[2-9][0-9]{2}[\s().-]*[2-9][0-9]{2}[\s().-]*[0-9]{4}` +
			`(?:$|[^0-9])`,
	)
	postalCodePattern = regexp.MustCompile(
		`(?:〒\s*)?\b[0-9]{3}-[0-9]{4}\b`,
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
	unicodeLatinFullNamePattern = regexp.MustCompile(
		`(?:^|[^\p{L}])` +
			`\p{Lu}\p{Ll}{1,30}(?:[-']\p{Lu}?\p{Ll}{1,30})?` +
			`\s+\p{Lu}\p{Ll}{1,30}(?:[-']\p{Lu}?\p{Ll}{1,30})?` +
			`(?:$|[^\p{L}])`,
	)
	commonJapaneseNamePattern = regexp.MustCompile(
		`(?:佐藤|鈴木|高橋|田中|伊藤|渡辺|山本|中村|` +
			`小林|加藤|吉田|山田|佐々木|山口|松本|井上|木村|斎藤|清水|` +
			`山崎|森|池田|橋本|阿部|石川|山下|中島|前田|藤田|小川|` +
			`後藤|岡田|長谷川|村上|近藤|石井|坂本|遠藤|青木|藤井|` +
			`西村|福田|太田|三浦|藤原|岡本|松田|中川|中野|原田|小鳥遊)` +
			`[・\s-]?` +
			`[一-龯々ぁ-んァ-ヶー]{1,8}(?:$|[\s」』"'、,）)])`,
	)
	commonLatinNamePattern = regexp.MustCompile(
		`(?i)(?:^|[^a-z])(?:john|jane|james|mary|robert|patricia|michael|` +
			`jennifer|william|linda|david|elizabeth|richard|barbara|` +
			`joseph|susan|thomas|jessica|charles|sarah|christopher|` +
			`karen|daniel|nancy|matthew|lisa|anthony|betty|mark|` +
			`sandra|donald|ashley|steven|kimberly|paul|emily|andrew|` +
			`alice|bob|xavier|turing|einstein|curie|tesla|hopper|lovelace)` +
			`[\s/._-]+[a-z][a-z'-]{1,30}(?:$|[^a-z])`,
	)
	healthPersonSlugPattern = regexp.MustCompile(
		`(?i)(?:^|[/\s])(?:[\p{L}]{1,30}[-_.・\s]+){1,4}` +
			`(?:adhd|ptsd|depression|bipolar|schizophrenia|autism)` +
			`(?:$|[^\p{L}])`,
	)
	patientIdentifierPattern = regexp.MustCompile(
		`(?i)(?:\bpatient|患者)[-_.\s]*` +
			`(?:(?:id|no|number|番号)[-_.\s]*)?` +
			`(?:[0-9][a-z0-9]{3,}|[a-z]{1,8}[0-9][a-z0-9]{2,})`,
	)
	base64TokenPattern = regexp.MustCompile(
		`[A-Za-z0-9+/_-]{4,}={0,2}`,
	)
	base32TokenPattern = regexp.MustCompile(
		`[A-Za-z2-7]{16,}={0,6}`,
	)
	splitBase64Pattern = regexp.MustCompile(
		`[A-Za-z0-9+/_=-]+(?:\s+[A-Za-z0-9+/_=-]+)+`,
	)
	percentEscapePattern = regexp.MustCompile(
		`(?i)%[0-9a-f]{2}`,
	)
	htmlCharacterReferencePattern = regexp.MustCompile(
		`(?i)&#(?:[0-9]+|x[0-9a-f]+);?`,
	)
)

const (
	maxSensitiveDecodeDepth = 8
	maxSensitiveScanStates  = 64
	maxSensitiveAttempts    = 256
	maxSensitiveScanBytes   = 16 * 1024
	maxSensitiveInputBytes  = 4 * 1024
)

type sensitiveScanBudget struct {
	states     int
	attempts   int
	totalBytes int
	seen       map[string]struct{}
}

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
		// DOI canonicalization lowercases the suffix. Base64 is
		// case-sensitive, so re-decoding the lowercased identifier could turn
		// an opaque DOI into unrelated bytes. Reversible encodings were
		// already screened on the exact outbound input above.
		if likelySensitiveLiteral(query.DOI) {
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
		if hasUnicodeScreenAmbiguity(query.Topic) {
			return Query{}, ErrSensitiveQuery
		}
		query.Topic = cleanText(query.Topic)
		if query.Topic == "" ||
			utf8.RuneCountInString(query.Topic) > MaxTopicRunes {
			return Query{}, ErrInvalidQuery
		}
		if containsResearchWithdrawal(query.Topic) {
			return Query{}, ErrInvalidQuery
		}
		if containsLongDigitSequence(query.Topic, 12) ||
			isBareContactNumber(query.Topic) {
			return Query{}, ErrSensitiveQuery
		}
		if likelySensitive(query.Topic) {
			return Query{}, ErrSensitiveQuery
		}
		if containsResearchClauseBoundary(query.Topic) ||
			containsUnsupportedTopicCharacter(query.Topic) {
			return Query{}, ErrInvalidQuery
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
		hasUnicodeScreenAmbiguity(value) ||
		utf8.RuneCountInString(value) > MaxDOIRunes ||
		!doiPrefixPattern.MatchString(value) ||
		len(doiAnywherePattern.FindAllStringIndex(value, -1)) != 1 ||
		containsResearchWithdrawal(value) {
		return "", ErrInvalidQuery
	}
	for _, char := range value {
		if char > unicode.MaxASCII ||
			unicode.IsSpace(char) ||
			unicode.IsControl(char) ||
			unicode.Is(unicode.Cf, char) ||
			char == '?' ||
			char == '#' ||
			char == '\\' ||
			strings.ContainsRune(",;、；", char) {
			return "", ErrInvalidQuery
		}
	}
	return strings.ToLower(value), nil
}

func likelySensitive(value string) bool {
	budget := &sensitiveScanBudget{
		seen: make(map[string]struct{}),
	}
	return likelySensitiveWithBudget(value, 0, budget)
}

func likelySensitiveWithBudget(
	value string,
	depth int,
	budget *sensitiveScanBudget,
) bool {
	if len(value) > maxSensitiveInputBytes ||
		depth > maxSensitiveDecodeDepth {
		return true
	}
	stateKey := strconv.Itoa(depth) + ":" + value
	if _, exists := budget.seen[stateKey]; exists {
		return false
	}
	budget.seen[stateKey] = struct{}{}
	budget.states++
	budget.totalBytes += len(value)
	if budget.states > maxSensitiveScanStates ||
		budget.totalBytes > maxSensitiveScanBytes {
		return true
	}

	canonical := norm.NFKC.String(value)
	for _, variant := range unicodeScreenVariants(canonical) {
		if likelySensitiveLiteral(variant) ||
			likelySensitiveBase64(variant, depth, budget) ||
			likelySensitiveBase32(variant, depth, budget) {
			return true
		}
	}
	htmlDecoded := html.UnescapeString(canonical)
	if htmlDecoded != canonical {
		if depth == maxSensitiveDecodeDepth {
			return true
		}
		return likelySensitiveWithBudget(htmlDecoded, depth+1, budget)
	}
	if !strings.Contains(canonical, "%") {
		return false
	}
	pathDecoded, err := url.PathUnescape(canonical)
	if err != nil {
		// A malformed escape can be appended to an otherwise reversible
		// email or secret encoding. Decode errors therefore fail closed.
		return true
	}
	if pathDecoded == canonical {
		return false
	}
	if depth == maxSensitiveDecodeDepth {
		return true
	}
	return likelySensitiveWithBudget(pathDecoded, depth+1, budget)
}

func likelySensitiveLiteral(value string) bool {
	if likelySensitiveHardLiteral(value) ||
		likelyPersonalResearchContext(value) {
		return true
	}
	return false
}

func likelyPhoneOrPostal(value string) bool {
	if !doiPrefixPattern.MatchString(value) {
		return japanesePhonePattern.MatchString(value) ||
			postalCodePattern.MatchString(value)
	}
	slash := strings.IndexByte(value, '/')
	if slash < 0 || slash+1 >= len(value) {
		return false
	}
	suffix := value[slash+1:]
	for _, pattern := range []*regexp.Regexp{
		japanesePhonePattern,
		postalCodePattern,
	} {
		match := pattern.FindStringIndex(suffix)
		if len(match) == 2 && match[0] == 0 {
			return true
		}
	}
	return false
}

func likelyPersonalResearchContext(value string) bool {
	return likelyExplicitPersonalResearchContext(value)
}

func likelyExplicitPersonalResearchContext(value string) bool {
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
	if patientIdentifierPattern.MatchString(value) {
		return true
	}
	if commonJapaneseNamePattern.MatchString(value) ||
		commonLatinNamePattern.MatchString(value) {
		return true
	}
	healthContext := healthContextPattern.MatchString(value)
	if healthContext && strings.Contains(value, "/") {
		return true
	}
	if healthContext &&
		(japaneseNameLikePattern.MatchString(value) ||
			latinFullNamePattern.MatchString(value) ||
			unicodeLatinFullNamePattern.MatchString(value) ||
			healthPersonSlugPattern.MatchString(value)) {
		return true
	}
	return false
}

func containsNonASCIIDigit(value string) bool {
	for _, char := range value {
		if char > unicode.MaxASCII &&
			(unicode.IsDigit(char) || unicode.IsNumber(char)) {
			return true
		}
	}
	return false
}

func containsLongDigitSequence(value string, minimum int) bool {
	var digits strings.Builder
	suspicious := func() bool {
		return digits.Len() >= minimum
	}
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9':
			digits.WriteRune(char)
		case unicode.IsSpace(char) ||
			unicode.IsPunct(char) ||
			unicode.IsSymbol(char):
			continue
		default:
			if suspicious() {
				return true
			}
			digits.Reset()
		}
	}
	return suspicious()
}

func containsDOIPaymentCard(value string) bool {
	if !doiPrefixPattern.MatchString(value) {
		return false
	}
	slash := strings.IndexByte(value, '/')
	if slash < 0 || slash+1 >= len(value) ||
		value[slash+1] < '0' || value[slash+1] > '9' {
		return false
	}
	var digits strings.Builder
scan:
	for _, char := range value[slash+1:] {
		switch {
		case char >= '0' && char <= '9':
			digits.WriteRune(char)
		case unicode.IsSpace(char) ||
			unicode.IsPunct(char) ||
			unicode.IsSymbol(char):
			continue
		default:
			break scan
		}
	}
	candidate := digits.String()
	return len(candidate) >= 13 &&
		len(candidate) <= 19 &&
		luhnValid(candidate)
}

func isBareContactNumber(value string) bool {
	digits := 0
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9':
			digits++
		case unicode.IsSpace(char) || char == '-':
			continue
		default:
			return false
		}
	}
	return digits >= 8 && digits <= 11
}

func containsDOIBareContactNumber(value string) bool {
	if !doiPrefixPattern.MatchString(value) {
		return false
	}
	slash := strings.IndexByte(value, '/')
	if slash < 0 || slash+1 >= len(value) {
		return false
	}
	return isBareContactNumber(value[slash+1:])
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

func hasUnicodeScreenAmbiguity(value string) bool {
	if norm.NFKC.String(value) != value {
		return true
	}
	if containsMixedLatinConfusableScriptToken(value) {
		return true
	}
	for _, char := range value {
		if unicode.Is(unicode.Cf, char) ||
			(unicode.IsControl(char) && !unicode.IsSpace(char)) {
			return true
		}
	}
	return false
}

func containsMixedLatinConfusableScriptToken(value string) bool {
	hasLatin := false
	hasConfusableScript := false
	flush := func() bool {
		mixed := hasLatin && hasConfusableScript
		hasLatin = false
		hasConfusableScript = false
		return mixed
	}
	for _, char := range value {
		switch {
		case unicode.In(char, unicode.Latin):
			hasLatin = true
		case unicode.In(char, unicode.Cyrillic, unicode.Greek):
			hasConfusableScript = true
		case unicode.IsLetter(char) || unicode.IsMark(char):
			continue
		default:
			if flush() {
				return true
			}
		}
	}
	return flush()
}

func unicodeScreenVariants(value string) []string {
	var spaced strings.Builder
	var joined strings.Builder
	var marksRemoved strings.Builder
	spaced.Grow(len(value))
	joined.Grow(len(value))
	marksRemoved.Grow(len(value))
	for _, char := range value {
		if unicode.Is(unicode.Cf, char) || unicode.IsControl(char) {
			spaced.WriteByte(' ')
			continue
		}
		spaced.WriteRune(char)
		joined.WriteRune(char)
		if !unicode.IsMark(char) {
			marksRemoved.WriteRune(char)
		}
	}

	variants := []string{
		value,
		cleanText(spaced.String()),
		cleanText(joined.String()),
		cleanText(marksRemoved.String()),
	}
	unique := make([]string, 0, len(variants))
	seen := make(map[string]struct{}, len(variants))
	for _, variant := range variants {
		if _, exists := seen[variant]; exists {
			continue
		}
		seen[variant] = struct{}{}
		unique = append(unique, variant)
	}
	return unique
}

func likelySensitiveBase64(
	value string,
	depth int,
	budget *sensitiveScanBudget,
) bool {
	for _, candidate := range base64Candidates(value) {
		token := candidate.encoded
		if len(token) > 344 {
			return true
		}
		for _, encoding := range []*base64.Encoding{
			base64.StdEncoding.Strict(),
			base64.RawStdEncoding.Strict(),
			base64.URLEncoding.Strict(),
			base64.RawURLEncoding.Strict(),
		} {
			budget.attempts++
			if budget.attempts > maxSensitiveAttempts {
				return true
			}
			decoded, err := encoding.DecodeString(token)
			if err != nil ||
				len(decoded) == 0 ||
				encoding.EncodeToString(decoded) != token {
				continue
			}
			budget.totalBytes += len(decoded)
			if budget.totalBytes > maxSensitiveScanBytes {
				return true
			}
			textLike := plausiblyTextBytes(decoded)
			for _, decodedView := range decodedScreenViews(decoded) {
				if likelySensitiveDecodedStructure(decodedView) {
					return true
				}
				replaced := value[:candidate.start] +
					decodedView +
					value[candidate.end:]
				if !textLike {
					continue
				}
				if likelySensitiveHardLiteral(decodedView) ||
					likelyExplicitPersonalResearchContext(decodedView) {
					return true
				}
				for _, variant := range unicodeScreenVariants(
					norm.NFKC.String(replaced),
				) {
					if likelySensitiveHardLiteral(variant) ||
						likelyExplicitPersonalResearchContext(variant) {
						return true
					}
				}
				if shouldRecurseDecodedView(decodedView) {
					if depth == maxSensitiveDecodeDepth ||
						likelySensitiveWithBudget(
							replaced,
							depth+1,
							budget,
						) {
						return true
					}
				}
			}
		}
	}
	return false
}

func likelySensitiveBase32(
	value string,
	depth int,
	budget *sensitiveScanBudget,
) bool {
	for _, index := range base32TokenPattern.FindAllStringIndex(value, -1) {
		token := value[index[0]:index[1]]
		canonicalToken := strings.ToUpper(token)
		for _, encoding := range []*base32.Encoding{
			base32.StdEncoding,
			base32.StdEncoding.WithPadding(base32.NoPadding),
		} {
			budget.attempts++
			if budget.attempts > maxSensitiveAttempts {
				return true
			}
			decoded, err := encoding.DecodeString(canonicalToken)
			if err != nil ||
				len(decoded) == 0 ||
				encoding.EncodeToString(decoded) != canonicalToken {
				continue
			}
			budget.totalBytes += len(decoded)
			if budget.totalBytes > maxSensitiveScanBytes {
				return true
			}
			textLike := plausiblyTextBytes(decoded)
			for _, decodedView := range decodedScreenViews(decoded) {
				if likelySensitiveDecodedStructure(decodedView) {
					return true
				}
				replaced := value[:index[0]] +
					decodedView +
					value[index[1]:]
				if !textLike {
					continue
				}
				if likelySensitiveHardLiteral(decodedView) ||
					likelyExplicitPersonalResearchContext(decodedView) {
					return true
				}
				for _, variant := range unicodeScreenVariants(
					norm.NFKC.String(replaced),
				) {
					if likelySensitiveHardLiteral(variant) ||
						likelyExplicitPersonalResearchContext(variant) {
						return true
					}
				}
				if shouldRecurseDecodedView(decodedView) {
					if depth == maxSensitiveDecodeDepth ||
						likelySensitiveWithBudget(
							replaced,
							depth+1,
							budget,
						) {
						return true
					}
				}
			}
		}
	}
	return false
}

func shouldRecurseDecodedView(value string) bool {
	if percentEscapePattern.MatchString(value) ||
		htmlCharacterReferencePattern.MatchString(value) {
		return true
	}
	for _, candidate := range base64Candidates(value) {
		if len(candidate.encoded) < 7 {
			continue
		}
		for _, encoding := range []*base64.Encoding{
			base64.StdEncoding.Strict(),
			base64.RawStdEncoding.Strict(),
			base64.URLEncoding.Strict(),
			base64.RawURLEncoding.Strict(),
		} {
			decoded, err := encoding.DecodeString(candidate.encoded)
			if err == nil &&
				len(decoded) > 0 &&
				encoding.EncodeToString(decoded) == candidate.encoded &&
				mostlyTextBytes(decoded) {
				return true
			}
		}
	}
	return false
}

func mostlyTextBytes(value []byte) bool {
	return textByteRatioAtLeast(value, 85)
}

func plausiblyTextBytes(value []byte) bool {
	return textByteRatioAtLeast(value, 80)
}

func textByteRatioAtLeast(value []byte, minimumPercent int) bool {
	if len(value) == 0 {
		return false
	}
	textBytes := 0
	for offset := 0; offset < len(value); {
		char, size := utf8.DecodeRune(value[offset:])
		if char == utf8.RuneError && size == 1 {
			offset++
			continue
		}
		if unicode.IsPrint(char) || unicode.IsSpace(char) {
			textBytes += size
		}
		offset += size
	}
	return textBytes*100 >= len(value)*minimumPercent
}

func decodedScreenViews(value []byte) []string {
	rawViews := [][]byte{
		bytes.ToValidUTF8(value, []byte(" ")),
		bytes.ToValidUTF8(value, nil),
	}
	seen := make(map[string]struct{})
	var views []string
	for _, raw := range rawViews {
		var spaced strings.Builder
		var joined strings.Builder
		for _, char := range string(raw) {
			if unicode.IsControl(char) || !unicode.IsPrint(char) {
				spaced.WriteByte(' ')
				continue
			}
			spaced.WriteRune(char)
			joined.WriteRune(char)
		}
		for _, view := range []string{
			cleanText(spaced.String()),
			cleanText(joined.String()),
		} {
			if view == "" {
				continue
			}
			if _, exists := seen[view]; exists {
				continue
			}
			seen[view] = struct{}{}
			views = append(views, view)
		}
	}
	return views
}

func likelySensitiveHardLiteral(value string) bool {
	if strings.ContainsRune(value, '@') ||
		emailPattern.MatchString(value) ||
		likelyCredentialValue(value) ||
		knownSecretPattern.MatchString(value) ||
		jwtPattern.MatchString(value) ||
		likelyPhoneOrPostal(value) ||
		phoneContextPattern.MatchString(value) ||
		northAmericanPhonePattern.MatchString(value) ||
		strings.Contains(value, "-----BEGIN PRIVATE KEY-----") ||
		strings.Contains(value, "-----BEGIN RSA PRIVATE KEY-----") ||
		containsNonASCIIDigit(value) ||
		containsLongDigitSequence(value, 15) ||
		containsDOIPaymentCard(value) ||
		containsDOIBareContactNumber(value) {
		return true
	}
	return false
}

func likelySensitiveDecodedStructure(value string) bool {
	if emailPattern.MatchString(value) ||
		likelyCredentialValue(value) ||
		knownSecretPattern.MatchString(value) ||
		jwtPattern.MatchString(value) ||
		likelyPhoneOrPostal(value) ||
		phoneContextPattern.MatchString(value) ||
		northAmericanPhonePattern.MatchString(value) ||
		isBareContactNumber(value) ||
		containsLongDigitSequence(value, 12) ||
		strings.Contains(value, "-----BEGIN PRIVATE KEY-----") ||
		strings.Contains(value, "-----BEGIN RSA PRIVATE KEY-----") ||
		likelyExplicitPersonalResearchContext(value) {
		return true
	}
	return false
}

func likelyCredentialValue(value string) bool {
	if credentialAssignmentPattern.MatchString(value) {
		return true
	}
	for _, match := range credentialLoosePattern.FindAllStringSubmatch(value, -1) {
		if len(match) != 2 {
			continue
		}
		switch strings.ToLower(match[1]) {
		case "rotation", "rotations", "management", "security", "storage",
			"handling", "leakage", "exposure", "detection", "scanning",
			"method", "methods", "research", "cryptography", "encryption",
			"authentication", "hash", "hashing", "policy", "policies",
			"lifecycle", "revocation", "validation", "verification",
			"generation", "spraying", "watermarking":
			continue
		default:
			return true
		}
	}
	return false
}

type base64Candidate struct {
	encoded string
	start   int
	end     int
}

func base64Candidates(value string) []base64Candidate {
	seen := make(map[string]struct{})
	var candidates []base64Candidate
	appendCandidate := func(candidate base64Candidate) {
		if len(candidate.encoded) < 4 ||
			candidate.start < 0 ||
			candidate.end > len(value) ||
			candidate.start >= candidate.end {
			return
		}
		key := strconv.Itoa(candidate.start) + ":" +
			strconv.Itoa(candidate.end) + ":" +
			candidate.encoded
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
	}

	for _, index := range base64TokenPattern.FindAllStringIndex(value, -1) {
		match := value[index[0]:index[1]]
		appendCandidate(base64Candidate{
			encoded: match,
			start:   index[0],
			end:     index[1],
		})
		for relativeIndex, char := range match {
			if char == '/' {
				appendCandidate(base64Candidate{
					encoded: match[relativeIndex+1:],
					start:   index[0] + relativeIndex + 1,
					end:     index[1],
				})
			}
		}
	}
	for _, index := range splitBase64Pattern.FindAllStringIndex(value, -1) {
		encoded := strings.Map(
			func(char rune) rune {
				if unicode.IsSpace(char) {
					return -1
				}
				return char
			},
			value[index[0]:index[1]],
		)
		appendCandidate(base64Candidate{
			encoded: encoded,
			start:   index[0],
			end:     index[1],
		})
	}
	return candidates
}

func containsResearchClauseBoundary(value string) bool {
	return strings.ContainsAny(value, "、,;；。！？!?")
}

func containsUnsupportedTopicCharacter(value string) bool {
	runes := []rune(value)
	for index, char := range runes {
		switch {
		case unicode.IsLetter(char) || unicode.IsMark(char):
			continue
		case char >= '0' && char <= '9':
			continue
		case unicode.IsSpace(char) || char == '-':
			continue
		case char == '.' &&
			index > 0 &&
			index+1 < len(runes) &&
			runes[index-1] >= '0' &&
			runes[index-1] <= '9' &&
			runes[index+1] >= '0' &&
			runes[index+1] <= '9':
			continue
		default:
			return true
		}
	}
	return false
}

func containsResearchWithdrawal(value string) bool {
	for _, variant := range unicodeScreenVariants(norm.NFKC.String(value)) {
		lower := strings.ToLower(variant)
		for _, phrase := range []string{
			"やめて", "やめます", "やめる", "中止", "キャンセル",
			"取り消", "取消", "検索不要", "調査不要", "照会不要",
			"いらない", "なしにして", "しないで", "しなくて",
			"never mind", "nevermind",
			"scratch that", "forget it", "changed my mind", "do not",
			"don't", "dont",
		} {
			if strings.Contains(lower, phrase) {
				return true
			}
		}
		for _, token := range strings.FieldsFunc(
			lower,
			func(char rune) bool {
				return !unicode.IsLetter(char)
			},
		) {
			switch token {
			case "cancel", "cancelled", "canceled", "abort", "withdraw":
				return true
			}
		}
	}
	return false
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
