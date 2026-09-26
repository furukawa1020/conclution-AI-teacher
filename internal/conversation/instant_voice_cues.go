package conversation

import (
	"strings"
	"unicode/utf8"
)

const maxInstantVoiceCueRunes = 64

// AuditedInstantVoiceCues is the complete server-authored vocabulary that may
// be prepared before a user turn. Every entry is independent of user text,
// model output and conversation state. Invalid or duplicate fixed entries fail
// closed by being omitted rather than widening the warmup boundary.
func AuditedInstantVoiceCues() []string {
	candidates := append(
		AuditedQARCCues(),
		phaticLocalSpokenReply,
		listenOnlyLocalSpokenReply,
		proxyAnswerOptOutLocalSpokenReply,
		interpretationClarificationSpokenReply,
		interpretationListenSpokenReply,
		plannerUnavailableSpokenReply,
		verificationUnavailableSpokenReply,
		urgentSafetyFallbackSpokenReply,
	)
	cues := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, cue := range candidates {
		cue = strings.TrimSpace(cue)
		if cue == "" || !utf8.ValidString(cue) ||
			utf8.RuneCountInString(cue) > maxInstantVoiceCueRunes {
			continue
		}
		if _, duplicate := seen[cue]; duplicate {
			continue
		}
		seen[cue] = struct{}{}
		cues = append(cues, cue)
	}
	return cues
}
