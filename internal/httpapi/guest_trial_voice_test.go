package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
)

type trialVoiceFunc func(context.Context, guesttrial.Owner, string, int64, string, []byte) (guesttrial.Snapshot, error)

func (f trialVoiceFunc) ProcessPCM(ctx context.Context, owner guesttrial.Owner, id string, gen int64, state string, pcm []byte) (guesttrial.Snapshot, error) {
	return f(ctx, owner, id, gen, state, pcm)
}

func trialVoiceBody() string {
	return `{"id":"` + strings.Repeat("a", 64) + `","generation":1,"state":"sealed","pcm16":"AQI="}`
}

func voiceTrialPost(h http.Handler, body string, alter func(*http.Request)) *httptest.ResponseRecorder {
	r := authenticatedRequest(http.MethodPost, guestTrialVoicePath, body)
	r.Header.Set("Origin", allowedWebOrigin)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Content-Type", "application/json")
	if alter != nil {
		alter(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGuestTrialVoiceIdentityAndAudioOwnership(t *testing.T) {
	var held []byte
	calls := 0
	f := trialVoiceFunc(func(ctx context.Context, o guesttrial.Owner, id string, gen int64, state string, pcm []byte) (guesttrial.Snapshot, error) {
		calls++
		if o.UID != "guest" || o.AppID != "app" || id != strings.Repeat("a", 64) || gen != 1 || state != "sealed" || string(pcm) != "\x01\x02" {
			t.Fatal("incorrect provenance or audio")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded work")
		}
		held = pcm
		return guesttrial.Snapshot{Status: guesttrial.AnswerFirst}, nil
	})
	w := voiceTrialPost(trialHandlerWithVoice(t, true, trialGuest(), f), trialVoiceBody(), nil)
	if w.Code != 200 || calls != 1 || string(held) != "\x00\x00" {
		t.Fatal(w.Code, calls, held)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Access-Control-Allow-Origin") != allowedWebOrigin || w.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Fatal("missing privacy/CORS headers")
	}
	if strings.Contains(w.Body.String(), "sealed") || strings.Contains(w.Body.String(), "pcm16") {
		t.Fatal("private input escaped")
	}
}

func TestGuestTrialVoiceRejectsInvalidBeforePipeline(t *testing.T) {
	f := trialVoiceFunc(func(context.Context, guesttrial.Owner, string, int64, string, []byte) (guesttrial.Snapshot, error) {
		t.Fatal("pipeline called for rejected input")
		return guesttrial.Snapshot{}, nil
	})
	h := trialHandlerWithVoice(t, true, trialGuest(), f)
	for _, tc := range []struct {
		name, body string
		alter      func(*http.Request)
		status     int
	}{
		{"auth", trialVoiceBody(), func(r *http.Request) { r.Header.Del("Authorization") }, 401},
		{"app", trialVoiceBody(), func(r *http.Request) { r.Header.Del("X-Firebase-AppCheck") }, 401},
		{"origin", trialVoiceBody(), func(r *http.Request) { r.Header.Set("Origin", "https://other.invalid") }, 403},
		{"query", trialVoiceBody(), func(r *http.Request) { r.URL.RawQuery = "uid=other" }, 400},
		{"encoding", trialVoiceBody(), func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, 400},
		{"unknown", strings.Replace(trialVoiceBody(), `"generation":1`, `"generation":1,"uid":"other"`, 1), nil, 400},
		{"duplicate", strings.Replace(trialVoiceBody(), `"generation":1`, `"generation":1,"generation":2`, 1), nil, 400},
		{"null", strings.Replace(trialVoiceBody(), `"generation":1`, `"generation":null`, 1), nil, 400},
		{"odd PCM", strings.Replace(trialVoiceBody(), "AQI=", "AQ==", 1), nil, 400},
		{"null PCM", strings.Replace(trialVoiceBody(), `"AQI="`, `null`, 1), nil, 400},
		{"array PCM", strings.Replace(trialVoiceBody(), `"AQI="`, `[1,2]`, 1), nil, 400},
		{"bad base64", strings.Replace(trialVoiceBody(), "AQI=", "!!!!", 1), nil, 400},
		{"trailing", trialVoiceBody() + "{}", nil, 400},
		{"oversize", strings.Replace(trialVoiceBody(), "AQI=", strings.Repeat("A", guestTrialMaxRequestBytes+4), 1), nil, 400},
		{"cancelled", trialVoiceBody(), func(r *http.Request) {
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			*r = *r.WithContext(ctx)
		}, 408},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := voiceTrialPost(h, tc.body, tc.alter)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	for _, enabled := range []bool{false, true} {
		w := voiceTrialPost(trialHandlerWithVoice(t, enabled, trialGuest(), nil), trialVoiceBody(), nil)
		if w.Code != 404 {
			t.Fatal("unconfigured route exposed", w.Code)
		}
	}
	if w := voiceTrialPost(trialHandlerWithVoice(t, false, trialGuest(), f), trialVoiceBody(), nil); w.Code != 404 {
		t.Fatal("voice without ledger exposed", w.Code)
	}
	account := trialGuest()
	account.Provider = "custom"
	account.AuthMethod = "passkey-v1"
	account.AccountVerified = true
	if w := voiceTrialPost(trialHandlerWithVoice(t, true, account, f), trialVoiceBody(), nil); w.Code != 403 {
		t.Fatal("account accepted", w.Code)
	}
}

func TestGuestTrialVoiceFailureDoesNotBecomeAnswer(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{guesttrial.ErrConflict, 409}, {guesttrial.ErrExpired, 410}, {guesttrial.ErrNotFound, 404},
		{ErrVoiceNotRecognized, 422}, {conversation.ErrInvalidStateToken, 409},
		{conversation.ErrExpiredStateToken, 409}, {context.Canceled, 408}, {context.DeadlineExceeded, 504},
		{errors.New("secret provider response"), 503},
	} {
		var held []byte
		f := trialVoiceFunc(func(_ context.Context, _ guesttrial.Owner, _ string, _ int64, _ string, pcm []byte) (guesttrial.Snapshot, error) {
			held = pcm
			return guesttrial.Snapshot{Status: guesttrial.AnswerFirst}, tc.err
		})
		w := voiceTrialPost(trialHandlerWithVoice(t, true, trialGuest(), f), trialVoiceBody(), nil)
		if w.Code != tc.status || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), string(guesttrial.AnswerFirst)) || string(held) != "\x00\x00" {
			t.Fatal(w.Code, w.Body.String(), held)
		}
	}
}

func TestGuestTrialVoicePreflight(t *testing.T) {
	f := trialVoiceFunc(func(context.Context, guesttrial.Owner, string, int64, string, []byte) (guesttrial.Snapshot, error) {
		panic("preflight must not call pipeline")
	})
	h := trialHandlerWithVoice(t, true, trialGuest(), f)
	r := httptest.NewRequest(http.MethodOptions, guestTrialVoicePath, nil)
	r.Header.Set("Origin", allowedWebOrigin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "authorization,content-type,x-firebase-appcheck")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
}
