package conversation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	speechAdaptationTTL       = 5 * time.Minute
	speechAdaptationMaxTerms  = 8
	speechAdaptationMaxRunes  = 24
	speechAdaptationMaxLeases = 256
)

var speechAdaptationAAD = []byte("kotae-question-bound-phrase-v1\x00")

type speechAdaptationFrame struct {
	QuestionDigest string `json:"question_digest,omitempty"`
	IssuedAt       int64  `json:"issued_at,omitempty"`
	ExpiresAt      int64  `json:"expires_at,omitempty"`
	Turn           int    `json:"turn,omitempty"`
}

type speechAdaptationLease struct {
	questionTerms []string
	userTerms     []string
	expiresAt     time.Time
}

func normalizeSpeechAdaptationFrame(frame speechAdaptationFrame, stateTurn int) (speechAdaptationFrame, error) {
	if frame.QuestionDigest == "" && frame.IssuedAt == 0 &&
		frame.ExpiresAt == 0 && frame.Turn == 0 {
		return speechAdaptationFrame{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(frame.QuestionDigest)
	if err != nil || len(raw) != sha256.Size || frame.Turn != stateTurn ||
		frame.ExpiresAt-frame.IssuedAt != int64(speechAdaptationTTL/time.Second) {
		return speechAdaptationFrame{}, ErrInvalidStateToken
	}
	return frame, nil
}

func validSpeechAdaptationTerm(term string) bool {
	if term == "" || term != strings.TrimSpace(term) || !utf8.ValidString(term) ||
		utf8.RuneCountInString(term) > speechAdaptationMaxRunes {
		return false
	}
	for _, r := range term {
		if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.IsPunct(r) {
			return false
		}
	}
	return true
}

func (agent *vertexAgent) newSpeechAdaptationFrame(
	userUtterance string,
	spokenReply string,
	turn int,
) speechAdaptationFrame {
	if agent == nil || agent.codec == nil || !agent.stateV2Writes ||
		turn < 1 || !endsWithQuestion(spokenReply) {
		return speechAdaptationFrame{}
	}
	questionTerms := finiteSpeechTerms(spokenReply)
	if len(questionTerms) == 0 {
		return speechAdaptationFrame{}
	}
	userTerms := excludeSpeechTerms(
		finiteSpeechTerms(userUtterance),
		questionTerms,
	)
	now := agent.codec.now().UTC().Truncate(time.Second)
	digest := agent.speechQuestionDigest(spokenReply, turn)
	if digest == [sha256.Size]byte{} {
		return speechAdaptationFrame{}
	}
	digestText := base64.RawURLEncoding.EncodeToString(digest[:])
	agent.storeSpeechAdaptationLease(digestText, speechAdaptationLease{
		questionTerms: append([]string(nil), questionTerms...),
		userTerms:     append([]string(nil), userTerms...),
		expiresAt:     now.Add(speechAdaptationTTL),
	})
	return speechAdaptationFrame{
		QuestionDigest: digestText,
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(speechAdaptationTTL).Unix(),
		Turn:           turn,
	}
}

func (agent *vertexAgent) storeSpeechAdaptationLease(
	digest string,
	lease speechAdaptationLease,
) {
	agent.speechAdaptationMu.Lock()
	defer agent.speechAdaptationMu.Unlock()
	if agent.speechAdaptations == nil {
		agent.speechAdaptations = make(map[string]speechAdaptationLease)
	}
	now := lease.expiresAt.Add(-speechAdaptationTTL)
	for key, existing := range agent.speechAdaptations {
		if !existing.expiresAt.After(now) {
			clear(existing.questionTerms)
			clear(existing.userTerms)
			delete(agent.speechAdaptations, key)
		}
	}
	if len(agent.speechAdaptations) >= speechAdaptationMaxLeases {
		var oldestKey string
		var oldest time.Time
		for key, existing := range agent.speechAdaptations {
			if oldestKey == "" || existing.expiresAt.Before(oldest) {
				oldestKey, oldest = key, existing.expiresAt
			}
		}
		evicted := agent.speechAdaptations[oldestKey]
		clear(evicted.questionTerms)
		clear(evicted.userTerms)
		delete(agent.speechAdaptations, oldestKey)
	}
	if replaced, ok := agent.speechAdaptations[digest]; ok {
		clear(replaced.questionTerms)
		clear(replaced.userTerms)
	}
	agent.speechAdaptations[digest] = lease
}

func (agent *vertexAgent) takeSpeechAdaptationLease(
	digest string,
	now time.Time,
) (speechAdaptationLease, bool) {
	agent.speechAdaptationMu.Lock()
	defer agent.speechAdaptationMu.Unlock()
	lease, ok := agent.speechAdaptations[digest]
	delete(agent.speechAdaptations, digest)
	if !ok || !now.Before(lease.expiresAt) {
		clear(lease.questionTerms)
		clear(lease.userTerms)
		return speechAdaptationLease{}, false
	}
	return lease, true
}

func endsWithQuestion(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasSuffix(value, "?") || strings.HasSuffix(value, "？") ||
		strings.HasSuffix(value, "か。") || strings.HasSuffix(value, "か")
}

func finiteSpeechTerms(value string) []string {
	var terms []string
	trimmed := strings.TrimSpace(value)
	if count := utf8.RuneCountInString(trimmed); count >= 2 && count <= 12 &&
		validSpeechAdaptationTerm(trimmed) {
		terms = appendUniqueSpeechTerm(terms, trimmed)
	}
	var run []rune
	runClass := 0
	flush := func() {
		if len(run) >= 2 && len(run) <= 12 {
			terms = appendUniqueSpeechTerm(terms, string(run))
		}
		run = run[:0]
		runClass = 0
	}
	for _, r := range value {
		class := speechTermRuneClass(r)
		if class != 0 {
			if runClass != 0 && class != runClass {
				flush()
			}
			runClass = class
			run = append(run, r)
			continue
		}
		flush()
	}
	flush()
	for _, interrogative := range []string{"いつ", "どこ", "どちら", "なぜ", "どう", "いくら", "何"} {
		if strings.Contains(value, interrogative) {
			terms = appendUniqueSpeechTerm(terms, interrogative)
		}
	}
	if len(terms) > speechAdaptationMaxTerms {
		terms = terms[:speechAdaptationMaxTerms]
	}
	return terms
}

func speechTermRuneClass(r rune) int {
	switch {
	case unicode.Is(unicode.Han, r):
		return 1
	case unicode.Is(unicode.Hiragana, r):
		return 2
	case unicode.Is(unicode.Katakana, r):
		return 3
	case unicode.IsLetter(r) && r <= unicode.MaxLatin1:
		return 4
	case unicode.IsNumber(r):
		return 5
	default:
		return 0
	}
}

func appendUniqueSpeechTerm(terms []string, term string) []string {
	if !validSpeechAdaptationTerm(term) {
		return terms
	}
	for _, existing := range terms {
		if existing == term {
			return terms
		}
	}
	return append(terms, term)
}

func mergeSpeechTerms(previous, current []string) []string {
	merged := make([]string, 0, speechAdaptationMaxTerms)
	for _, group := range [][]string{current, previous} {
		for _, term := range group {
			merged = appendUniqueSpeechTerm(merged, term)
			if len(merged) == speechAdaptationMaxTerms {
				return merged
			}
		}
	}
	return merged
}

func excludeSpeechTerms(terms, excluded []string) []string {
	filtered := make([]string, 0, len(terms))
	for _, term := range terms {
		drop := false
		for _, excludedTerm := range excluded {
			if term == excludedTerm {
				drop = true
				break
			}
		}
		if !drop {
			filtered = append(filtered, term)
		}
	}
	return filtered
}

func (agent *vertexAgent) speechQuestionDigest(question string, turn int) [sha256.Size]byte {
	var empty [sha256.Size]byte
	if agent == nil || len(agent.continuityKey) != sha256.Size || question == "" || turn < 1 {
		return empty
	}
	mac := hmac.New(sha256.New, agent.continuityKey)
	_, _ = mac.Write(speechAdaptationAAD)
	_, _ = mac.Write([]byte(question))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strconv.Itoa(turn)))
	var result [sha256.Size]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func (agent *vertexAgent) IssueQuestionBoundPhraseCapability(
	uid string,
	token string,
	turnGeneration string,
) (QuestionBoundPhraseCapability, error) {
	if agent == nil || agent.codec == nil || token == "" ||
		!validSpeechTurnGeneration(turnGeneration) {
		return QuestionBoundPhraseCapability{}, ErrInvalidStateToken
	}
	state, err := agent.codec.open(uid, token)
	if err != nil {
		return QuestionBoundPhraseCapability{}, err
	}
	frame := state.SpeechAdaptation
	now := agent.codec.now().UTC()
	if frame.QuestionDigest == "" || now.Unix() >= frame.ExpiresAt {
		return QuestionBoundPhraseCapability{}, ErrInvalidStateToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(frame.QuestionDigest)
	if err != nil || len(raw) != sha256.Size {
		return QuestionBoundPhraseCapability{}, ErrInvalidStateToken
	}
	var digest [sha256.Size]byte
	copy(digest[:], raw)
	lease, ok := agent.takeSpeechAdaptationLease(frame.QuestionDigest, now)
	if !ok || !lease.expiresAt.Equal(time.Unix(frame.ExpiresAt, 0).UTC()) {
		clear(lease.questionTerms)
		clear(lease.userTerms)
		return QuestionBoundPhraseCapability{}, ErrInvalidStateToken
	}
	questionTerms := append([]string(nil), lease.questionTerms...)
	userTerms := append([]string(nil), lease.userTerms...)
	clear(lease.questionTerms)
	clear(lease.userTerms)
	return QuestionBoundPhraseCapability{
		QuestionDigest: digest,
		TurnGeneration: turnGeneration,
		ExpiresAt:      time.Unix(frame.ExpiresAt, 0).UTC(),
		QuestionTerms:  questionTerms,
		UserTerms:      userTerms,
	}, nil
}

func validSpeechTurnGeneration(value string) bool {
	if len(value) != 24 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 12 || hex.EncodeToString(decoded) != value {
		clear(decoded)
		return false
	}
	clear(decoded)
	return true
}
