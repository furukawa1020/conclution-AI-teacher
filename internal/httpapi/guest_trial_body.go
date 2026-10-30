package httpapi

import (
	"context"
	"net/http"
	"time"
)

// Interrupt the socket read, not merely the work after JSON decoding. Clear the
// deadline after joining the cancellation callback so it cannot poison keepalive.
func boundGuestTrialBody(ctx context.Context, w http.ResponseWriter) func() {
	controller := http.NewResponseController(w)
	deadline, ok := ctx.Deadline()
	if !ok || controller.SetReadDeadline(deadline) != nil {
		return func() {}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		_ = controller.SetReadDeadline(time.Now())
	})
	return func() {
		if !stop() {
			<-done
		}
		_ = controller.SetReadDeadline(time.Time{})
	}
}

func guestTrialBodyContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}
