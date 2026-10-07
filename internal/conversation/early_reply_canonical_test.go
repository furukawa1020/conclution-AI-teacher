package conversation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
)

func TestEarlyReplyCanonicalizationReusesCompletedAudit(t *testing.T) {
	for _, reply := range []string{"  Aです。 Bです。  ", "Aです。\n\tBです。", "Aです。\u3000\u00a0 Bです。"} {
		t.Run(reply, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				plan := validModelPlan()
				plan.SpokenReply = reply
				generator := &heldPlannerAuditGenerator{
					planner: encodePlan(t, plan), plannerHeld: make(chan struct{}), releasePlanner: make(chan struct{}),
				}
				agent := newTestAgent(t, generator)
				candidate := make(chan SealedSpeechCandidate, 1)
				type outcome struct {
					result VoiceTurnResult
					err    error
				}
				done := make(chan outcome, 1)
				go func() {
					result, err := agent.ProcessWithSealedCandidate(ctx, "canonical-reply-user", VoiceTurn{
						SchemaVersion: SchemaVersion, Utterance: "次に何をすればいいですか",
					}, func(value SealedSpeechCandidate) { candidate <- value })
					done <- outcome{result, err}
				}()
				<-generator.plannerHeld
				synctest.Wait()
				if generator.criticCalls.Load() != 1 {
					t.Fatal("early audit did not complete")
				}
				var sealed SealedSpeechCandidate
				select {
				case sealed = <-candidate:
				default:
					t.Fatal("private synthesis candidate was not available")
				}
				select {
				case <-done:
					t.Fatal("final result escaped before planner completion")
				default:
				}
				close(generator.releasePlanner)
				synctest.Wait()
				completed := <-done
				if completed.err != nil || completed.result.Route != "fast" ||
					completed.result.SpokenReply != "Aです。 Bです。" ||
					sealed.SpokenReply != completed.result.SpokenReply || generator.criticCalls.Load() != 1 {
					t.Fatalf("candidate=%q final=%q route=%q critic=%d err=%v", sealed.SpokenReply,
						completed.result.SpokenReply, completed.result.Route, generator.criticCalls.Load(), completed.err)
				}
			})
		})
	}
}

func TestEarlyReplyCanonicalizationPreservesRawAndNormalizedGuards(t *testing.T) {
	for _, test := range []struct {
		name, reply string
		valid       bool
	}{
		{"canonical", "Aです。", true},
		{"whitespace", " \t\n\u3000", false},
		{"raw over limit", strings.Repeat(" ", MaxSpokenReplyRunes) + "A", false},
		{"unsafe speech", "  https://example.com を開いて  ", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := validModelPlan()
			plan.SpokenReply = test.reply
			raw, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			candidate, ready := earlyCandidateFromJSON(raw)
			if ready != test.valid || (ready && candidate.SpokenReply != collapseSpace(test.reply)) {
				t.Fatalf("ready=%v reply=%q", ready, candidate.SpokenReply)
			}
		})
	}
}
