package conversation

import "testing"

import (
	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestConversationSupportFadesOnlyAfterTwoVerifiedFirstAnswers(t *testing.T) {
	profile := conversationSupport{}
	profile = recordVerifiedFirstAnswer(profile)
	if profile.FadingStage != 0 ||
		profile.VerifiedFirstAnswers != 1 ||
		profile.QuestionCooldown != questionCooldownAfterAnswer {
		t.Fatalf("first verified answer changed support too far: %#v", profile)
	}

	profile = recordVerifiedFirstAnswer(profile)
	if profile.FadingStage != 1 || profile.VerifiedFirstAnswers != 0 {
		t.Fatalf("second verified answer did not fade one stage: %#v", profile)
	}
	profile = recordVerifiedFirstAnswer(profile)
	profile = recordVerifiedFirstAnswer(profile)
	if profile.FadingStage != maxSupportFadingStage ||
		profile.VerifiedFirstAnswers != 0 ||
		supportPromptStyle(profile) != "listen" {
		t.Fatalf("four verified answers did not reach bounded fading: %#v", profile)
	}

	profile.QuestionCooldown = 0
	if supportPromptStyle(profile) != "natural" {
		t.Fatalf("fully faded support style = %q", supportPromptStyle(profile))
	}
}

func TestConversationSupportAdaptsBackAfterReleaseWithoutProfilingText(t *testing.T) {
	profile := conversationSupport{
		FadingStage:          maxSupportFadingStage,
		VerifiedFirstAnswers: 0,
	}
	profile = recordSupportRelease(profile)
	if profile.FadingStage != 1 ||
		profile.QuestionCooldown != questionCooldownAfterRelease ||
		profile.VerifiedFirstAnswers != 0 {
		t.Fatalf("release did not restore one bounded scaffold: %#v", profile)
	}

	for remaining := uint8(questionCooldownAfterRelease - 1); ; remaining-- {
		var blocked bool
		profile, blocked = consumeQuestionCooldown(profile)
		if !blocked || profile.QuestionCooldown != remaining {
			t.Fatalf("cooldown step = %#v blocked=%v", profile, blocked)
		}
		if remaining == 0 {
			break
		}
	}
	if _, blocked := consumeQuestionCooldown(profile); blocked {
		t.Fatal("zero cooldown remained blocked")
	}
}

func TestConversationSupportCompanionModeOverridesScaffolding(t *testing.T) {
	profile := conversationSupport{
		FadingStage:      1,
		QuestionCooldown: 2,
		CompanionOnly:    true,
	}
	if supportPromptStyle(profile) != "companion" {
		t.Fatalf("companion style = %q", supportPromptStyle(profile))
	}
	unchanged := recordVerifiedFirstAnswer(profile)
	if unchanged != profile {
		t.Fatalf("companion-only turn changed learning metadata: %#v", unchanged)
	}
}

func TestConversationSupportRejectsOutOfRangeAuthenticatedState(t *testing.T) {
	for _, profile := range []conversationSupport{
		{FadingStage: maxSupportFadingStage + 1},
		{VerifiedFirstAnswers: verifiedAnswersToFade},
		{QuestionCooldown: maxQuestionCooldown + 1},
	} {
		candidate := profile
		if _, err := normalizeConversationSupport(&candidate); err == nil {
			t.Fatalf("accepted out-of-range support state: %#v", profile)
		}
	}
	if compactConversationSupport(conversationSupport{}) != nil {
		t.Fatal("zero support metadata was not omitted")
	}
}

func TestConversationSupportRejectsCompanionModeWithActiveCoach(t *testing.T) {
	state := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	)
	state.Support = &conversationSupport{CompanionOnly: true}
	if _, err := normalizeConversationState(state); err == nil {
		t.Fatal("companion-only state retained an active coaching capability")
	}
}
