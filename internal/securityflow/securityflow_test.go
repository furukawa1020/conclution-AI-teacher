package securityflow

import (
	"bytes"
	"context"
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

type countingVerifier struct {
	calls []research.Query
	err   error
}

func (verifier *countingVerifier) Verify(
	_ context.Context,
	query research.Query,
) (research.Verification, error) {
	verifier.calls = append(verifier.calls, query)
	return research.Verification{QueryKind: query.Kind}, verifier.err
}

type panicVerifier struct {
	calls int
}

func (verifier *panicVerifier) Verify(
	context.Context,
	research.Query,
) (research.Verification, error) {
	verifier.calls++
	panic("PRIVATE-QUERY-MUST-NOT-ESCAPE")
}

type atomicVerifier struct {
	calls atomic.Int32
}

func (verifier *atomicVerifier) Verify(
	_ context.Context,
	query research.Query,
) (research.Verification, error) {
	verifier.calls.Add(1)
	return research.Verification{QueryKind: query.Kind}, nil
}

func TestCrossrefLeaseRequiresBoundCurrentSpeechAndIsOneShot(t *testing.T) {
	guard, now := newTestGuard(t)
	scope := testScope()
	query := testQuery(t, "quantum error correction")
	proposal, _, err := guard.ProposeCrossref(
		query,
		SourceDeclaredIntentionalAudio|SourceModelOutput|SourcePDF,
	)
	if err != nil {
		t.Fatalf("ProposeCrossref: %v", err)
	}
	grant, _, err := guard.BindDeclaredIntentionalAudioForCrossref(
		scope,
		query,
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("BindDeclaredIntentionalAudioForCrossref: %v", err)
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

func TestCrossrefExecutorEnforcesLeaseAndFinalArgumentBinding(t *testing.T) {
	guard, _ := newTestGuard(t)
	scope := testScope()
	query := testQuery(t, "quantum error correction")
	otherQuery := testQuery(t, "protein folding")
	proposal, _, err := guard.ProposeCrossref(
		query,
		SourceDeclaredIntentionalAudio|SourceModelOutput|SourcePDF,
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, _, err := guard.BindDeclaredIntentionalAudioForCrossref(
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
	rawEquivalentQuery := query
	rawEquivalentQuery.Topic = "  quantum   error correction  "
	rawEquivalentQuery.From = query.From.Add(17*time.Hour + 30*time.Minute)
	rawEquivalentQuery.Until = query.Until.Add(23*time.Hour + 59*time.Minute)
	verifier := &countingVerifier{}
	executor, err := NewCrossrefExecutor(guard, verifier)
	if err != nil {
		t.Fatal(err)
	}

	_, event, err := executor.Verify(
		context.Background(),
		lease,
		scope,
		proposal,
		otherQuery,
	)
	if !errors.Is(err, ErrDenied) ||
		event.Reason != ReasonArgumentMismatch ||
		len(verifier.calls) != 0 {
		t.Fatalf(
			"argument substitution reached verifier: event=%#v calls=%d err=%v",
			event,
			len(verifier.calls),
			err,
		)
	}

	verification, event, err := executor.Verify(
		context.Background(),
		lease,
		scope,
		proposal,
		rawEquivalentQuery,
	)
	if err != nil ||
		event.Decision != DecisionAllow ||
		event.Reason != ReasonAuthorized ||
		verification.QueryKind != query.Kind ||
		len(verifier.calls) != 1 {
		t.Fatalf(
			"authorized execution failed: event=%#v verification=%#v calls=%d err=%v",
			event,
			verification,
			len(verifier.calls),
			err,
		)
	}
	if verifier.calls[0] != query {
		t.Fatalf(
			"executor passed non-canonical query to verifier: got=%#v want=%#v",
			verifier.calls[0],
			query,
		)
	}

	_, event, err = executor.Verify(
		context.Background(),
		lease,
		scope,
		proposal,
		query,
	)
	if !errors.Is(err, ErrDenied) ||
		event.Reason != ReasonReplay ||
		len(verifier.calls) != 1 {
		t.Fatalf(
			"replayed execution reached verifier: event=%#v calls=%d err=%v",
			event,
			len(verifier.calls),
			err,
		)
	}

	_, event, err = executor.Verify(
		context.Background(),
		Lease{},
		scope,
		proposal,
		query,
	)
	if !errors.Is(err, ErrDenied) ||
		event.Decision != DecisionDeny ||
		len(verifier.calls) != 1 {
		t.Fatalf(
			"zero lease reached verifier: event=%#v calls=%d err=%v",
			event,
			len(verifier.calls),
			err,
		)
	}
}

func TestCrossrefExecutorRejectsMissingBoundaryComponents(t *testing.T) {
	guard, _ := newTestGuard(t)
	var typedNil *countingVerifier
	for name, candidate := range map[string]func() (*CrossrefExecutor, error){
		"nil guard": func() (*CrossrefExecutor, error) {
			return NewCrossrefExecutor(nil, &countingVerifier{})
		},
		"nil verifier": func() (*CrossrefExecutor, error) {
			return NewCrossrefExecutor(guard, nil)
		},
		"typed nil verifier": func() (*CrossrefExecutor, error) {
			return NewCrossrefExecutor(guard, typedNil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := candidate(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("invalid executor accepted: %v", err)
			}
		})
	}
}

func TestCrossrefExecutorNormalizesProviderFailureAndPanic(t *testing.T) {
	failing := &countingVerifier{
		err: errors.New("PRIVATE-PROVIDER-DETAIL"),
	}
	panicking := &panicVerifier{}
	for _, test := range []struct {
		name     string
		verifier research.Verifier
		calls    func() int
	}{
		{
			name:     "provider error",
			verifier: failing,
			calls:    func() int { return len(failing.calls) },
		},
		{
			name:     "provider panic",
			verifier: panicking,
			calls:    func() int { return panicking.calls },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			guard, _ := newTestGuard(t)
			scope := testScope()
			query := testQuery(t, "quantum error correction")
			proposal, _, err := guard.ProposeCrossref(
				query,
				SourceDeclaredIntentionalAudio|SourceModelOutput,
			)
			if err != nil {
				t.Fatal(err)
			}
			grant, _, err := guard.BindDeclaredIntentionalAudioForCrossref(
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
			executor, err := NewCrossrefExecutor(guard, test.verifier)
			if err != nil {
				t.Fatal(err)
			}

			_, event, err := executor.Verify(
				context.Background(),
				lease,
				scope,
				proposal,
				query,
			)
			if !errors.Is(err, ErrExecutorUnavailable) ||
				event.Decision != DecisionAllow ||
				event.Reason != ReasonAuthorized ||
				strings.Contains(err.Error(), "PRIVATE") ||
				test.calls() != 1 {
				t.Fatalf(
					"unsafe provider failure: event=%#v calls=%d err=%v",
					event,
					test.calls(),
					err,
				)
			}
			_, event, err = executor.Verify(
				context.Background(),
				lease,
				scope,
				proposal,
				query,
			)
			if !errors.Is(err, ErrDenied) ||
				event.Reason != ReasonReplay ||
				test.calls() != 1 {
				t.Fatalf(
					"failed provider reused capability: event=%#v calls=%d err=%v",
					event,
					test.calls(),
					err,
				)
			}
		})
	}
}

func TestCrossrefLeaseRejectsAuthorityAndArgumentEscalation(t *testing.T) {
	guard, _ := newTestGuard(t)
	scope := testScope()
	query := testQuery(t, "quantum error correction")
	otherQuery := testQuery(t, "protein folding")
	proposal, _, err := guard.ProposeCrossref(
		query,
		SourceDeclaredIntentionalAudio|SourceModelOutput,
	)
	if err != nil {
		t.Fatal(err)
	}
	otherProposal, _, err := guard.ProposeCrossref(
		otherQuery,
		SourceDeclaredIntentionalAudio|
			SourceModelOutput|
			SourceConversationState,
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("no current speech authority", func(t *testing.T) {
		_, event, err := guard.MintCrossref(
			DeclaredIntentionalAudioGrant{},
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
		grant, _, err := guard.BindDeclaredIntentionalAudioForCrossref(
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
		replayGuard, _ := newTestGuard(t)
		replayProposal, _, err := replayGuard.ProposeCrossref(
			query,
			SourceDeclaredIntentionalAudio|SourceModelOutput,
		)
		if err != nil {
			t.Fatal(err)
		}
		grant, _, err := replayGuard.BindDeclaredIntentionalAudioForCrossref(
			scope,
			query,
			2*time.Second,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := replayGuard.MintCrossref(
			grant,
			scope,
			replayProposal,
			time.Second,
		); err != nil {
			t.Fatal(err)
		}
		_, event, err := replayGuard.MintCrossref(
			grant,
			scope,
			replayProposal,
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
				SourceDeclaredIntentionalAudio|SourceModelOutput,
			)
			if err != nil {
				t.Fatal(err)
			}
			grant, _, err := guard.BindDeclaredIntentionalAudioForCrossref(
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
			SourceDeclaredIntentionalAudio|SourceModelOutput,
		)
		grant, _, _ := guard.BindDeclaredIntentionalAudioForCrossref(
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
			SourceDeclaredIntentionalAudio|SourceModelOutput,
		)
		grant, _, _ := guard.BindDeclaredIntentionalAudioForCrossref(
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
			SourceDeclaredIntentionalAudio|SourceModelOutput,
		)
		grant, _, _ := guard.BindDeclaredIntentionalAudioForCrossref(
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

func TestCrossrefExecutorConcurrentCopiesReachSinkOnce(t *testing.T) {
	guard, _ := newTestGuard(t)
	scope := testScope()
	query := testQuery(t, "quantum error correction")
	proposal, _, err := guard.ProposeCrossref(
		query,
		SourceDeclaredIntentionalAudio|SourceModelOutput,
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, _, err := guard.BindDeclaredIntentionalAudioForCrossref(
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
	verifier := &atomicVerifier{}
	executor, err := NewCrossrefExecutor(guard, verifier)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 64
	var allowed atomic.Int32
	var denied atomic.Int32
	var unexpected atomic.Int32
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			if _, _, err := executor.Verify(
				context.Background(),
				lease,
				scope,
				proposal,
				query,
			); err == nil {
				allowed.Add(1)
			} else if errors.Is(err, ErrDenied) {
				denied.Add(1)
			} else {
				unexpected.Add(1)
			}
		}()
	}
	wait.Wait()
	if allowed.Load() != 1 ||
		denied.Load() != workers-1 ||
		unexpected.Load() != 0 ||
		verifier.calls.Load() != 1 {
		t.Fatalf(
			"allowed=%d denied=%d unexpected=%d sink_calls=%d",
			allowed.Load(),
			denied.Load(),
			unexpected.Load(),
			verifier.calls.Load(),
		)
	}
}

func TestCrossrefAuthorityIssuesOncePerRequestAndAction(t *testing.T) {
	guard, now := newTestGuard(t)
	scope := testScope()
	query := testQuery(t, "quantum error correction")
	otherQuery := testQuery(t, "protein folding")
	if _, _, err := guard.BindDeclaredIntentionalAudioForCrossref(
		scope,
		query,
		time.Second,
	); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []research.Query{query, otherQuery} {
		if _, event, err := guard.BindDeclaredIntentionalAudioForCrossref(
			scope,
			candidate,
			time.Second,
		); !errors.Is(err, ErrDenied) ||
			event.Reason != ReasonReplay {
			t.Fatalf(
				"duplicate request authority issued: event=%#v err=%v",
				event,
				err,
			)
		}
	}

	otherRequest := scope
	otherRequest.RequestID = "1123456789abcdef01234567"
	if _, _, err := guard.BindDeclaredIntentionalAudioForCrossref(
		otherRequest,
		query,
		time.Second,
	); err != nil {
		t.Fatalf("new request was denied: %v", err)
	}

	*now = now.Add(time.Minute)
	if _, _, err := guard.BindDeclaredIntentionalAudioForCrossref(
		scope,
		query,
		time.Second,
	); err != nil {
		t.Fatalf("expired request issuance was not cleaned: %v", err)
	}
}

func TestDefenseArtifactsContainNoContentOrIdentifiers(t *testing.T) {
	guard, _ := newTestGuard(t)
	scope := testScope()
	const topic = "rare-private-research-topic"
	query := testQuery(t, topic)
	proposal, proposalEvent, err := guard.ProposeCrossref(
		query,
		SourceDeclaredIntentionalAudio|SourceModelOutput|SourcePDF,
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, grantEvent, err := guard.BindDeclaredIntentionalAudioForCrossref(
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
	executor, err := NewCrossrefExecutor(guard, &countingVerifier{})
	if err != nil {
		t.Fatal(err)
	}

	events, err := json.Marshal(
		[]DefenseEvent{proposalEvent, grantEvent, leaseEvent},
	)
	if err != nil {
		t.Fatal(err)
	}
	opaqueJSON, err := json.Marshal(
		[]any{scope, proposal, grant, lease, guard, executor},
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := string(events) + string(opaqueJSON) +
		fmt.Sprintf(
			"%#v %#v %#v %#v %#v %#v",
			scope,
			proposal,
			grant,
			lease,
			guard,
			executor,
		)
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
		SourceDeclaredIntentionalAudio | SourceModelOutput | SourceAmbientSpeech,
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
		Key:         bytes.Repeat([]byte{1}, 31),
		Policy:      PolicyPCCMPhase1,
		MaxTTL:      time.Second,
		IssuanceTTL: time.Minute,
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
			SourceDeclaredIntentionalAudio|SourceModelOutput|SourcePDF,
		)
		if err != nil {
			t.Fatal(err)
		}
		grant, _, err := guard.BindDeclaredIntentionalAudioForCrossref(
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
		Key:         bytes.Repeat([]byte{0x42}, keyBytes),
		Policy:      PolicyPCCMPhase1,
		MaxTTL:      5 * time.Second,
		IssuanceTTL: time.Minute,
		Now:         func() time.Time { return now },
		Random:      bytes.NewReader(randomBytes),
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
