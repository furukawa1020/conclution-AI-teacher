package speechio

import (
	"testing"
)

func TestDirectPCMRouteCircuitDoesNotRetryFailedRouteInSameInstance(t *testing.T) {
	t.Parallel()

	var circuit directPCMRouteCircuit
	if !circuit.begin() {
		t.Fatal("initial direct PCM probe was blocked")
	}
	if circuit.begin() {
		t.Fatal("concurrent direct PCM probe was admitted")
	}
	circuit.failed()
	for range 10_000 {
		if circuit.begin() {
			t.Fatal("failed direct PCM route was retried in the same instance")
		}
	}

	var freshInstance directPCMRouteCircuit
	if !freshInstance.begin() {
		t.Fatal("fresh instance did not receive one startup probe")
	}
	freshInstance.succeeded()
	if !freshInstance.begin() {
		t.Fatal("successful probe did not close the circuit")
	}
	freshInstance.canceled()
	if !freshInstance.begin() {
		t.Fatal("canceled probe was treated as provider failure")
	}
	freshInstance.canceled()
}
