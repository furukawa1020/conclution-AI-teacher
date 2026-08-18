package httpapi

import (
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/longmemory"
)

const maxSessionContextBytes = 4096

// VoiceMemoryStatus is content-free transport evidence. It is safe to observe
// because it never contains the opaque token, decrypted memory, UID, or App ID.
type VoiceMemoryStatus string

const (
	VoiceMemoryAbsent   VoiceMemoryStatus = "absent"
	VoiceMemoryAccepted VoiceMemoryStatus = "accepted"
	VoiceMemoryRejected VoiceMemoryStatus = "rejected"
)

func (s *Server) attachVoiceMemory(
	principal identity.Principal,
	input *VoiceTurnInput,
	token string,
) {
	if input == nil {
		return
	}
	clearVoiceMemory(input)
	input.MemoryStatus = VoiceMemoryAbsent
	if token == "" {
		return
	}
	input.MemoryStatus = VoiceMemoryRejected
	if principal.IsGuest() || input.StrictCloudMinimization ||
		len(token) > maxSessionContextBytes || s.voice.SessionContextOpener == nil {
		return
	}
	payload, generation, err := s.voice.SessionContextOpener.OpenSessionContext(
		principal.UID,
		principal.AppID,
		token,
	)
	if err != nil || generation < 1 {
		return
	}
	input.Memory = &payload
	input.MemoryGeneration = generation
	input.MemoryStatus = VoiceMemoryAccepted
}

func clearVoiceMemory(input *VoiceTurnInput) {
	if input == nil || input.Memory == nil {
		if input != nil {
			input.MemoryGeneration = 0
		}
		return
	}
	for index := range input.Memory.Topics {
		input.Memory.Topics[index] = ""
	}
	for index := range input.Memory.Preferences {
		input.Memory.Preferences[index] = ""
	}
	for index := range input.Memory.OpenLoops {
		input.Memory.OpenLoops[index] = ""
	}
	*input.Memory = longmemory.Payload{}
	input.Memory = nil
	input.MemoryGeneration = 0
}
