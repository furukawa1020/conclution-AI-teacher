package conversation

const (
	maxSupportFadingStage        = 2
	verifiedAnswersToFade        = 2
	questionCooldownAfterAnswer  = 2
	questionCooldownAfterPass    = 2
	questionCooldownAfterRelease = 3
	maxQuestionCooldown          = 3
)

// conversationSupport contains only bounded, non-semantic session metadata.
// It never stores audio, transcript text, a question, an answer, a diagnosis,
// or a model-authored description of the person. The encrypted state token
// expires after the normal fifteen-minute state TTL.
type conversationSupport struct {
	FadingStage          uint8 `json:"fading_stage,omitempty"`
	VerifiedFirstAnswers uint8 `json:"verified_first_answers,omitempty"`
	QuestionCooldown     uint8 `json:"question_cooldown,omitempty"`
	CompanionOnly        bool  `json:"companion_only,omitempty"`
}

func normalizeConversationSupport(
	profile *conversationSupport,
) (*conversationSupport, error) {
	if profile == nil {
		return nil, nil
	}
	normalized := *profile
	if normalized.FadingStage > maxSupportFadingStage ||
		normalized.VerifiedFirstAnswers >= verifiedAnswersToFade ||
		normalized.QuestionCooldown > maxQuestionCooldown {
		return nil, ErrInvalidStateToken
	}
	if normalized.FadingStage == maxSupportFadingStage {
		normalized.VerifiedFirstAnswers = 0
	}
	return compactConversationSupport(normalized), nil
}

func conversationSupportValue(
	profile *conversationSupport,
) conversationSupport {
	if profile == nil {
		return conversationSupport{}
	}
	return *profile
}

func compactConversationSupport(
	profile conversationSupport,
) *conversationSupport {
	if profile == (conversationSupport{}) {
		return nil
	}
	copy := profile
	return &copy
}

func supportPromptStyle(profile conversationSupport) string {
	if profile.CompanionOnly {
		return "companion"
	}
	if profile.QuestionCooldown > 0 {
		return "listen"
	}
	switch profile.FadingStage {
	case 0:
		return "guided"
	case 1:
		return "light"
	default:
		return "natural"
	}
}

func recordVerifiedFirstAnswer(
	profile conversationSupport,
) conversationSupport {
	if profile.CompanionOnly {
		return profile
	}
	profile.QuestionCooldown = questionCooldownAfterAnswer
	if profile.FadingStage >= maxSupportFadingStage {
		profile.VerifiedFirstAnswers = 0
		return profile
	}
	profile.VerifiedFirstAnswers++
	if profile.VerifiedFirstAnswers >= verifiedAnswersToFade {
		profile.FadingStage++
		profile.VerifiedFirstAnswers = 0
	}
	return profile
}

func recordSupportRelease(profile conversationSupport) conversationSupport {
	profile.VerifiedFirstAnswers = 0
	profile.QuestionCooldown = questionCooldownAfterRelease
	if profile.FadingStage > 0 {
		profile.FadingStage--
	}
	return profile
}

func recordSupportPass(profile conversationSupport) conversationSupport {
	profile.VerifiedFirstAnswers = 0
	profile.QuestionCooldown = questionCooldownAfterPass
	return profile
}

func consumeQuestionCooldown(
	profile conversationSupport,
) (conversationSupport, bool) {
	if profile.QuestionCooldown == 0 {
		return profile, false
	}
	profile.QuestionCooldown--
	return profile, true
}
