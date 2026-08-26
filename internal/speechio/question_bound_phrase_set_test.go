package speechio

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/speech/apiv2/speechpb"
)

func TestQuestionBoundPhraseSetUsesOnlyInlineBoundedPhrasesAfterBaselineMiss(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 1, 2, 3, 0, time.UTC)
	digest := sha256.Sum256([]byte("displayed current question"))
	set, err := NewQuestionBoundPhraseSet(
		now,
		now.Add(5*time.Minute),
		digest,
		"turn_generation_42",
		QuestionBoundPhraseSource{
			QuestionTerms: []string{"導入時期", "いつ"},
			UserTerms:     []string{"来年度", "導入時期"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var requests []*speechpb.RecognizeRequest
	service := &CloudService{
		speechModel: "chirp_3",
		recognizeCall: func(_ context.Context, request *speechpb.RecognizeRequest) (*speechpb.RecognizeResponse, error) {
			requests = append(requests, request)
			if len(requests) == 1 {
				return &speechpb.RecognizeResponse{}, nil
			}
			return recognizedResponse("来年度", .91), nil
		},
	}
	text, confidence, err := service.TranscribeQuestionBound(
		context.Background(),
		[]byte("real audio is opaque to this unit boundary"),
		set,
		now,
		digest,
		"turn_generation_42",
	)
	if err != nil || text != "来年度" || confidence != .91 {
		t.Fatalf("result=(%q,%f,%v)", text, confidence, err)
	}
	if len(requests) != 2 || requests[0].Config.Adaptation != nil {
		t.Fatalf("baseline requests=%d adaptation=%+v", len(requests), requests[0].Config.Adaptation)
	}
	adaptation := requests[1].Config.GetAdaptation()
	if adaptation == nil || len(adaptation.PhraseSets) != 1 || len(adaptation.CustomClasses) != 0 {
		t.Fatalf("adaptation=%+v", adaptation)
	}
	inline := adaptation.PhraseSets[0].GetInlinePhraseSet()
	if inline == nil || adaptation.PhraseSets[0].GetPhraseSet() != "" ||
		inline.Name != "" || inline.DisplayName != "" || inline.Boost != questionBoundPhraseSetBoost ||
		len(inline.Phrases) != 3 {
		t.Fatalf("inline phrase set=%+v", inline)
	}
	want := []string{"導入時期", "いつ", "来年度"}
	for index, phrase := range inline.Phrases {
		if phrase.Value != want[index] || phrase.Boost != 0 {
			t.Fatalf("phrase[%d]=%+v", index, phrase)
		}
	}
}

func TestQuestionBoundPhraseSetSkipsAdaptedRequestWhenBaselineIsSufficient(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	digest := sha256.Sum256([]byte("question"))
	set := validPhraseSet(t, now, digest, "generation_1")
	var calls int
	service := &CloudService{
		speechModel: "chirp_3",
		recognizeCall: func(_ context.Context, request *speechpb.RecognizeRequest) (*speechpb.RecognizeResponse, error) {
			calls++
			if request.Config.Adaptation != nil {
				t.Fatal("adaptation was sent after a sufficient baseline")
			}
			return recognizedResponse("そのまま認識", .88), nil
		},
	}
	text, confidence, err := service.TranscribeQuestionBound(context.Background(), []byte{1}, set, now, digest, "generation_1")
	if err != nil || text != "そのまま認識" || confidence != .88 || calls != 1 {
		t.Fatalf("result=(%q,%f,%v) calls=%d", text, confidence, err, calls)
	}
	if _, _, err := service.TranscribeQuestionBound(context.Background(), []byte{1}, set, now, digest, "generation_1"); !errors.Is(err, ErrQuestionBoundPhraseSetInvalid) {
		t.Fatalf("replay error=%v", err)
	}
}

func TestQuestionBoundPhraseSetRejectsContradictoryAdaptedTranscript(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	digest := sha256.Sum256([]byte("question"))
	set := validPhraseSet(t, now, digest, "generation_2")
	var calls int
	service := &CloudService{
		speechModel: "chirp_3",
		recognizeCall: func(_ context.Context, _ *speechpb.RecognizeRequest) (*speechpb.RecognizeResponse, error) {
			calls++
			if calls == 1 {
				return recognizedResponse("来年度", .4), nil
			}
			return recognizedResponse("今年度", .92), nil
		},
	}
	_, _, err := service.TranscribeQuestionBound(context.Background(), []byte{1}, set, now, digest, "generation_2")
	if !errors.Is(err, ErrQuestionBoundPhraseSetUnresolved) {
		t.Fatalf("error=%v", err)
	}
}

func TestQuestionBoundPhraseSetBindingAndLimitsFailClosed(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	digest := sha256.Sum256([]byte("question"))
	otherDigest := sha256.Sum256([]byte("other question"))
	tests := []struct {
		name       string
		expires    time.Time
		digest     [sha256.Size]byte
		generation string
		source     QuestionBoundPhraseSource
	}{
		{name: "zero digest", expires: now.Add(time.Minute), generation: "generation_3", source: QuestionBoundPhraseSource{QuestionTerms: []string{"質問"}}},
		{name: "over ttl", expires: now.Add(5*time.Minute + time.Nanosecond), digest: digest, generation: "generation_3", source: QuestionBoundPhraseSource{QuestionTerms: []string{"質問"}}},
		{name: "no question", expires: now.Add(time.Minute), digest: digest, generation: "generation_3", source: QuestionBoundPhraseSource{UserTerms: []string{"本人語"}}},
		{name: "too many", expires: now.Add(time.Minute), digest: digest, generation: "generation_3", source: QuestionBoundPhraseSource{QuestionTerms: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}}},
		{name: "too long", expires: now.Add(time.Minute), digest: digest, generation: "generation_3", source: QuestionBoundPhraseSource{QuestionTerms: []string{strings.Repeat("あ", questionBoundPhraseSetMaxRunes+1)}}},
		{name: "control", expires: now.Add(time.Minute), digest: digest, generation: "generation_3", source: QuestionBoundPhraseSource{QuestionTerms: []string{"質問\n答え"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewQuestionBoundPhraseSet(now, test.expires, test.digest, test.generation, test.source); !errors.Is(err, ErrQuestionBoundPhraseSetInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	for _, mismatch := range []struct {
		name       string
		at         time.Time
		digest     [sha256.Size]byte
		generation string
	}{
		{name: "question", at: now, digest: otherDigest, generation: "generation_3"},
		{name: "generation", at: now, digest: digest, generation: "generation_4"},
		{name: "expired", at: now.Add(5 * time.Minute), digest: digest, generation: "generation_3"},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			set := validPhraseSet(t, now, digest, "generation_3")
			if _, err := set.take(mismatch.at, mismatch.digest, mismatch.generation); !errors.Is(err, ErrQuestionBoundPhraseSetInvalid) {
				t.Fatalf("error=%v", err)
			}
			if len(set.phrases) != 0 {
				t.Fatal("phrases were not zeroized")
			}
		})
	}
}

func validPhraseSet(t *testing.T, now time.Time, digest [sha256.Size]byte, generation string) *QuestionBoundPhraseSet {
	t.Helper()
	set, err := NewQuestionBoundPhraseSet(now, now.Add(5*time.Minute), digest, generation, QuestionBoundPhraseSource{
		QuestionTerms: []string{"導入時期"},
		UserTerms:     []string{"来年度"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return set
}
