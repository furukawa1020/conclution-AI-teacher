package main

import (
	"net/http"
	"testing"
	"time"
)

func TestAPIServerKeepsBoundedDefaultsForOrdinaryRoutes(t *testing.T) {
	t.Parallel()
	server := newAPIServer(":0", http.NotFoundHandler())
	if server.ReadHeaderTimeout != 5*time.Second ||
		server.ReadTimeout != 120*time.Second ||
		server.WriteTimeout != 120*time.Second ||
		server.IdleTimeout != 120*time.Second {
		t.Fatalf("unexpected API server timeouts: %+v", server)
	}
}
