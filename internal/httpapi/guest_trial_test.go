package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
)

type trialStateFunc func(guesttrial.Owner, guesttrial.Snapshot) (string, error)

func (f trialStateFunc) IssueGuestTrialState(o guesttrial.Owner, s guesttrial.Snapshot) (string, error) {
	return f(o, s)
}

func TestGuestTrialControlIssuesOnlyOwnerBoundPendingState(t *testing.T) {
	calls := 0
	issuer := trialStateFunc(func(o guesttrial.Owner, s guesttrial.Snapshot) (string, error) {
		calls++
		if o.UID != "guest" || o.AppID != "app" || s.Status != guesttrial.Pending || s.Question != guesttrial.QuestionText || len(s.ID) != 64 {
			t.Fatal("untrusted snapshot")
		}
		return "v2.sealed", nil
	})
	h := trialHandlerWithCapabilities(t, true, trialGuest(), nil, issuer)
	w := trialPost(h, `{"action":"begin"}`, nil)
	var initial guestTrialControlResponse
	if json.Unmarshal(w.Body.Bytes(), &initial) != nil || w.Code != 200 || initial.State != "v2.sealed" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, action := range []string{"read", "help", "stop", "read"} {
		body := `{"action":"` + action + `","id":"` + initial.ID + `"`
		if action == "help" {
			body += `,"generation":1`
		}
		if action == "stop" {
			body += `,"generation":2`
		}
		body += `}`
		w = trialPost(h, body, nil)
		var reply guestTrialControlResponse
		if json.Unmarshal(w.Body.Bytes(), &reply) != nil || w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if (reply.State != "") != (reply.Status == guesttrial.Pending) {
			t.Fatal("terminal capability issued")
		}
	}
	if calls != 3 {
		t.Fatal("unexpected issuance count", calls)
	}
}

func TestGuestTrialControlIssuanceFailureIsNotSuccess(t *testing.T) {
	for _, tc := range []struct {
		token string
		err   error
	}{{"", nil}, {strings.Repeat("a", maxStateBytes+1), nil}, {"", errors.New("private issuer detail")}} {
		issuer := trialStateFunc(func(guesttrial.Owner, guesttrial.Snapshot) (string, error) { return tc.token, tc.err })
		w := trialPost(trialHandlerWithCapabilities(t, true, trialGuest(), nil, issuer), `{"action":"begin"}`, nil)
		if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func trialHandler(t *testing.T, enabled bool, principal identity.Principal) http.Handler {
	return trialHandlerWithVoice(t, enabled, principal, nil)
}

func trialHandlerWithVoice(t *testing.T, enabled bool, principal identity.Principal, voice GuestTrialVoiceService) http.Handler {
	return trialHandlerWithCapabilities(t, enabled, principal, voice, nil)
}

func trialHandlerWithCapabilities(t *testing.T, enabled bool, principal identity.Principal, voice GuestTrialVoiceService, issuer GuestTrialStateIssuer) http.Handler {
	return trialHandlerWithOptions(t, enabled, principal, voice, issuer, nil)
}

func trialHandlerWithOptions(t *testing.T, enabled bool, principal identity.Principal, voice GuestTrialVoiceService, issuer GuestTrialStateIssuer, configure func(*VoiceOptions)) http.Handler {
	t.Helper()
	store, _ := guesttrial.NewMemoryStore(16)
	service, err := guesttrial.NewService(store, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	opts := VoiceOptions{GuestModeEnabled: true, GuestRateLimiter: &fakeLimiter{wantKey: principal.UID}, GuestAppRateLimiter: &fakeLimiter{wantKey: "app:" + principal.AppID}, RequestTimeout: time.Second}
	opts.GuestTrialVoice = voice
	opts.GuestTrialState = issuer
	opts.GuestTrialControlLimiter = &fakeLimiter{wantKey: principal.UID}
	opts.GuestTrialControlAppLimiter = &fakeLimiter{wantKey: "app:" + principal.AppID}
	if configure != nil {
		configure(&opts)
	}
	if enabled {
		opts.GuestTrials = service
	}
	return NewWithVoice(slog.New(slog.NewTextHandler(io.Discard, nil)), fakeVerifier{principal: principal}, &fakeLimiter{}, &fakeEvaluator{}, &fakeStore{}, time.Second, 4096, opts)
}

func trialGuest() identity.Principal {
	return identity.Principal{UID: "guest", AppID: "app", Provider: "anonymous", AuthMethod: "guest-v1"}
}

func TestGuestTrialControlDoesNotConsumeExhaustedVoiceBudget(t *testing.T) {
	voiceUID := &fakeLimiter{err: guard.ErrRateLimitExceeded}
	voiceApp := &fakeLimiter{err: guard.ErrRateLimitExceeded}
	h := trialHandlerWithOptions(t, true, trialGuest(), nil, nil, func(o *VoiceOptions) {
		o.GuestRateLimiter, o.GuestAppRateLimiter = voiceUID, voiceApp
	})
	w := trialPost(h, `{"action":"begin"}`, nil)
	var first guesttrial.Snapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{
		`{"action":"read","id":"` + first.ID + `"}`,
		`{"action":"stop","id":"` + first.ID + `","generation":1}`,
	} {
		if w := trialPost(h, body, nil); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if voiceUID.calls != 0 || voiceApp.calls != 0 {
		t.Fatal("control consumed voice quota")
	}
}

func TestGuestTrialControlBudgetRemainsBoundedAndFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		uid, app guard.Limiter
		want     int
	}{
		{"missing uid", nil, &fakeLimiter{}, 503},
		{"missing app", &fakeLimiter{}, nil, 503},
		{"uid exceeded", &fakeLimiter{err: guard.ErrRateLimitExceeded}, &fakeLimiter{}, 429},
		{"app exceeded", &fakeLimiter{}, &fakeLimiter{err: guard.ErrRateLimitExceeded}, 429},
		{"store unavailable", &fakeLimiter{err: errors.New("private storage detail")}, &fakeLimiter{}, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := trialHandlerWithOptions(t, true, trialGuest(), nil, nil, func(o *VoiceOptions) {
				o.GuestTrialControlLimiter, o.GuestTrialControlAppLimiter = tc.uid, tc.app
			})
			w := trialPost(h, `{"action":"begin"}`, nil)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
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
