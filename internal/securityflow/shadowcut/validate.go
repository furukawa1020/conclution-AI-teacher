package shadowcut

type preparedTrace struct {
	trace    Trace
	index    map[NodeID]int
	incoming [][]int
	outgoing [][]int
	sink     []bool
}

type preparedCorpus struct {
	traces       []preparedTrace
	traceSetHash [32]byte
	attacks      uint32
	benignWeight uint64
}

func prepareCorpus(traces []Trace) (preparedCorpus, error) {
	if len(traces) == 0 || len(traces) > MaxTraces {
		return preparedCorpus{}, ErrInvalidCorpus
	}

	prepared := preparedCorpus{
		traces: make([]preparedTrace, 0, len(traces)),
	}
	for _, trace := range traces {
		item, err := prepareTrace(cloneTrace(trace))
		if err != nil {
			return preparedCorpus{}, err
		}
		prepared.traces = append(prepared.traces, item)
		switch trace.Class {
		case TraceAttack:
			prepared.attacks++
		case TraceBenign:
			prepared.benignWeight += uint64(trace.Weight)
		default:
			return preparedCorpus{}, ErrInvalidCorpus
		}
	}
	if prepared.attacks == 0 || prepared.benignWeight == 0 {
		return preparedCorpus{}, ErrInvalidCorpus
	}

	hash, err := canonicalTraceSetHash(prepared.traces)
	if err != nil {
		return preparedCorpus{}, err
	}
	prepared.traceSetHash = hash
	return prepared, nil
}

func cloneTrace(trace Trace) Trace {
	cloned := trace
	cloned.Nodes = append([]Node(nil), trace.Nodes...)
	cloned.Edges = append([]Edge(nil), trace.Edges...)
	cloned.Sources = append([]NodeID(nil), trace.Sources...)
	cloned.Sinks = append([]NodeID(nil), trace.Sinks...)
	return cloned
}

func prepareTrace(trace Trace) (preparedTrace, error) {
	if trace.Class <= TraceUnknown || trace.Class >= traceClassLimit ||
		trace.Weight == 0 ||
		len(trace.Nodes) == 0 ||
		len(trace.Nodes) > MaxNodesPerTrace ||
		len(trace.Edges) == 0 ||
		len(trace.Edges) > MaxEdgesPerTrace ||
		len(trace.Sources) == 0 ||
		len(trace.Sources) > MaxSourcesPerTrace ||
		len(trace.Sinks) == 0 ||
		len(trace.Sinks) > MaxSinksPerTrace {
		return preparedTrace{}, ErrInvalidTrace
	}

	prepared := preparedTrace{
		trace:    trace,
		index:    make(map[NodeID]int, len(trace.Nodes)),
		incoming: make([][]int, len(trace.Nodes)),
		outgoing: make([][]int, len(trace.Nodes)),
		sink:     make([]bool, len(trace.Nodes)),
	}
	for index, node := range trace.Nodes {
		if _, exists := prepared.index[node.ID]; exists ||
			!validNode(node) {
			return preparedTrace{}, ErrInvalidTrace
		}
		prepared.index[node.ID] = index
	}

	type edgeKey struct {
		from NodeID
		to   NodeID
		kind EdgeKind
	}
	edges := make(map[edgeKey]struct{}, len(trace.Edges))
	for edgeIndex, edge := range trace.Edges {
		fromIndex, fromExists := prepared.index[edge.From]
		toIndex, toExists := prepared.index[edge.To]
		key := edgeKey{from: edge.From, to: edge.To, kind: edge.Kind}
		_, duplicate := edges[key]
		if !fromExists || !toExists ||
			edge.From == edge.To ||
			edge.Kind <= EdgeUnknown || edge.Kind >= edgeKindLimit ||
			!validControlMask(edge.Controls) ||
			duplicate ||
			!validEdgeKinds(
				trace.Nodes[fromIndex].Kind,
				trace.Nodes[toIndex].Kind,
				edge.Kind,
			) {
			return preparedTrace{}, ErrInvalidTrace
		}
		edges[key] = struct{}{}
		prepared.outgoing[fromIndex] = append(
			prepared.outgoing[fromIndex],
			edgeIndex,
		)
		prepared.incoming[toIndex] = append(
			prepared.incoming[toIndex],
			edgeIndex,
		)
	}

	if !validEndpoints(&prepared) ||
		!acyclic(&prepared) ||
		!allNodesReachSink(&prepared) ||
		!allSourcesReachSink(&prepared, 0) ||
		!validAuthorityFlow(&prepared) {
		return preparedTrace{}, ErrInvalidTrace
	}
	return prepared, nil
}

func validNode(node Node) bool {
	if node.Kind <= NodeUnknown || node.Kind >= nodeKindLimit ||
		node.Integrity <= IntegrityUnknown ||
		node.Integrity >= integrityLimit ||
		node.Binding >= bindingLimit ||
		node.Authority&^allAuthorities != 0 ||
		node.Requires&^allAuthorities != 0 {
		return false
	}

	switch node.Kind {
	case NodeAuthenticatedIntent:
		return node.Integrity == IntegrityAuthenticated &&
			node.Authority != 0 &&
			node.Requires == 0 &&
			node.Binding == BindingExactScopeActionArgs
	case NodeAmbient, NodePDF, NodeWeb, NodeModel, NodeTool, NodeMemory:
		return node.Integrity == IntegrityUntrusted &&
			node.Authority == 0 &&
			node.Requires == 0 &&
			node.Binding == BindingNone
	case NodeGrant:
		return node.Integrity == IntegritySystem &&
			node.Authority != 0 &&
			node.Requires == 0 &&
			node.Binding == BindingExactScopeActionArgs
	case NodeSink:
		if node.Requires == 0 {
			return node.Authority == 0 && node.Binding == BindingNone
		}
		return node.Authority == node.Requires &&
			node.Binding == BindingExactScopeActionArgs
	default:
		return false
	}
}

func validEdgeKinds(from NodeKind, to NodeKind, kind EdgeKind) bool {
	switch kind {
	case EdgeIssueGrant:
		return from == NodeAuthenticatedIntent && to == NodeGrant
	case EdgeConsume:
		return from == NodeGrant && to == NodeSink
	case EdgeRecall:
		return from == NodeMemory && to != NodeGrant
	case EdgeExecute, EdgeRespond:
		return to == NodeSink && from != NodeGrant
	default:
		return from != NodeGrant && to != NodeGrant
	}
}

func validEndpoints(trace *preparedTrace) bool {
	sourceSeen := make(map[NodeID]struct{}, len(trace.trace.Sources))
	for _, source := range trace.trace.Sources {
		index, exists := trace.index[source]
		if !exists || len(trace.incoming[index]) != 0 {
			return false
		}
		if _, duplicate := sourceSeen[source]; duplicate {
			return false
		}
		sourceSeen[source] = struct{}{}
		if trace.trace.Class == TraceAttack &&
			trace.trace.Nodes[index].Authority != 0 {
			return false
		}
	}

	sinkSeen := make(map[NodeID]struct{}, len(trace.trace.Sinks))
	for _, sink := range trace.trace.Sinks {
		index, exists := trace.index[sink]
		if !exists ||
			trace.trace.Nodes[index].Kind != NodeSink ||
			len(trace.outgoing[index]) != 0 {
			return false
		}
		if _, duplicate := sinkSeen[sink]; duplicate {
			return false
		}
		sinkSeen[sink] = struct{}{}
		trace.sink[index] = true
	}

	for index, incoming := range trace.incoming {
		if len(incoming) != 0 {
			continue
		}
		node := trace.trace.Nodes[index]
		if _, declared := sourceSeen[node.ID]; !declared &&
			node.Kind != NodeAuthenticatedIntent {
			return false
		}
	}
	return true
}

func acyclic(trace *preparedTrace) bool {
	degree := make([]int, len(trace.incoming))
	queue := make([]int, 0, len(degree))
	for index := range degree {
		degree[index] = len(trace.incoming[index])
		if degree[index] == 0 {
			queue = append(queue, index)
		}
	}

	visited := 0
	for len(queue) > 0 {
		index := queue[0]
		queue = queue[1:]
		visited++
		for _, edgeIndex := range trace.outgoing[index] {
			to := trace.index[trace.trace.Edges[edgeIndex].To]
			degree[to]--
			if degree[to] == 0 {
				queue = append(queue, to)
			}
		}
	}
	return visited == len(trace.trace.Nodes)
}

func allNodesReachSink(trace *preparedTrace) bool {
	reached := make([]bool, len(trace.trace.Nodes))
	queue := make([]int, 0, len(trace.trace.Sinks))
	for index, isSink := range trace.sink {
		if isSink {
			reached[index] = true
			queue = append(queue, index)
		}
	}
	for len(queue) > 0 {
		index := queue[0]
		queue = queue[1:]
		for _, edgeIndex := range trace.incoming[index] {
			from := trace.index[trace.trace.Edges[edgeIndex].From]
			if !reached[from] {
				reached[from] = true
				queue = append(queue, from)
			}
		}
	}
	for _, value := range reached {
		if !value {
			return false
		}
	}
	return true
}

func allSourcesReachSink(trace *preparedTrace, cut ControlMask) bool {
	for _, source := range trace.trace.Sources {
		if !sourceReachesSink(trace, trace.index[source], cut) {
			return false
		}
	}
	return true
}

func sourceReachesSink(
	trace *preparedTrace,
	source int,
	cut ControlMask,
) bool {
	seen := make([]bool, len(trace.trace.Nodes))
	queue := []int{source}
	seen[source] = true
	for len(queue) > 0 {
		index := queue[0]
		queue = queue[1:]
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
				queue = append(queue, to)
			}
		}
	}
	return false
}

func validAuthorityFlow(trace *preparedTrace) bool {
	for index, node := range trace.trace.Nodes {
		incoming := trace.incoming[index]
		if node.Kind == NodeAuthenticatedIntent &&
			len(incoming) != 0 {
			return false
		}

		if node.Kind == NodeGrant {
			if len(incoming) != 1 {
				return false
			}
			edge := trace.trace.Edges[incoming[0]]
			from := trace.trace.Nodes[trace.index[edge.From]]
			if edge.Kind != EdgeIssueGrant ||
				from.Kind != NodeAuthenticatedIntent ||
				from.Binding != BindingExactScopeActionArgs ||
				node.Authority&^from.Authority != 0 {
				return false
			}
			continue
		}

		if len(incoming) == 0 {
			if node.Kind != NodeAuthenticatedIntent &&
				node.Authority != 0 {
				return false
			}
			continue
		}

		var inherited Authority
		minimumIntegrity := IntegritySystem
		for _, edgeIndex := range incoming {
			edge := trace.trace.Edges[edgeIndex]
			parent := trace.trace.Nodes[trace.index[edge.From]]
			inherited |= parent.Authority
			if parent.Integrity < minimumIntegrity {
				minimumIntegrity = parent.Integrity
			}
		}
		if node.Authority&^inherited != 0 ||
			node.Integrity > minimumIntegrity {
			return false
		}

		if node.Kind == NodeSink && node.Requires != 0 {
			authorized := false
			for _, edgeIndex := range incoming {
				edge := trace.trace.Edges[edgeIndex]
				if edge.Kind != EdgeConsume {
					continue
				}
				grant := trace.trace.Nodes[trace.index[edge.From]]
				if grant.Kind == NodeGrant &&
					grant.Binding == BindingExactScopeActionArgs &&
					node.Requires&^grant.Authority == 0 {
					authorized = true
					break
				}
			}
			if !authorized {
				return false
			}
		}
	}
	return true
}
