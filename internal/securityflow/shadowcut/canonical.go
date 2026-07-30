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
	nodes := make(map[NodeID]Node, len(trace.Nodes))
	incoming := make(map[NodeID]uint16, len(trace.Nodes))
	outgoing := make(map[NodeID]Edge, len(trace.Edges))
	sources := make(map[NodeID]struct{}, len(trace.Sources))
	for _, node := range trace.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range trace.Edges {
		incoming[edge.To]++
		outgoing[edge.From] = edge
	}
	for _, source := range trace.Sources {
		sources[source] = struct{}{}
	}

	// Validation restricts a trace to disjoint causal chains that meet only at
	// one terminal sink. Encoding each root-to-sink chain and sorting the
	// resulting bytes therefore gives an exact structural normal form without
	// raw NodeID or BindingRef values.
	paths := make([][]byte, 0, len(trace.Sources)+1)
	for _, node := range trace.Nodes {
		if incoming[node.ID] != 0 {
			continue
		}
		path := []byte{0x50, 0x41, 0x54, 0x48}
		if _, declared := sources[node.ID]; declared {
			path = append(path, 1)
		} else {
			path = append(path, 0)
		}
		current := node.ID
		for {
			currentNode := nodes[current]
			path = appendCanonicalNode(path, currentNode)
			edge, exists := outgoing[current]
			if !exists {
				break
			}
			path = appendCanonicalEdge(path, edge)
			current = edge.To
		}
		paths = append(paths, path)
	}
	sort.Slice(paths, func(left, right int) bool {
		return bytes.Compare(paths[left], paths[right]) < 0
	})

	canonical := []byte{0x54, 0x52, 0x43, 0x45}
	canonical = append(canonical, byte(trace.Class))
	canonical = binary.BigEndian.AppendUint32(canonical, trace.Weight)
	canonical = binary.BigEndian.AppendUint16(
		canonical,
		uint16(len(trace.Nodes)),
	)
	canonical = binary.BigEndian.AppendUint16(
		canonical,
		uint16(len(trace.Edges)),
	)
	canonical = binary.BigEndian.AppendUint16(
		canonical,
		uint16(len(trace.Sources)),
	)
	canonical = binary.BigEndian.AppendUint16(
		canonical,
		uint16(len(paths)),
	)
	for _, path := range paths {
		canonical = binary.BigEndian.AppendUint32(
			canonical,
			uint32(len(path)),
		)
		canonical = append(canonical, path...)
	}
	return canonical
}

func appendCanonicalNode(encoded []byte, node Node) []byte {
	encoded = append(
		encoded,
		0x4e,
		byte(node.Kind),
		byte(node.Integrity),
	)
	encoded = binary.BigEndian.AppendUint16(
		encoded,
		uint16(node.Authority),
	)
	encoded = binary.BigEndian.AppendUint16(
		encoded,
		uint16(node.Requires),
	)
	encoded = append(encoded, byte(node.Binding))
	if node.BindingRef == 0 {
		return append(encoded, 0)
	}
	return append(encoded, 1)
}

func appendCanonicalEdge(encoded []byte, edge Edge) []byte {
	encoded = append(encoded, 0x45, byte(edge.Kind))
	return binary.BigEndian.AppendUint16(
		encoded,
		uint16(edge.Controls),
	)
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
