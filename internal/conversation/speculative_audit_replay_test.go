package conversation

import (
	"context"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
)

func TestSpeculativeAuditResultCanBeReadByPreparationAndFinalDecision(t *testing.T) {
	ctx := context.Background()
	_, cancel := context.WithCancel(ctx)
	audit := &speculativeAudit{
		candidate: modelPlan{SpokenReply: "監査済み候補"},
		cancel:    cancel,
		done:      make(chan struct{}),
		result: speculativeAuditResult{
			assessment: answercontract.Assessment{
				Outcome: answercontract.OutcomeKeep,
			},
		},
	}
	close(audit.done)

	for reader := 0; reader < 2; reader++ {
		assessment, err := awaitSpeculativeAudit(ctx, audit)
		if err != nil || assessment.Outcome != answercontract.OutcomeKeep {
			t.Fatalf("reader %d: assessment=%+v err=%v", reader, assessment, err)
		}
	}
}
