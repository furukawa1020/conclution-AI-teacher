package shadowcut

import (
	"errors"
	"testing"
)

func FuzzSynthesizeVerifyConsistency(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{
		255, 0, 127, 64, 32, 16, 8, 4,
		2, 1, 0, 255, 128, 63, 15, 3,
	})

	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 64 {
			input = input[:64]
		}
		evidence := ControlMask(1 << (ControlEvidenceIngress - 1))
		planner := ControlMask(1 << (ControlPlannerBoundary - 1))
		traces := []Trace{
			attackTrace(evidence | planner),
			benignTrace(5, evidence),
			benignTrace(3, planner),
		}

		for index, value := range input {
			trace := &traces[index%len(traces)]
			switch index % 10 {
			case 0:
				edge := index % len(trace.Edges)
				trace.Edges[edge].Controls = ControlMask(value)
			case 1:
				edge := index % len(trace.Edges)
				trace.Edges[edge].Kind = EdgeKind(value)
			case 2:
				node := index % len(trace.Nodes)
				trace.Nodes[node].Kind = NodeKind(value)
			case 3:
				node := index % len(trace.Nodes)
				trace.Nodes[node].Integrity = Integrity(value)
			case 4:
				node := index % len(trace.Nodes)
				trace.Nodes[node].Authority = Authority(value)
			case 5:
				edge := index % len(trace.Edges)
				trace.Edges[edge].To = NodeID(value)
			case 6:
				trace.Sources[0] = NodeID(value)
			case 7:
				trace.Sinks[0] = NodeID(value)
			case 8:
				trace.Weight = uint32(value)
			case 9:
				node := index % len(trace.Nodes)
				trace.Nodes[node].BindingRef = BindingRef(value)
			}
		}

		candidate, certificate, err := Synthesize(traces)
		if err != nil {
			return
		}
		if err := Verify(traces, candidate, certificate); err != nil {
			t.Fatalf("synthesized certificate did not verify: %v", err)
		}
		secondCandidate, secondCertificate, err := Synthesize(traces)
		if err != nil ||
			secondCandidate != candidate ||
			secondCertificate != certificate {
			t.Fatalf(
				"nondeterministic synthesis: candidate=%#v certificate=%#v err=%v",
				secondCandidate,
				secondCertificate,
				err,
			)
		}

		tampered := certificate
		tampered.CandidateHash[0] ^= 1
		if err := Verify(
			traces,
			candidate,
			tampered,
		); !errors.Is(err, ErrVerification) {
			t.Fatalf("tampered certificate accepted: %v", err)
		}
	})
}
