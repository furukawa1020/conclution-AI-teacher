package conversation

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAuditedInstantVoiceCuesContainsFixedLocalAndQARCCopy(t *testing.T) {
	t.Parallel()

	cues := AuditedInstantVoiceCues()
	wantLocal := []string{
		phaticLocalSpokenReply,
		listenOnlyLocalSpokenReply,
		proxyAnswerOptOutLocalSpokenReply,
		interpretationClarificationSpokenReply,
		interpretationListenSpokenReply,
		plannerUnavailableSpokenReply,
		verificationUnavailableSpokenReply,
		urgentSafetyFallbackSpokenReply,
	}
	wantCount := len(AuditedQARCCues()) + len(wantLocal)
	if len(cues) != wantCount {
		t.Fatalf("instant cue count = %d, want %d", len(cues), wantCount)
	}
	seen := make(map[string]bool, len(cues))
	for _, cue := range cues {
		if strings.TrimSpace(cue) == "" || !utf8.ValidString(cue) {
			t.Fatalf("invalid instant cue %q", cue)
		}
		if utf8.RuneCountInString(cue) > maxInstantVoiceCueRunes {
			t.Fatalf("instant cue exceeds direct synthesis bound: %q", cue)
		}
		if seen[cue] {
			t.Fatalf("duplicate instant cue %q", cue)
		}
		seen[cue] = true
	}
	for _, cue := range append(AuditedQARCCues(), wantLocal...) {
		if !seen[cue] {
			t.Fatalf("fixed cue missing from instant catalog: %q", cue)
		}
	}
}

func TestAuditedInstantVoiceCuesReturnsIndependentSlice(t *testing.T) {
	t.Parallel()

	first := AuditedInstantVoiceCues()
	first[0] = "mutated"
	second := AuditedInstantVoiceCues()
	if second[0] == "mutated" {
		t.Fatal("caller mutated the audited instant cue catalog")
	}
}
