package speechio

import (
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"cloud.google.com/go/speech/apiv2/speechpb"
)

const (
	questionBoundPhraseSetTTL        = 5 * time.Minute
	questionBoundPhraseSetBoost      = float32(4)
	questionBoundPhraseSetMaxTerms   = 16
	questionBoundPhraseSetMaxRunes   = 24
	questionBoundPhraseSetTotalRunes = 192
	questionBoundAdaptedConfidence   = float32(0.65)
)

var (
	ErrQuestionBoundPhraseSetInvalid    = errors.New("question_bound_phrase_set_invalid")
	ErrQuestionBoundPhraseSetUnresolved = errors.New("question_bound_phrase_set_unresolved")
	phraseSetGenerationPattern          = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
)

// QuestionBoundPhraseSource keeps the two permitted sources explicit. The
// caller may supply only terms visibly present in the current question or
// terms previously spoken by the user and authenticated by conversation
// state. Expected answers, model hypotheses and document prose are not valid
// sources.
type QuestionBoundPhraseSource struct {
	QuestionTerms []string
	UserTerms     []string
}

// QuestionBoundTranscriber is an optional recognition capability. Callers
// must construct the one-use set from authenticated conversation state; the
// ordinary Service interface intentionally cannot accept free-form hints.
type QuestionBoundTranscriber interface {
	TranscribeQuestionBound(
		ctx context.Context,
		audio []byte,
		set *QuestionBoundPhraseSet,
		now time.Time,
		questionDigest [sha256.Size]byte,
		turnGeneration string,
	) (string, float32, error)
}

// QuestionBoundPhraseSet is a process-local, one-use capability. It contains
// no UID, question text, transcript or answer and cannot create a persistent
// Speech PhraseSet resource.
type QuestionBoundPhraseSet struct {
	mu sync.Mutex

	used           bool
	questionDigest [sha256.Size]byte
	turnGeneration string
	expiresAt      time.Time
	phrases        []string
}

func NewQuestionBoundPhraseSet(
	now time.Time,
	expiresAt time.Time,
	questionDigest [sha256.Size]byte,
	turnGeneration string,
	source QuestionBoundPhraseSource,
) (*QuestionBoundPhraseSet, error) {
	if now.IsZero() || expiresAt.IsZero() || !expiresAt.After(now) ||
		expiresAt.After(now.Add(questionBoundPhraseSetTTL)) ||
		questionDigest == [sha256.Size]byte{} ||
		!phraseSetGenerationPattern.MatchString(turnGeneration) ||
		len(source.QuestionTerms) == 0 ||
		len(source.QuestionTerms) > 8 || len(source.UserTerms) > 8 {
		return nil, ErrQuestionBoundPhraseSetInvalid
	}
	phrases := make([]string, 0, len(source.QuestionTerms)+len(source.UserTerms))
	seen := make(map[string]struct{}, cap(phrases))
	totalRunes := 0
	for _, group := range [][]string{source.QuestionTerms, source.UserTerms} {
		for _, phrase := range group {
			if !validQuestionBoundPhrase(phrase) {
				return nil, ErrQuestionBoundPhraseSetInvalid
			}
			if _, duplicate := seen[phrase]; duplicate {
				continue
			}
			totalRunes += utf8.RuneCountInString(phrase)
			if totalRunes > questionBoundPhraseSetTotalRunes {
				return nil, ErrQuestionBoundPhraseSetInvalid
			}
			seen[phrase] = struct{}{}
			phrases = append(phrases, phrase)
		}
	}
	if len(phrases) == 0 || len(phrases) > questionBoundPhraseSetMaxTerms {
		return nil, ErrQuestionBoundPhraseSetInvalid
	}
	return &QuestionBoundPhraseSet{
		questionDigest: questionDigest,
		turnGeneration: turnGeneration,
		expiresAt:      expiresAt,
		phrases:        phrases,
	}, nil
}

func validQuestionBoundPhrase(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > questionBoundPhraseSetMaxRunes {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func (set *QuestionBoundPhraseSet) take(
	now time.Time,
	questionDigest [sha256.Size]byte,
	turnGeneration string,
) (*speechpb.SpeechAdaptation, error) {
	if set == nil {
		return nil, ErrQuestionBoundPhraseSetInvalid
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.used || now.IsZero() || !now.Before(set.expiresAt) ||
		questionDigest != set.questionDigest || turnGeneration != set.turnGeneration {
		set.used = true
		clear(set.phrases)
		set.phrases = nil
		return nil, ErrQuestionBoundPhraseSetInvalid
	}
	set.used = true
	phrases := make([]*speechpb.PhraseSet_Phrase, 0, len(set.phrases))
	for _, value := range set.phrases {
		phrases = append(phrases, &speechpb.PhraseSet_Phrase{Value: value})
	}
	clear(set.phrases)
	set.phrases = nil
	return &speechpb.SpeechAdaptation{
		PhraseSets: []*speechpb.SpeechAdaptation_AdaptationPhraseSet{
			{
				Value: &speechpb.SpeechAdaptation_AdaptationPhraseSet_InlinePhraseSet{
					InlinePhraseSet: &speechpb.PhraseSet{
						Phrases: phrases,
						Boost:   questionBoundPhraseSetBoost,
					},
				},
			},
		},
	}, nil
}

// TranscribeQuestionBound first runs the unchanged baseline. Adaptation is
// sent only when the baseline has no sufficient observation, and only through
// an inline PhraseSet. If both paths heard nonempty but contradictory text,
// neither is selected.
func (s *CloudService) TranscribeQuestionBound(
	ctx context.Context,
	audio []byte,
	set *QuestionBoundPhraseSet,
	now time.Time,
	questionDigest [sha256.Size]byte,
	turnGeneration string,
) (string, float32, error) {
	if len(audio) == 0 || s == nil || validateConversationSpeechModel(s.speechModel) != nil {
		return "", 0, ErrQuestionBoundPhraseSetInvalid
	}
	adaptation, err := set.take(now, questionDigest, turnGeneration)
	if err != nil {
		return "", 0, err
	}
	baselineResponse, err := s.recognize(ctx, audio, s.speechModel)
	if err != nil {
		return "", 0, err
	}
	baseline, baselineConfidence := recognizedText(baselineResponse)
	if baseline != "" && baselineConfidence >= questionBoundAdaptedConfidence {
		return baseline, baselineConfidence, nil
	}
	adaptedResponse, err := s.recognizeWithAdaptation(ctx, audio, s.speechModel, adaptation)
	if err != nil {
		return "", 0, err
	}
	adapted, adaptedConfidence := recognizedText(adaptedResponse)
	if adapted == "" || adaptedConfidence < questionBoundAdaptedConfidence {
		return "", 0, ErrNoSpeech
	}
	if baseline != "" && canonicalPairedTranscript(baseline) != canonicalPairedTranscript(adapted) {
		return "", 0, ErrQuestionBoundPhraseSetUnresolved
	}
	return adapted, adaptedConfidence, nil
}
