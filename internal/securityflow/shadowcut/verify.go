package shadowcut

import "math/bits"

// Verify independently revalidates the corpus and re-enumerates every finite
// candidate. It does not call Synthesize or trust certificate metrics.
func Verify(
	traces []Trace,
	candidate Candidate,
	certificate Certificate,
) error {
	if candidate.Schema != SchemaVersion ||
		!validControlMask(candidate.Controls) {
		return ErrInvalidCandidate
	}

	corpus, err := prepareCorpus(traces)
	if err != nil {
		return err
	}

	var best objective
	found := false
	for raw := uint32(0); raw <= uint32(knownControlMask); raw++ {
		mask := ControlMask(raw)
		score := verifyObjective(&corpus, mask)
		if score.blockedAttack != corpus.attacks {
			continue
		}
		if !found || verifyBetter(score, best) {
			best = score
			found = true
		}
	}
	if !found {
		return ErrInfeasible
	}
	if candidate.Controls != best.mask {
		return ErrVerification
	}

	expected := Certificate{
		Schema:               SchemaVersion,
		TraceSetHash:         corpus.traceSetHash,
		CandidateHash:        canonicalCandidateHash(candidate),
		AttackTraceCount:     corpus.attacks,
		BlockedAttackCount:   best.blockedAttack,
		BenignWeightTotal:    corpus.benignWeight,
		BenignWeightRetained: best.retained,
		ControlCount:         best.controlCount,
		ControlCost:          best.controlCost,
	}
	if certificate != expected {
		return ErrVerification
	}
	return nil
}

func verifyObjective(
	corpus *preparedCorpus,
	mask ControlMask,
) objective {
	score := objective{
		controlCount: uint8(bits.OnesCount16(uint16(mask))),
		controlCost:  controlCost(mask),
		mask:         mask,
	}
	for index := range corpus.traces {
		trace := &corpus.traces[index]
		switch trace.trace.Class {
		case TraceAttack:
			reachable := false
			for _, source := range trace.trace.Sources {
				if verifySourceReachesSink(
					trace,
					trace.index[source],
					mask,
				) {
					reachable = true
					break
				}
			}
			if !reachable {
				score.blockedAttack++
			}
		case TraceBenign:
			retained := true
			for _, source := range trace.trace.Sources {
				if !verifySourceReachesSink(
					trace,
					trace.index[source],
					mask,
				) {
					retained = false
					break
				}
			}
			if retained {
				score.retained += uint64(trace.trace.Weight)
			}
		}
	}
	return score
}

// verifySourceReachesSink deliberately uses a LIFO traversal separate from
// the synthesizer's FIFO implementation.
func verifySourceReachesSink(
	trace *preparedTrace,
	source int,
	cut ControlMask,
) bool {
	seen := make([]bool, len(trace.trace.Nodes))
	stack := []int{source}
	seen[source] = true
	for len(stack) > 0 {
		last := len(stack) - 1
		index := stack[last]
		stack = stack[:last]
		if trace.sink[index] {
			return true
		}
		for _, edgeIndex := range trace.outgoing[index] {
			edge := trace.trace.Edges[edgeIndex]
			if edge.Controls&cut != 0 {
				continue
			}
			to := trace.index[edge.To]
			if !seen[to] {
				seen[to] = true
				stack = append(stack, to)
			}
		}
	}
	return false
}

func verifyBetter(candidate, incumbent objective) bool {
	if candidate.retained > incumbent.retained {
		return true
	}
	if candidate.retained < incumbent.retained {
		return false
	}
	if candidate.controlCount < incumbent.controlCount {
		return true
	}
	if candidate.controlCount > incumbent.controlCount {
		return false
	}
	if candidate.controlCost < incumbent.controlCost {
		return true
	}
	if candidate.controlCost > incumbent.controlCost {
		return false
	}
	return candidate.mask < incumbent.mask
}
