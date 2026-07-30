package shadowcut

import (
	"errors"
	"sync"
	"testing"
)

func TestSynthesizeObjectiveOrder(t *testing.T) {
	evidence := mustMask(t, ControlEvidenceIngress)
	planner := mustMask(t, ControlPlannerBoundary)
	memory := mustMask(t, ControlMemoryBoundary)
	tool := mustMask(t, ControlToolBoundary)
	egress := mustMask(t, ControlExternalEgress)

	tests := []struct {
		name   string
		traces []Trace
		want   ControlMask
	}{
		{
			name: "benign retention precedes lower cost",
			traces: []Trace{
				attackTrace(evidence | planner),
				benignTrace(100, evidence),
				benignTrace(10, planner),
			},
			want: planner,
		},
		{
			name: "control count precedes lower cost",
			traces: []Trace{
				attackTrace(evidence | egress),
				attackTrace(planner | egress),
				benignTrace(1, 0),
			},
			want: egress,
		},
		{
			name: "cost precedes mask",
			traces: []Trace{
				attackTrace(evidence | planner),
				benignTrace(1, 0),
			},
			want: evidence,
		},
		{
			name: "mask is final deterministic tie break",
			traces: []Trace{
				attackTrace(memory | tool),
				benignTrace(1, 0),
			},
			want: memory,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate, certificate, err := Synthesize(test.traces)
			if err != nil {
				t.Fatal(err)
			}
			if candidate.Schema != SchemaVersion ||
				candidate.Controls != test.want {
				t.Fatalf("candidate=%#v want_mask=%d", candidate, test.want)
			}
			if certificate.AttackTraceCount == 0 ||
				certificate.BlockedAttackCount !=
					certificate.AttackTraceCount {
				t.Fatalf("attacks not blocked: %#v", certificate)
			}
			if err := Verify(test.traces, candidate, certificate); err != nil {
				t.Fatalf("independent verify: %v", err)
			}
		})
	}
}

func TestCanonicalHashAndSynthesisIgnoreInputOrder(t *testing.T) {
	evidence := mustMask(t, ControlEvidenceIngress)
	planner := mustMask(t, ControlPlannerBoundary)
	original := []Trace{
		privilegedAttack(evidence, planner),
		benignTrace(100, evidence),
		benignTrace(10, planner),
	}
	reordered := make([]Trace, len(original))
	for index := range original {
		reordered[len(original)-1-index] = reversedTrace(original[index])
	}

	firstCandidate, firstCertificate, err := Synthesize(original)
	if err != nil {
		t.Fatal(err)
	}
	secondCandidate, secondCertificate, err := Synthesize(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if firstCandidate != secondCandidate ||
		firstCertificate != secondCertificate {
		t.Fatalf(
			"input order changed result: first=%#v/%#v second=%#v/%#v",
			firstCandidate,
			firstCertificate,
			secondCandidate,
			secondCertificate,
		)
	}
}

func TestVerifyRejectsCandidateCertificateAndTraceTamper(t *testing.T) {
	evidence := mustMask(t, ControlEvidenceIngress)
	planner := mustMask(t, ControlPlannerBoundary)
	traces := []Trace{
		privilegedAttack(evidence, planner),
		benignTrace(100, evidence),
		benignTrace(10, planner),
	}
	candidate, certificate, err := Synthesize(traces)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("candidate", func(t *testing.T) {
		tampered := candidate
		tampered.Controls = evidence
		if err := Verify(traces, tampered, certificate); !errors.Is(
			err,
			ErrVerification,
		) {
			t.Fatalf("tampered candidate accepted: %v", err)
		}
	})

	t.Run("certificate", func(t *testing.T) {
		tampered := certificate
		tampered.TraceSetHash[0] ^= 1
		if err := Verify(traces, candidate, tampered); !errors.Is(
			err,
			ErrVerification,
		) {
			t.Fatalf("tampered certificate accepted: %v", err)
		}
	})

	t.Run("trace", func(t *testing.T) {
		tampered := append([]Trace(nil), traces...)
		tampered[1] = cloneTrace(tampered[1])
		tampered[1].Weight++
		if err := Verify(tampered, candidate, certificate); !errors.Is(
			err,
			ErrVerification,
		) {
			t.Fatalf("tampered trace accepted: %v", err)
		}
	})
}

func TestTraceValidationRejectsUnsafeGraphs(t *testing.T) {
	evidence := mustMask(t, ControlEvidenceIngress)
	planner := mustMask(t, ControlPlannerBoundary)
	base := privilegedAttack(evidence, planner)

	tests := []struct {
		name   string
		mutate func(*Trace)
	}{
		{
			name: "unknown node",
			mutate: func(trace *Trace) {
				trace.Nodes[0].Kind = NodeKind(255)
			},
		},
		{
			name: "unknown control",
			mutate: func(trace *Trace) {
				trace.Edges[0].Controls = 1 << 15
			},
		},
		{
			name: "control on nonexistent boundary",
			mutate: func(trace *Trace) {
				trace.Edges[0].Controls = mustMask(
					t,
					ControlSpeechBoundary,
				)
			},
		},
		{
			name: "boundary kind substitution",
			mutate: func(trace *Trace) {
				trace.Edges[0].Kind = EdgeDerive
				trace.Edges[0].Controls = 0
			},
		},
		{
			name: "dangling edge",
			mutate: func(trace *Trace) {
				trace.Edges[0].To = NodeID(99)
			},
		},
		{
			name: "cycle",
			mutate: func(trace *Trace) {
				trace.Edges = append(trace.Edges, Edge{
					From: 6,
					To:   2,
					Kind: EdgePropose,
				})
			},
		},
		{
			name: "authority escalation",
			mutate: func(trace *Trace) {
				trace.Nodes[1].Authority = AuthorityExternalWrite
			},
		},
		{
			name: "binding substitution",
			mutate: func(trace *Trace) {
				trace.Nodes[5].BindingRef++
			},
		},
		{
			name: "sink capability not inherited",
			mutate: func(trace *Trace) {
				trace.Nodes[6].Authority = AuthorityExternalWrite
				trace.Nodes[6].Requires = AuthorityExternalWrite
			},
		},
		{
			name: "grant carries surplus authority",
			mutate: func(trace *Trace) {
				trace.Nodes[4].Authority |= AuthorityExternalWrite
				trace.Nodes[5].Authority |= AuthorityExternalWrite
			},
		},
		{
			name: "undeclared internal sink",
			mutate: func(trace *Trace) {
				trace.Nodes = append(trace.Nodes, Node{
					ID:        8,
					Kind:      NodeSink,
					Integrity: IntegrityUntrusted,
				})
				trace.Edges = append(trace.Edges, Edge{
					From: 2,
					To:   8,
					Kind: EdgeRespond,
				})
			},
		},
		{
			name: "multiple sinks",
			mutate: func(trace *Trace) {
				trace.Nodes = append(trace.Nodes, Node{
					ID:        8,
					Kind:      NodeSink,
					Integrity: IntegrityUntrusted,
				})
				trace.Edges = append(trace.Edges, Edge{
					From: 2,
					To:   8,
					Kind: EdgeRespond,
				})
				trace.Sinks = append(trace.Sinks, 8)
			},
		},
		{
			name: "source and sink overlap",
			mutate: func(trace *Trace) {
				trace.Sources = append(trace.Sources, trace.Sinks[0])
			},
		},
		{
			name: "multiple grants consumed by one action",
			mutate: func(trace *Trace) {
				trace.Nodes = append(
					trace.Nodes,
					Node{
						ID:         8,
						Kind:       NodeAuthenticatedIntent,
						Integrity:  IntegrityAuthenticated,
						Authority:  AuthorityResearchRead,
						Binding:    BindingExactScopeActionArgs,
						BindingRef: 2,
					},
					Node{
						ID:         9,
						Kind:       NodeGrant,
						Integrity:  IntegritySystem,
						Authority:  AuthorityResearchRead,
						Binding:    BindingExactScopeActionArgs,
						BindingRef: 2,
					},
				)
				trace.Edges = append(
					trace.Edges,
					Edge{From: 8, To: 9, Kind: EdgeIssueGrant},
					Edge{From: 9, To: 5, Kind: EdgeConsume},
				)
			},
		},
		{
			name: "grant consumed by unprivileged sink",
			mutate: func(trace *Trace) {
				trace.Nodes[6].Authority = 0
				trace.Nodes[6].Requires = 0
				trace.Nodes[6].Binding = BindingNone
				trace.Nodes[6].BindingRef = 0
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := cloneTrace(base)
			test.mutate(&invalid)
			corpus := []Trace{invalid, benignTrace(1, 0)}
			if _, _, err := Synthesize(corpus); !errors.Is(
				err,
				ErrInvalidTrace,
			) {
				t.Fatalf("unsafe trace accepted: %v", err)
			}
		})
	}
}

func TestPrivilegedBenignTraceRequiresEveryRootAndExactBinding(t *testing.T) {
	evidence := mustMask(t, ControlEvidenceIngress)
	planner := mustMask(t, ControlPlannerBoundary)
	benign := privilegedAttack(evidence, planner)
	benign.Class = TraceBenign
	benign.Weight = 5
	benign.Sources = []NodeID{1, 3}

	corpus := []Trace{
		privilegedAttack(evidence, planner),
		benign,
	}
	candidate, certificate, err := Synthesize(corpus)
	if err != nil {
		t.Fatalf("valid privileged benign trace rejected: %v", err)
	}
	if err := Verify(corpus, candidate, certificate); err != nil {
		t.Fatalf("valid privileged benign trace did not verify: %v", err)
	}

	undeclaredIntent := cloneTrace(benign)
	undeclaredIntent.Sources = []NodeID{1}
	if _, _, err := Synthesize(
		[]Trace{
			privilegedAttack(evidence, planner),
			undeclaredIntent,
		},
	); !errors.Is(err, ErrInvalidTrace) {
		t.Fatalf("undeclared benign intent root accepted: %v", err)
	}

	substitutedBinding := cloneTrace(benign)
	substitutedBinding.Nodes[6].BindingRef = 2
	if _, _, err := Synthesize(
		[]Trace{
			privilegedAttack(evidence, planner),
			substitutedBinding,
		},
	); !errors.Is(err, ErrInvalidTrace) {
		t.Fatalf("substituted sink binding accepted: %v", err)
	}
}

func TestUncuttableAttackIsInfeasible(t *testing.T) {
	traces := []Trace{
		privilegedAttack(0, 0),
		benignTrace(1, 0),
	}
	if _, _, err := Synthesize(traces); !errors.Is(err, ErrInfeasible) {
		t.Fatalf("uncuttable attack result: %v", err)
	}
	if err := Verify(
		traces,
		Candidate{Schema: SchemaVersion},
		Certificate{},
	); !errors.Is(err, ErrInfeasible) {
		t.Fatalf("uncuttable verify result: %v", err)
	}
}

func TestSynthesisIsDeterministic(t *testing.T) {
	evidence := mustMask(t, ControlEvidenceIngress)
	planner := mustMask(t, ControlPlannerBoundary)
	traces := []Trace{
		privilegedAttack(evidence, planner),
		benignTrace(100, evidence),
		benignTrace(10, planner),
	}
	wantCandidate, wantCertificate, err := Synthesize(traces)
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 32; iteration++ {
		candidate, certificate, err := Synthesize(traces)
		if err != nil {
			t.Fatal(err)
		}
		if candidate != wantCandidate || certificate != wantCertificate {
			t.Fatalf(
				"iteration %d changed result: %#v/%#v",
				iteration,
				candidate,
				certificate,
			)
		}
	}
}

func TestConcurrentSynthesisIsDeterministic(t *testing.T) {
	evidence := mustMask(t, ControlEvidenceIngress)
	planner := mustMask(t, ControlPlannerBoundary)
	traces := []Trace{
		attackTrace(evidence | planner),
		benignTrace(100, evidence),
		benignTrace(10, planner),
	}
	wantCandidate, wantCertificate, err := Synthesize(traces)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 64
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			candidate, certificate, err := Synthesize(traces)
			if err != nil {
				errs <- err
				return
			}
			if candidate != wantCandidate || certificate != wantCertificate {
				errs <- ErrVerification
				return
			}
			errs <- Verify(traces, candidate, certificate)
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent synthesis or verification failed: %v", err)
		}
	}
}

func TestCanonicalDuplicateTraceIsRejected(t *testing.T) {
	evidence := mustMask(t, ControlEvidenceIngress)
	benign := benignTrace(1, 0)
	if _, _, err := Synthesize([]Trace{
		attackTrace(evidence),
		benign,
		reversedTrace(benign),
	}); !errors.Is(err, ErrInvalidCorpus) {
		t.Fatalf("canonical duplicate trace accepted: %v", err)
	}
}

func privilegedAttack(
	firstControl ControlMask,
	secondControl ControlMask,
) Trace {
	return Trace{
		Class:  TraceAttack,
		Weight: 1,
		Nodes: []Node{
			{
				ID:        1,
				Kind:      NodePDF,
				Integrity: IntegrityUntrusted,
			},
			{
				ID:        2,
				Kind:      NodeModel,
				Integrity: IntegrityUntrusted,
			},
			{
				ID:        6,
				Kind:      NodeModel,
				Integrity: IntegrityUntrusted,
			},
			{
				ID:        7,
				Kind:      NodeTool,
				Integrity: IntegrityUntrusted,
			},
			{
				ID:         3,
				Kind:       NodeAuthenticatedIntent,
				Integrity:  IntegrityAuthenticated,
				Authority:  AuthorityResearchRead,
				Binding:    BindingExactScopeActionArgs,
				BindingRef: 1,
			},
			{
				ID:         4,
				Kind:       NodeGrant,
				Integrity:  IntegritySystem,
				Authority:  AuthorityResearchRead,
				Binding:    BindingExactScopeActionArgs,
				BindingRef: 1,
			},
			{
				ID:         5,
				Kind:       NodeSink,
				Integrity:  IntegrityUntrusted,
				Authority:  AuthorityResearchRead,
				Requires:   AuthorityResearchRead,
				Binding:    BindingExactScopeActionArgs,
				BindingRef: 1,
			},
		},
		Edges: []Edge{
			{
				From:     1,
				To:       2,
				Kind:     EdgeQuote,
				Controls: firstControl,
			},
			{
				From:     2,
				To:       6,
				Kind:     EdgePropose,
				Controls: secondControl,
			},
			{From: 6, To: 7, Kind: EdgeToolCall},
			{From: 7, To: 5, Kind: EdgeExecute},
			{From: 3, To: 4, Kind: EdgeIssueGrant},
			{From: 4, To: 5, Kind: EdgeConsume},
		},
		Sources: []NodeID{1},
		Sinks:   []NodeID{5},
	}
}

func benignTrace(weight uint32, control ControlMask) Trace {
	trace := attackTrace(control)
	trace.Class = TraceBenign
	trace.Weight = weight
	return trace
}

func attackTrace(controls ControlMask) Trace {
	evidence := ControlMask(1 << (ControlEvidenceIngress - 1))
	memory := ControlMask(1 << (ControlMemoryBoundary - 1))
	planner := ControlMask(1 << (ControlPlannerBoundary - 1))
	tool := ControlMask(1 << (ControlToolBoundary - 1))
	speech := ControlMask(1 << (ControlSpeechBoundary - 1))
	egress := ControlMask(1 << (ControlExternalEgress - 1))

	nodes := []Node{
		{ID: 1, Kind: NodePDF, Integrity: IntegrityUntrusted},
		{ID: 2, Kind: NodeMemory, Integrity: IntegrityUntrusted},
		{ID: 3, Kind: NodeModel, Integrity: IntegrityUntrusted},
		{ID: 4, Kind: NodeModel, Integrity: IntegrityUntrusted},
		{ID: 5, Kind: NodeTool, Integrity: IntegrityUntrusted},
	}
	edges := []Edge{
		{
			From:     1,
			To:       2,
			Kind:     EdgeQuote,
			Controls: controls & evidence,
		},
		{
			From:     2,
			To:       3,
			Kind:     EdgeRecall,
			Controls: controls & memory,
		},
		{
			From:     3,
			To:       4,
			Kind:     EdgePropose,
			Controls: controls & planner,
		},
		{
			From:     4,
			To:       5,
			Kind:     EdgeToolCall,
			Controls: controls & tool,
		},
	}
	if controls&egress != 0 {
		nodes = append(nodes, Node{
			ID:        6,
			Kind:      NodeSink,
			Integrity: IntegrityUntrusted,
		})
		edges = append(edges, Edge{
			From:     5,
			To:       6,
			Kind:     EdgeExecute,
			Controls: egress,
		})
	} else {
		nodes = append(
			nodes,
			Node{ID: 6, Kind: NodeModel, Integrity: IntegrityUntrusted},
			Node{ID: 7, Kind: NodeSink, Integrity: IntegrityUntrusted},
		)
		edges = append(
			edges,
			Edge{From: 5, To: 6, Kind: EdgeDerive},
			Edge{
				From:     6,
				To:       7,
				Kind:     EdgeRespond,
				Controls: controls & speech,
			},
		)
	}
	sink := nodes[len(nodes)-1].ID
	return Trace{
		Class:   TraceAttack,
		Weight:  1,
		Nodes:   nodes,
		Edges:   edges,
		Sources: []NodeID{1},
		Sinks:   []NodeID{sink},
	}
}

func mustMask(t *testing.T, controls ...Control) ControlMask {
	t.Helper()
	mask, err := ControlMaskOf(controls...)
	if err != nil {
		t.Fatal(err)
	}
	return mask
}

func reversedTrace(trace Trace) Trace {
	reversed := cloneTrace(trace)
	reverseNodes := func(nodes []Node) {
		for left, right := 0, len(nodes)-1; left < right; left, right =
			left+1, right-1 {
			nodes[left], nodes[right] = nodes[right], nodes[left]
		}
	}
	reverseEdges := func(edges []Edge) {
		for left, right := 0, len(edges)-1; left < right; left, right =
			left+1, right-1 {
			edges[left], edges[right] = edges[right], edges[left]
		}
	}
	reverseIDs := func(ids []NodeID) {
		for left, right := 0, len(ids)-1; left < right; left, right =
			left+1, right-1 {
			ids[left], ids[right] = ids[right], ids[left]
		}
	}
	reverseNodes(reversed.Nodes)
	reverseEdges(reversed.Edges)
	reverseIDs(reversed.Sources)
	reverseIDs(reversed.Sinks)
	return reversed
}
