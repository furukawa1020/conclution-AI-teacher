package conversation

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

const (
	SchemaVersion = 1

	// Four thousand Unicode code points admit a fast three-minute monologue
	// while keeping the current-turn model prompt and injection surface finite.
	MaxUtteranceRunes      = 4_000
	MaxStateTokenBytes     = 16 * 1024
	MaxInlinePDFBytes      = 7 * 1024 * 1024
	MaxSpokenReplyRunes    = 480
	MaxLatentQuestionRunes = 240
	MaxAnswerAttemptRunes  = 1_600
	MaxResearchRecords     = 5

	maxConversationSummaryRunes = 320
	maxDocumentSummaryRunes     = 480
	maxGraphNodesPerKind        = 3
	maxGraphNodeRunes           = 100
	maxPendingSubjectRunes      = 100
	coachContinuityTagBytes     = 16
	assistantFollowUpSubject    = "KOTAEが直前に尋ねたこと"
)

var (
	ErrInvalidTurn       = errors.New("conversation: invalid turn")
	ErrInvalidStateToken = errors.New("conversation: invalid state token")
	// ErrExpiredStateToken is joined with ErrInvalidStateToken only after a
	// state token has passed authenticated decryption and structural checks.
	// It lets a voice caller start a fresh state without treating a tampered
	// token as recoverable.
	ErrExpiredStateToken  = errors.New("conversation: expired state token")
	ErrModelUnavailable   = errors.New("conversation: model unavailable")
	ErrModelOutputInvalid = errors.New("conversation: invalid model output")
	// ErrSpeculativeExternalAction means a provisional transcript asked for
	// an outbound action. Callers must discard that provisional run and repeat
	// the finalized turn non-speculatively; provisional audio must never be
	// released.
	ErrSpeculativeExternalAction = errors.New(
		"conversation: speculative turn requires an external action",
	)
)

// VoiceTurn is the semantic input to the agent after speech recognition.
// Ambient marks speech whose provenance has no intentional-turn authority.
// Foreground marks an active-conversation continuation that expects a reply
// while retaining all ambient authority restrictions.
// Process consumes PDF.Data and clears the caller-provided byte slice before returning.
type VoiceTurn struct {
	SchemaVersion int    `json:"schemaVersion"`
	Utterance     string `json:"utterance"`
	StateToken    string `json:"stateToken,omitempty"`
	RequestID     string `json:"-"`
	Ambient       bool   `json:"ambient,omitempty"`
	Foreground    bool   `json:"foreground,omitempty"`
	// ExtendedSpeech is derived only after the server accepts a finalized
	// transcript. It is model-visible for this turn, never decoded from a client
	// request, written to state, or interpreted as a trait or skill score.
	ExtendedSpeech bool `json:"-"`
	// GuestExperience is server-authored and activates only the bounded first
	// two-turn word-mining experience. It grants no account authority.
	GuestExperience bool `json:"-"`
	// Speculative permits pure model computation while speech recognition is
	// still provisional. It never widens authority: outbound research and any
	// future executable action must fail before execution.
	Speculative bool `json:"-"`
	// InputOrigin is server-authored provenance. Only the voice pipeline may
	// mark an utterance as committed voice input, after recognition has reached
	// its final boundary. Model text, provisional recognition, tests, and future
	// non-voice callers must leave it unset or use a different fixed value.
	InputOrigin InputOrigin `json:"-"`
	// OutputCancelable is server-authored proof that any output produced for this
	// turn can be stopped before it reaches the user. Callers must fail closed
	// when the output path cannot provide that guarantee.
	OutputCancelable bool `json:"-"`
	// FloorEvidence records the finite transport proof available at this
	// decision boundary. A committed transcript alone is insufficient: QARC
	// may speak only after both the client commit and provider speech-end are
	// observed, or compute speculatively while every byte remains behind the
	// exact-final commit gate.
	FloorEvidence FloorEvidence `json:"-"`
	// ResearchDisabled is a server-authored capability restriction. It is not
	// model-visible input and must be checked before any outbound verifier call.
	ResearchDisabled bool       `json:"-"`
	PDF              *InlinePDF `json:"pdf,omitempty"`
}

// InputOrigin is deliberately finite so an AnswerProof can never be minted
// from model-written or merely provisional text.
type InputOrigin string

const (
	InputOriginUnknown          InputOrigin = ""
	InputOriginProvisionalVoice InputOrigin = "provisional_voice"
	InputOriginCommittedVoice   InputOrigin = "committed_voice"
)

type FloorEvidence uint8

const (
	FloorEvidenceUnknown FloorEvidence = iota
	FloorEvidenceProvisionalCommitGate
	FloorEvidenceHybridCommitted
)

type InlinePDF struct {
	MIMEType string `json:"mimeType"`
	Data     []byte `json:"data"`
}

type VoiceTurnResult struct {
	SchemaVersion         int                   `json:"schemaVersion"`
	Domain                string                `json:"domain"`
	Intent                string                `json:"intent"`
	AssistanceTarget      string                `json:"assistance_target"`
	RespondentStage       string                `json:"respondent_stage"`
	CoachPhase            string                `json:"coach_phase"`
	CoachAction           string                `json:"coach_action"`
	AnswerProof           AnswerProof           `json:"answer_proof"`
	AnswerTransitionProof AnswerTransitionProof `json:"answer_transition_proof"`
	GuestAFirstOutcome    GuestAFirstOutcome    `json:"guest_a_first_outcome"`
	// AnswerProofCandidate is process-private evidence produced for both
	// provisional and committed voice input. The voice pipeline may promote it
	// only after its exact final-transcript commit boundary. It is never
	// serialized, persisted, or returned by an HTTP handler.
	AnswerProofCandidate AnswerProof `json:"-"`
	// AnswerTransitionProofCandidate is process-private and may be promoted
	// only by the same exact-final voice boundary as AnswerProofCandidate.
	AnswerTransitionProofCandidate AnswerTransitionProof  `json:"-"`
	ResearchStatus                 string                 `json:"research_status"`
	ResearchRecords                []ResearchRecord       `json:"research_records"`
	LatentQuestion                 string                 `json:"latent_question"`
	ArgumentStructure              string                 `json:"argument_structure"`
	InterventionPolicy             string                 `json:"intervention_policy"`
	SpokenReply                    string                 `json:"spoken_reply"`
	Confidence                     float64                `json:"confidence"`
	Intervention                   ArbiterDecision        `json:"intervention"`
	SelfCorrectionGrace            bool                   `json:"self_correction_grace"`
	AnswerContract                 answercontract.Metrics `json:"answer_contract_metrics"`
	Route                          string                 `json:"route"`
	NeedsClarification             bool                   `json:"needs_clarification"`
	StateToken                     string                 `json:"state_token"`
}

// AnswerProof is a content-free, current-turn server attestation. It is not a
// correctness score or speaker identity proof. The verified value means only
// that a committed input utterance was bound to a screened reported-question
// instance and both independent gates found complete required-slot evidence
// at the front without using an AI reconstruction.
type AnswerProof string

const (
	AnswerProofNone                          AnswerProof = "none"
	AnswerProofQuestionBoundInputAnswerFirst AnswerProof = "question_bound_input_answer_first"
)

// AnswerTransitionProof is a content-free, current-turn attestation that the
// same question-bound, person-originated answer clause moved from later in the
// preceding turn to the front of this turn. It is not a score, identity proof,
// correctness claim, or evidence of durable learning.
type AnswerTransitionProof string

const (
	AnswerTransitionProofNone                                 AnswerTransitionProof = "none"
	AnswerTransitionProofQuestionBoundInputClauseLaterToFirst AnswerTransitionProof = "question_bound_input_clause_later_to_first"
)

// GuestAFirstOutcome is a content-free projection of independently verified
// answer position. It carries no answer, transcript, tag, score, or identity.
type GuestAFirstOutcome string

const (
	GuestAFirstOutcomeNoVerifiedChange     GuestAFirstOutcome = "no_verified_change"
	GuestAFirstOutcomeChangedToAnswerFirst GuestAFirstOutcome = "changed_to_answer_first"
	GuestAFirstOutcomeStayedAnswerFirst    GuestAFirstOutcome = "stayed_answer_first"
)

// AnswerTransitionEvidence is the finite encrypted-state antecedent for
// QBA-Delta. It contains no question, answer, transcript, evidence span, or
// generated text.
type AnswerTransitionEvidence string

const (
	AnswerTransitionEvidenceNone                          AnswerTransitionEvidence = ""
	AnswerTransitionEvidenceQuestionBoundInputClauseLater AnswerTransitionEvidence = "question_bound_input_clause_later"
)

// ResearchRecord is bounded, current-turn discovery metadata. It deliberately
// excludes abstracts and never represents claim-level evidence.
type ResearchRecord struct {
	Title     string `json:"title"`
	DOI       string `json:"doi"`
	URL       string `json:"url"`
	Published string `json:"published,omitempty"`
	Source    string `json:"source"`
}

type ArbiterDecision struct {
	Benefit          float64 `json:"benefit"`
	InterruptionCost float64 `json:"interruption_cost"`
	Urgency          float64 `json:"urgency"`
	Confidence       float64 `json:"confidence"`
	Score            float64 `json:"score"`
	Act              string  `json:"act"`
}

// ThoughtStateGraph contains short semantic abstractions only. It deliberately
// has no transcript or document-content field.
type ThoughtStateGraph struct {
	Goals          []string `json:"goals"`
	Claims         []string `json:"claims"`
	Grounds        []string `json:"grounds"`
	Assumptions    []string `json:"assumptions"`
	Constraints    []string `json:"constraints"`
	OpenLoops      []string `json:"open_loops"`
	Contradictions []string `json:"contradictions"`
	Decisions      []string `json:"decisions"`
}

// ThoughtStateDelta is the bounded increment emitted for one turn and merged
// into the encrypted ThoughtStateGraph.
type ThoughtStateDelta struct {
	Goals          []string `json:"goals"`
	Claims         []string `json:"claims"`
	Grounds        []string `json:"grounds"`
	Assumptions    []string `json:"assumptions"`
	Constraints    []string `json:"constraints"`
	OpenLoops      []string `json:"open_loops"`
	Contradictions []string `json:"contradictions"`
	Decisions      []string `json:"decisions"`
}

// PendingAnswerFrame is the minimum cross-turn state needed to help a person
// answer someone else's question. It intentionally stores no transcript,
// question span, answer attempt, evidence span, hypothesis prose, reconstructed
// reply, or model-written prompt. RestatementTag is retained as a compatibility
// reader for the preceding rollout. QuestionInstanceTag,
// QuestionContinuityTag, and ContinuityTag are domain-separated, truncated
// HMACs of the screened reported-question instance, its bounded subject, and
// the person's exact target answer. NativeCoachScopeTag is a separately
// domain-separated HMAC proving only that this user explicitly opened one
// generic Native coach scope; it is never interpreted as question or answer
// continuity. None of these server-only proofs is included in a model prompt.
// ExpansionOptIn records only the person's deterministic, question-scoped
// request for one bounded follow-up; it carries no question or answer text and
// is never model-writable.
type PendingAnswerFrame struct {
	Active                   bool                          `json:"active"`
	Operator                 answercontract.Operator       `json:"operator,omitempty"`
	Subject                  string                        `json:"subject,omitempty"`
	RequiredSlots            []answercontract.RequiredSlot `json:"required_slots,omitempty"`
	ExpansionOperator        answercontract.Operator       `json:"expansion_operator,omitempty"`
	Phase                    respondent.CoachPhase         `json:"phase,omitempty"`
	Attempts                 uint8                         `json:"attempts,omitempty"`
	AssistantFollowUp        bool                          `json:"assistant_follow_up,omitempty"`
	ExpansionOptIn           bool                          `json:"expansion_opt_in,omitempty"`
	RestatementTag           string                        `json:"restatement_tag,omitempty"`
	QuestionInstanceTag      string                        `json:"question_instance_tag,omitempty"`
	QuestionContinuityTag    string                        `json:"question_continuity_tag,omitempty"`
	ContinuityTag            string                        `json:"continuity_tag,omitempty"`
	AnswerTransitionEvidence AnswerTransitionEvidence      `json:"answer_transition_evidence,omitempty"`
	NativeCoachScopeTag      string                        `json:"native_coach_scope_tag,omitempty"`
	// VerifierProgress is a fixed-size posterior over current-question verifier
	// progress. It carries no prose, answer, transcript, diagnosis, or retrieval
	// claim and is never exposed to a model prompt.
	VerifierProgress *respondent.StoredVerifierProgress `json:"verifier_progress,omitempty"`
}

func (turn VoiceTurn) Validate() error {
	_, err := normalizeTurn(turn)
	return err
}

func normalizeTurn(turn VoiceTurn) (VoiceTurn, error) {
	if turn.SchemaVersion != SchemaVersion {
		return VoiceTurn{}, ErrInvalidTurn
	}
	switch turn.InputOrigin {
	case InputOriginUnknown,
		InputOriginProvisionalVoice,
		InputOriginCommittedVoice:
	default:
		return VoiceTurn{}, ErrInvalidTurn
	}
	switch turn.FloorEvidence {
	case FloorEvidenceUnknown,
		FloorEvidenceProvisionalCommitGate,
		FloorEvidenceHybridCommitted:
	default:
		return VoiceTurn{}, ErrInvalidTurn
	}
	if turn.Speculative && turn.InputOrigin == InputOriginCommittedVoice {
		return VoiceTurn{}, ErrInvalidTurn
	}
	if (turn.Speculative && turn.FloorEvidence == FloorEvidenceHybridCommitted) ||
		(!turn.Speculative &&
			turn.FloorEvidence == FloorEvidenceProvisionalCommitGate) {
		return VoiceTurn{}, ErrInvalidTurn
	}
	if turn.Foreground && !turn.Ambient {
		return VoiceTurn{}, ErrInvalidTurn
	}
	if !utf8.ValidString(turn.Utterance) {
		return VoiceTurn{}, ErrInvalidTurn
	}
	turn.Utterance = collapseSpace(turn.Utterance)
	if turn.Utterance == "" || utf8.RuneCountInString(turn.Utterance) > MaxUtteranceRunes {
		return VoiceTurn{}, ErrInvalidTurn
	}
	if !utf8.ValidString(turn.StateToken) || len(turn.StateToken) > MaxStateTokenBytes {
		return VoiceTurn{}, ErrInvalidTurn
	}
	if turn.StateToken != strings.TrimSpace(turn.StateToken) {
		return VoiceTurn{}, ErrInvalidTurn
	}
	if !utf8.ValidString(turn.RequestID) ||
		len(turn.RequestID) > 64 ||
		turn.RequestID != strings.TrimSpace(turn.RequestID) {
		return VoiceTurn{}, ErrInvalidTurn
	}

	if turn.PDF == nil {
		return turn, nil
	}
	pdf := *turn.PDF
	turn.PDF = &pdf
	turn.PDF.MIMEType = strings.ToLower(strings.TrimSpace(turn.PDF.MIMEType))
	if turn.PDF.MIMEType != "application/pdf" ||
		len(turn.PDF.Data) == 0 ||
		len(turn.PDF.Data) > MaxInlinePDFBytes ||
		!bytes.HasPrefix(turn.PDF.Data, []byte("%PDF-")) {
		return VoiceTurn{}, ErrInvalidTurn
	}
	return turn, nil
}

func normalizeGraph(graph ThoughtStateGraph) (ThoughtStateGraph, error) {
	var err error
	if graph.Goals, err = normalizeNodes(graph.Goals); err != nil {
		return ThoughtStateGraph{}, err
	}
	if graph.Claims, err = normalizeNodes(graph.Claims); err != nil {
		return ThoughtStateGraph{}, err
	}
	if graph.Grounds, err = normalizeNodes(graph.Grounds); err != nil {
		return ThoughtStateGraph{}, err
	}
	if graph.Assumptions, err = normalizeNodes(graph.Assumptions); err != nil {
		return ThoughtStateGraph{}, err
	}
	if graph.Constraints, err = normalizeNodes(graph.Constraints); err != nil {
		return ThoughtStateGraph{}, err
	}
	if graph.OpenLoops, err = normalizeNodes(graph.OpenLoops); err != nil {
		return ThoughtStateGraph{}, err
	}
	if graph.Contradictions, err = normalizeNodes(graph.Contradictions); err != nil {
		return ThoughtStateGraph{}, err
	}
	if graph.Decisions, err = normalizeNodes(graph.Decisions); err != nil {
		return ThoughtStateGraph{}, err
	}
	return graph, nil
}

func normalizeDelta(delta ThoughtStateDelta) (ThoughtStateDelta, error) {
	graph, err := normalizeGraph(ThoughtStateGraph(delta))
	return ThoughtStateDelta(graph), err
}

func normalizePendingAnswer(frame PendingAnswerFrame) (PendingAnswerFrame, error) {
	if !frame.Active {
		return emptyPendingAnswer(), nil
	}
	if frame.Phase == "" {
		// Tokens issued by the immediately preceding schema did not yet carry
		// coaching control state. Treat an authenticated legacy frame as an
		// unanswered question, without recovering any answer text.
		frame.Phase = respondent.CoachPhaseAwaitingAnswer
	}
	if frame.ExpansionOperator == "" {
		frame.ExpansionOperator = answercontract.Operator(
			respondent.ExpansionOperator(respondent.Operator(frame.Operator)),
		)
	}
	if frame.Operator == answercontract.OperatorQuantity {
		hasUnit := false
		for _, slot := range frame.RequiredSlots {
			if slot == answercontract.SlotUnit {
				hasUnit = true
				break
			}
		}
		if !hasUnit {
			if len(frame.RequiredSlots) >= answercontract.MaxRequiredSlots {
				return PendingAnswerFrame{}, ErrInvalidStateToken
			}
			frame.RequiredSlots = append(frame.RequiredSlots, answercontract.SlotUnit)
		}
	}
	target, ok := answercontract.TargetSlot(frame.Operator)
	_, expansionOK := answercontract.TargetSlot(frame.ExpansionOperator)
	continuityProtected := frame.QuestionInstanceTag != "" ||
		frame.QuestionContinuityTag != "" ||
		frame.ContinuityTag != ""
	nativeCoachScope := frame.NativeCoachScopeTag != ""
	if frame.VerifierProgress != nil && !frame.VerifierProgress.Valid() {
		return PendingAnswerFrame{}, ErrInvalidStateToken
	}
	if continuityProtected {
		// Once continuity proofs exist, cross-turn identity is carried only by
		// those non-reversible values. Never renew model-written subject prose.
		if frame.AssistantFollowUp {
			frame.Subject = assistantFollowUpSubject
		} else {
			frame.Subject = pendingSubjectForOperator(frame.Operator)
		}
	} else if nativeCoachScope {
		frame.Subject = pendingSubjectForOperator(answercontract.OperatorOpen)
	} else {
		// Compatibility readers preserve the bounded legacy label so rolling
		// traffic does not mutate otherwise authenticated v1 state.
		frame.Subject = collapseSpace(frame.Subject)
	}
	switch frame.AnswerTransitionEvidence {
	case AnswerTransitionEvidenceNone,
		AnswerTransitionEvidenceQuestionBoundInputClauseLater:
	default:
		return PendingAnswerFrame{}, ErrInvalidStateToken
	}
	if !ok ||
		!expansionOK ||
		!activeCoachPhase(frame.Phase) ||
		frame.Attempts > respondent.MaxCoachAttempts ||
		!utf8.ValidString(frame.Subject) ||
		frame.Subject == "" ||
		utf8.RuneCountInString(frame.Subject) > maxPendingSubjectRunes ||
		(!continuityProtected && containsSensitiveStateText(frame.Subject)) ||
		(!continuityProtected && frame.AssistantFollowUp &&
			frame.Subject != assistantFollowUpSubject) ||
		(frame.ExpansionOptIn &&
			(frame.AssistantFollowUp || frame.QuestionContinuityTag == "")) ||
		(frame.QuestionInstanceTag != "" &&
			(frame.QuestionContinuityTag == "" || frame.AssistantFollowUp)) ||
		(frame.AssistantFollowUp && frame.Phase == respondent.CoachPhaseExpanding) ||
		(nativeCoachScope &&
			(frame.Phase != respondent.CoachPhaseAwaitingAnswer ||
				frame.Operator != answercontract.OperatorOpen ||
				frame.ExpansionOperator != answercontract.Operator(
					respondent.ExpansionOperator(respondent.Operator(answercontract.OperatorOpen)),
				) ||
				len(frame.RequiredSlots) != 1 ||
				frame.RequiredSlots[0] != answercontract.SlotPosition ||
				frame.AssistantFollowUp || frame.ExpansionOptIn ||
				frame.RestatementTag != "" ||
				frame.QuestionInstanceTag != "" ||
				frame.QuestionContinuityTag != "" ||
				frame.ContinuityTag != "")) ||
		(frame.RestatementTag != "" &&
			(frame.Phase != respondent.CoachPhaseAwaitingRestatement ||
				!validCoachRestatementTag(frame.RestatementTag))) ||
		(frame.AnswerTransitionEvidence != AnswerTransitionEvidenceNone &&
			(frame.Phase != respondent.CoachPhaseAwaitingRestatement ||
				frame.QuestionInstanceTag == "" ||
				frame.QuestionContinuityTag == "" ||
				frame.ContinuityTag == "" ||
				frame.AssistantFollowUp || frame.ExpansionOptIn)) ||
		len(frame.RequiredSlots) == 0 ||
		len(frame.RequiredSlots) > answercontract.MaxRequiredSlots {
		return PendingAnswerFrame{}, ErrInvalidStateToken
	}
	for _, encodedTag := range []string{
		frame.QuestionInstanceTag,
		frame.QuestionContinuityTag,
		frame.ContinuityTag,
		frame.NativeCoachScopeTag,
	} {
		if encodedTag == "" {
			continue
		}
		if !validCoachControlTag(encodedTag) {
			return PendingAnswerFrame{}, ErrInvalidStateToken
		}
	}
	seen := make(map[answercontract.RequiredSlot]struct{}, len(frame.RequiredSlots))
	slots := make([]answercontract.RequiredSlot, 0, len(frame.RequiredSlots))
	hasTarget := false
	for _, slot := range frame.RequiredSlots {
		if _, duplicate := seen[slot]; duplicate {
			return PendingAnswerFrame{}, ErrInvalidStateToken
		}
		seen[slot] = struct{}{}
		slots = append(slots, slot)
		if slot == target {
			hasTarget = true
		}
	}
	if !hasTarget {
		return PendingAnswerFrame{}, ErrInvalidStateToken
	}
	frame.RequiredSlots = slots
	return frame, nil
}

func emptyPendingAnswer() PendingAnswerFrame {
	return PendingAnswerFrame{
		RequiredSlots: []answercontract.RequiredSlot{},
		Phase:         respondent.CoachPhaseNone,
	}
}

func activeCoachPhase(phase respondent.CoachPhase) bool {
	switch phase {
	case respondent.CoachPhaseAwaitingAnswer,
		respondent.CoachPhaseAwaitingRestatement,
		respondent.CoachPhaseExpanding:
		return true
	default:
		return false
	}
}

func normalizeNodes(nodes []string) ([]string, error) {
	if len(nodes) > maxGraphNodesPerKind {
		return nil, ErrModelOutputInvalid
	}
	result := make([]string, 0, len(nodes))
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if !utf8.ValidString(node) {
			return nil, ErrModelOutputInvalid
		}
		node = collapseSpace(node)
		if node == "" || utf8.RuneCountInString(node) > maxGraphNodeRunes {
			return nil, ErrModelOutputInvalid
		}
		if _, exists := seen[node]; exists {
			continue
		}
		seen[node] = struct{}{}
		result = append(result, node)
	}
	return result, nil
}

func validateArbiter(decision ArbiterDecision) error {
	for name, value := range map[string]float64{
		"benefit":          decision.Benefit,
		"interruptionCost": decision.InterruptionCost,
		"urgency":          decision.Urgency,
		"confidence":       decision.Confidence,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return fmt.Errorf("%w: %s", ErrModelOutputInvalid, name)
		}
	}
	if !allowedAct(decision.Act) {
		return ErrModelOutputInvalid
	}
	return nil
}

func allowedAct(act string) bool {
	switch act {
	case "silent", "reflect", "clarify", "counterexample", "restructure", "paper_check":
		return true
	default:
		return false
	}
}

func allowedDomain(domain string) bool {
	switch domain {
	case "general", "daily", "work", "education", "research", "technical",
		"health", "legal", "finance", "creative", "other":
		return true
	default:
		return false
	}
}

func allowedIntent(intent string) bool {
	switch intent {
	case "answer", "explain", "decide", "compare", "plan", "debug", "learn",
		"practice", "verify", "create", "other":
		return true
	default:
		return false
	}
}

func allowedArgumentStructure(structure string) bool {
	switch structure {
	case "direct_answer", "conclusion_reason", "claim_evidence_limit",
		"hypothesis_evidence_limit", "steps_checks",
		"comparison_criteria_recommendation", "clarifying_question",
		"safety_boundary":
		return true
	default:
		return false
	}
}

func allowedInterventionPolicy(policy string) bool {
	switch policy {
	case "answer", "coach", "clarify", "safety", "wait", "paper_check":
		return true
	default:
		return false
	}
}

func allowedAssistanceTarget(target string) bool {
	return target == "assistant" || target == "respondent"
}

func allowedRespondentStage(stage string) bool {
	switch stage {
	case "none", "awaiting_answer", "restructure":
		return true
	default:
		return false
	}
}

func collapseSpace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func wipe(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
