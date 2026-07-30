package securityflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/research"
)

func TestCrossrefLeaseRequiresBoundCurrentSpeechAndIsOneShot(t *testing.T) {
	guard, now := newTestGuard(t)
	scope := testScope()
	query := testQuery(t, "quantum error correction")
	proposal, _, err := guard.ProposeCrossref(
		query,
		SourceCurrentUserSpeech|SourceModelOutput|SourcePDF,
	)
	if err != nil {
		t.Fatalf("ProposeCrossref: %v", err)
	}
	grant, _, err := guard.BindCurrentUserSpeechForCrossref(
		scope,
		query,
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("BindCurrentUserSpeechForCrossref: %v", err)
	}
	lease, _, err := guard.MintCrossref(
		grant,
		scope,
		proposal,
		time.Second,
	)
	if err != nil {
		t.Fatalf("MintCrossref: %v", err)
	}
	event, err := guard.ConsumeCrossref(lease, scope, proposal)
	if err != nil ||
		event.Decision != DecisionAllow ||
		event.Reason != ReasonAuthorized {
		t.Fatalf("ConsumeCrossref: event=%#v err=%v", event, err)
	}
	event, err = guard.ConsumeCrossref(lease, scope, proposal)
	if !errors.Is(err, ErrDenied) || event.Reason != ReasonReplay {
		t.Fatalf("replayed lease: event=%#v err=%v", event, err)
	}
	if now.IsZero() {
		t.Fatal("test clock was not initialized")
	}
}

func TestCrossrefLeaseRejectsAuthorityAndArgumentEscalation(t *testing.T) {
	guard, _ := newTestGuard(t)
	scope := testScope()
	query := testQuery(t, "quantum error correction")
	otherQuery := testQuery(t, "protein folding")
	proposal, _, err := guard.ProposeCrossref(
		query,
		SourceCurrentUserSpeech|SourceModelOutput,
	)
	if err != nil {
		t.Fatal(err)
	}
	otherProposal, _, err := guard.ProposeCrossref(
		otherQuery,
		SourceCurrentUserSpeech|
			SourceModelOutput|
			SourceConversationState,
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("no current speech authority", func(t *testing.T) {
		_, event, err := guard.MintCrossref(
			CurrentUserSpeech{},
			scope,
			proposal,
			time.Second,
		)
		if !errors.Is(err, ErrDenied) ||
			event.Decision != DecisionDeny {
			t.Fatalf("zero authority minted: event=%#v err=%v", event, err)
		}
	})

	t.Run("argument substitution", func(t *testing.T) {
		grant, _, err := guard.BindCurrentUserSpeechForCrossref(
			scope,
			query,
			2*time.Second,
		)
		if err != nil {
			t.Fatal(err)
		}
		_, event, err := guard.MintCrossref(
			grant,
			scope,
			otherProposal,
			time.Second,
		)
		if !errors.Is(err, ErrDenied) ||
			event.Reason != ReasonArgumentMismatch {
			t.Fatalf("argument swap minted: event=%#v err=%v", event, err)
		}
	})

	t.Run("grant replay", func(t *testing.T) {
		grant, _, err := guard.BindCurrentUserSpeechForCrossref(
			scope,
			query,
			2*time.Second,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := guard.MintCrossref(
			grant,
			scope,
			proposal,
			time.Second,
		); err != nil {
			t.Fatal(err)
		}
		_, event, err := guard.MintCrossref(
			grant,
			scope,
			proposal,
			time.Second,
		)
		if !errors.Is(err, ErrDenied) || event.Reason != ReasonReplay {
			t.Fatalf("grant replay minted: event=%#v err=%v", event, err)
		}
	})
}

func TestCrossrefLeaseBindsUIDSessionRequestAndIntegrity(t *testing.T) {
	for _, field := range []string{"uid", "session", "request"} {
		field := field
		t.Run(field, func(t *testing.T) {
			guard, _ := newTestGuard(t)
			scope := testScope()
			query := testQuery(t, "quantum error correction")
			proposal, _, err := guard.ProposeCrossref(
				query,
				SourceCurrentUserSpeech|SourceModelOutput,
			)
			if err != nil {
				t.Fatal(err)
			}
			grant, _, err := guard.BindCurrentUserSpeechForCrossref(
				scope,
				query,
				2*time.Second,
			)
			if err != nil {
				t.Fatal(err)
			}
			lease, _, err := guard.MintCrossref(
				grant,
				scope,
				proposal,
				time.Second,
			)
			if err != nil {
				t.Fatal(err)
			}
			wrong := scope
			switch field {
			case "uid":
				wrong.UID = "other-user"
			case "session":
				wrong.SessionID = "other-session"
			case "request":
				wrong.RequestID = "other-request"
			}
			event, err := guard.ConsumeCrossref(lease, wrong, proposal)
			if !errors.Is(err, ErrDenied) ||
				event.Reason != ReasonInvalidScope {
				t.Fatalf("cross-use accepted: event=%#v err=%v", event, err)
			}
		})
	}

	t.Run("tamper", func(t *testing.T) {
		guard, _ := newTestGuard(t)
		scope := testScope()
		query := testQuery(t, "quantum error correction")
		proposal, _, _ := guard.ProposeCrossref(
			query,
			SourceCurrentUserSpeech|SourceModelOutput,
		)
		grant, _, _ := guard.BindCurrentUserSpeechForCrossref(
			scope,
			query,
			2*time.Second,
		)
		lease, _, _ := guard.MintCrossref(
			grant,
			scope,
			proposal,
			time.Second,
		)
		lease.signature[0] ^= 1
		event, err := guard.ConsumeCrossref(lease, scope, proposal)
		if !errors.Is(err, ErrDenied) || event.Reason != ReasonTampered {
			t.Fatalf("tampered lease accepted: event=%#v err=%v", event, err)
		}
	})
}

func TestCrossrefLeaseExpiresAndConcurrentCopiesHaveOneWinner(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		guard, now := newTestGuard(t)
		scope := testScope()
		query := testQuery(t, "quantum error correction")
		proposal, _, _ := guard.ProposeCrossref(
			query,
			SourceCurrentUserSpeech|SourceModelOutput,
		)
		grant, _, _ := guard.BindCurrentUserSpeechForCrossref(
			scope,
			query,
			time.Second,
		)
		lease, _, _ := guard.MintCrossref(
			grant,
			scope,
			proposal,
			time.Second,
		)
		*now = now.Add(time.Second)
		event, err := guard.ConsumeCrossref(lease, scope, proposal)
		if !errors.Is(err, ErrDenied) || event.Reason != ReasonExpired {
			t.Fatalf("expired lease accepted: event=%#v err=%v", event, err)
		}
	})

	t.Run("concurrent one shot", func(t *testing.T) {
		guard, _ := newTestGuard(t)
		scope := testScope()
		query := testQuery(t, "quantum error correction")
		proposal, _, _ := guard.ProposeCrossref(
			query,
			SourceCurrentUserSpeech|SourceModelOutput,
		)
		grant, _, _ := guard.BindCurrentUserSpeechForCrossref(
			scope,
			query,
			2*time.Second,
		)
		lease, _, _ := guard.MintCrossref(
			grant,
			scope,
			proposal,
			time.Second,
		)

		const workers = 64
		var allowed atomic.Int32
		var denied atomic.Int32
		var wait sync.WaitGroup
		wait.Add(workers)
		for range workers {
			go func() {
				defer wait.Done()
				if _, err := guard.ConsumeCrossref(
					lease,
					scope,
					proposal,
				); err == nil {
					allowed.Add(1)
				} else if errors.Is(err, ErrDenied) {
					denied.Add(1)
				}
			}()
		}
		wait.Wait()
		if allowed.Load() != 1 || denied.Load() != workers-1 {
			t.Fatalf(
				"allowed=%d denied=%d; want 1/%d",
				allowed.Load(),
				denied.Load(),
				workers-1,
			)
		}
	})
}

func TestDefenseArtifactsContainNoContentOrIdentifiers(t *testing.T) {
	guard, _ := newTestGuard(t)
	scope := testScope()
	const topic = "rare-private-research-topic"
	query := testQuery(t, topic)
	proposal, proposalEvent, err := guard.ProposeCrossref(
		query,
		SourceCurrentUserSpeech|SourceModelOutput|SourcePDF,
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, grantEvent, err := guard.BindCurrentUserSpeechForCrossref(
		scope,
		query,
		2*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	lease, leaseEvent, err := guard.MintCrossref(
		grant,
		scope,
		proposal,
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}

	events, err := json.Marshal(
		[]DefenseEvent{proposalEvent, grantEvent, leaseEvent},
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := string(events) +
		fmt.Sprintf("%#v %#v %#v", proposal, grant, lease)
	for _, private := range []string{
		topic,
		scope.UID,
		scope.SessionID,
		scope.RequestID,
	} {
		if strings.Contains(artifacts, private) {
			t.Fatalf("defense artifact contains private input %q", private)
		}
	}
}

func TestGuardRejectsUntrustedOnlyProposalAndInvalidConfiguration(t *testing.T) {
	guard, _ := newTestGuard(t)
	query := testQuery(t, "quantum error correction")
	for _, sources := range []SourceSet{
		0,
		SourcePDF,
		SourceConversationState,
		SourceToolOutput,
		SourceModelOutput,
		SourceCurrentUserSpeech | SourceModelOutput | SourceAmbientSpeech,
		SourceSet(1 << 15),
	} {
		if _, event, err := guard.ProposeCrossref(query, sources); !errors.Is(err, ErrDenied) ||
			event.Decision != DecisionDeny {
			t.Fatalf(
				"untrusted-only proposal accepted: sources=%d event=%#v err=%v",
				sources,
				event,
				err,
			)
		}
	}
	if _, err := NewGuard(Config{
		Key:           bytes.Repeat([]byte{1}, 31),
		PolicyVersion: "pccm-v1",
		MaxTTL:        time.Second,
	}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("short key accepted: %v", err)
	}
}

func FuzzCrossrefLeaseTamperNeverAuthorizes(f *testing.F) {
	for selector := uint8(0); selector < 11; selector++ {
		f.Add(selector, uint64(selector)+1)
	}
	f.Fuzz(func(t *testing.T, selector uint8, delta uint64) {
		guard, _ := newTestGuard(t)
		scope := testScope()
		query := testQuery(t, "quantum error correction")
		proposal, _, err := guard.ProposeCrossref(
			query,
			SourceCurrentUserSpeech|SourceModelOutput|SourcePDF,
		)
		if err != nil {
			t.Fatal(err)
		}
		grant, _, err := guard.BindCurrentUserSpeechForCrossref(
			scope,
			query,
			2*time.Second,
		)
		if err != nil {
			t.Fatal(err)
		}
		lease, _, err := guard.MintCrossref(
			grant,
			scope,
			proposal,
			time.Second,
		)
		if err != nil {
			t.Fatal(err)
		}

		nonzero := byte(delta) | 1
		switch selector % 11 {
		case 0:
			lease.issuer[0] ^= nonzero
		case 1:
			lease.nonce[0] ^= nonzero
		case 2:
			lease.scope[0] ^= nonzero
		case 3:
			lease.args[0] ^= nonzero
		case 4:
			lease.policy[0] ^= nonzero
		case 5:
			lease.action ^= Action(nonzero)
		case 6:
			lease.sources ^= SourceSet(nonzero)
		case 7:
			lease.issuedAt ^= int64(delta | 1)
		case 8:
			lease.expiresAt ^= int64(delta | 1)
		case 9:
			lease.uses ^= nonzero
		case 10:
			lease.signature[0] ^= nonzero
		}

		event, err := guard.ConsumeCrossref(lease, scope, proposal)
		if !errors.Is(err, ErrDenied) ||
			event.Decision != DecisionDeny {
			t.Fatalf(
				"tampered lease authorized: selector=%d event=%#v err=%v",
				selector,
				event,
				err,
			)
		}
	})
}

func newTestGuard(t *testing.T) (*Guard, *time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	randomBytes := make([]byte, 16*1_024)
	for index := range randomBytes {
		randomBytes[index] = byte(index % 251)
	}
	guard, err := NewGuard(Config{
		Key:           bytes.Repeat([]byte{0x42}, keyBytes),
		PolicyVersion: "pccm-v1",
		MaxTTL:        5 * time.Second,
		Now:           func() time.Time { return now },
		Random:        bytes.NewReader(randomBytes),
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	return guard, &now
}

func testScope() Scope {
	return Scope{
		UID:       "user-123",
		SessionID: "encrypted-session-id",
		RequestID: "0123456789abcdef01234567",
	}
}

func testQuery(t *testing.T, topic string) research.Query {
	t.Helper()
	query, err := research.NewRecentTopicQuery(
		topic,
		time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
		5,
	)
	if err != nil {
		t.Fatalf("NewRecentTopicQuery: %v", err)
	}
	return query
}
