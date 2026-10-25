package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
)

func TestGuestTrialAvailabilityRequiresCompleteAuthenticatedGuestComposition(t *testing.T) {
	voice := trialVoiceFunc(func(context.Context, guesttrial.Owner, string, int64, string, []byte) (guesttrial.Snapshot, error) {
		t.Fatal("availability invoked speech")
		return guesttrial.Snapshot{}, nil
	})
	issuer := trialStateFunc(func(guesttrial.Owner, guesttrial.Snapshot) (string, error) {
		t.Fatal("availability issued state")
		return "", nil
	})
	for _, tc := range []struct {
		name      string
		configure func(*VoiceOptions)
		principal identity.Principal
		want      int
	}{
		{"complete", nil, trialGuest(), 204},
		{"no state", func(o *VoiceOptions) { o.GuestTrialState = nil }, trialGuest(), 404},
		{"no voice", func(o *VoiceOptions) { o.GuestTrialVoice = nil }, trialGuest(), 404},
		{"no control quota", func(o *VoiceOptions) { o.GuestTrialControlLimiter = nil }, trialGuest(), 404},
		{"account", nil, identity.Principal{UID: "account", AppID: "app", Provider: "custom", AuthMethod: "passkey-v1", AccountVerified: true}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := trialHandlerWithOptions(t, true, tc.principal, voice, issuer, tc.configure)
			r := authenticatedRequest(http.MethodGet, guestTrialAvailabilityPath, "")
			r.Header.Set("Origin", allowedWebOrigin)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Code == 204 && (w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store") {
				t.Fatal("availability leaked or cached data")
			}
			if w.Code == 204 && w.Header().Get("Access-Control-Allow-Origin") != allowedWebOrigin {
				t.Fatal("CORS missing")
			}
			if tc.want == 204 {
				for _, origin := range []string{allowedWebOrigin, "https://foreign.invalid"} {
					p := httptest.NewRequest(http.MethodOptions, guestTrialAvailabilityPath, nil)
					p.Header.Set("Origin", origin)
					p.Header.Set("Access-Control-Request-Method", "GET")
					p.Header.Set("Access-Control-Request-Headers", "authorization,x-firebase-appcheck")
					pw := httptest.NewRecorder()
					h.ServeHTTP(pw, p)
					if (pw.Code == 204) != (origin == allowedWebOrigin) {
						t.Fatal("preflight", pw.Code)
					}
				}
				r.Header.Del("Authorization")
				uw := httptest.NewRecorder()
				h.ServeHTTP(uw, r)
				if uw.Code != 401 {
					t.Fatal("missing identity accepted", uw.Code)
				}
			}
		})
	}
}
