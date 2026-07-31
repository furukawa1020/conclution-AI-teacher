package conversation

import (
	"strings"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

const coachExpansionOptInPhrase = "理由まで一問お願いします"

// explicitCoachExpansionOptIn recognizes one deliberately narrow capability
// phrase. It is never inferred from intent, model output, quoted speech, or a
// report that somebody else used the phrase.
func explicitCoachExpansionOptIn(utterance string) bool {
	phrase := normalizeExplicitCoachPhrase(utterance)
	searchFrom := 0
	for searchFrom < len(phrase) {
		relative := strings.Index(
			phrase[searchFrom:],
			coachExpansionOptInPhrase,
		)
		if relative < 0 {
			return false
		}
		start := searchFrom + relative
		end := start + len(coachExpansionOptInPhrase)
		if expansionOptInBoundary(phrase[:start], true) &&
			expansionOptInBoundary(phrase[end:], false) &&
			!coachTextPositionInsideQuote(phrase, start) &&
			!expansionOptInReportedTail(phrase[end:]) {
			return true
		}
		searchFrom = end
	}
	return false
}

func standaloneCoachExpansionOptIn(utterance string) bool {
	return normalizeExplicitCoachPhrase(utterance) ==
		coachExpansionOptInPhrase
}

func expansionOptInBoundary(text string, before bool) bool {
	if text == "" {
		return true
	}
	var current rune
	if before {
		current, _ = utf8.DecodeLastRuneInString(text)
	} else {
		current, _ = utf8.DecodeRuneInString(text)
	}
	switch current {
	case '。', '！', '!', '？', '?', '；', ';', '、', ',', '\n', '\r':
		return true
	default:
		return false
	}
}

func expansionOptInReportedTail(tail string) bool {
	tail = strings.TrimLeft(tail, " \t\r\n。！？!?；;、,")
	if thirdPartyReportContext(tail) ||
		thirdPartyReportContext(strings.TrimPrefix(tail, "と")) ||
		thirdPartyReportContext(strings.TrimPrefix(tail, "って")) {
		return true
	}
	for _, reported := range []string{
		"と言", "って言", "とは言", "という", "っていう",
		"と話", "って話", "と頼", "って頼", "を頼",
		"とお願い", "をお願い", "と指示", "と命じ", "と書",
		"って書", "と聞いた", "って聞いた",
	} {
		if strings.HasPrefix(tail, reported) {
			return true
		}
	}
	return false
}

func (agent *vertexAgent) completeCoachExpansionOptInLocal(
	uid string,
	state conversationState,
) (VoiceTurnResult, error) {
	decision := ArbiterDecision{
		Benefit:    1,
		Confidence: 1,
		Score:      1,
		Act:        "reflect",
	}
	nextState := conversationState{
		SessionID:           state.SessionID,
		Turn:                state.Turn + 1,
		Graph:               state.Graph,
		PendingAnswer:       state.PendingAnswer,
		Support:             state.Support,
		SelfCorrectionGrace: state.SelfCorrectionGrace,
		LastIntervention:    state.LastIntervention,
	}
	stateToken, err := agent.sealState(uid, nextState)
	if err != nil {
		return VoiceTurnResult{}, err
	}
	return VoiceTurnResult{
		SchemaVersion:       SchemaVersion,
		Domain:              "daily",
		Intent:              "other",
		AssistanceTarget:    "assistant",
		RespondentStage:     "none",
		CoachPhase:          string(respondent.CoachPhaseNone),
		CoachAction:         string(respondent.CoachActionNone),
		ResearchStatus:      "none",
		ResearchRecords:     []ResearchRecord{},
		ArgumentStructure:   "direct_answer",
		InterventionPolicy:  "wait",
		SpokenReply:         "わかりました。まず聞かれたことに答えたあと、理由を一問だけ聞きます。",
		Confidence:          1,
		Intervention:        decision,
		SelfCorrectionGrace: state.SelfCorrectionGrace,
		AnswerContract: answercontract.Metrics{
			CommitmentFrontPosition: answercontract.PositionAbsent,
		},
		Route:              "coach-expansion-opt-in-local",
		NeedsClarification: false,
		StateToken:         stateToken,
	}, nil
}

func (agent *vertexAgent) completeCoachExpansionHoldLocal(
	uid string,
	state conversationState,
) (VoiceTurnResult, error) {
	nextState := conversationState{
		SessionID:           state.SessionID,
		Turn:                state.Turn + 1,
		Graph:               state.Graph,
		PendingAnswer:       state.PendingAnswer,
		Support:             state.Support,
		SelfCorrectionGrace: state.SelfCorrectionGrace,
		LastIntervention:    state.LastIntervention,
	}
	stateToken, err := agent.sealState(uid, nextState)
	if err != nil {
		return VoiceTurnResult{}, err
	}
	decision := ArbiterDecision{Confidence: 1, Act: "silent"}
	return VoiceTurnResult{
		SchemaVersion:       SchemaVersion,
		Domain:              "daily",
		Intent:              "other",
		AssistanceTarget:    "respondent",
		RespondentStage:     "awaiting_answer",
		CoachPhase:          string(respondent.CoachPhaseExpanding),
		CoachAction:         string(respondent.CoachActionExpand),
		ResearchStatus:      "none",
		ResearchRecords:     []ResearchRecord{},
		ArgumentStructure:   "direct_answer",
		InterventionPolicy:  "wait",
		SpokenReply:         "",
		Confidence:          1,
		Intervention:        decision,
		SelfCorrectionGrace: state.SelfCorrectionGrace,
		AnswerContract: answercontract.Metrics{
			CommitmentFrontPosition: answercontract.PositionAbsent,
		},
		Route:              "coach-expansion-hold-local",
		NeedsClarification: false,
		StateToken:         stateToken,
	}, nil
}
