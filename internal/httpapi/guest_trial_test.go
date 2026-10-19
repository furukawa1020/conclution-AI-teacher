package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
)

func trialHandler(t *testing.T, enabled bool, principal identity.Principal) http.Handler {
	t.Helper()
	store, _ := guesttrial.NewMemoryStore(16)
	service, err := guesttrial.NewService(store, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	opts := VoiceOptions{GuestModeEnabled: true, GuestRateLimiter: &fakeLimiter{}, GuestAppRateLimiter: &fakeLimiter{}, RequestTimeout: time.Second}
	if enabled {
		opts.GuestTrials = service
	}
	return NewWithVoice(slog.New(slog.NewTextHandler(io.Discard, nil)), fakeVerifier{principal: principal}, &fakeLimiter{}, &fakeEvaluator{}, &fakeStore{}, time.Second, 4096, opts)
}

func trialGuest() identity.Principal {
	return identity.Principal{UID: "guest", AppID: "app", Provider: "anonymous", AuthMethod: "guest-v1"}
}

func trialPost(handler http.Handler, body string, alter func(*http.Request)) *httptest.ResponseRecorder {
	r := authenticatedRequest(http.MethodPost, guestTrialControlPath, body)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", allowedWebOrigin)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if alter != nil {
		alter(r)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestGuestTrialControlRoundTripAndReplay(t *testing.T) {
	h := trialHandler(t, true, trialGuest())
	w := trialPost(h, `{"action":"begin"}`, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w.Header().Get("Access-Control-Allow-Origin") != allowedWebOrigin || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("privacy/CORS headers")
	}
	var first guesttrial.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	body := `{"action":"help","id":"` + first.ID + `","generation":1}`
	w = trialPost(h, body, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = trialPost(h, body, nil)
	if w.Code != 409 {
		t.Fatal("replay", w.Code)
	}
	w = trialPost(h, `{"action":"read","id":"`+first.ID+`"}`, nil)
	var recovered guesttrial.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &recovered); err != nil || !recovered.Assisted || recovered.Generation != 2 {
		t.Fatal(recovered, err)
	}
	w = trialPost(h, `{"action":"stop","id":"`+first.ID+`","generation":2}`, nil)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = trialPost(h, `{"action":"help","id":"`+first.ID+`","generation":3}`, nil)
	if w.Code == 200 {
		t.Fatal("terminal accepted")
	}
}

func TestGuestTrialAPIRejectsUnauthorizedMalformedAndUnconfigured(t *testing.T) {
	h := trialHandler(t, true, trialGuest())
	for _, tc := range []struct {
		name, body string
		alter      func(*http.Request)
		want       int
	}{
		{"missing auth", `{"action":"begin"}`, func(r *http.Request) { r.Header.Del("Authorization") }, 401},
		{"missing app check", `{"action":"begin"}`, func(r *http.Request) { r.Header.Del("X-Firebase-AppCheck") }, 401},
		{"foreign origin", `{"action":"begin"}`, func(r *http.Request) { r.Header.Set("Origin", "https://attacker.invalid") }, 403},
		{"unknown field", `{"action":"begin","answer":"invented"}`, nil, 400},
		{"completion", `{"action":"complete"}`, nil, 400},
		{"client id", `{"action":"begin","id":"chosen"}`, nil, 400},
		{"trailing", `{"action":"begin"}{}`, nil, 400},
		{"oversize", `{"action":"` + strings.Repeat("x", 513) + `"}`, nil, 400},
		{"query", `{"action":"begin"}`, func(r *http.Request) { r.URL.RawQuery = "uid=other" }, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := trialPost(h, tc.body, tc.alter)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if w := trialPost(trialHandler(t, false, trialGuest()), `{"action":"begin"}`, nil); w.Code != 404 {
		t.Fatal("unconfigured exposed", w.Code)
	}
	account := identity.Principal{UID: "account", AppID: "app", Provider: "custom", AuthMethod: "passkey-v1", AccountVerified: true}
	if w := trialPost(trialHandler(t, true, account), `{"action":"begin"}`, nil); w.Code != 403 {
		t.Fatal("non-guest accepted", w.Code)
	}
}

func TestGuestTrialPreflight(t *testing.T) {
	h := trialHandler(t, true, trialGuest())
	for _, origin := range []string{allowedWebOrigin, "https://attacker.invalid"} {
		r := httptest.NewRequest(http.MethodOptions, guestTrialControlPath, nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "authorization,content-type,x-firebase-appcheck")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if (w.Code == 204) != (origin == allowedWebOrigin) {
			t.Fatal(origin, w.Code)
		}
	}
}
