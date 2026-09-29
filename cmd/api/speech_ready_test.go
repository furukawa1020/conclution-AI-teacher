package main

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
)

type auditedSpeechWarmerStub struct {
	result      speechio.StreamingSynthesisWarmupResult
	received    []string
	concurrency int
}

func (stub *auditedSpeechWarmerStub) WarmStreamingSynthesis(
	_ context.Context,
	cues []string,
	concurrency int,
) speechio.StreamingSynthesisWarmupResult {
	stub.received = append([]string(nil), cues...)
	stub.concurrency = concurrency
	return stub.result
}

func TestRequireAuditedSpeechReadyRequiresEveryFixedCue(t *testing.T) {
	cues := conversation.AuditedInstantVoiceCues()
	stub := &auditedSpeechWarmerStub{
		result: speechio.StreamingSynthesisWarmupResult{
			Requested: len(cues),
			Warmed:    len(cues),
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := requireAuditedSpeechReady(context.Background(), logger, stub); err != nil {
		t.Fatal(err)
	}
	if stub.concurrency != 2 || !reflect.DeepEqual(stub.received, cues) {
		t.Fatalf("warmup input=%q concurrency=%d", stub.received, stub.concurrency)
	}
}

func TestRequireAuditedSpeechReadyRejectsPartialCache(t *testing.T) {
	cues := conversation.AuditedInstantVoiceCues()
	stub := &auditedSpeechWarmerStub{
		result: speechio.StreamingSynthesisWarmupResult{
			Requested: len(cues),
			Warmed:    len(cues) - 1,
			Failed:    1,
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := requireAuditedSpeechReady(context.Background(), logger, stub); err == nil {
		t.Fatal("partial PCM cache was accepted as ready")
	}
}
