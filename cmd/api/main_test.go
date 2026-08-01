package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
)

func TestAPIServerTimeoutsCoverBoundedLiveVoiceConnection(t *testing.T) {
	t.Parallel()
	server := newAPIServer(":0", http.NotFoundHandler())
	if server.ReadHeaderTimeout != 5*time.Second ||
		server.ReadTimeout != httpapi.VoiceLiveConnectionTimeout ||
		server.WriteTimeout != httpapi.VoiceLiveConnectionTimeout ||
		server.IdleTimeout != httpapi.VoiceLiveConnectionTimeout {
		t.Fatalf("unexpected API server timeouts: %+v", server)
	}
}
