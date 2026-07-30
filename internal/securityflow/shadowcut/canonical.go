package shadowcut

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

func canonicalTraceSetHash(traces []preparedTrace) ([32]byte, error) {
	encoded := make([][]byte, 0, len(traces))
	for _, trace := range traces {
		encoded = append(encoded, canonicalTraceBytes(trace.trace))
	}
	sort.Slice(encoded, func(left, right int) bool {
		return bytes.Compare(encoded[left], encoded[right]) < 0
	})

	// The prefix and every multibyte integer are fixed-width and big-endian.
	canonical := []byte{0x4b, 0x53, 0x43, 0x54}
	canonical = binary.BigEndian.AppendUint16(canonical, SchemaVersion)
	canonical = binary.BigEndian.AppendUint32(
		canonical,
		uint32(len(encoded)),
	)
	for _, trace := range encoded {
		canonical = binary.BigEndian.AppendUint32(
			canonical,
			uint32(len(trace)),
		)
		canonical = append(canonical, trace...)
	}
	return sha256.Sum256(canonical), nil
}

func canonicalTraceBytes(trace Trace) []byte {
	nodes := append([]Node(nil), trace.Nodes...)
	edges := append([]Edge(nil), trace.Edges...)
	sources := append([]NodeID(nil), trace.Sources...)
	sinks := append([]NodeID(nil), trace.Sinks...)

	sort.Slice(nodes, func(left, right int) bool {
		return nodes[left].ID < nodes[right].ID
	})
	sort.Slice(edges, func(left, right int) bool {
		a, b := edges[left], edges[right]
		switch {
		case a.From != b.From:
			return a.From < b.From
		case a.To != b.To:
			return a.To < b.To
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		default:
			return a.Controls < b.Controls
		}
	})
	sort.Slice(sources, func(left, right int) bool {
		return sources[left] < sources[right]
	})
	sort.Slice(sinks, func(left, right int) bool {
		return sinks[left] < sinks[right]
	})

	canonical := []byte{0x54, 0x52, 0x43, 0x45}
	canonical = append(canonical, byte(trace.Class))
	canonical = binary.BigEndian.AppendUint32(canonical, trace.Weight)
	canonical = binary.BigEndian.AppendUint16(
		canonical,
		uint16(len(nodes)),
	)
	for _, node := range nodes {
		canonical = binary.BigEndian.AppendUint16(
			canonical,
			uint16(node.ID),
		)
		canonical = append(
			canonical,
			byte(node.Kind),
			byte(node.Integrity),
		)
		canonical = binary.BigEndian.AppendUint16(
			canonical,
			uint16(node.Authority),
		)
		canonical = binary.BigEndian.AppendUint16(
			canonical,
			uint16(node.Requires),
		)
		canonical = append(canonical, byte(node.Binding))
		canonical = binary.BigEndian.AppendUint16(
			canonical,
			uint16(node.BindingRef),
		)
	}

	canonical = binary.BigEndian.AppendUint16(
		canonical,
		uint16(len(edges)),
	)
	for _, edge := range edges {
		canonical = binary.BigEndian.AppendUint16(
			canonical,
			uint16(edge.From),
		)
		canonical = binary.BigEndian.AppendUint16(
			canonical,
			uint16(edge.To),
		)
		canonical = append(canonical, byte(edge.Kind))
		canonical = binary.BigEndian.AppendUint16(
			canonical,
			uint16(edge.Controls),
		)
	}

	canonical = binary.BigEndian.AppendUint16(
		canonical,
		uint16(len(sources)),
	)
	for _, source := range sources {
		canonical = binary.BigEndian.AppendUint16(
			canonical,
			uint16(source),
		)
	}
	canonical = binary.BigEndian.AppendUint16(
		canonical,
		uint16(len(sinks)),
	)
	for _, sink := range sinks {
		canonical = binary.BigEndian.AppendUint16(
			canonical,
			uint16(sink),
		)
	}
	return canonical
}

func canonicalCandidateHash(candidate Candidate) [32]byte {
	canonical := []byte{0x4b, 0x53, 0x43, 0x50}
	canonical = binary.BigEndian.AppendUint16(
		canonical,
		candidate.Schema,
	)
	canonical = binary.BigEndian.AppendUint16(
		canonical,
		uint16(candidate.Controls),
	)
	return sha256.Sum256(canonical)
}
