package speechio

import "sync"

// directPCMRouteCircuit is content-free instance-local transport state. One
// request may probe the unary PCM route; peers immediately use streaming. A
// provider-shape failure disables that optional route for this instance. A
// fresh instance probes during its traffic-free startup warmup, so no user
// turn is charged for periodic recovery probes.
type directPCMRouteCircuit struct {
	mu       sync.Mutex
	probing  bool
	disabled bool
}

func (circuit *directPCMRouteCircuit) begin() bool {
	circuit.mu.Lock()
	defer circuit.mu.Unlock()
	if circuit.probing || circuit.disabled {
		return false
	}
	circuit.probing = true
	return true
}

func (circuit *directPCMRouteCircuit) succeeded() {
	circuit.mu.Lock()
	circuit.probing = false
	circuit.disabled = false
	circuit.mu.Unlock()
}

func (circuit *directPCMRouteCircuit) failed() {
	circuit.mu.Lock()
	circuit.probing = false
	circuit.disabled = true
	circuit.mu.Unlock()
}

func (circuit *directPCMRouteCircuit) canceled() {
	circuit.mu.Lock()
	circuit.probing = false
	circuit.mu.Unlock()
}
