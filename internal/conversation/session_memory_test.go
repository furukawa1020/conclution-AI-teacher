package conversation

import (
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/longmemory"
)

func TestNormalizeTurnCopiesFiniteMemoryAndRejectsGuestOrMalformed(t *testing.T) {
	memory := &longmemory.Payload{Topics: []string{"safe topic"}}
	normalized, err := normalizeTurn(VoiceTurn{
		SchemaVersion: SchemaVersion, Utterance: "hello",
		Memory: memory, MemoryGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	memory.Topics[0] = "caller mutation"
	if normalized.Memory == memory || normalized.Memory.Topics[0] != "safe topic" {
		t.Fatalf("memory was not copied at the trust boundary: %+v", normalized.Memory)
	}

	for _, turn := range []VoiceTurn{
		{SchemaVersion: SchemaVersion, Utterance: "hello", MemoryGeneration: 1},
		{SchemaVersion: SchemaVersion, Utterance: "hello", Memory: &longmemory.Payload{Topics: []string{"safe"}}},
		{SchemaVersion: SchemaVersion, Utterance: "hello", GuestExperience: true, Memory: &longmemory.Payload{Topics: []string{"safe"}}, MemoryGeneration: 1},
		{SchemaVersion: SchemaVersion, Utterance: "hello", Memory: &longmemory.Payload{Topics: []string{"person@example.com"}}, MemoryGeneration: 1},
	} {
		if _, err := normalizeTurn(turn); err == nil {
			t.Fatalf("invalid memory turn was accepted: %+v", turn)
		}
	}
}

func TestGuestTurnErasesAnyExistingSessionMemory(t *testing.T) {
	state := conversationState{
		MemoryGeneration: 7,
		SessionMemory: &longmemory.Payload{
			Topics: []string{"must disappear"},
		},
	}
	turn := VoiceTurn{GuestExperience: true}
	bindSessionMemory(&state, &turn)
	if state.SessionMemory != nil || state.MemoryGeneration != 0 {
		t.Fatalf("guest retained session memory: %+v", state)
	}
}
