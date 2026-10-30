package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
)

type stalledTrialBody struct {
	started, stopped chan struct{}
	once             sync.Once
}

func (b *stalledTrialBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.stopped
	return 0, io.ErrUnexpectedEOF
}
func (*stalledTrialBody) Close() error { return nil }

type trialDeadlineWriter struct {
	*httptest.ResponseRecorder
	body    *stalledTrialBody
	once    sync.Once
	cleared bool
}

func (w *trialDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		w.cleared = true
		return nil
	}
	if !deadline.After(time.Now()) {
		w.once.Do(func() { close(w.body.stopped) })
	}
	return nil
}

func TestGuestTrialBodyCancellationInterruptsReadAndClearsDeadline(t *testing.T) {
	for _, path := range []string{guestTrialVoicePath, "/api/v1/guest-trials/control"} {
		t.Run(path, func(t *testing.T) {
			voice := trialVoiceFunc(func(context.Context, guesttrial.Owner, string, int64, string, []byte) (guesttrial.Snapshot, error) {
				t.Error("pipeline called")
				return guesttrial.Snapshot{}, nil
			})
			h := trialHandlerWithVoice(t, true, trialGuest(), voice)
			r := authenticatedRequest(http.MethodPost, path, "")
			r.Header.Set("Origin", allowedWebOrigin)
			r.Header.Set("Content-Type", "application/json")
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			body := &stalledTrialBody{started: make(chan struct{}), stopped: make(chan struct{})}
			r.Body = body
			w := &trialDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), body: body}
			go func() {
				select {
				case <-body.started:
					cancel()
				case <-ctx.Done():
				}
			}()
			done := make(chan struct{})
			go func() { defer close(done); h.ServeHTTP(w, r) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("body read did not stop")
			}
			if w.Code != 408 || !w.cleared {
				t.Fatal(w.Code, w.cleared, w.Body.String())
			}
		})
	}
}
