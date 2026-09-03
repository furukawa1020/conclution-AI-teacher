// Package semanticshadow builds a content-free Semantica context graph from
// finite server-authored answer proofs. It must never receive audio, captions,
// user identifiers, tokens, or model-generated reasoning.
package semanticshadow

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const (
	SchemaVersion        = 1
	QuestionSchema       = "qba.v1"
	SemanticaVersion     = "0.6.5"
	SemanticaWheelSHA256 = "5bc33a5529aaa496dfdbca6d0bfc8301cb8f795b127e360d93ecb5b16a6a5fc0"
)

type Signals struct {
	AssistanceTarget      string `json:"assistanceTarget"`
	RespondentStage       string `json:"respondentStage"`
	CoachPhase            string `json:"coachPhase"`
	CoachAction           string `json:"coachAction"`
	AnswerProof           string `json:"answerProof"`
	AnswerTransitionProof string `json:"answerTransitionProof"`
	GuestAFirstOutcome    string `json:"guestAFirstOutcome"`
}

type Node struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type Edge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Type     string `json:"type"`
	Relation string `json:"relation,omitempty"`
}

type Provenance struct {
	TurnDigest           string `json:"turnDigest"`
	QuestionSchema       string `json:"questionSchema"`
	SemanticaVersion     string `json:"semanticaVersion"`
	SemanticaWheelSHA256 string `json:"semanticaWheelSha256"`
}

type Graph struct {
	SchemaVersion int        `json:"schemaVersion"`
	Provenance    Provenance `json:"provenance"`
	Nodes         []Node     `json:"nodes"`
	Edges         []Edge     `json:"edges"`
}

func TurnDigest(key []byte, requestID string) (string, error) {
	if len(key) != 32 || requestID == "" {
		return "", errors.New("semantic shadow digest input is invalid")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("kotae-semantic-shadow-turn-v1\x00"))
	_, _ = mac.Write([]byte(requestID))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func Build(turnDigest string, signals Signals) (Graph, error) {
	if len(turnDigest) != sha256.Size*2 {
		return Graph{}, errors.New("semantic shadow turn digest is invalid")
	}
	if _, err := hex.DecodeString(turnDigest); err != nil {
		return Graph{}, errors.New("semantic shadow turn digest is invalid")
	}
	relation, err := relationOf(signals)
	if err != nil {
		return Graph{}, err
	}
	return Graph{
		SchemaVersion: SchemaVersion,
		Provenance: Provenance{
			TurnDigest: turnDigest, QuestionSchema: QuestionSchema,
			SemanticaVersion:     SemanticaVersion,
			SemanticaWheelSHA256: SemanticaWheelSHA256,
		},
		Nodes: []Node{
			{ID: "question", Type: "Question"},
			{ID: "utterance", Type: "RespondentUtterance"},
			{ID: "claim", Type: "Claim"},
			{ID: "verification", Type: "Verification"},
		},
		Edges: []Edge{
			{Source: "question", Target: "utterance", Type: "elicits"},
			{Source: "utterance", Target: "claim", Type: "expresses"},
			{Source: "claim", Target: "verification", Type: "verified_as", Relation: relation},
		},
	}, nil
}

func CanonicalJSON(graph Graph) ([]byte, error) {
	return json.Marshal(graph)
}

func relationOf(s Signals) (string, error) {
	if !oneOf(s.AssistanceTarget, "assistant", "respondent") ||
		!oneOf(s.RespondentStage, "none", "awaiting_answer", "restructure") ||
		!oneOf(s.CoachPhase, "none", "awaiting_answer", "awaiting_restatement", "expanding", "complete", "blocked") ||
		!oneOf(s.CoachAction, "none", "elicit", "restate", "expand", "complete", "retry", "release") ||
		!oneOf(s.AnswerProof, "none", "question_bound_input_answer_first") ||
		!oneOf(s.AnswerTransitionProof, "none", "question_bound_input_clause_later_to_first") ||
		!oneOf(s.GuestAFirstOutcome, "no_verified_change", "changed_to_answer_first", "stayed_answer_first") {
		return "", errors.New("semantic shadow signals contain an unknown enum")
	}
	if s.AnswerTransitionProof == "question_bound_input_clause_later_to_first" {
		if s.AnswerProof != "question_bound_input_answer_first" || s.GuestAFirstOutcome != "changed_to_answer_first" {
			return "conflict", nil
		}
		return "restatement", nil
	}
	if s.AnswerProof == "question_bound_input_answer_first" {
		if s.GuestAFirstOutcome != "stayed_answer_first" && s.GuestAFirstOutcome != "no_verified_change" {
			return "conflict", nil
		}
		return "direct", nil
	}
	if s.GuestAFirstOutcome != "no_verified_change" {
		return "conflict", nil
	}
	return "unresolved", nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
