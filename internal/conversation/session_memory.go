package conversation

import "github.com/furukawa1020/conclution-ai-teacher/internal/longmemory"

// bindSessionMemory imports one authenticated memory generation into the
// encrypted fifteen-minute conversation state. A later turn may present the
// same context again, but can never replace or mix the generation already
// owned by this session.
func bindSessionMemory(state *conversationState, turn *VoiceTurn) {
	if state == nil || turn == nil {
		return
	}
	if turn.GuestExperience {
		clearSessionMemory(state.SessionMemory)
		state.SessionMemory = nil
		state.MemoryGeneration = 0
		clearSessionMemory(turn.Memory)
		turn.Memory = nil
		turn.MemoryGeneration = 0
		return
	}
	if turn.Memory == nil {
		return
	}
	if state.MemoryGeneration == 0 {
		state.SessionMemory = turn.Memory
		state.MemoryGeneration = turn.MemoryGeneration
		turn.Memory = nil
		turn.MemoryGeneration = 0
		return
	}
	// Same-generation repeats and stale/different generations are both ignored.
	// The first authenticated generation remains the sole session authority.
	clearSessionMemory(turn.Memory)
	turn.Memory = nil
	turn.MemoryGeneration = 0
}

func clearSessionMemory(memory *longmemory.Payload) {
	if memory == nil {
		return
	}
	for index := range memory.Topics {
		memory.Topics[index] = ""
	}
	for index := range memory.Preferences {
		memory.Preferences[index] = ""
	}
	for index := range memory.OpenLoops {
		memory.OpenLoops[index] = ""
	}
	*memory = longmemory.Payload{}
}
