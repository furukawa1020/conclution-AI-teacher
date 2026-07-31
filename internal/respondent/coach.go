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
	Phase       CoachPhase
	Action      CoachAction
	SpokenReply string
	Attempts    uint8
	KeepPending bool
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

// HoldForHesitation leaves space after filler-only speech. It neither consumes
// an attempt nor repeats the question, so thinking aloud cannot create a
// coercive prompt loop.
func HoldForHesitation(
	phase CoachPhase,
	attempts uint8,
) CoachDecision {
	action := CoachActionElicit
	switch phase {
	case CoachPhaseAwaitingRestatement:
		action = CoachActionRestate
	case CoachPhaseExpanding:
		action = CoachActionExpand
	case CoachPhaseBlocked:
		action = CoachActionRetry
	case CoachPhaseAwaitingAnswer:
	default:
		phase = CoachPhaseAwaitingAnswer
	}
	return CoachDecision{
		Phase:       phase,
		Action:      action,
		Attempts:    attempts,
		KeepPending: true,
	}
}

// GuideAwaitingInPhase preserves an already-authorized scope when a follow-up
// utterance does not yet contain an answer attempt. A counted miss may produce
// one gentle retry; the second counted miss always returns to ordinary
// conversation.
func GuideAwaitingInPhase(
	operator Operator,
	phase CoachPhase,
	attempts uint8,
	countAttempt bool,
) CoachDecision {
	nextAttempts := attempts
	if countAttempt {
		if attempts >= MaxCoachAttempts-1 {
			return releaseDecision(MaxCoachAttempts)
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
// independent LAC critic must agree before a turn can complete.
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
		if attempts >= MaxCoachAttempts-1 {
			return releaseDecision(MaxCoachAttempts)
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
		if oneShot {
			return CoachDecision{
				Phase:       CoachPhaseComplete,
				Action:      CoachActionComplete,
				SpokenReply: naturalContinuationReply(operator, abstained),
				Attempts:    attempts,
				KeepPending: false,
			}
		}
		if phase == CoachPhaseExpanding || abstained {
			return CoachDecision{
				Phase:       CoachPhaseComplete,
				Action:      CoachActionComplete,
				SpokenReply: completionReply(abstained),
				Attempts:    attempts,
				KeepPending: false,
			}
		}
		return CoachDecision{
			Phase:       CoachPhaseComplete,
			Action:      CoachActionComplete,
			SpokenReply: naturalContinuationReply(operator, abstained),
			Attempts:    attempts,
			KeepPending: false,
		}
	}

	// Expansion existed in older state tokens. It is an optional continuation,
	// never a second answer-first test: once both deterministic checks found
	// substantive target content, close it regardless of clause order.
	if phase == CoachPhaseExpanding &&
		gate.OriginalTargetCoverage == 1 &&
		critic.Metrics.TargetSlotCoverage == 1 {
		return CoachDecision{
			Phase:       CoachPhaseComplete,
			Action:      CoachActionComplete,
			SpokenReply: completionReply(abstained),
			Attempts:    attempts,
			KeepPending: false,
		}
	}

	nextAttempts := attempts + 1
	if nextAttempts >= MaxCoachAttempts {
		return releaseDecision(MaxCoachAttempts)
	}
	if phase == CoachPhaseExpanding {
		// A legacy expansion with no target content gets at most one gentle
		// prompt before the coach releases it.
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
	_ = operator
	return "そこまでちゃんと聞こえています。今の言葉は変えず、答えになっている一文から、もう一度だけ話してみますか？"
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
		return "うん、そのままで大丈夫です。"
	}
	return "うん、なるほど。"
}

// naturalContinuationReply is operator-conditioned but contains no model text,
// answer text, inferred personal trait, or specific question the next turn
// would need to remember. It keeps a successful exchange conversational
// without opening a second test or an unaudited TTS path.
func naturalContinuationReply(operator Operator, abstained bool) string {
	if abstained {
		return "うん、そのままで大丈夫です。話したいところから続けてください。"
	}
	switch operator {
	case OperatorBoolean, OperatorChoice:
		return "うん、そちらなんですね。その続きも聞かせてください。"
	case OperatorQuantity:
		return "なるほど、そのくらいなんですね。その続きも聞かせてください。"
	case OperatorState:
		return "なるほど、今はそうなんですね。その続きも聞かせてください。"
	case OperatorCause, OperatorPurpose:
		return "なるほど、そこが大事なんですね。その続きも聞かせてください。"
	case OperatorProcedure:
		return "なるほど、そこから始めるんですね。その続きも聞かせてください。"
	case OperatorDefinition:
		return "なるほど、そう捉えているんですね。その続きも聞かせてください。"
	case OperatorComparison, OperatorEvidence:
		return "なるほど、そこが判断の軸なんですね。その続きも聞かせてください。"
	default:
		return "うん、なるほど。その続きも聞かせてください。"
	}
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
