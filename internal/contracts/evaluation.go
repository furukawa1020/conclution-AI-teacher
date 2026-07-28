package contracts

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxQuestionRunes = 1_000
	MaxAnswerRunes   = 8_000
)

type EvaluationInput struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Mode     string `json:"mode"`
}

func (in EvaluationInput) Validate() error {
	in.Question = strings.TrimSpace(in.Question)
	in.Answer = strings.TrimSpace(in.Answer)

	if in.Question == "" {
		return errors.New("question is required")
	}
	if in.Answer == "" {
		return errors.New("answer is required")
	}
	if utf8.RuneCountInString(in.Question) > MaxQuestionRunes {
		return fmt.Errorf("question exceeds %d characters", MaxQuestionRunes)
	}
	if utf8.RuneCountInString(in.Answer) > MaxAnswerRunes {
		return fmt.Errorf("answer exceeds %d characters", MaxAnswerRunes)
	}
	if !allowedMode(in.Mode) {
		return errors.New("unsupported mode")
	}
	return nil
}

func allowedMode(mode string) bool {
	switch mode {
	case "decision", "status", "interview", "pitch", "research", "technical", "daily":
		return true
	default:
		return false
	}
}

type EvaluationResult struct {
	Answered              bool     `json:"answered"`
	EstimatedConclusion   string   `json:"estimatedConclusion"`
	ConclusionStartRune   int      `json:"conclusionStartRune"`
	ConclusionFirst       bool     `json:"conclusionFirst"`
	DirectnessScore       int      `json:"directnessScore"`
	FirstSentenceComplete bool     `json:"firstSentenceComplete"`
	CalibrationScore      int      `json:"calibrationScore"`
	PrimaryIssue          string   `json:"primaryIssue"`
	SecondaryIssues       []string `json:"secondaryIssues"`
	Feedback              string   `json:"feedback"`
	RetryInstruction      string   `json:"retryInstruction"`
	Confidence            float64  `json:"confidence"`
	EvidenceExcerpt       string   `json:"evidenceExcerpt"`
	NeedsPrecisionPath    bool     `json:"needsPrecisionPath"`
	ModelLogicalID        string   `json:"modelLogicalId"`
	RubricVersion         string   `json:"rubricVersion"`
	PromptVersion         string   `json:"promptVersion"`
}

func (out EvaluationResult) Validate(answer string) error {
	answerRunes := utf8.RuneCountInString(answer)
	if out.ConclusionStartRune < -1 || out.ConclusionStartRune > answerRunes {
		return errors.New("invalid conclusionStartRune")
	}
	for name, score := range map[string]int{
		"directnessScore":  out.DirectnessScore,
		"calibrationScore": out.CalibrationScore,
	} {
		if score < 0 || score > 100 {
			return fmt.Errorf("%s must be between 0 and 100", name)
		}
	}
	if out.Confidence < 0 || out.Confidence > 1 {
		return errors.New("confidence must be between 0 and 1")
	}
	if !allowedIssue(out.PrimaryIssue) {
		return errors.New("unsupported primaryIssue")
	}
	if strings.TrimSpace(out.Feedback) == "" || strings.TrimSpace(out.RetryInstruction) == "" {
		return errors.New("feedback and retryInstruction are required")
	}
	return nil
}

func allowedIssue(issue string) bool {
	switch issue {
	case "none",
		"background_first",
		"question_restatement",
		"no_conclusion",
		"unanswered",
		"multiple_conclusions",
		"ambiguous_conclusion",
		"first_sentence_too_long",
		"overqualified",
		"overconfident",
		"condition_separated",
		"too_abstract",
		"reason_without_judgment",
		"judgment_without_context",
		"too_much_preamble",
		"off_topic",
		"contradiction",
		"meaning_not_preserved",
		"speech_recognition_uncertain",
		"not_evaluable":
		return true
	default:
		return false
	}
}
