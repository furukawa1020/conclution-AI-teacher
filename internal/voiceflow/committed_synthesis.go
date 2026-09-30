package voiceflow

import (
	"context"

	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

// streamCommittedSynthesis consumes an already-ready, single-use connection.
// Its caller has audited the reply and authorized output. Taking a capability
// never waits for configuration: a slow preparation cannot delay this path.
func streamCommittedSynthesis(
	ctx context.Context,
	speech speechio.StreamingService,
	preparation *speculativeSynthesisPreparation,
	text string,
	onAudio func([]byte) error,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	prepared := preparation.takeReady()
	if prepared == nil {
		return speech.StreamSynthesize(ctx, text, onAudio)
	}
	// Preparation belongs to the enclosing turn; the committed processing
	// deadline may be shorter. Closing the transferred capability cancels its
	// provider without canceling that enclosing turn.
	stopCancellation := context.AfterFunc(ctx, prepared.Close)
	defer stopCancellation()
	defer prepared.Close()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	chunkObserved := false
	var callbackErr error
	mime, err := prepared.StreamSynthesize(text, func(chunk []byte) error {
		chunkObserved = true
		if callbackErr != nil {
			return callbackErr
		}
		if callbackErr = ctx.Err(); callbackErr != nil {
			return callbackErr
		}
		callbackErr = onAudio(chunk)
		return callbackErr
	})
	stopCancellation()
	prepared.Close()
	if contextErr := ctx.Err(); contextErr != nil {
		return "", contextErr
	}
	if callbackErr != nil {
		return "", callbackErr
	}
	// An idle configuration stream can expire while the person is speaking.
	// Only a provider failure with no callback may retry. Even silent, partial,
	// or rejected PCM forbids retrying and concatenating two provider runs.
	if err != nil && !chunkObserved {
		return speech.StreamSynthesize(ctx, text, onAudio)
	}
	return mime, err
}
