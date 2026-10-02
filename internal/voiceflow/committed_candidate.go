package voiceflow

import (
	"context"
	"sync"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type recognizedTurnProcessor func(context.Context, string, conversation.VoiceTurn) (conversation.VoiceTurnResult, error)

func ordinaryCommittedCandidateMatches(candidate, final string, result httpapi.VoiceTurnResult) bool {
	return candidate == final && result.AssistanceTarget == "assistant" &&
		result.RespondentStage == "none" && result.ResearchStatus == "none" && len(result.ResearchRecords) == 0 &&
		(result.CoachPhase == "" || result.CoachPhase == "none") &&
		(result.CoachAction == "" || result.CoachAction == "none")
}

// Each invocation has its own closed flag: a late callback from an expired
// state attempt cannot capture the next attempt's provider or private buffer.
func processWithPrivateCandidate(
	ctx context.Context, agent conversation.SealedCandidateAgent, uid string, turn conversation.VoiceTurn,
	speech speechio.StreamingService, preparation *speculativeSynthesisPreparation, deliver func([]byte) error,
) (conversation.VoiceTurnResult, *speculativeSynthesis, error) {
	var mu sync.Mutex
	var synthesis *speculativeSynthesis
	closed := false
	decision, err := agent.ProcessWithSealedCandidate(ctx, uid, turn, func(candidate conversation.SealedSpeechCandidate) {
		mu.Lock()
		defer mu.Unlock()
		if closed || synthesis != nil || candidate.SpokenReply == "" || ctx.Err() != nil {
			return
		}
		synthesis = startSpeculativeSynthesis(ctx, speech, preparation.takeReady(), candidate.SpokenReply, deliver)
	})
	mu.Lock()
	closed = true
	staged := synthesis
	mu.Unlock()
	return decision, staged, err
}

// Preserve the existing completion-or-full-buffer commit boundary. The bool
// records an attempted release, not just successful publication: any failure
// after this boundary is terminal and must never trigger another provider run.
func releaseCommittedCandidate(ctx context.Context, synthesis *speculativeSynthesis) (int64, bool, error) {
	result, completed := synthesis.commitBoundary(ctx)
	if completed {
		if result.err != nil {
			return -1, false, result.err
		}
		if synthesis.firstChunkMS() < 0 {
			return -1, false, errSpeculativeAudioChunk
		}
	}
	releaseMS, err := synthesis.buffer.release(ctx)
	if err == nil && !completed {
		err = synthesis.await(ctx).err
	}
	return releaseMS, true, err
}

// Caption and HTTP output accept valid digital silence, but not an empty
// candidate stream. Audible-latency metrics still require meaningful PCM.
// A release attempt is terminal on failure, including a rejected callback.
func releaseCommittedPCMCandidate(ctx context.Context, synthesis *speculativeSynthesis) (int64, bool, error) {
	result, completed := synthesis.commitBoundary(ctx)
	if completed {
		if result.err != nil {
			return -1, false, result.err
		}
		if synthesis.buffer.peakBufferedBytes() == 0 {
			return -1, false, errSpeculativeAudioChunk
		}
	}
	releaseMS, err := synthesis.buffer.release(ctx)
	if err == nil && !completed {
		err = synthesis.await(ctx).err
	}
	return releaseMS, true, err
}
