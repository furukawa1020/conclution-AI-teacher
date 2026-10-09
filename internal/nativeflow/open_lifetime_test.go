package nativeflow

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
)

type lifetimeNativeOpener struct {
	ctx             context.Context
	started, finish chan struct{}
	contextValue    string
	session         nativevoice.Session
	err             error
}

func (o *lifetimeNativeOpener) Open(ctx context.Context) (nativevoice.Session, error) {
	o.ctx = ctx
	close(o.started)
	<-o.finish
	return o.session, o.err
}

func (o *lifetimeNativeOpener) OpenWithContext(ctx context.Context, value string) (nativevoice.Session, error) {
	o.contextValue = value
	return o.Open(ctx)
}

func TestNativeOpenLifetime(t *testing.T) {
	for _, contextual := range []bool{false, true} {
		for _, stop := range []string{"shutdown", "request", "failure", "release"} {
			t.Run(stop+map[bool]string{false: "/ordinary", true: "/contextual"}[contextual], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					parent, cancel := context.WithCancel(context.Background())
					defer cancel()
					session := newScriptedSession()
					opener := &lifetimeNativeOpener{started: make(chan struct{}), finish: make(chan struct{}), session: session}
					if stop == "failure" {
						opener.err = errors.New("setup failed")
					}
					service, err := New(opener, fakePreparer{token: "opaque-state"})
					if err != nil {
						t.Fatal(err)
					}
					defer service.Close()
					value := ""
					if contextual {
						value = "previous exchange"
					}
					done := make(chan error, 1)
					var acquired *pooledSession
					go func() {
						var acquireErr error
						acquired, acquireErr = service.acquire(parent, "lifetime-user", value, nil)
						done <- acquireErr
					}()
					<-opener.started
					if stop == "shutdown" {
						_ = service.Close()
					}
					if stop == "request" {
						cancel()
					}
					if (stop == "shutdown" || stop == "request") && opener.ctx.Err() != context.Canceled {
						t.Error("provider setup was not canceled before its late return")
					}
					// Simulate a provider returning a connection even after cancel.
					close(opener.finish)
					acquireErr := <-done
					if stop == "release" {
						if acquireErr != nil || acquired == nil || opener.ctx.Err() != nil {
							t.Fatalf("acquire=%v ctx=%v", acquireErr, opener.ctx.Err())
						}
						service.release("lifetime-user", acquired, false)
						service.release("lifetime-user", acquired, false)
					} else if !errors.Is(acquireErr, errNativeFlowUnavailable) || acquired != nil {
						t.Fatalf("canceled/failed acquisition returned %v, %v", acquired, acquireErr)
					}
					if opener.ctx.Err() != context.Canceled {
						t.Error("provider context survived retirement")
					}
					if stop != "request" && parent.Err() != nil {
						t.Error("retirement canceled caller context")
					}
					if opener.contextValue != value {
						t.Error("conversation context was lost")
					}
					if len(service.sessions) != 0 || session.closes != 1 || session.discards != 1 {
						t.Fatalf("leases=%d closes=%d discards=%d", len(service.sessions), session.closes, session.discards)
					}
				})
			})
		}
	}
}
