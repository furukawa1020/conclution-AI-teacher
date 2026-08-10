package conversation

import (
	"bytes"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func transitionTestFrame() PendingAnswerFrame {
	return PendingAnswerFrame{
		Active:                   true,
		Operator:                 answercontract.OperatorChoice,
		RequiredSlots:            []answercontract.RequiredSlot{answercontract.SlotPosition},
		ExpansionOperator:        answercontract.OperatorCause,
		Phase:                    respondent.CoachPhaseAwaitingRestatement,
		Attempts:                 1,
		QuestionInstanceTag:      "AAAAAAAAAAAAAAAAAAAAAA",
		QuestionContinuityTag:    "BBBBBBBBBBBBBBBBBBBBBB",
		ContinuityTag:            "CCCCCCCCCCCCCCCCCCCCCC",
		AnswerTransitionEvidence: AnswerTransitionEvidenceQuestionBoundInputClauseLater,
	}
}

func transitionTestDecision() respondent.CoachDecision {
	return respondent.CoachDecision{
		Phase:         respondent.CoachPhaseComplete,
		Action:        respondent.CoachActionComplete,
		VerifiedFirst: true,
	}
}

func TestAnswerTransitionEvidenceRequiresTwoIndependentLaterSignals(t *testing.T) {
	frame := transitionTestFrame()
	frame.AnswerTransitionEvidence = AnswerTransitionEvidenceNone
	decision := respondent.CoachDecision{
		Phase:       respondent.CoachPhaseAwaitingRestatement,
		Action:      respondent.CoachActionRestate,
		KeepPending: true,
	}
	gate := respondent.Assessment{
		Outcome:                    respondent.OutcomeClarify,
		OriginalCommitmentPosition: respondent.PositionLater,
		CommitmentPosition:         respondent.PositionLater,
		OriginalTargetCoverage:     1,
		TargetCoverage:             1,
	}
	critic := answercontract.Assessment{
		Outcome: answercontract.OutcomeRestructure,
		Metrics: answercontract.Metrics{
			CommitmentFrontPosition: answercontract.PositionLater,
			TargetSlotCoverage:      1,
			HypothesisEntropy:       0,
			MeaningPreservation:     1,
			HypothesisGap:           1,
		},
	}
	if got := answerTransitionEvidenceForLateTurn(
		frame, decision, gate, critic,
	); got != AnswerTransitionEvidenceQuestionBoundInputClauseLater {
		t.Fatalf("evidence = %q", got)
	}

	critic.Metrics.CommitmentFrontPosition = answercontract.PositionFirst
	if got := answerTransitionEvidenceForLateTurn(
		frame, decision, gate, critic,
	); got != AnswerTransitionEvidenceNone {
		t.Fatalf("disagreeing critic minted evidence: %q", got)
	}
}

func TestAnswerTransitionProofRequiresSameBoundClauseAndQBAProof(t *testing.T) {
	previous := transitionTestFrame()
	current := previous
	turn := VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "A。理由です。",
		InputOrigin:   InputOriginCommittedVoice,
	}
	decision := transitionTestDecision()
	got := answerTransitionProofForTurn(
		turn,
		previous,
		current,
		decision,
		AnswerProofQuestionBoundInputAnswerFirst,
		true,
		true,
		"respondent",
		"restructure",
		true,
	)
	if got != AnswerTransitionProofQuestionBoundInputClauseLaterToFirst {
		t.Fatalf("transition proof = %q", got)
	}

	tests := []struct {
		name   string
		mutate func(*VoiceTurn, *PendingAnswerFrame, *PendingAnswerFrame, *respondent.CoachDecision, *AnswerProof, *bool, *bool, *bool)
	}{
		{"first turn A-first", func(_ *VoiceTurn, previous, _ *PendingAnswerFrame, _ *respondent.CoachDecision, _ *AnswerProof, _, _, _ *bool) {
			previous.AnswerTransitionEvidence = AnswerTransitionEvidenceNone
		}},
		{"changed or paraphrased A", func(_ *VoiceTurn, _, current *PendingAnswerFrame, _ *respondent.CoachDecision, _ *AnswerProof, _, _, _ *bool) {
			current.ContinuityTag = "DDDDDDDDDDDDDDDDDDDDDD"
		}},
		{"different question", func(_ *VoiceTurn, _, current *PendingAnswerFrame, _ *respondent.CoachDecision, _ *AnswerProof, _, _, _ *bool) {
			current.QuestionInstanceTag = "DDDDDDDDDDDDDDDDDDDDDD"
		}},
		{"different operator", func(_ *VoiceTurn, _, current *PendingAnswerFrame, _ *respondent.CoachDecision, _ *AnswerProof, _, _, _ *bool) {
			current.Operator = answercontract.OperatorCause
		}},
		{"different slots", func(_ *VoiceTurn, _, current *PendingAnswerFrame, _ *respondent.CoachDecision, _ *AnswerProof, _, _, _ *bool) {
			current.RequiredSlots = []answercontract.RequiredSlot{answercontract.SlotCause}
		}},
		{"A-later again", func(_ *VoiceTurn, _, _ *PendingAnswerFrame, decision *respondent.CoachDecision, _ *AnswerProof, _, _, _ *bool) {
			decision.VerifiedFirst = false
			decision.Phase = respondent.CoachPhaseBlocked
		}},
		{"no ordinary QBA proof", func(_ *VoiceTurn, _, _ *PendingAnswerFrame, _ *respondent.CoachDecision, proof *AnswerProof, _, _, _ *bool) {
			*proof = AnswerProofNone
		}},
		{"proxy quote or correction continuity rejected", func(_ *VoiceTurn, _, _ *PendingAnswerFrame, _ *respondent.CoachDecision, _ *AnswerProof, continuity, _, _ *bool) {
			*continuity = false
		}},
		{"unbound span", func(_ *VoiceTurn, _, _ *PendingAnswerFrame, _ *respondent.CoachDecision, _ *AnswerProof, _, span, _ *bool) {
			*span = false
		}},
		{"provisional caption", func(turn *VoiceTurn, _, _ *PendingAnswerFrame, _ *respondent.CoachDecision, _ *AnswerProof, _, _, _ *bool) {
			turn.InputOrigin = InputOriginProvisionalVoice
			turn.Speculative = true
		}},
		{"behavior disabled", func(_ *VoiceTurn, _, _ *PendingAnswerFrame, _ *respondent.CoachDecision, _ *AnswerProof, _, _, enabled *bool) {
			*enabled = false
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateTurn := turn
			candidatePrevious := previous
			candidateCurrent := current
			candidateDecision := decision
			proof := AnswerProofQuestionBoundInputAnswerFirst
			continuity, spanBound, enabled := true, true, true
			test.mutate(&candidateTurn, &candidatePrevious, &candidateCurrent, &candidateDecision, &proof, &continuity, &spanBound, &enabled)
			if got := answerTransitionProofForTurn(
				candidateTurn, candidatePrevious, candidateCurrent,
				candidateDecision, proof, continuity, spanBound,
				"respondent", "restructure", enabled,
			); got != AnswerTransitionProofNone {
				t.Fatalf("false transition proof = %q", got)
			}
		})
	}
}

func TestAnswerTransitionStateRejectsUnknownEvidenceAndWriterCanStripIt(t *testing.T) {
	frame := transitionTestFrame()
	frame.AnswerTransitionEvidence = "future"
	if _, err := normalizePendingAnswer(frame); err == nil {
		t.Fatal("unknown transition evidence was accepted")
	}

	codec, err := NewStateCodec(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	agent := &vertexAgent{codec: codec, answerTransitionWrites: false}
	state := conversationState{
		Turn:             1,
		Graph:            ThoughtStateGraph{},
		LastIntervention: ArbiterDecision{Act: "silent"},
	}
	state.PendingAnswer = transitionTestFrame()
	token, err := agent.sealState("transition-reader-first", state)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := agent.codec.open("transition-reader-first", token)
	if err != nil {
		t.Fatal(err)
	}
	if opened.PendingAnswer.AnswerTransitionEvidence != AnswerTransitionEvidenceNone {
		t.Fatal("reader-first revision emitted transition evidence")
	}
}
