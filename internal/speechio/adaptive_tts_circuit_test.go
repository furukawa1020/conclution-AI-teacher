package speechio

import (
	"testing"
	"time"
)

func TestDirectPCMRouteCircuitRetriesOnlyAfterCooldown(t *testing.T) {
	t.Parallel()

	var circuit directPCMRouteCircuit
	now := time.Unix(1_700_000_000, 0)
	if !circuit.begin(now) {
		t.Fatal("initial direct PCM probe was blocked")
	}
	if circuit.begin(now) {
		t.Fatal("concurrent direct PCM probe was admitted")
	}
	circuit.failed(now)
	if circuit.begin(now.Add(directPCMFailureCooldown - time.Nanosecond)) {
		t.Fatal("direct PCM probe escaped the failure cooldown")
	}
	if !circuit.begin(now.Add(directPCMFailureCooldown)) {
		t.Fatal("direct PCM probe did not recover after cooldown")
	}
	circuit.succeeded()
	if !circuit.begin(now.Add(directPCMFailureCooldown)) {
		t.Fatal("successful probe did not close the circuit")
	}
	circuit.canceled()
	if !circuit.begin(now.Add(directPCMFailureCooldown)) {
		t.Fatal("canceled probe was treated as provider failure")
	}
}
