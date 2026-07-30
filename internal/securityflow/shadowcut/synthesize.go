package shadowcut

import "math/bits"

type objective struct {
	blockedAttack uint32
	retained      uint64
	controlCount  uint8
	controlCost   uint32
	mask          ControlMask
}

// Synthesize exhaustively searches the complete finite control space. The
// result is shadow-only and cannot mutate or authorize production behavior.
func Synthesize(traces []Trace) (Candidate, Certificate, error) {
	corpus, err := prepareCorpus(traces)
	if err != nil {
		return Candidate{}, Certificate{}, err
	}
	if int(controlLimit-1) > MaxControls {
		return Candidate{}, Certificate{}, ErrInvalidCorpus
	}

	var best objective
	found := false
	for raw := uint32(0); raw <= uint32(knownControlMask); raw++ {
		mask := ControlMask(raw)
		score := synthesizeObjective(&corpus, mask)
		if score.blockedAttack != corpus.attacks {
			continue
		}
		if !found || betterObjective(score, best) {
			best = score
			found = true
		}
	}
	if !found {
		return Candidate{}, Certificate{}, ErrInfeasible
	}

	candidate := Candidate{
		Schema:   SchemaVersion,
		Controls: best.mask,
	}
	certificate := Certificate{
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
	return candidate, certificate, nil
}

func synthesizeObjective(
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
			if !anySourceReachesSink(trace, mask) {
				score.blockedAttack++
			}
		case TraceBenign:
			if allSourcesReachSink(trace, mask) {
				score.retained += uint64(trace.trace.Weight)
			}
		}
	}
	return score
}

func anySourceReachesSink(
	trace *preparedTrace,
	cut ControlMask,
) bool {
	for _, source := range trace.trace.Sources {
		if sourceReachesSink(trace, trace.index[source], cut) {
			return true
		}
	}
	return false
}

func betterObjective(candidate, incumbent objective) bool {
	switch {
	case candidate.retained != incumbent.retained:
		return candidate.retained > incumbent.retained
	case candidate.controlCount != incumbent.controlCount:
		return candidate.controlCount < incumbent.controlCount
	case candidate.controlCost != incumbent.controlCost:
		return candidate.controlCost < incumbent.controlCost
	default:
		return candidate.mask < incumbent.mask
	}
}
