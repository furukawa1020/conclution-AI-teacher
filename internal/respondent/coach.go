package respondent

import "github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"

// CoachPhase is bounded control state for helping a person answer in their
// own words. It never contains their answer, a transcript, or model prose.
type CoachPhase string

const (
	CoachPhaseNone                CoachPhase = "none"
	CoachPhaseAwaitingAnswer      CoachPhase = "awaiting_answer"
	CoachPhaseAwaitingRestatement CoachPhase = "awaiting_restatement"
	CoachPhaseExpanding           CoachPhase = "expanding"
	CoachPhaseComplete            CoachPhase = "complete"
	CoachPhaseBlocked             CoachPhase = "blocked"
	MaxCoachAttempts                         = 2
)

// CoachAction is the authoritative, server-derived next action. The planner's
// respondent stage is advisory and must not be used as UI completion state.
type CoachAction string

const (
	CoachActionNone     CoachAction = "none"
	CoachActionElicit   CoachAction = "elicit"
	CoachActionRestate  CoachAction = "restate"
	CoachActionExpand   CoachAction = "expand"
	CoachActionComplete CoachAction = "complete"
	CoachActionRetry    CoachAction = "retry"
	CoachActionRelease  CoachAction = "release"
)

// CoachDecision contains fixed control values and fixed spoken guidance. It
// never repeats or reconstructs the person's answer.
type CoachDecision struct {
	Phase          CoachPhase
	Action         CoachAction
	SpokenReply    string
	Attempts       uint8
	KeepPending    bool
	StartExpansion bool
}

// GuideAwaiting asks only for the answer type requested by the question.
// Existing attempts are bounded so coaching cannot trap a person in a loop.
func GuideAwaiting(
	operator Operator,
	attempts uint8,
	countAttempt bool,
) CoachDecision {
	return GuideAwaitingInPhase(
		operator,
		CoachPhaseAwaitingAnswer,
		attempts,
		countAttempt,
	)
}

// GuideAwaitingInPhase preserves the already-authorized expansion scope when
// a follow-up utterance does not yet contain an answer attempt.
func GuideAwaitingInPhase(
	operator Operator,
	phase CoachPhase,
	attempts uint8,
	countAttempt bool,
) CoachDecision {
	nextAttempts := attempts
	if countAttempt {
		if attempts >= MaxCoachAttempts {
			return releaseDecision(attempts)
		}
		nextAttempts++
	}
	if phase == CoachPhaseExpanding {
		return CoachDecision{
			Phase:       CoachPhaseExpanding,
			Action:      CoachActionExpand,
			SpokenReply: expansionPrompt(operator),
			Attempts:    nextAttempts,
			KeepPending: true,
		}
	}
	if phase == CoachPhaseAwaitingRestatement {
		return CoachDecision{
			Phase:       CoachPhaseAwaitingRestatement,
			Action:      CoachActionRestate,
			SpokenReply: gentleReaskPrompt(operator),
			Attempts:    nextAttempts,
			KeepPending: true,
		}
	}
	return CoachDecision{
		Phase:       CoachPhaseAwaitingAnswer,
		Action:      CoachActionElicit,
		SpokenReply: corePrompt(operator),
		Attempts:    nextAttempts,
		KeepPending: true,
	}
}

// GuideAttempt decides whether the person—not an AI reconstruction—has put
// the requested answer first. Both the exact-span respondent gate and the
// independent LAC critic must agree before a turn can complete or expand.
func GuideAttempt(
	operator Operator,
	phase CoachPhase,
	attempts uint8,
	gate Assessment,
	critic answercontract.Assessment,
	verificationAvailable bool,
	abstained bool,
	oneShot bool,
) CoachDecision {
	if !verificationAvailable {
		if attempts >= MaxCoachAttempts {
			return releaseDecision(attempts)
		}
		return CoachDecision{
			Phase:       CoachPhaseBlocked,
			Action:      CoachActionRetry,
			SpokenReply: "今のところを、もう少しだけ聞かせてもらえますか？",
			Attempts:    attempts + 1,
			KeepPending: true,
		}
	}

	succeeded := gate.Outcome == OutcomeKeep &&
		gate.OriginalCommitmentPosition == PositionFirst &&
		gate.OriginalTargetCoverage == 1 &&
		gate.TargetSatisfied &&
		critic.Outcome == answercontract.OutcomeKeep &&
		critic.TargetSatisfied &&
		critic.Metrics.CommitmentFrontPosition == answercontract.PositionFirst &&
		critic.Metrics.TargetSlotCoverage == 1
	if succeeded {
		if phase == CoachPhaseExpanding || abstained || oneShot {
			return CoachDecision{
				Phase:       CoachPhaseComplete,
				Action:      CoachActionComplete,
				SpokenReply: completionReply(abstained),
				Attempts:    attempts,
				KeepPending: false,
			}
		}
		return CoachDecision{
			Phase:          CoachPhaseExpanding,
			Action:         CoachActionExpand,
			SpokenReply:    expansionPrompt(expansionOperator(operator)),
			Attempts:       0,
			KeepPending:    true,
			StartExpansion: true,
		}
	}

	if attempts >= MaxCoachAttempts {
		return releaseDecision(attempts)
	}
	nextAttempts := attempts + 1
	if phase == CoachPhaseExpanding {
		// Expansion is a single bounded follow-up. Keep that scope while the
		// person retries, rather than silently returning to the original
		// question after one incomplete reason or example.
		reply := expansionPrompt(operator)
		if gate.OriginalCommitmentPosition == PositionLater ||
			critic.Metrics.CommitmentFrontPosition == answercontract.PositionLater {
			reply = "今の話には支えの核があります。その一文を最初にして、あなたの言葉でもう一度どうぞ。"
		}
		return CoachDecision{
			Phase:       CoachPhaseExpanding,
			Action:      CoachActionExpand,
			SpokenReply: reply,
			Attempts:    nextAttempts,
			KeepPending: true,
		}
	}
	if gate.OriginalCommitmentPosition == PositionLater ||
		critic.Metrics.CommitmentFrontPosition == answercontract.PositionLater {
		return CoachDecision{
			Phase:       CoachPhaseAwaitingRestatement,
			Action:      CoachActionRestate,
			SpokenReply: gentleReaskPrompt(operator),
			Attempts:    nextAttempts,
			KeepPending: true,
		}
	}
	if gate.OriginalTargetCoverage < 1 ||
		gate.OriginalCommitmentPosition == PositionAbsent ||
		critic.Metrics.TargetSlotCoverage < 1 ||
		critic.Metrics.CommitmentFrontPosition == answercontract.PositionAbsent ||
		gate.Outcome == OutcomeClarify ||
		critic.Outcome == answercontract.OutcomeClarify ||
		critic.Ambiguous {
		return CoachDecision{
			Phase:       CoachPhaseAwaitingAnswer,
			Action:      CoachActionElicit,
			SpokenReply: corePrompt(operator),
			Attempts:    nextAttempts,
			KeepPending: true,
		}
	}
	return CoachDecision{
		Phase:       CoachPhaseBlocked,
		Action:      CoachActionRetry,
		SpokenReply: "そこまで聞けています。もう少しだけ、続けてもらえますか？",
		Attempts:    nextAttempts,
		KeepPending: true,
	}
}

func gentleReaskPrompt(operator Operator) string {
	return "そこまでちゃんと聞こえています。" + corePrompt(operator)
}

// ExpansionOperator chooses one bounded follow-up question. It is structural,
// not a generated claim, and creates no external action authority.
func ExpansionOperator(operator Operator) Operator {
	return expansionOperator(operator)
}

func expansionOperator(operator Operator) Operator {
	switch operator {
	case OperatorDefinition, OperatorQuantity, OperatorComparison,
		OperatorEvidence:
		return OperatorEvidence
	case OperatorProcedure:
		return OperatorState
	default:
		return OperatorCause
	}
}

func corePrompt(operator Operator) string {
	switch operator {
	case OperatorBoolean:
		return "ひとつだけ、するのかしないのかなら、どちらですか？"
	case OperatorChoice:
		return "ひとつだけ選ぶなら、どれですか？"
	case OperatorQuantity:
		return "数字だけなら、いくつですか？"
	case OperatorState:
		return "今どうなっているか、一言だけなら？"
	case OperatorCause:
		return "いちばん大きな理由を一つだけ挙げるなら？"
	case OperatorPurpose:
		return "いちばん実現したいことを一つだけ挙げるなら？"
	case OperatorProcedure:
		return "最初の一歩だけ挙げるなら、何をしますか？"
	case OperatorDefinition:
		return "それが何か、一言だけなら？"
	case OperatorComparison:
		return "いちばん大きな違いを一つだけ挙げるなら？"
	case OperatorEvidence:
		return "いちばん確かな根拠を一つだけ挙げるなら？"
	default:
		return "いちばん言いたいことを、一言だけなら？"
	}
}

func expansionPrompt(operator Operator) string {
	switch operator {
	case OperatorEvidence:
		return "今は聞かれたことへ先に答えられています。次に、それを支える具体例か根拠を一つだけ言うと？"
	case OperatorState:
		return "今は聞かれたことへ先に答えられています。次に、最初の一歩は何ですか？"
	default:
		return "今は聞かれたことへ先に答えられています。次に、その理由を一つだけ言うと？"
	}
}

func completionReply(abstained bool) string {
	if abstained {
		return "「まだ分からない」でも大丈夫です。今の言い方で、ちゃんと伝わっています。"
	}
	return "うん、今の言い方なら、すっと伝わりました。"
}

func releaseDecision(attempts uint8) CoachDecision {
	return CoachDecision{
		Phase:       CoachPhaseBlocked,
		Action:      CoachActionRelease,
		SpokenReply: "大丈夫です。言い直さなくても、今のままで話を続けられます。",
		Attempts:    attempts,
		KeepPending: false,
	}
}
