package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const longTermMemoryPath = "/api/v1/conversation-memory"

type longTermMemoryResponse struct {
	Enabled bool `json:"enabled"`
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
