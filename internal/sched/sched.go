// Package sched is the server's cron scheduler. Exactly one server
// instance schedules at a time, the one holding the Postgres advisory lock
// store.SchedulerLockKey; every instance executes, because the scheduler
// only enqueues jobs and the worker pools of all instances run them.
//
// Every Tick the leader enqueues, with a dedupe key naming the slot so a
// slot is enqueued once however many ticks or leaders see it:
//
//   - "reconcile" every minute, key reconcile:<minute unix>;
//   - "prune" every hour, key prune:<hour unix>;
//   - "stale_locks" daily at 08:00 UTC, key stale_locks:<08:00 unix>;
//   - "schedule_drift" with payload {"repo_id": id} for every repository
//     whose stored configuration sets drift.schedule, at each cron fire
//     time, key schedule_drift:<repo id>:<fire unix>.
//
// A slot is due when its fire time falls within (last tick, now]. Missed
// ticks are caught up, but only the latest fire time of each schedule in
// the window is enqueued, and a new leader looks back at most an hour, so
// an outage never releases a burst of identical jobs. Cron expressions
// are evaluated in UTC unless they carry a CRON_TZ= prefix.
package sched

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/store"
)

const catchUp = time.Hour

// ErrRunning is returned by Run when the scheduler is already running.
var ErrRunning = errors.New("sched: scheduler is already running")

var housekeeping = []struct {
	kind     string
	schedule cron.Schedule
}{
	{runs.JobReconcile, mustParse("* * * * *")},
	{runs.JobPrune, mustParse("0 * * * *")},
	{runs.JobStaleLocks, mustParse("0 8 * * *")},
}

func mustParse(spec string) cron.Schedule {
	s, err := config.ParseCron(spec)
	if err != nil {
		panic(fmt.Sprintf("sched: %s: %v", spec, err))
	}
	return s
}

// Enqueuer adds a job; *worker.Pool implements it. A dedupe key already
// taken must make the call a no-op.
type Enqueuer interface {
	Enqueue(ctx context.Context, kind string, payload any, runAfter time.Time, dedupeKey string) error
}

// Options configure a Scheduler. Zero fields take the defaults noted.
type Options struct {
	// Clock returns the current time; default time.Now.
	Clock func() time.Time
	// Tick is how often the leader enqueues due slots and every other
	// instance tries to take the lead; default 30 s.
	Tick time.Duration
	// Logger receives the scheduler's logs; discarded when nil.
	Logger *slog.Logger
	// Metrics records scheduler_leader; optional.
	Metrics *metrics.Registry
}

type repoLister interface {
	ListRepos(ctx context.Context, accounts ...string) ([]store.Repo, error)
}

type leaderLock interface {
	Held(ctx context.Context) bool
	Release()
}

type lockFunc func(ctx context.Context) (leaderLock, bool, error)

type parsedCron struct {
	expr     string
	schedule cron.Schedule
	err      error
}

// Scheduler enqueues scheduled jobs while it leads. Run it once per
// server instance.
type Scheduler struct {
	repos   repoLister
	tryLock lockFunc
	q       Enqueuer
	now     func() time.Time
	tick    time.Duration
	log     *slog.Logger
	metrics *metrics.Registry
	leader  atomic.Bool
	running atomic.Bool
	crons   map[int64]parsedCron
}

// New returns a Scheduler that elects itself through st's advisory lock,
// reads repository configuration from st and enqueues through q.
func New(st *store.Store, q Enqueuer, opts Options) *Scheduler {
	return newScheduler(st, func(ctx context.Context) (leaderLock, bool, error) {
		return st.TryAdvisoryLock(ctx, store.SchedulerLockKey)
	}, q, opts)
}

func newScheduler(repos repoLister, tryLock lockFunc, q Enqueuer, opts Options) *Scheduler {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Tick <= 0 {
		opts.Tick = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Scheduler{
		repos:   repos,
		tryLock: tryLock,
		q:       q,
		now:     opts.Clock,
		tick:    opts.Tick,
		log:     opts.Logger,
		metrics: opts.Metrics,
		crons:   map[int64]parsedCron{},
	}
}

// Leader reports whether this scheduler currently holds the lead.
func (s *Scheduler) Leader() bool {
	return s.leader.Load()
}

// Run tries to take the lead every Tick and, while leading, enqueues due
// slots every Tick, checking first that the advisory lock is still held.
// A lost lock, such as after its connection dropped, ends the lead until
// the lock can be taken again. Run returns nil once ctx is done, releasing
// the lock, and ErrRunning when the scheduler is already running.
func (s *Scheduler) Run(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return ErrRunning
	}
	defer s.running.Store(false)
	var lock leaderLock
	defer func() {
		if lock != nil {
			lock.Release()
		}
		s.setLeader(false)
	}()
	var last time.Time
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		now := s.now().UTC()
		switch {
		case lock == nil:
			l, ok, err := s.tryLock(ctx)
			switch {
			case err != nil:
				if ctx.Err() == nil {
					s.log.WarnContext(ctx, "scheduler could not try the leader lock", "error", err)
				}
			case ok:
				lock, last = l, now.Add(-catchUp)
				s.setLeader(true)
				s.log.InfoContext(ctx, "scheduler took the lead")
			}
		case !lock.Held(ctx):
			if ctx.Err() == nil {
				s.log.WarnContext(ctx, "scheduler lost the leader lock")
			}
			lock.Release()
			lock = nil
			s.setLeader(false)
		}
		if lock != nil {
			if err := s.enqueueDue(ctx, last, now); err != nil {
				if ctx.Err() == nil {
					s.log.WarnContext(ctx, "scheduler tick incomplete; retrying the window next tick", "error", err)
				}
			} else {
				last = now
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) setLeader(leader bool) {
	s.leader.Store(leader)
	s.metrics.SetSchedulerLeader(leader)
}

func (s *Scheduler) enqueueDue(ctx context.Context, last, now time.Time) error {
	from := last.UTC()
	if floor := now.Add(-catchUp); from.Before(floor) {
		from = floor
	}
	var errs []error
	for _, h := range housekeeping {
		if fire, ok := latestFire(h.schedule, from, now); ok {
			errs = append(errs, s.enqueue(ctx, h.kind, nil, fire, fmt.Sprintf("%s:%d", h.kind, fire.Unix())))
		}
	}
	repos, err := s.repos.ListRepos(ctx)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("sched: list repos: %w", err))...)
	}
	for _, r := range repos {
		if r.Suspended || r.Config == nil || r.Config.Drift.Schedule == "" {
			continue
		}
		schedule, ok := s.cron(ctx, r)
		if !ok {
			continue
		}
		if fire, ok := latestFire(schedule, from, now); ok {
			key := fmt.Sprintf("%s:%d:%d", runs.JobScheduleDrift, r.ID, fire.Unix())
			errs = append(errs, s.enqueue(ctx, runs.JobScheduleDrift, runs.ScheduleDriftJob{RepoID: r.ID}, fire, key))
		}
	}
	return errors.Join(errs...)
}

func (s *Scheduler) enqueue(ctx context.Context, kind string, payload any, fire time.Time, key string) error {
	if err := s.q.Enqueue(ctx, kind, payload, fire, key); err != nil {
		return fmt.Errorf("sched: enqueue %s: %w", key, err)
	}
	s.log.DebugContext(ctx, "scheduled", "kind", kind, "dedupe_key", key)
	return nil
}

func (s *Scheduler) cron(ctx context.Context, r store.Repo) (cron.Schedule, bool) {
	expr := r.Config.Drift.Schedule
	p, seen := s.crons[r.ID]
	if !seen || p.expr != expr {
		p = parsedCron{expr: expr}
		p.schedule, p.err = config.ParseCron(expr)
		s.crons[r.ID] = p
		if p.err != nil {
			s.log.WarnContext(ctx, "ignoring invalid drift.schedule", "repo", r.FullName, "schedule", expr, "error", p.err)
		}
	}
	return p.schedule, p.err == nil
}

func latestFire(schedule cron.Schedule, from, to time.Time) (time.Time, bool) {
	var last time.Time
	for t := schedule.Next(from.UTC()); !t.IsZero() && !t.After(to); t = schedule.Next(t) {
		last = t
	}
	return last, !last.IsZero()
}
