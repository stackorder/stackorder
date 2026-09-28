// Package worker runs the server's queue: a pool of goroutines that claim
// webhook events and internal jobs from Postgres with SELECT ... FOR
// UPDATE SKIP LOCKED and hand each to the handler registered for its
// kind. It replaces a message broker: any number of server instances can
// run a pool on the same database, and a row claimed by an instance that
// died is released after StaleClaimAge and claimed again elsewhere, so
// every handler must be idempotent.
//
// A failed handler is retried after Backoff(attempt); after MaxAttempts
// the row is marked done with its last error kept, which is the dead
// letter. Rows of a kind nobody handles are completed at once: the App
// subscribes to more events than the server acts on.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/store"
)

const (
	maintenanceInterval = time.Minute
	settleTimeout       = 10 * time.Second
	cancelGrace         = 5 * time.Second
)

var workerSeq atomic.Int64

var (
	// ErrRunning is returned by Run when the pool is already running.
	ErrRunning = errors.New("worker: pool is already running")
	// ErrDrainTimeout is returned by Run when in-flight handlers did not
	// finish within DrainTimeout of the context ending and were cancelled.
	ErrDrainTimeout = errors.New("worker: drain timed out")
)

// EventHandler processes one webhook event. It must be idempotent and
// return when ctx is done.
type EventHandler func(ctx context.Context, ev store.Event) error

// JobHandler processes one job. It must be idempotent and return when ctx
// is done.
type JobHandler func(ctx context.Context, job store.Job) error

// Queue is the part of the store the pool works on. *store.Store
// implements it.
type Queue interface {
	ClaimEvents(ctx context.Context, worker string, n int) ([]store.Event, error)
	CompleteEvent(ctx context.Context, id string) error
	FailEvent(ctx context.Context, id string, cause error, retryAfter time.Duration) error
	AbandonEvent(ctx context.Context, id string, cause error) error
	EnqueueJob(ctx context.Context, kind string, payload json.RawMessage, runAfter time.Time, dedupeKey string) (store.Job, bool, error)
	ClaimJobs(ctx context.Context, worker string, n int) ([]store.Job, error)
	CompleteJob(ctx context.Context, id uuid.UUID) error
	FailJob(ctx context.Context, id uuid.UUID, cause error, retryAfter time.Duration) error
	AbandonJob(ctx context.Context, id uuid.UUID, cause error) error
	ReleaseClaims(ctx context.Context, worker string) (int64, error)
	ReleaseStaleClaims(ctx context.Context, olderThan time.Duration) (int64, error)
	QueueDepth(ctx context.Context) (store.QueueDepth, error)
}

// Options configure a Pool. Zero fields take the defaults noted.
type Options struct {
	// Workers is the number of goroutines claiming rows; default 4.
	Workers int
	// PollInterval is how long an idle worker waits before claiming
	// again when nobody calls Notify; default 2 s.
	PollInterval time.Duration
	// ClaimBatch is how many events, and then how many jobs, a worker
	// claims at once; default 1. A worker runs its batch one row after
	// another, so rows of a larger batch wait behind a slow handler even
	// while other workers are idle.
	ClaimBatch int
	// HandlerTimeout bounds one handler call; default 5 min.
	HandlerTimeout time.Duration
	// MaxAttempts is the number of failed attempts after which a row is
	// given up; default 5.
	MaxAttempts int
	// Backoff returns the delay before retrying after the given failed
	// attempt, counted from 1; default DefaultBackoff.
	Backoff func(attempt int) time.Duration
	// StaleClaimAge is how long a claim may stay unfinished before any
	// pool returns the row to the queue; default 10 min. It is raised to
	// twice HandlerTimeout when not above it, so the claim of a running
	// handler never looks stale, and a worker starts no further row of a
	// batch once the batch is older than StaleClaimAge - HandlerTimeout.
	StaleClaimAge time.Duration
	// DrainTimeout is how long Run waits for in-flight handlers after its
	// context ends before cancelling them; default 30 s.
	DrainTimeout time.Duration
	// Clock returns the current time; default time.Now.
	Clock func() time.Time
	// Logger receives the pool's logs; discarded when nil.
	Logger *slog.Logger
	// Metrics records processing outcomes, webhook lag and queue depth;
	// optional.
	Metrics *metrics.Registry
}

// DefaultBackoff waits 1 min, 5 min, 30 min and 2 h after the first four
// failed attempts and 6 h after any later one.
func DefaultBackoff(attempt int) time.Duration {
	steps := [...]time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	return steps[min(max(attempt, 1), len(steps))-1]
}

// Pool claims and runs queued events and jobs. Each worker goroutine
// claims as "host:pid:n", with n unique within the process. Handlers may
// be registered before or while the pool runs. It is safe for concurrent
// use.
type Pool struct {
	q       Queue
	opts    Options
	name    string
	log     *slog.Logger
	metrics *metrics.Registry
	wake    []chan struct{}
	ids     []string
	running atomic.Bool

	mu     sync.RWMutex
	events map[string]EventHandler
	jobs   map[string]JobHandler
}

// New returns a pool working on q, which is normally a *store.Store.
func New(q Queue, opts Options) *Pool {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 2 * time.Second
	}
	if opts.ClaimBatch <= 0 {
		opts.ClaimBatch = 1
	}
	if opts.HandlerTimeout <= 0 {
		opts.HandlerTimeout = 5 * time.Minute
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 5
	}
	if opts.Backoff == nil {
		opts.Backoff = DefaultBackoff
	}
	if opts.StaleClaimAge <= 0 {
		opts.StaleClaimAge = 10 * time.Minute
	}
	if opts.StaleClaimAge <= opts.HandlerTimeout {
		opts.StaleClaimAge = 2 * opts.HandlerTimeout
	}
	if opts.DrainTimeout <= 0 {
		opts.DrainTimeout = 30 * time.Second
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	p := &Pool{
		q:       q,
		opts:    opts,
		name:    fmt.Sprintf("%s:%d", host, os.Getpid()),
		log:     opts.Logger,
		metrics: opts.Metrics,
		wake:    make([]chan struct{}, opts.Workers),
		ids:     make([]string, opts.Workers),
		events:  map[string]EventHandler{},
		jobs:    map[string]JobHandler{},
	}
	for i := range p.wake {
		p.wake[i] = make(chan struct{}, 1)
		p.ids[i] = fmt.Sprintf("%s:%d", p.name, workerSeq.Add(1)-1)
	}
	return p
}

// OnEvent registers h for webhook events of kind, the X-GitHub-Event name,
// replacing any earlier handler. It panics when h is nil.
func (p *Pool) OnEvent(kind string, h EventHandler) {
	if h == nil {
		panic("worker: nil event handler for " + kind)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events[kind] = h
}

// OnJob registers h for jobs of kind, replacing any earlier handler. It
// panics when h is nil.
func (p *Pool) OnJob(kind string, h JobHandler) {
	if h == nil {
		panic("worker: nil job handler for " + kind)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs[kind] = h
}

// Notify wakes every idle worker so new rows are claimed without waiting
// for the poll interval. It never blocks.
func (p *Pool) Notify() {
	for _, ch := range p.wake {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Enqueue adds a job of kind with payload marshalled as JSON (nil means
// {}), due at runAfter (now when zero). A non-empty dedupeKey that is
// already taken makes the call a no-op. New jobs wake the workers.
func (p *Pool) Enqueue(ctx context.Context, kind string, payload any, runAfter time.Time, dedupeKey string) error {
	raw, err := marshalPayload(payload)
	if err != nil {
		return fmt.Errorf("worker: enqueue %s: %w", kind, err)
	}
	job, inserted, err := p.q.EnqueueJob(ctx, kind, raw, runAfter, dedupeKey)
	if err != nil {
		return fmt.Errorf("worker: enqueue %s: %w", kind, err)
	}
	p.log.DebugContext(ctx, "job enqueued", "kind", kind, "job", job.ID, "dedupe_key", dedupeKey, "inserted", inserted)
	if inserted {
		p.Notify()
	}
	return nil
}

func marshalPayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	if string(raw) == "null" {
		return nil, nil
	}
	return raw, nil
}

// Run claims and processes rows until ctx is done, then stops claiming
// and waits up to DrainTimeout for in-flight handlers before cancelling
// them. Rows a worker claimed but had not started are returned to the
// queue. It returns nil after a complete drain, ErrDrainTimeout when
// handlers had to be cancelled, and ErrRunning when called on a pool
// that is already running.
func (p *Pool) Run(ctx context.Context) error {
	if !p.running.CompareAndSwap(false, true) {
		return ErrRunning
	}
	defer p.running.Store(false)

	handlerCtx, cancelHandlers := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelHandlers()
	var wg sync.WaitGroup
	for i := range p.opts.Workers {
		wg.Go(func() { p.work(ctx, handlerCtx, i) })
	}
	wg.Go(func() { p.maintain(ctx) })
	p.log.InfoContext(ctx, "worker pool started", "workers", p.opts.Workers, "name", p.name)

	<-ctx.Done()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	drain := time.NewTimer(p.opts.DrainTimeout)
	defer drain.Stop()
	select {
	case <-done:
		p.log.Info("worker pool stopped")
		return nil
	case <-drain.C:
	}
	p.log.Warn("worker pool drain timed out; cancelling handlers", "timeout", p.opts.DrainTimeout)
	cancelHandlers()
	grace := time.NewTimer(cancelGrace)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
		p.log.Error("worker pool handlers ignored cancellation", "grace", cancelGrace)
	}
	return fmt.Errorf("%w after %s", ErrDrainTimeout, p.opts.DrainTimeout)
}

func (p *Pool) work(ctx, handlerCtx context.Context, index int) {
	id := p.ids[index]
	defer p.releaseClaims(id)
	idle := time.NewTimer(p.opts.PollInterval)
	defer idle.Stop()
	for ctx.Err() == nil {
		if p.claimAndRun(ctx, handlerCtx, id) > 0 {
			continue
		}
		idle.Reset(p.opts.PollInterval)
		select {
		case <-ctx.Done():
			return
		case <-p.wake[index]:
		case <-idle.C:
		}
	}
}

func (p *Pool) claimAndRun(ctx, handlerCtx context.Context, worker string) int {
	claimed := p.now()
	events, err := p.q.ClaimEvents(ctx, worker, p.opts.ClaimBatch)
	if err != nil && ctx.Err() == nil {
		p.log.WarnContext(ctx, "claim events failed", "worker", worker, "error", err)
	}
	for i, ev := range events {
		if !p.mayStart(ctx, claimed, i) {
			p.releaseClaims(worker)
			return len(events)
		}
		p.process(handlerCtx, p.eventItem(ev, p.now().Sub(claimed)))
	}
	claimed = p.now()
	jobs, err := p.q.ClaimJobs(ctx, worker, p.opts.ClaimBatch)
	if err != nil && ctx.Err() == nil {
		p.log.WarnContext(ctx, "claim jobs failed", "worker", worker, "error", err)
	}
	for i, job := range jobs {
		if !p.mayStart(ctx, claimed, i) {
			p.releaseClaims(worker)
			return len(events) + len(jobs)
		}
		p.process(handlerCtx, p.jobItem(job))
	}
	return len(events) + len(jobs)
}

func (p *Pool) mayStart(ctx context.Context, claimed time.Time, index int) bool {
	if ctx.Err() != nil {
		return false
	}
	return index == 0 || p.now().Sub(claimed)+p.opts.HandlerTimeout < p.opts.StaleClaimAge
}

func (p *Pool) releaseClaims(worker string) {
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	n, err := p.q.ReleaseClaims(ctx, worker)
	if err != nil {
		p.log.WarnContext(ctx, "release claims failed", "worker", worker, "error", err)
		return
	}
	if n > 0 {
		p.log.DebugContext(ctx, "released unstarted claims", "worker", worker, "rows", n)
		p.Notify()
	}
}

type item struct {
	queue     string
	kind      string
	label     string
	id        string
	attempts  int
	lastError string
	run       func(ctx context.Context) error
	complete  func(ctx context.Context) error
	fail      func(ctx context.Context, cause error, retryAfter time.Duration) error
	abandon   func(ctx context.Context, cause error) error
	record    func(kind string, result metrics.Result)
}

func (p *Pool) eventItem(ev store.Event, waited time.Duration) item {
	if ev.Attempts == 0 && ev.ClaimedAt != nil {
		p.metrics.ObserveWebhookLag(ev.ClaimedAt.Sub(ev.ReceivedAt) + waited)
	}
	it := item{
		queue:     "event",
		kind:      ev.Kind,
		label:     metrics.Other,
		id:        ev.ID,
		attempts:  ev.Attempts,
		lastError: ev.LastError,
		complete:  func(ctx context.Context) error { return p.q.CompleteEvent(ctx, ev.ID) },
		fail: func(ctx context.Context, cause error, retryAfter time.Duration) error {
			return p.q.FailEvent(ctx, ev.ID, cause, retryAfter)
		},
		abandon: func(ctx context.Context, cause error) error { return p.q.AbandonEvent(ctx, ev.ID, cause) },
		record:  p.metrics.EventProcessed,
	}
	p.mu.RLock()
	h, ok := p.events[ev.Kind]
	p.mu.RUnlock()
	if ok {
		it.label = ev.Kind
		it.run = func(ctx context.Context) error { return h(ctx, ev) }
	}
	return it
}

func (p *Pool) jobItem(job store.Job) item {
	it := item{
		queue:     "job",
		kind:      job.Kind,
		label:     job.Kind,
		id:        job.ID.String(),
		attempts:  job.Attempts,
		lastError: job.LastError,
		complete:  func(ctx context.Context) error { return p.q.CompleteJob(ctx, job.ID) },
		fail: func(ctx context.Context, cause error, retryAfter time.Duration) error {
			return p.q.FailJob(ctx, job.ID, cause, retryAfter)
		},
		abandon: func(ctx context.Context, cause error) error { return p.q.AbandonJob(ctx, job.ID, cause) },
		record:  p.metrics.JobProcessed,
	}
	p.mu.RLock()
	h, ok := p.jobs[job.Kind]
	p.mu.RUnlock()
	if ok {
		it.run = func(ctx context.Context) error { return h(ctx, job) }
	}
	return it
}

func (p *Pool) process(handlerCtx context.Context, it item) {
	log := p.log.With(it.queue, it.id, "kind", it.kind)

	if it.run == nil {
		log.DebugContext(handlerCtx, "no handler registered; completing")
		p.settle(handlerCtx, log, it.complete)
		it.record(it.label, metrics.ResultIgnored)
		return
	}
	if it.attempts >= p.opts.MaxAttempts {
		cause := fmt.Errorf("worker: gave up after %d attempts: %s", it.attempts, it.lastError)
		log.ErrorContext(handlerCtx, "giving up on queued row", "attempts", it.attempts, "error", it.lastError)
		p.settle(handlerCtx, log, func(ctx context.Context) error { return it.abandon(ctx, cause) })
		it.record(it.label, metrics.ResultDead)
		return
	}

	attempt := it.attempts + 1
	start := p.now()
	err := p.invoke(handlerCtx, log, it)
	elapsed := p.now().Sub(start)
	switch {
	case err == nil:
		log.DebugContext(handlerCtx, "handled", "attempt", attempt, "duration", elapsed)
		p.settle(handlerCtx, log, it.complete)
		it.record(it.label, metrics.ResultOK)
	case handlerCtx.Err() != nil:
		log.WarnContext(handlerCtx, "handler cancelled by shutdown; returned to the queue", "attempt", attempt, "duration", elapsed, "error", err)
		p.settle(handlerCtx, log, func(ctx context.Context) error { return it.fail(ctx, err, 0) })
		it.record(it.label, metrics.ResultError)
	case attempt >= p.opts.MaxAttempts:
		log.ErrorContext(handlerCtx, "handler failed on its last attempt; giving up", "attempt", attempt, "duration", elapsed, "error", err)
		p.settle(handlerCtx, log, func(ctx context.Context) error { return it.abandon(ctx, err) })
		it.record(it.label, metrics.ResultDead)
	default:
		retry := p.opts.Backoff(attempt)
		log.WarnContext(handlerCtx, "handler failed; will retry", "attempt", attempt, "duration", elapsed, "retry_in", retry, "error", err)
		p.settle(handlerCtx, log, func(ctx context.Context) error { return it.fail(ctx, err, retry) })
		it.record(it.label, metrics.ResultError)
	}
}

func (p *Pool) invoke(parent context.Context, log *slog.Logger, it item) (err error) {
	ctx, cancel := context.WithTimeout(parent, p.opts.HandlerTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			log.ErrorContext(ctx, "handler panicked", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			err = fmt.Errorf("worker: %s handler panicked: %v", it.kind, r)
		}
	}()
	return it.run(ctx)
}

func (p *Pool) settle(handlerCtx context.Context, log *slog.Logger, record func(ctx context.Context) error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(handlerCtx), settleTimeout)
	defer cancel()
	if err := record(ctx); err != nil {
		log.ErrorContext(ctx, "could not record the outcome; the claim will expire and the row run again", "error", err)
	}
}

func (p *Pool) maintain(ctx context.Context) {
	p.maintenance(ctx)
	tick := time.NewTicker(maintenanceInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.maintenance(ctx)
		}
	}
}

func (p *Pool) maintenance(ctx context.Context) {
	n, err := p.q.ReleaseStaleClaims(ctx, p.opts.StaleClaimAge)
	switch {
	case err != nil && ctx.Err() == nil:
		p.log.WarnContext(ctx, "release stale claims failed", "error", err)
	case n > 0:
		p.log.InfoContext(ctx, "released stale claims", "rows", n, "older_than", p.opts.StaleClaimAge)
		p.Notify()
	}
	depth, err := p.q.QueueDepth(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.log.WarnContext(ctx, "queue depth failed", "error", err)
		}
		return
	}
	p.metrics.SetQueueDepth(metrics.QueueEvents, depth.Events)
	p.metrics.SetQueueDepth(metrics.QueueJobs, depth.Jobs)
}

func (p *Pool) now() time.Time {
	return p.opts.Clock()
}
