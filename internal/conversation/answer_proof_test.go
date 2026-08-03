package conversation

import (
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestAnswerProofRequiresCommittedQuestionInstanceBoundInput(t *testing.T) {
	validTurn := VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "東京です。",
		InputOrigin:   InputOriginCommittedVoice,
	}
	validFrame := PendingAnswerFrame{
		Active:                true,
		Phase:                 respondent.CoachPhaseAwaitingAnswer,
		QuestionInstanceTag:   "question-instance",
		QuestionContinuityTag: "question-subject",
		ContinuityTag:         "required-answer",
	}
	validDecision := respondent.CoachDecision{
		Phase:         respondent.CoachPhaseComplete,
		Action:        respondent.CoachActionComplete,
		VerifiedFirst: true,
	}

	if got := answerProofForTurn(
		validTurn,
		validFrame,
		validDecision,
		true,
		true,
		"respondent",
		"restructure",
	); got != AnswerProofQuestionBoundInputAnswerFirst {
		t.Fatalf("valid answer proof = %q", got)
	}

	tests := []struct {
		name       string
		turn       VoiceTurn
		frame      PendingAnswerFrame
		decision   respondent.CoachDecision
		continuity bool
		spanBound  bool
		target     string
		stage      string
	}{
		{name: "unknown input origin", turn: withInputOrigin(validTurn, InputOriginUnknown), frame: validFrame, decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "provisional input", turn: withInputOrigin(validTurn, InputOriginProvisionalVoice), frame: validFrame, decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "speculative input", turn: withSpeculative(validTurn), frame: validFrame, decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "strict input", turn: withResearchDisabled(validTurn), frame: validFrame, decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "document input", turn: withPDF(validTurn), frame: validFrame, decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "question instance missing", turn: validTurn, frame: withoutQuestionInstance(validFrame), decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "question subject missing", turn: validTurn, frame: withoutQuestionSubject(validFrame), decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "answer binding missing", turn: validTurn, frame: withoutAnswerBinding(validFrame), decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "assistant follow up", turn: validTurn, frame: withAssistantFollowUp(validFrame), decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "expansion answer", turn: validTurn, frame: withCoachPhase(validFrame, respondent.CoachPhaseExpanding), decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "continuity rejected", turn: validTurn, frame: validFrame, decision: validDecision, continuity: false, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "proof span unbound", turn: validTurn, frame: validFrame, decision: validDecision, continuity: true, spanBound: false, target: "respondent", stage: "restructure"},
		{name: "dual verification missing", turn: validTurn, frame: validFrame, decision: withoutVerifiedFirst(validDecision), continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
		{name: "assistant target", turn: validTurn, frame: validFrame, decision: validDecision, continuity: true, spanBound: true, target: "assistant", stage: "none"},
		{name: "wrong respondent stage", turn: validTurn, frame: validFrame, decision: validDecision, continuity: true, spanBound: true, target: "respondent", stage: "awaiting_answer"},
		{name: "nonterminal action", turn: validTurn, frame: validFrame, decision: withCoachDecision(validDecision, respondent.CoachPhaseAwaitingAnswer, respondent.CoachActionElicit), continuity: true, spanBound: true, target: "respondent", stage: "restructure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := answerProofForTurn(
				test.turn,
				test.frame,
				test.decision,
				test.continuity,
				test.spanBound,
				test.target,
				test.stage,
			); got != AnswerProofNone {
				t.Fatalf("unsafe answer proof = %q", got)
			}
		})
	}
}

func TestAnswerProofAllowsAuthorizedExpansionTriggeredByVerifiedAnswer(t *testing.T) {
	got := answerProofForTurn(
		VoiceTurn{InputOrigin: InputOriginCommittedVoice},
		PendingAnswerFrame{
			Phase:                 respondent.CoachPhaseAwaitingAnswer,
			QuestionInstanceTag:   "question-instance",
			QuestionContinuityTag: "question-subject",
			ContinuityTag:         "required-answer",
		},
		respondent.CoachDecision{
			Phase:         respondent.CoachPhaseExpanding,
			Action:        respondent.CoachActionExpand,
			VerifiedFirst: true,
		},
		true,
		true,
		"respondent",
		"restructure",
	)
	if got != AnswerProofQuestionBoundInputAnswerFirst {
		t.Fatalf("authorized expansion proof = %q", got)
	}
}

func TestQuestionInstanceTagIsContentFreeAndInstanceSpecific(t *testing.T) {
	agent := &vertexAgent{continuityKey: []byte(strings.Repeat("k", 32))}
	const (
		sessionA = "QUFBQUFBQUFBQUFBQUFBQQ"
		sessionB = "QkJCQkJCQkJCQkJCQkJCQg"
	)
	first := agent.coachQuestionInstanceTag(
		sessionA,
		"導入目的は何ですか？",
	)
	second := agent.coachQuestionInstanceTag(
		sessionA,
		"導入目的を教えてください？",
	)
	otherSession := agent.coachQuestionInstanceTag(
		sessionB,
		"導入目的は何ですか？",
	)
	if first == "" || second == "" || otherSession == "" ||
		first == second || first == otherSession {
		t.Fatalf(
			"question instance tags are not distinct: %q %q %q",
			first,
			second,
			otherSession,
		)
	}
	for _, tag := range []string{first, second, otherSession} {
		if strings.Contains(tag, "導入目的") || strings.Contains(tag, "質問") {
			t.Fatalf("question text leaked into tag: %q", tag)
		}
	}
}

func TestReportedQuestionOperatorMustBeUniqueAndMatchThePlan(t *testing.T) {
	tests := []struct {
		span string
		want answercontract.Operator
		ok   bool
	}{
		{span: "導入目的は何かと聞かれました", want: answercontract.OperatorPurpose, ok: true},
		{span: "導入時期はいつかと聞かれました", want: answercontract.OperatorOpen, ok: true},
		{span: "目的は何ですかと聞かれました", ok: false},
	}
	for _, test := range tests {
		got, ok := boundedReportedCoachQuestionOperator(test.span)
		if got != test.want || ok != test.ok {
			t.Fatalf("operator(%q)=(%q,%v), want (%q,%v)", test.span, got, ok, test.want, test.ok)
		}
	}
}

func TestQuestionInstanceAnchorBindsTheSameLatestReportedQuestion(t *testing.T) {
	const utterance = "上司に予算はいくらかと聞かれました。次に、導入目的は何かと聞かれました。答え方を一問だけ手伝って"
	anchor, ok := boundedReportedCoachQuestionInstanceAnchor(
		utterance,
		"導入目的",
	)
	if !ok || !strings.Contains(anchor, "導入目的") ||
		strings.Contains(anchor, "予算") {
		t.Fatalf("wrong reported-question instance anchor: %q %v", anchor, ok)
	}
	if _, ok := boundedReportedCoachQuestionInstanceAnchor(
		utterance,
		"予算",
	); ok {
		t.Fatal("an earlier reported question was accepted as the latest focus")
	}
}

func TestPendingAnswerDoesNotBindPlannerOnlyQuestion(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true
	agent.answerProofWrites = true
	plan := validModelPlan()
	plan.AssistanceTarget = "respondent"
	plan.RespondentStage = "awaiting_answer"
	plan.AnswerContract.QuestionFrame.Subject = "導入目的"
	plan.AnswerContract.QuestionFrame.Operator = "purpose"

	frame := agent.pendingAnswerFromPlan(
		plan,
		"上司に納期はいつかと聞かれました。答え方を手伝って",
		"QUFBQUFBQUFBQUFBQUFBQQ",
	)
	if frame.QuestionInstanceTag != "" || frame.QuestionContinuityTag != "" {
		t.Fatalf("planner-only question was bound: %#v", frame)
	}
}

func TestPendingAnswerDoesNotMintProofCapabilityForOperatorMismatch(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	agent.stateV2Writes = true
	agent.answerProofWrites = true
	plan := validModelPlan()
	plan.AssistanceTarget = "respondent"
	plan.RespondentStage = "awaiting_answer"
	plan.AnswerContract.QuestionFrame.Subject = "導入時期"
	plan.AnswerContract.QuestionFrame.Operator = answercontract.OperatorPurpose
	plan.AnswerContract.QuestionFrame.RequiredSlots =
		[]answercontract.RequiredSlot{answercontract.SlotPurpose}

	frame := agent.pendingAnswerFromPlan(
		plan,
		"上司に導入時期はいつかと聞かれました。答え方を手伝って",
		"QUFBQUFBQUFBQUFBQUFBQQ",
	)
	if frame.QuestionContinuityTag == "" {
		t.Fatal("ordinary coaching continuity was unexpectedly removed")
	}
	if frame.QuestionInstanceTag != "" {
		t.Fatalf("operator mismatch minted proof capability: %#v", frame)
	}
}

func withInputOrigin(turn VoiceTurn, origin InputOrigin) VoiceTurn {
	turn.InputOrigin = origin
	return turn
}

func withSpeculative(turn VoiceTurn) VoiceTurn {
	turn.Speculative = true
	return turn
}

func withResearchDisabled(turn VoiceTurn) VoiceTurn {
	turn.ResearchDisabled = true
	return turn
}

func withPDF(turn VoiceTurn) VoiceTurn {
	turn.PDF = &InlinePDF{MIMEType: "application/pdf", Data: []byte("pdf")}
	return turn
}

func withoutQuestionInstance(frame PendingAnswerFrame) PendingAnswerFrame {
	frame.QuestionInstanceTag = ""
	return frame
}

func withoutQuestionSubject(frame PendingAnswerFrame) PendingAnswerFrame {
	frame.QuestionContinuityTag = ""
	return frame
}

func withoutAnswerBinding(frame PendingAnswerFrame) PendingAnswerFrame {
	frame.ContinuityTag = ""
	return frame
}

func withAssistantFollowUp(frame PendingAnswerFrame) PendingAnswerFrame {
	frame.AssistantFollowUp = true
	return frame
}

func withCoachPhase(
	frame PendingAnswerFrame,
	phase respondent.CoachPhase,
) PendingAnswerFrame {
	frame.Phase = phase
	return frame
}

func withoutVerifiedFirst(
	decision respondent.CoachDecision,
) respondent.CoachDecision {
	decision.VerifiedFirst = false
	return decision
}

func withCoachDecision(
	decision respondent.CoachDecision,
	phase respondent.CoachPhase,
	action respondent.CoachAction,
) respondent.CoachDecision {
	decision.Phase = phase
	decision.Action = action
	return decision
}
