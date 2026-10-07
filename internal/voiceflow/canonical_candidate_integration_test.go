package voiceflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"google.golang.org/genai"
)

// Only provider responses are fake: the actual conversation agent's early
// parser, independent audit, final validation and voice pipeline all execute.
type canonicalCandidateGenerator struct {
	planner     string
	release     chan struct{}
	criticCalls atomic.Int32
}

func canonicalCandidateContract(reply string) answercontract.Contract {
	return answercontract.Contract{
		QuestionFrame: answercontract.QuestionFrame{
			Operator: answercontract.OperatorOpen, Subject: "current request",
			RequiredSlots: []answercontract.RequiredSlot{answercontract.SlotPosition},
			Hypotheses:    []answercontract.Hypothesis{{Interpretation: "answer the request", Confidence: 1}},
		},
		CommitmentFront: answercontract.CommitmentFront{
			FirstCommitment: reply, FillsTarget: true, TargetCoverage: 1,
			FilledSlots:   []answercontract.RequiredSlot{answercontract.SlotPosition},
			PositionClass: answercontract.PositionFirst, Calibration: answercontract.CalibrationCommitted,
			Issue: answercontract.IssueNone,
		},
		CounterfactualRepair: answercontract.CounterfactualRepair{
			MinimalAnswer: reply, ReconstructedAnswer: reply, MeaningPreservationConfidence: 1,
		},
	}
}

func (g *canonicalCandidateGenerator) GenerateContentStream(ctx context.Context, _ string, _ []*genai.Content, _ *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		if !yield(&genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
			Content: genai.NewContentFromText(g.planner, genai.RoleModel), FinishReason: genai.FinishReasonStop,
		}}}, nil) {
			return
		}
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
}

func (g *canonicalCandidateGenerator) GenerateContent(_ context.Context, _ string, contents []*genai.Content, _ *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	g.criticCalls.Add(1)
	var prompt strings.Builder
	for _, content := range contents {
		for _, part := range content.Parts {
			prompt.WriteString(part.Text)
		}
	}
	const marker = "<lac_critic_data>\n"
	_, data, ok := strings.Cut(prompt.String(), marker)
	if !ok {
		return nil, errors.New("missing independent critic input")
	}
	data, _, ok = strings.Cut(data, "\n</lac_critic_data>")
	if !ok {
		return nil, errors.New("missing critic data end")
	}
	var payload struct {
		Reply string `json:"candidate_spoken_reply"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return nil, err
	}
	body, err := json.Marshal(canonicalCandidateContract(payload.Reply))
	if err != nil {
		return nil, err
	}
	return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		Content: genai.NewContentFromText(string(body), genai.RoleModel), FinishReason: genai.FinishReasonStop,
	}}}, nil
}

func TestCanonicalEarlyReplyAvoidsSecondSynthesisThroughRealAgent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		const rawReply = "  Aです。\n\t Bです。  "
		const canonicalReply = "Aです。 Bです。"
		plan := map[string]any{
			"domain": "general", "intent": "answer", "assistance_target": "assistant",
			"respondent_stage": "none", "answer_attempt": "", "respondent_slot_evidence": []any{},
			"respondent_protected_spans": []string{}, "research_action": "none", "research_query": "",
			"latent_question": "次の一歩は何か", "argument_structure": "conclusion_reason",
			"intervention_policy": "answer", "spoken_reply": rawReply, "confidence": .9,
			"conversation_summary": "次の行動を整理中", "document_summary": "",
			"thought_state_delta": conversation.ThoughtStateDelta{}, "self_correction_grace": false,
			"intervention":    map[string]any{"benefit": .8, "interruption_cost": .1, "urgency": .2, "confidence": .9, "act": "reflect"},
			"answer_contract": canonicalCandidateContract(rawReply),
		}
		body, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		generator := &canonicalCandidateGenerator{planner: string(body), release: make(chan struct{})}
		agent, err := conversation.NewAgent(generator, "", "", bytes.Repeat([]byte{17}, 32))
		if err != nil {
			t.Fatal(err)
		}
		speech := newHTTPCandidateSpeech()
		speech.fakeSpeech.transcript = "次に何をすればいいですか"
		pipeline, err := New(speech, agent)
		if err != nil {
			t.Fatal(err)
		}
		var output []byte
		done := startHTTPCandidate(pipeline, ctx, httpapi.VoiceTurnInput{}, func(chunk []byte) error {
			output = append(output, chunk...)
			return nil
		})
		synctest.Wait()
		if len(speech.recordedTexts()) != 1 || generator.criticCalls.Load() != 1 || len(output) != 0 {
			t.Fatalf("private work or output gate changed: synthesis=%v critic=%d output=%v", speech.recordedTexts(), generator.criticCalls.Load(), output)
		}
		select {
		case <-done:
			t.Fatal("result escaped before planner completion")
		default:
		}
		close(generator.release)
		outcome := completedCommittedCandidateLive(t, done)
		texts := speech.recordedTexts()
		if outcome.err != nil || outcome.result.Caption != canonicalReply || len(texts) != 1 ||
			texts[0] != canonicalReply || generator.criticCalls.Load() != 1 || !bytes.Equal(output, []byte{1, 1}) {
			t.Fatalf("unnecessary work or wrong audio: synthesis=%v critic=%d output=%v caption=%q error=%v", texts,
				generator.criticCalls.Load(), output, outcome.result.Caption, outcome.err)
		}
	})
}
