package respondent

import (
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
)

func TestGuideAttemptAcceptsLateTargetWithoutCompulsoryRestatement(t *testing.T) {
	gate := Gate(purposeInput(
		"判断のばらつきを減らします。目的は評価基準をそろえることです。",
		"",
	))
	critic := successfulCritic()
	critic.Metrics.CommitmentFrontPosition = answercontract.PositionLater
	critic.TargetSatisfied = false
	critic.Outcome = answercontract.OutcomeRestructure

	decision := GuideAttempt(
		OperatorPurpose,
		CoachPhaseAwaitingAnswer,
		0,
		gate,
		critic,
		true,
		false,
		false,
	)
	if decision.Action != CoachActionComplete ||
		decision.Phase != CoachPhaseComplete ||
		decision.KeepPending ||
		decision.VerifiedFirst ||
		!strings.Contains(decision.SpokenReply, "最初に置く") ||
		strings.HasSuffix(decision.SpokenReply, "？") {
		t.Fatalf("late answer became a compulsory restatement: %#v", decision)
	}
	if strings.Contains(decision.SpokenReply, "評価基準") {
		t.Fatalf("coach repeated the person's answer: %q", decision.SpokenReply)
	}
}

func TestGuideAttemptUsesOperatorPromptForAmbiguousTarget(t *testing.T) {
	gate := Gate(Input{
		Frame: QuestionFrame{
			Operator:      OperatorQuantity,
			Subject:       "件数",
			RequiredSlots: []Slot{SlotQuantity},
			Ambiguous:     true,
		},
		Attempt: AnswerAttempt{
			Text: "三件です。",
			SlotEvidence: []SlotBinding{{
				Slot: SlotQuantity,
				Span: "三件です。",
			}},
		},
	})
	critic := successfulCritic()
	critic.Ambiguous = true
	critic.TargetSatisfied = false
	critic.Outcome = answercontract.OutcomeClarify

	decision := GuideAttempt(
		OperatorQuantity,
		CoachPhaseAwaitingAnswer,
		0,
		gate,
		critic,
		true,
		false,
		false,
	)
	if decision.Action != CoachActionElicit ||
		decision.Phase != CoachPhaseAwaitingAnswer ||
		!strings.Contains(decision.SpokenReply, "数字だけ") ||
		strings.Contains(decision.SpokenReply, "三件") {
		t.Fatalf("ambiguous target was not narrowed structurally: %#v", decision)
	}
}

func TestGuideAttemptCompletesWithoutOpeningAnotherQuestion(t *testing.T) {
	gate := Gate(purposeInput(
		"目的は評価基準をそろえることです。",
		"",
	))
	decision := GuideAttempt(
		OperatorPurpose,
		CoachPhaseAwaitingRestatement,
		1,
		gate,
		successfulCritic(),
		true,
		false,
		false,
	)
	if decision.Action != CoachActionComplete ||
		decision.Phase != CoachPhaseComplete ||
		decision.KeepPending ||
		!decision.VerifiedFirst ||
		strings.HasSuffix(decision.SpokenReply, "？") {
		t.Fatalf("successful core answer did not return to natural conversation: %#v", decision)
	}
}

func TestGuideAttemptCompletesExpansionAndAbstention(t *testing.T) {
	success := successfulCritic()
	gate := Gate(purposeInput(
		"目的は評価基準をそろえることです。",
		"",
	))
	expanded := GuideAttempt(
		OperatorPurpose,
		CoachPhaseExpanding,
		0,
		gate,
		success,
		true,
		false,
		false,
	)
	if expanded.Action != CoachActionComplete ||
		expanded.Phase != CoachPhaseComplete ||
		expanded.KeepPending ||
		expanded.VerifiedFirst {
		t.Fatalf("expanded answer did not complete: %#v", expanded)
	}

	abstained := GuideAttempt(
		OperatorPurpose,
		CoachPhaseAwaitingAnswer,
		0,
		gate,
		success,
		true,
		true,
		false,
	)
	if abstained.Action != CoachActionComplete ||
		abstained.KeepPending ||
		!abstained.VerifiedFirst ||
		!strings.Contains(abstained.SpokenReply, "大丈夫") {
		t.Fatalf("abstention was not accepted as an answer: %#v", abstained)
	}
}

func TestGuideAttemptOneShotCompletesWithoutStartingExpansion(t *testing.T) {
	gate := Gate(purposeInput(
		"目的は評価基準をそろえることです。",
		"",
	))
	decision := GuideAttempt(
		OperatorPurpose,
		CoachPhaseAwaitingAnswer,
		0,
		gate,
		successfulCritic(),
		true,
		false,
		true,
	)
	if decision.Action != CoachActionComplete ||
		decision.Phase != CoachPhaseComplete ||
		decision.KeepPending ||
		!decision.VerifiedFirst ||
		decision.SpokenReply != "なるほど、そう考えているんですね。" ||
		strings.Contains(decision.SpokenReply, "評価基準") ||
		strings.HasSuffix(decision.SpokenReply, "？") {
		t.Fatalf("one-shot answer started another coaching question: %#v", decision)
	}
}

func TestGuideAttemptVerificationFailureDoesNotCountAsAnAttempt(t *testing.T) {
	gate := Gate(purposeInput(
		"目的は評価基準をそろえることです。",
		"",
	))
	for _, attempts := range []uint8{0, MaxCoachAttempts - 1} {
		decision := GuideAttempt(
			OperatorPurpose,
			CoachPhaseAwaitingRestatement,
			attempts,
			gate,
			successfulCritic(),
			false,
			false,
			false,
		)
		if decision.Action != CoachActionRetry ||
			decision.Phase != CoachPhaseBlocked ||
			!decision.KeepPending ||
			decision.Attempts != attempts ||
			decision.VerifiedFirst ||
			!strings.Contains(decision.SpokenReply, "あなたの言い方の問題ではありません") ||
			strings.HasSuffix(decision.SpokenReply, "？") {
			t.Fatalf("verification failure was counted or blamed on the speaker: %#v", decision)
		}
	}
}

func TestHoldForHesitationWaitsWithoutRepromptingOrCounting(t *testing.T) {
	for _, test := range []struct {
		phase  CoachPhase
		action CoachAction
	}{
		{CoachPhaseAwaitingAnswer, CoachActionElicit},
		{CoachPhaseAwaitingRestatement, CoachActionRestate},
		{CoachPhaseExpanding, CoachActionExpand},
		{CoachPhaseBlocked, CoachActionRetry},
	} {
		decision := HoldForHesitation(test.phase, 1)
		if decision.Phase != test.phase ||
			decision.Action != test.action ||
			decision.SpokenReply != "" ||
			decision.Attempts != 1 ||
			!decision.KeepPending {
			t.Fatalf("HoldForHesitation(%q): %#v", test.phase, decision)
		}
	}
}

func TestGuideAttemptReleasesAfterOneGentleRetry(t *testing.T) {
	gate := Gate(Input{
		Frame: QuestionFrame{
			Operator:      OperatorPurpose,
			Subject:       "目的",
			RequiredSlots: []Slot{SlotPurpose},
		},
		Attempt: AnswerAttempt{Text: "背景を説明します。"},
	})
	critic := successfulCritic()
	critic.Metrics.CommitmentFrontPosition = answercontract.PositionAbsent
	critic.Metrics.TargetSlotCoverage = 0
	critic.TargetSatisfied = false
	critic.Outcome = answercontract.OutcomeClarify

	first := GuideAttempt(
		OperatorPurpose,
		CoachPhaseAwaitingAnswer,
		0,
		gate,
		critic,
		true,
		false,
		false,
	)
	if first.Action != CoachActionElicit || first.Attempts != 1 {
		t.Fatalf("first miss was not elicited: %#v", first)
	}
	second := GuideAttempt(
		OperatorPurpose,
		first.Phase,
		first.Attempts,
		gate,
		critic,
		true,
		false,
		false,
	)
	if second.Action != CoachActionRelease ||
		second.KeepPending ||
		second.Attempts != MaxCoachAttempts {
		t.Fatalf("second miss did not return to normal conversation: %#v", second)
	}
}

func TestGuideAttemptNeverTestsOptionalExpansion(t *testing.T) {
	gate := Gate(Input{
		Frame: QuestionFrame{
			Operator:      OperatorCause,
			Subject:       "理由",
			RequiredSlots: []Slot{SlotCause},
		},
		Attempt: AnswerAttempt{
			Text: "背景から説明します。理由は時間を減らせるからです。",
			SlotEvidence: []SlotBinding{{
				Slot: SlotCause,
				Span: "理由は時間を減らせるからです。",
			}},
		},
	})
	critic := successfulCritic()
	critic.Outcome = answercontract.OutcomeRestructure
	critic.TargetSatisfied = false
	critic.Metrics.CommitmentFrontPosition = answercontract.PositionLater

	decision := GuideAttempt(
		OperatorCause,
		CoachPhaseExpanding,
		0,
		gate,
		critic,
		true,
		false,
		false,
	)
	if decision.Phase != CoachPhaseComplete ||
		decision.Action != CoachActionComplete ||
		decision.KeepPending {
		t.Fatalf("optional expansion became a second test: %#v", decision)
	}
}

func TestGuideAwaitingUsesQuestionShapeWithoutInventingAnswer(t *testing.T) {
	decision := GuideAwaiting(OperatorChoice, 0, false)
	if decision.Action != CoachActionElicit ||
		decision.Phase != CoachPhaseAwaitingAnswer ||
		!decision.KeepPending ||
		!strings.Contains(decision.SpokenReply, "どれですか") {
		t.Fatalf("choice prompt mismatch: %#v", decision)
	}
}

func TestGuideAwaitingPreservesExpansionScope(t *testing.T) {
	decision := GuideAwaitingInPhase(
		OperatorCause,
		CoachPhaseExpanding,
		0,
		true,
	)
	if decision.Phase != CoachPhaseExpanding ||
		decision.Action != CoachActionExpand ||
		decision.Attempts != 1 ||
		!decision.KeepPending {
		t.Fatalf("awaiting follow-up lost expansion scope: %#v", decision)
	}
}

func TestGuideAwaitingPreservesRestatementScope(t *testing.T) {
	decision := GuideAwaitingInPhase(
		OperatorPurpose,
		CoachPhaseAwaitingRestatement,
		0,
		true,
	)
	if decision.Phase != CoachPhaseAwaitingRestatement ||
		decision.Action != CoachActionRestate ||
		decision.Attempts != 1 ||
		!decision.KeepPending {
		t.Fatalf("awaiting retry lost restatement scope: %#v", decision)
	}
}

func TestGuideAwaitingAllowsOneRetryBeforeRelease(t *testing.T) {
	first := GuideAwaiting(OperatorPurpose, 0, true)
	second := GuideAwaiting(OperatorPurpose, first.Attempts, true)

	if first.Action != CoachActionElicit || first.Attempts != 1 ||
		second.Action != CoachActionRelease || second.KeepPending ||
		second.Attempts != MaxCoachAttempts {
		t.Fatalf("awaiting retry cap mismatch: first=%#v second=%#v", first, second)
	}
}

func TestExpansionOperatorIsBounded(t *testing.T) {
	tests := map[Operator]Operator{
		OperatorDefinition: OperatorEvidence,
		OperatorQuantity:   OperatorEvidence,
		OperatorComparison: OperatorEvidence,
		OperatorEvidence:   OperatorEvidence,
		OperatorProcedure:  OperatorState,
		OperatorPurpose:    OperatorCause,
		OperatorBoolean:    OperatorCause,
	}
	for input, want := range tests {
		if got := ExpansionOperator(input); got != want {
			t.Fatalf("ExpansionOperator(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestExplicitExpansionStartsOnceAndClosesWithoutSecondSuccess(t *testing.T) {
	started := BeginExpansion(OperatorCause)
	if started.Phase != CoachPhaseExpanding ||
		started.Action != CoachActionExpand ||
		!started.KeepPending ||
		!started.VerifiedFirst ||
		started.Attempts != 0 ||
		!strings.Contains(started.SpokenReply, "『その理由は』") {
		t.Fatalf("explicit expansion did not start with one bounded prompt: %#v", started)
	}

	completed := CompleteExpansion(false)
	if completed.Phase != CoachPhaseComplete ||
		completed.Action != CoachActionComplete ||
		completed.KeepPending ||
		completed.VerifiedFirst ||
		strings.HasSuffix(completed.SpokenReply, "？") {
		t.Fatalf("expansion became a recursive test: %#v", completed)
	}
}

func successfulCritic() answercontract.Assessment {
	return answercontract.Assessment{
		Metrics: answercontract.Metrics{
			TargetSlotCoverage:      1,
			CommitmentFrontPosition: answercontract.PositionFirst,
		},
		Outcome:         answercontract.OutcomeKeep,
		TargetSatisfied: true,
	}
}
