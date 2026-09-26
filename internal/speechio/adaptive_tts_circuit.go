package speechio

import (
	"sync"
	"time"
)

const directPCMFailureCooldown = 5 * time.Minute

// directPCMRouteCircuit is content-free instance-local transport state. One
// request may probe the unary PCM route; peers immediately use streaming. A
// provider-shape failure opens a bounded cooldown instead of charging every
// short reply for the same failed round trip.
type directPCMRouteCircuit struct {
	mu           sync.Mutex
	probing      bool
	blockedUntil time.Time
}

func (circuit *directPCMRouteCircuit) begin(now time.Time) bool {
	circuit.mu.Lock()
	defer circuit.mu.Unlock()
	if circuit.probing || now.Before(circuit.blockedUntil) {
		return false
	}
	circuit.probing = true
	return true
}

func (circuit *directPCMRouteCircuit) succeeded() {
	circuit.mu.Lock()
	circuit.probing = false
	circuit.blockedUntil = time.Time{}
	circuit.mu.Unlock()
}

func (circuit *directPCMRouteCircuit) failed(now time.Time) {
	circuit.mu.Lock()
	circuit.probing = false
	circuit.blockedUntil = now.Add(directPCMFailureCooldown)
	circuit.mu.Unlock()
}

func (circuit *directPCMRouteCircuit) canceled() {
	circuit.mu.Lock()
	circuit.probing = false
	circuit.mu.Unlock()
}
