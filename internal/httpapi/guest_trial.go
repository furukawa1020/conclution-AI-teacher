package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
)

const guestTrialControlPath = "/api/v1/guest-trials/control"

func (s *Server) guestTrialPreflight(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != allowedWebOrigin || r.Header.Get("Access-Control-Request-Method") != http.MethodPost ||
		!validMemoryContextPreflightHeaders(r.Header.Get("Access-Control-Request-Headers"), true) {
		writeProblem(w, http.StatusForbidden, "cross_site_request", "Cross-site writes are not allowed.")
		return
	}
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Firebase-AppCheck")
	w.Header().Set("Access-Control-Allow-Methods", http.MethodPost)
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) guestTrialControl(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || !principal.IsGuest() {
		writeProblem(w, http.StatusForbidden, "guest_trial_forbidden", "A guest session is required.")
		return
	}
	if s.voice.GuestTrials == nil {
		writeProblem(w, http.StatusServiceUnavailable, "guest_trial_unavailable", "Trial control is unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if !s.consumeVoiceQuota(w, ctx, principal, time.Now().UTC()) {
		return
	}
	if r.URL.RawQuery != "" || !isJSONContentType(r) {
		writeProblem(w, http.StatusBadRequest, "guest_trial_invalid", "Invalid trial control.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Action     string `json:"action"`
		ID         string `json:"id"`
		Generation int64  `json:"generation"`
	}
	if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		writeProblem(w, http.StatusBadRequest, "guest_trial_invalid", "Invalid trial control.")
		return
	}
	owner := guesttrial.Owner{UID: principal.UID, AppID: principal.AppID}
	var result guesttrial.Snapshot
	var err error
	switch input.Action {
	case "begin":
		if input.ID != "" || input.Generation != 0 {
			err = guesttrial.ErrInvalid
			break
		}
		result, err = s.voice.GuestTrials.Begin(ctx, owner)
	case "read":
		if input.Generation != 0 {
			err = guesttrial.ErrInvalid
			break
		}
		result, err = s.voice.GuestTrials.Read(ctx, owner, input.ID)
	case "help", "stop":
		result, err = s.voice.GuestTrials.Update(ctx, owner, input.ID, input.Generation, input.Action)
	default:
		err = guesttrial.ErrInvalid
	}
	if err != nil {
		code, kind := http.StatusServiceUnavailable, "guest_trial_unavailable"
		switch {
		case errors.Is(err, guesttrial.ErrInvalid):
			code, kind = http.StatusBadRequest, "guest_trial_invalid"
		case errors.Is(err, guesttrial.ErrNotFound):
			code, kind = http.StatusNotFound, "guest_trial_not_found"
		case errors.Is(err, guesttrial.ErrConflict):
			code, kind = http.StatusConflict, "guest_trial_conflict"
		case errors.Is(err, guesttrial.ErrExpired):
			code, kind = http.StatusGone, "guest_trial_expired"
		case errors.Is(err, guesttrial.ErrCapacity):
			code, kind = http.StatusTooManyRequests, "guest_trial_capacity"
		}
		writeProblem(w, code, kind, "The trial operation was not accepted.")
		return
	}
	_ = writeJSON(w, http.StatusOK, result)
}
