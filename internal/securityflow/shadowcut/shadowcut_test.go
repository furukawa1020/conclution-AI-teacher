package shadowcut

import (
	"errors"
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
				privilegedAttack(evidence, planner),
				benignTrace(100, evidence),
				benignTrace(10, planner),
			},
			want: planner,
		},
		{
			name: "control count precedes lower cost",
			traces: []Trace{
				privilegedAttack(evidence|egress, 0),
				privilegedAttack(planner|egress, 0),
				benignTrace(1, 0),
			},
			want: egress,
		},
		{
			name: "cost precedes mask",
			traces: []Trace{
				privilegedAttack(evidence|planner, 0),
				benignTrace(1, 0),
			},
			want: evidence,
		},
		{
			name: "mask is final deterministic tie break",
			traces: []Trace{
				privilegedAttack(memory|tool, 0),
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
			name: "dangling edge",
			mutate: func(trace *Trace) {
				trace.Edges[0].To = NodeID(99)
			},
		},
		{
			name: "cycle",
			mutate: func(trace *Trace) {
				trace.Nodes = append(trace.Nodes, Node{
					ID:        6,
					Kind:      NodeModel,
					Integrity: IntegrityUntrusted,
				})
				trace.Edges = append(
					trace.Edges,
					Edge{From: 2, To: 6, Kind: EdgeDerive},
					Edge{From: 6, To: 2, Kind: EdgeDerive},
				)
			},
		},
		{
			name: "authority escalation",
			mutate: func(trace *Trace) {
				trace.Nodes[1].Authority = AuthorityExternalWrite
			},
		},
		{
			name: "sink capability not inherited",
			mutate: func(trace *Trace) {
				trace.Nodes[4].Authority = AuthorityExternalWrite
				trace.Nodes[4].Requires = AuthorityExternalWrite
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
				ID:        3,
				Kind:      NodeAuthenticatedIntent,
				Integrity: IntegrityAuthenticated,
				Authority: AuthorityResearchRead,
				Binding:   BindingExactScopeActionArgs,
			},
			{
				ID:        4,
				Kind:      NodeGrant,
				Integrity: IntegritySystem,
				Authority: AuthorityResearchRead,
				Binding:   BindingExactScopeActionArgs,
			},
			{
				ID:        5,
				Kind:      NodeSink,
				Integrity: IntegrityUntrusted,
				Authority: AuthorityResearchRead,
				Requires:  AuthorityResearchRead,
				Binding:   BindingExactScopeActionArgs,
			},
		},
		Edges: []Edge{
			{
				From:     1,
				To:       2,
				Kind:     EdgeDerive,
				Controls: firstControl,
			},
			{
				From:     2,
				To:       5,
				Kind:     EdgePropose,
				Controls: secondControl,
			},
			{From: 3, To: 4, Kind: EdgeIssueGrant},
			{From: 4, To: 5, Kind: EdgeConsume},
		},
		Sources: []NodeID{1},
		Sinks:   []NodeID{5},
	}
}

func benignTrace(weight uint32, control ControlMask) Trace {
	return Trace{
		Class:  TraceBenign,
		Weight: weight,
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
				ID:        3,
				Kind:      NodeSink,
				Integrity: IntegrityUntrusted,
			},
		},
		Edges: []Edge{
			{
				From:     1,
				To:       2,
				Kind:     EdgeDerive,
				Controls: control,
			},
			{From: 2, To: 3, Kind: EdgeRespond},
		},
		Sources: []NodeID{1},
		Sinks:   []NodeID{3},
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
