package respondent

import (
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
)

func TestGuideAttemptRequiresPersonToSayTargetFirst(t *testing.T) {
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
	if decision.Action != CoachActionRestate ||
		decision.Phase != CoachPhaseAwaitingRestatement ||
		!decision.KeepPending ||
		decision.StartExpansion {
		t.Fatalf("late answer was not kept for restatement: %#v", decision)
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

func TestGuideAttemptStartsExactlyOneExpansionAfterValidCoreAnswer(t *testing.T) {
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
	if decision.Action != CoachActionExpand ||
		decision.Phase != CoachPhaseExpanding ||
		!decision.StartExpansion ||
		!decision.KeepPending ||
		decision.Attempts != 0 ||
		!strings.HasSuffix(decision.SpokenReply, "？") {
		t.Fatalf("successful core answer did not start one bounded expansion: %#v", decision)
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
		expanded.KeepPending {
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
		!strings.Contains(abstained.SpokenReply, "まだ分からない") {
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
		decision.StartExpansion {
		t.Fatalf("one-shot answer started another coaching question: %#v", decision)
	}
}

func TestGuideAttemptNeverCompletesWithoutIndependentVerification(t *testing.T) {
	gate := Gate(purposeInput(
		"目的は評価基準をそろえることです。",
		"",
	))
	decision := GuideAttempt(
		OperatorPurpose,
		CoachPhaseAwaitingRestatement,
		0,
		gate,
		successfulCritic(),
		false,
		false,
		false,
	)
	if decision.Action != CoachActionRetry ||
		decision.Phase != CoachPhaseBlocked ||
		!decision.KeepPending ||
		decision.StartExpansion ||
		decision.Attempts != 1 {
		t.Fatalf("unverified answer was treated as success: %#v", decision)
	}
	second := GuideAttempt(
		OperatorPurpose,
		CoachPhaseAwaitingRestatement,
		decision.Attempts,
		gate,
		successfulCritic(),
		false,
		false,
		false,
	)
	if second.Action != CoachActionRetry ||
		!second.KeepPending ||
		second.Attempts != MaxCoachAttempts {
		t.Fatalf("second unverified attempt did not use the final retry: %#v", second)
	}
	third := GuideAttempt(
		OperatorPurpose,
		CoachPhaseAwaitingRestatement,
		second.Attempts,
		gate,
		successfulCritic(),
		false,
		false,
		false,
	)
	if third.Action != CoachActionRelease || third.KeepPending ||
		third.Attempts != MaxCoachAttempts {
		t.Fatalf("unverified attempts exceeded the retry cap: %#v", third)
	}
}

func TestGuideAttemptReleasesAfterTwoBoundedRetries(t *testing.T) {
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
	if second.Action != CoachActionElicit ||
		!second.KeepPending ||
		second.Attempts != MaxCoachAttempts {
		t.Fatalf("second miss did not use the final retry: %#v", second)
	}
	third := GuideAttempt(
		OperatorPurpose,
		second.Phase,
		second.Attempts,
		gate,
		critic,
		true,
		false,
		false,
	)
	if third.Action != CoachActionRelease || third.KeepPending ||
		third.Attempts != MaxCoachAttempts {
		t.Fatalf("third miss did not return to normal conversation: %#v", third)
	}
}

func TestGuideAttemptKeepsExpansionUntilItIsAnswered(t *testing.T) {
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
	if decision.Phase != CoachPhaseExpanding ||
		decision.Action != CoachActionExpand ||
		!decision.KeepPending ||
		decision.Attempts != 1 {
		t.Fatalf("incomplete expansion lost its bounded scope: %#v", decision)
	}

	completed := GuideAttempt(
		OperatorCause,
		decision.Phase,
		decision.Attempts,
		Gate(Input{
			Frame: QuestionFrame{
				Operator:      OperatorCause,
				Subject:       "理由",
				RequiredSlots: []Slot{SlotCause},
			},
			Attempt: AnswerAttempt{
				Text: "理由は時間を減らせるからです。",
				SlotEvidence: []SlotBinding{{
					Slot: SlotCause,
					Span: "理由は時間を減らせるからです",
				}},
			},
		}),
		successfulCritic(),
		true,
		false,
		false,
	)
	if completed.Phase != CoachPhaseComplete ||
		completed.Action != CoachActionComplete ||
		completed.KeepPending || completed.StartExpansion {
		t.Fatalf("valid expansion did not complete: %#v", completed)
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

func TestGuideAwaitingAllowsTwoRetriesBeforeRelease(t *testing.T) {
	first := GuideAwaiting(OperatorPurpose, 0, true)
	second := GuideAwaiting(OperatorPurpose, first.Attempts, true)
	third := GuideAwaiting(OperatorPurpose, second.Attempts, true)

	if first.Action != CoachActionElicit || first.Attempts != 1 ||
		second.Action != CoachActionElicit || !second.KeepPending ||
		second.Attempts != MaxCoachAttempts ||
		third.Action != CoachActionRelease || third.KeepPending ||
		third.Attempts != MaxCoachAttempts {
		t.Fatalf("awaiting retry cap mismatch: first=%#v second=%#v third=%#v", first, second, third)
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
