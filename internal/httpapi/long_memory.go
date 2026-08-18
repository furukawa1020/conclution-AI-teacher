package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/longmemory"
)

func (s *Server) memoryContextPreflight(w http.ResponseWriter, r *http.Request) {
	requireContentType := r.URL.Path == longTermMemoryContextConsumePath
	if r.Header.Get("Origin") != allowedWebOrigin ||
		r.Header.Get("Access-Control-Request-Method") != http.MethodPost ||
		!validMemoryContextPreflightHeaders(r.Header.Get("Access-Control-Request-Headers"), requireContentType) {
		writeProblem(w, http.StatusForbidden, "cross_site_request", "Cross-site writes are not allowed.")
		return
	}
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Firebase-AppCheck")
	w.Header().Set("Access-Control-Allow-Methods", http.MethodPost)
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

func validMemoryContextPreflightHeaders(value string, requireContentType bool) bool {
	required := map[string]bool{"authorization": false, "x-firebase-appcheck": false}
	if requireContentType {
		required["content-type"] = false
	}
	for _, raw := range strings.Split(value, ",") {
		header := strings.ToLower(strings.TrimSpace(raw))
		if header == "" {
			continue
		}
		if header != "authorization" && header != "content-type" && header != "x-firebase-appcheck" {
			return false
		}
		if _, needed := required[header]; needed {
			required[header] = true
		}
	}
	for _, present := range required {
		if !present {
			return false
		}
	}
	return true
}

const (
	longTermMemoryPath               = "/api/v1/conversation-memory"
	longTermMemoryContextBeginPath   = "/api/v1/conversation-memory/context:begin"
	longTermMemoryContextConsumePath = "/api/v1/conversation-memory/context:consume"
)

type longTermMemoryResponse struct {
	Enabled bool `json:"enabled"`
}

type longTermMemoryContextResponse struct {
	Available  bool   `json:"available"`
	Capability string `json:"capability,omitempty"`
}

type longTermMemoryContextConsumeRequest struct {
	Capability string `json:"capability"`
}

type longTermMemorySessionResponse struct {
	SessionContext   string `json:"sessionContext"`
	ExpiresInSeconds int64  `json:"expiresInSeconds"`
}

type longTermMemoryContextOutcome string

const (
	longTermMemoryContextIssued      longTermMemoryContextOutcome = "issued"
	longTermMemoryContextUnavailable longTermMemoryContextOutcome = "unavailable"
	longTermMemoryContextFailed      longTermMemoryContextOutcome = "failed"
	longTermMemoryContextReplay      longTermMemoryContextOutcome = "replay_rejected"
)

func (s *Server) consumeLongTermMemoryContext(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || s.sessionContext == nil || r.URL.RawQuery != "" || !isJSONContentType(r) {
		s.observeLongTermMemoryContext(r, longTermMemoryContextFailed)
		writeLongTermMemoryContextConsumeFailure(w, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4608)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body longTermMemoryContextConsumeRequest
	if decoder.Decode(&body) != nil || len(body.Capability) < 6 || len(body.Capability) > 4096 ||
		!errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		s.observeLongTermMemoryContext(r, longTermMemoryContextFailed)
		writeLongTermMemoryContextConsumeFailure(w, http.StatusBadRequest)
		return
	}
	sessionContext, expiresInSeconds, err := s.sessionContext.ConsumeSessionContext(
		r.Context(), principal.UID, principal.AppID, body.Capability,
	)
	if err != nil || sessionContext == "" || expiresInSeconds != int64(longmemory.SessionContextTTL/time.Second) {
		outcome := longTermMemoryContextFailed
		if errors.Is(err, longmemory.ErrReplay) {
			outcome = longTermMemoryContextReplay
		}
		s.observeLongTermMemoryContext(r, outcome)
		writeLongTermMemoryContextConsumeFailure(w, http.StatusConflict)
		return
	}
	if writeJSON(w, http.StatusOK, longTermMemorySessionResponse{
		SessionContext: sessionContext, ExpiresInSeconds: expiresInSeconds,
	}) != nil {
		s.observeLongTermMemoryContext(r, longTermMemoryContextFailed)
		return
	}
	s.observeLongTermMemoryContext(r, longTermMemoryContextIssued)
}

func (s *Server) beginLongTermMemoryContext(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || s.memoryContext == nil || r.URL.RawQuery != "" || !emptyJSONRequest(r) {
		s.observeLongTermMemoryContext(r, longTermMemoryContextFailed)
		writeLongTermMemoryContextFailure(w)
		return
	}
	capability, available, err := s.memoryContext.BeginContext(
		r.Context(),
		principal.UID,
		principal.AppID,
	)
	if err != nil || available != (capability != "") {
		s.observeLongTermMemoryContext(r, longTermMemoryContextFailed)
		writeLongTermMemoryContextFailure(w)
		return
	}
	if writeJSON(w, http.StatusOK, longTermMemoryContextResponse{
		Available:  available,
		Capability: capability,
	}) != nil {
		s.observeLongTermMemoryContext(r, longTermMemoryContextFailed)
		return
	}
	if available {
		s.observeLongTermMemoryContext(r, longTermMemoryContextIssued)
		return
	}
	s.observeLongTermMemoryContext(r, longTermMemoryContextUnavailable)
}

func (s *Server) observeLongTermMemoryContext(r *http.Request, outcome longTermMemoryContextOutcome) {
	if s.logger == nil {
		return
	}
	s.logger.InfoContext(r.Context(), "conversation memory context", "outcome", string(outcome))
}

func (s *Server) longTermMemoryStatus(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || s.longTermMemory == nil || r.URL.RawQuery != "" {
		writeLongTermMemoryFailure(w)
		return
	}
	consent, err := s.longTermMemory.Status(r.Context(), principal.UID)
	if err != nil {
		writeLongTermMemoryFailure(w)
		return
	}
	writeJSON(w, http.StatusOK, longTermMemoryResponse{Enabled: consent.Enabled})
}

func (s *Server) enableLongTermMemory(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || s.longTermMemory == nil || r.URL.RawQuery != "" || !isJSONContentType(r) {
		writeLongTermMemoryFailure(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64)
	var body struct {
		Enabled bool `json:"enabled"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || !body.Enabled || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		writeLongTermMemoryFailure(w)
		return
	}
	consent, err := s.longTermMemory.Enable(r.Context(), principal.UID)
	if err != nil || !consent.Enabled {
		writeLongTermMemoryFailure(w)
		return
	}
	writeJSON(w, http.StatusOK, longTermMemoryResponse{Enabled: true})
}

func (s *Server) disableLongTermMemory(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || s.longTermMemory == nil || r.URL.RawQuery != "" || !emptyJSONRequest(r) {
		writeLongTermMemoryFailure(w)
		return
	}
	if err := s.longTermMemory.DisableAndDelete(r.Context(), principal.UID); err != nil {
		writeLongTermMemoryFailure(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeLongTermMemoryFailure(w http.ResponseWriter) {
	writeProblem(w, http.StatusServiceUnavailable, "conversation_memory_management_failed", "Conversation memory management failed.")
}

func writeLongTermMemoryContextFailure(w http.ResponseWriter) {
	writeProblem(w, http.StatusServiceUnavailable, "conversation_memory_context_failed", "Conversation memory context could not be prepared.")
}

func writeLongTermMemoryContextConsumeFailure(w http.ResponseWriter, status int) {
	writeProblem(w, status, "conversation_memory_context_consume_failed", "Conversation memory context could not be consumed.")
}
