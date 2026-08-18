package conversation

import (
	"strings"
	"testing"
)

func TestLongTermMemoryCandidateUsesOnlyFiniteSemanticGraph(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	uid := "memory-candidate-user"
	state := conversationState{
		Turn: 2,
		Graph: ThoughtStateGraph{
			Goals:     []string{"安心して小声で話す", "安心して小声で話す"},
			Claims:    []string{"会話の文脈を保つ"},
			OpenLoops: []string{"次回も続きを話す"},
		},
		ConversationSummary: "直前の利用者: 保存してはいけないcaption / 直前のAI: 保存してはいけない応答",
		PendingAnswer:       emptyPendingAnswer(),
		LastIntervention:    ArbiterDecision{Act: "silent"},
	}
	token, err := agent.codec.seal(uid, state)
	if err != nil {
		t.Fatal(err)
	}
	payload, ok, err := agent.LongTermMemoryCandidate(uid, token)
	if err != nil || !ok {
		t.Fatalf("payload=%+v ok=%v err=%v", payload, ok, err)
	}
	joined := strings.Join(append(append([]string{}, payload.Topics...), payload.OpenLoops...), " ")
	if len(payload.Topics) != 2 || len(payload.OpenLoops) != 1 || strings.Contains(joined, "保存してはいけない") {
		t.Fatalf("unexpected candidate: %+v", payload)
	}
	if _, _, err := agent.LongTermMemoryCandidate("foreign-user", token); err == nil {
		t.Fatal("foreign UID opened state")
	}
}

func TestLongTermMemoryCandidateSkipsEmptyGraphEvenWithNativeCaptions(t *testing.T) {
	agent := newTestAgent(t, &fakeGenerator{})
	uid := "empty-memory-candidate-user"
	state := conversationState{Turn: 1, Graph: ThoughtStateGraph{}, ConversationSummary: "直前の利用者: caption", PendingAnswer: emptyPendingAnswer(), LastIntervention: ArbiterDecision{Act: "silent"}}
	token, err := agent.codec.seal(uid, state)
	if err != nil {
		t.Fatal(err)
	}
	payload, ok, err := agent.LongTermMemoryCandidate(uid, token)
	if err != nil || ok || len(payload.Topics) != 0 {
		t.Fatalf("payload=%+v ok=%v err=%v", payload, ok, err)
	}
}
