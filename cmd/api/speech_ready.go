package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

const auditedSpeechReadyTimeout = 25 * time.Second

type auditedSpeechWarmer interface {
	WarmStreamingSynthesis(
		context.Context,
		[]string,
		int,
	) speechio.StreamingSynthesisWarmupResult
}

// requireAuditedSpeechReady keeps a fresh instance out of service until every
// fixed, server-authored immediate reply is resident in its bounded PCM cache.
// User text, identifiers and conversation state never cross this boundary.
func requireAuditedSpeechReady(
	ctx context.Context,
	logger *slog.Logger,
	warmer auditedSpeechWarmer,
) error {
	if warmer == nil {
		return nil
	}
	readyCtx, cancel := context.WithTimeout(ctx, auditedSpeechReadyTimeout)
	defer cancel()
	cues := conversation.AuditedInstantVoiceCues()
	result := warmer.WarmStreamingSynthesis(readyCtx, cues, 2)
	logger.Info("audited instant speech readiness completed",
		"requested", result.Requested,
		"warmed", result.Warmed,
		"failed", result.Failed,
	)
	if err := readyCtx.Err(); err != nil {
		return errors.Join(errors.New("audited instant speech readiness timed out"), err)
	}
	if result.Requested != len(cues) ||
		result.Warmed != result.Requested ||
		result.Failed != 0 {
		return errors.New("audited instant speech readiness incomplete")
	}
	return nil
}
