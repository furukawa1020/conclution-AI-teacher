package conversation

import (
	"strings"

	"github.com/furukawa1020/conclution-ai-teacher/internal/longmemory"
)

// LongTermMemoryCandidate authenticates the short-term state and maps only its
// already-filtered semantic graph into the finite persistence schema. Native
// captions, ConversationSummary, coach evidence, and model responses are
// intentionally not read here.
func (agent *vertexAgent) LongTermMemoryCandidate(
	uid string,
	stateToken string,
) (longmemory.Payload, bool, error) {
	if agent == nil || agent.codec == nil || stateToken == "" {
		return longmemory.Payload{}, false, ErrInvalidStateToken
	}
	state, err := agent.codec.open(uid, stateToken)
	if err != nil {
		return longmemory.Payload{}, false, err
	}
	payload := longmemory.Payload{
		Topics:    finiteMemoryValues(state.Graph.Goals, state.Graph.Claims),
		OpenLoops: finiteMemoryValues(state.Graph.OpenLoops),
	}
	if len(payload.Topics)+len(payload.OpenLoops) == 0 {
		return longmemory.Payload{}, false, nil
	}
	return payload, true, nil
}

func finiteMemoryValues(groups ...[]string) []string {
	const maximum = 4
	result := make([]string, 0, maximum)
	seen := make(map[string]struct{}, maximum)
	for _, group := range groups {
		for _, value := range group {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			key := strings.ToLower(value)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, value)
			if len(result) == maximum {
				return result
			}
		}
	}
	return result
}
