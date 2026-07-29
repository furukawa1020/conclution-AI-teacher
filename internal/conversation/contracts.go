package conversation

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion = 1

	MaxUtteranceRunes     = 2_000
	MaxStateTokenBytes    = 16 * 1024
	MaxInlinePDFBytes     = 7 * 1024 * 1024
	MaxSpokenReplyRunes   = 480
	MaxLatentQuestionRunes = 240

	maxConversationSummaryRunes = 320
	maxDocumentSummaryRunes     = 480
	maxGraphNodesPerKind        = 3
	maxGraphNodeRunes           = 100
)

var (
	ErrInvalidTurn        = errors.New("conversation: invalid turn")
	ErrInvalidStateToken  = errors.New("conversation: invalid state token")
	ErrModelUnavailable   = errors.New("conversation: model unavailable")
	ErrModelOutputInvalid = errors.New("conversation: invalid model output")
)

// VoiceTurn is the semantic input to the agent after speech recognition.
// Ambient distinguishes passive transcript fragments from an intentional user turn.
// Process consumes PDF.Data and clears the caller-provided byte slice before returning.
type VoiceTurn struct {
	SchemaVersion int        `json:"schemaVersion"`
	Utterance     string     `json:"utterance"`
	StateToken    string     `json:"stateToken,omitempty"`
	Ambient       bool       `json:"ambient,omitempty"`
	PDF           *InlinePDF `json:"pdf,omitempty"`
}

type InlinePDF struct {
	MIMEType string `json:"mimeType"`
	Data     []byte `json:"data"`
}

type VoiceTurnResult struct {
	SchemaVersion       int             `json:"schemaVersion"`
	Domain              string          `json:"domain"`
	Intent              string          `json:"intent"`
	LatentQuestion      string          `json:"latent_question"`
	ArgumentStructure   string          `json:"argument_structure"`
	InterventionPolicy  string          `json:"intervention_policy"`
	SpokenReply         string          `json:"spoken_reply"`
	Confidence          float64         `json:"confidence"`
	Intervention        ArbiterDecision `json:"intervention"`
	SelfCorrectionGrace bool            `json:"self_correction_grace"`
	Route               string          `json:"route"`
	NeedsClarification  bool            `json:"needs_clarification"`
	StateToken          string          `json:"state_token"`
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
	Claims         []string `json:"claims"`
	Grounds        []string `json:"grounds"`
	Assumptions    []string `json:"assumptions"`
	OpenLoops      []string `json:"open_loops"`
	Contradictions []string `json:"contradictions"`
	Decisions      []string `json:"decisions"`
}

// ThoughtStateDelta is the bounded increment emitted for one turn and merged
// into the encrypted ThoughtStateGraph.
type ThoughtStateDelta struct {
	Claims         []string `json:"claims"`
	Grounds        []string `json:"grounds"`
	Assumptions    []string `json:"assumptions"`
	OpenLoops      []string `json:"open_loops"`
	Contradictions []string `json:"contradictions"`
	Decisions      []string `json:"decisions"`
}

func (turn VoiceTurn) Validate() error {
	_, err := normalizeTurn(turn)
	return err
}

func normalizeTurn(turn VoiceTurn) (VoiceTurn, error) {
	if turn.SchemaVersion != SchemaVersion {
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

	if turn.PDF == nil {
		return turn, nil
	}
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
	if graph.Claims, err = normalizeNodes(graph.Claims); err != nil {
		return ThoughtStateGraph{}, err
	}
	if graph.Grounds, err = normalizeNodes(graph.Grounds); err != nil {
		return ThoughtStateGraph{}, err
	}
	if graph.Assumptions, err = normalizeNodes(graph.Assumptions); err != nil {
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

func collapseSpace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func wipe(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
