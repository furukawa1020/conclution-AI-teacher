package nativevoice

import (
	"context"
	"errors"
	"sync"
)

// A single token serializes input without making cancelled callers wait for
// an unrelated provider write. Initialization also supports zero-value sessions.
type inputLock struct {
	once  sync.Once
	token chan struct{}
}

func (l *inputLock) LockContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("native voice input context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.once.Do(func() { l.token = make(chan struct{}, 1); l.token <- struct{}{} })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.token:
		if err := ctx.Err(); err != nil {
			l.Unlock()
			return err
		}
		return nil
	}
}
func (l *inputLock) Unlock() { l.token <- struct{}{} }

func (s *liveSession) lockInput(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("native voice input context is required")
	}
	operation, cancel := context.WithTimeout(ctx, s.config.SendTimeout)
	stop := context.AfterFunc(s.ctx, cancel)
	if err := s.sendMu.LockContext(operation); err != nil {
		stop()
		cancel()
		return nil, nil, safeContextError("send", err)
	}
	return operation, func() { s.sendMu.Unlock(); stop(); cancel() }, nil
}
