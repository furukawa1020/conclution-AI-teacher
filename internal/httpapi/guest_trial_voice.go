package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
)

const guestTrialVoicePath = "/api/v1/guest-trials/voice"
const guestTrialMaxPCMBytes = 30 * 16000 * 2
const guestTrialMaxRequestBytes = guestTrialMaxPCMBytes*4/3 + maxStateBytes + 1024

// The capability intentionally has no ordinary dialogue, native session or TTS
// method. Identity comes exclusively from the authenticated request context.
type GuestTrialVoiceService interface {
	ProcessPCM(context.Context, guesttrial.Owner, string, int64, string, []byte) (guesttrial.Snapshot, error)
}

type guestTrialVoiceInput struct {
	ID         string
	Generation int64
	State      string
	PCM        []byte
}

// The wire format is finalized 16kHz mono signed little-endian PCM16 in base64.
// A token-by-token decoder rejects duplicates, unknown fields and nulls rather
// than allowing later JSON fields to silently overwrite earlier ones.
func decodeGuestTrialVoice(r io.Reader, in *guestTrialVoiceInput) error {
	d := json.NewDecoder(r)
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return guesttrial.ErrInvalid
	}
	seen := make(map[string]bool, 4)
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return guesttrial.ErrInvalid
		}
		seen[key] = true
		switch key {
		case "id":
			err = d.Decode(&in.ID)
		case "generation":
			err = d.Decode(&in.Generation)
		case "state":
			err = d.Decode(&in.State)
		case "pcm16":
			var encoded string
			err = d.Decode(&encoded)
			if err == nil {
				in.PCM, err = base64.StdEncoding.Strict().DecodeString(encoded)
			}
		default:
			return guesttrial.ErrInvalid
		}
		if err != nil {
			return guesttrial.ErrInvalid
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return guesttrial.ErrInvalid
	}
	if !errors.Is(d.Decode(&struct{}{}), io.EOF) {
		return guesttrial.ErrInvalid
	}
	id, err := hex.DecodeString(in.ID)
	if len(seen) != 4 || err != nil || len(id) != 32 || hex.EncodeToString(id) != in.ID ||
		in.Generation < 1 || in.Generation > 2 || strings.TrimSpace(in.State) == "" || len(in.State) > maxStateBytes ||
		len(in.PCM) < 2 || len(in.PCM)%2 != 0 || len(in.PCM) > guestTrialMaxPCMBytes {
		return guesttrial.ErrInvalid
	}
	return nil
}

func (s *Server) guestTrialVoice(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok || !principal.IsGuest() {
		writeProblem(w, http.StatusForbidden, "guest_trial_forbidden", "A guest session is required.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if !s.consumeVoiceQuota(w, ctx, principal, time.Now().UTC()) {
		return
	}
	if r.URL.RawQuery != "" || !isJSONContentType(r) || r.Header.Get("Content-Encoding") != "" {
		writeGuestTrialVoiceError(w, guesttrial.ErrInvalid)
		return
	}
	if err := guestTrialBodyContextError(ctx); err != nil {
		writeGuestTrialVoiceError(w, err)
		return
	}
	releaseBodyDeadline := boundGuestTrialBody(ctx, w)
	defer releaseBodyDeadline()
	r.Body = http.MaxBytesReader(w, r.Body, guestTrialMaxRequestBytes)
	var input guestTrialVoiceInput
	// Clear even partial decodes and errors; the pipeline also consumes its PCM.
	defer func() { clear(input.PCM) }()
	if err := decodeGuestTrialVoice(r.Body, &input); err != nil {
		if contextErr := guestTrialBodyContextError(ctx); contextErr != nil {
			err = contextErr
		}
		writeGuestTrialVoiceError(w, err)
		return
	}
	if err := ctx.Err(); err != nil {
		writeGuestTrialVoiceError(w, err)
		return
	}
	ctx, timings := guesttrial.WithTimings(ctx)
	result, err := s.voice.GuestTrialVoice.ProcessPCM(ctx,
		guesttrial.Owner{UID: principal.UID, AppID: principal.AppID}, input.ID, input.Generation, input.State, input.PCM)
	if value := timings.ServerTiming(); value != "" {
		w.Header().Set("Server-Timing", value)
		w.Header().Set("Access-Control-Expose-Headers", "Server-Timing")
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		writeGuestTrialVoiceError(w, err)
		return
	}
	// No caption, model output, replacement answer or response audio is exposed.
	_ = writeJSON(w, http.StatusOK, result)
}

func writeGuestTrialVoiceError(w http.ResponseWriter, err error) {
	status, kind := http.StatusServiceUnavailable, "guest_trial_unavailable"
	switch {
	case errors.Is(err, guesttrial.ErrInvalid):
		status, kind = http.StatusBadRequest, "guest_trial_invalid"
	case errors.Is(err, guesttrial.ErrNotFound):
		status, kind = http.StatusNotFound, "guest_trial_not_found"
	case errors.Is(err, guesttrial.ErrConflict):
		status, kind = http.StatusConflict, "guest_trial_conflict"
	case errors.Is(err, guesttrial.ErrExpired):
		status, kind = http.StatusGone, "guest_trial_expired"
	case errors.Is(err, guesttrial.ErrCapacity):
		status, kind = http.StatusTooManyRequests, "guest_trial_capacity"
	case errors.Is(err, conversation.ErrInvalidStateToken), errors.Is(err, conversation.ErrExpiredStateToken):
		status, kind = http.StatusConflict, "guest_trial_state_invalid"
	case errors.Is(err, ErrVoiceNotRecognized):
		status, kind = http.StatusUnprocessableEntity, "guest_trial_not_recognized"
	case errors.Is(err, context.Canceled):
		status, kind = http.StatusRequestTimeout, "guest_trial_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		status, kind = http.StatusGatewayTimeout, "guest_trial_timeout"
	}
	writeProblem(w, status, kind, "The trial voice operation was not completed.")
}
