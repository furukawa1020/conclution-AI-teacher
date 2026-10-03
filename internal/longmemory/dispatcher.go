package longmemory

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	defaultQueueCapacity = 32
	defaultWorkers       = 1
	defaultJobTTL        = 30 * time.Second
	defaultJobTimeout    = 10 * time.Second
)

type CandidateSource interface {
	LongTermMemoryCandidate(uid string, stateToken string) (Payload, bool, error)
}

// Enqueuer is the response-path boundary. Implementations must return without
// waiting for storage or additional inference.
type Enqueuer interface {
	Enqueue(uid string, stateToken string) bool
}

type Outcome string

const (
	OutcomeSaved       Outcome = "saved"
	OutcomeDisabled    Outcome = "disabled"
	OutcomeEmpty       Outcome = "empty"
	OutcomeInvalid     Outcome = "invalid"
	OutcomeStale       Outcome = "stale"
	OutcomeStoreFailed Outcome = "store_failed"
	OutcomeExpired     Outcome = "expired"
	OutcomeQueueFull   Outcome = "queue_full"
)

type Observer func(Outcome, time.Duration)

type DispatcherOptions struct {
	QueueCapacity int
	Workers       int
	JobTTL        time.Duration
	JobTimeout    time.Duration
	Observer      Observer
}

type memoryJob struct {
	uid        string
	stateToken []byte
	createdAt  time.Time
}

type Dispatcher struct {
	manager *Manager
	source  CandidateSource
	jobs    chan memoryJob
	// Overflow observation is best-effort and coalesced. The marker never
	// contains a UID or state, and response callers never invoke the observer.
	queueFullWake chan struct{}
	now           func() time.Time
	ttl           time.Duration
	timeout       time.Duration
	observe       Observer
	cancel        context.CancelFunc
	done          chan struct{}
	once          sync.Once
}

func NewDispatcher(manager *Manager, source CandidateSource, options DispatcherOptions) (*Dispatcher, error) {
	if manager == nil || source == nil {
		return nil, ErrInvalid
	}
	capacity := options.QueueCapacity
	if capacity == 0 {
		capacity = defaultQueueCapacity
	}
	workers := options.Workers
	if workers == 0 {
		workers = defaultWorkers
	}
	ttl := options.JobTTL
	if ttl == 0 {
		ttl = defaultJobTTL
	}
	timeout := options.JobTimeout
	if timeout == 0 {
		timeout = defaultJobTimeout
	}
	if capacity < 1 || capacity > 256 || workers < 1 || workers > 4 || ttl <= 0 || ttl > time.Minute || timeout <= 0 || timeout > 30*time.Second {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{manager: manager, source: source, jobs: make(chan memoryJob, capacity), queueFullWake: make(chan struct{}, 1), now: time.Now, ttl: ttl, timeout: timeout, observe: options.Observer, cancel: cancel, done: make(chan struct{})}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() { defer wg.Done(); d.worker(ctx) }()
	}
	go func() { wg.Wait(); close(d.done) }()
	return d, nil
}

// Enqueue copies only the bounded opaque state token and never blocks. The
// caller invokes this after the final response has been committed.
func (d *Dispatcher) Enqueue(uid, stateToken string) bool {
	if d == nil || uid == "" || stateToken == "" || len(stateToken) > 16*1024 {
		return false
	}
	job := memoryJob{uid: uid, stateToken: append([]byte(nil), stateToken...), createdAt: d.now().UTC()}
	select {
	case d.jobs <- job:
		return true
	default:
		clear(job.stateToken)
		if d.observe != nil {
			select {
			case d.queueFullWake <- struct{}{}:
			default:
			}
		}
		return false
	}
}

func (d *Dispatcher) Close(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.once.Do(d.cancel)
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher) worker(ctx context.Context) {
	defer func() {
		if recover() != nil {
			d.zeroPending()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			d.zeroPending()
			return
		case <-d.queueFullWake:
			if !d.notifyQueueFull(ctx) {
				d.zeroPending()
				return
			}
		case job := <-d.jobs:
			d.process(ctx, job)
		}
	}
}

func (d *Dispatcher) notifyQueueFull(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	// A pending marker combines repeated rejections, so this event is not a
	// drop counter and its zero latency is not delivery latency.
	d.notify(OutcomeQueueFull, 0)
	return true
}

func (d *Dispatcher) process(parent context.Context, job memoryJob) {
	started := d.now()
	defer func() {
		clear(job.stateToken)
		if recover() != nil {
			d.notify(OutcomeInvalid, d.now().Sub(started))
		}
	}()
	if d.now().UTC().Sub(job.createdAt) > d.ttl {
		d.notify(OutcomeExpired, d.now().Sub(started))
		return
	}
	ctx, cancel := context.WithTimeout(parent, d.timeout)
	defer cancel()
	consent, err := d.manager.Status(ctx, job.uid)
	if err != nil || !consent.Enabled {
		d.notify(OutcomeDisabled, d.now().Sub(started))
		return
	}
	payload, ok, err := d.source.LongTermMemoryCandidate(job.uid, string(job.stateToken))
	if err != nil {
		d.notify(OutcomeInvalid, d.now().Sub(started))
		return
	}
	if !ok {
		d.notify(OutcomeEmpty, d.now().Sub(started))
		return
	}
	err = d.manager.Save(ctx, job.uid, consent.Generation, payload)
	switch {
	case err == nil:
		d.notify(OutcomeSaved, d.now().Sub(started))
	case errors.Is(err, ErrDisabled), errors.Is(err, ErrStale):
		d.notify(OutcomeStale, d.now().Sub(started))
	default:
		d.notify(OutcomeStoreFailed, d.now().Sub(started))
	}
}

func (d *Dispatcher) zeroPending() {
	for {
		select {
		case job := <-d.jobs:
			clear(job.stateToken)
		default:
			return
		}
	}
}

func (d *Dispatcher) notify(outcome Outcome, latency time.Duration) {
	if d.observe == nil {
		return
	}
	defer func() { _ = recover() }()
	d.observe(outcome, latency)
}
