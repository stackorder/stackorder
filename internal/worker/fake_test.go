package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/stackorder/stackorder/internal/store"
)

type fakeQueue struct {
	mu       sync.Mutex
	events   []*store.Event
	jobs     []*store.Job
	keys     map[string]*store.Job
	retries  []time.Duration
	released []string
	claims   int
	depths   int
}

func (f *fakeQueue) addEvent(id, kind string, mutate ...func(*store.Event)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	ev := &store.Event{ID: id, Kind: kind, Payload: json.RawMessage(`{}`), ReceivedAt: now, RunAfter: now}
	for _, m := range mutate {
		m(ev)
	}
	f.events = append(f.events, ev)
}

func (f *fakeQueue) event(id string) store.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.events {
		if ev.ID == id {
			return *ev
		}
	}
	panic("no event " + id)
}

func (f *fakeQueue) job(kind string) store.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if j.Kind == kind {
			return *j
		}
	}
	panic("no job " + kind)
}

func (f *fakeQueue) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims
}

func (f *fakeQueue) backoffs() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.retries...)
}

func (f *fakeQueue) releases() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.released...)
}

func (f *fakeQueue) allDone() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.events {
		if ev.DoneAt == nil {
			return false
		}
	}
	for _, j := range f.jobs {
		if j.DoneAt == nil {
			return false
		}
	}
	return true
}

func due(done, claimed *time.Time, runAfter, now time.Time) bool {
	return done == nil && claimed == nil && !runAfter.After(now)
}

func (f *fakeQueue) ClaimEvents(_ context.Context, worker string, n int) ([]store.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	now := time.Now().UTC()
	var out []store.Event
	for _, ev := range f.events {
		if len(out) == n {
			break
		}
		if due(ev.DoneAt, ev.ClaimedAt, ev.RunAfter, now) {
			ev.ClaimedBy, ev.ClaimedAt = worker, &now
			out = append(out, *ev)
		}
	}
	return out, nil
}

func (f *fakeQueue) findEvent(id string) (*store.Event, error) {
	for _, ev := range f.events {
		if ev.ID == id {
			return ev, nil
		}
	}
	return nil, fmt.Errorf("event %s: %w", id, store.ErrNotFound)
}

func (f *fakeQueue) CompleteEvent(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ev, err := f.findEvent(id)
	if err != nil {
		return err
	}
	if ev.DoneAt == nil {
		now := time.Now().UTC()
		ev.DoneAt = &now
	}
	return nil
}

func (f *fakeQueue) FailEvent(_ context.Context, id string, cause error, retryAfter time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ev, err := f.findEvent(id)
	if err != nil || ev.DoneAt != nil {
		return fmt.Errorf("fail event %s: %w", id, store.ErrNotFound)
	}
	f.retries = append(f.retries, retryAfter)
	ev.Attempts++
	ev.LastError = cause.Error()
	ev.ClaimedBy, ev.ClaimedAt = "", nil
	ev.RunAfter = time.Now().UTC().Add(retryAfter)
	return nil
}

func (f *fakeQueue) AbandonEvent(_ context.Context, id string, cause error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ev, err := f.findEvent(id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	ev.Attempts++
	ev.LastError = cause.Error()
	ev.DoneAt = &now
	return nil
}

func (f *fakeQueue) EnqueueJob(_ context.Context, kind string, payload json.RawMessage, runAfter time.Time, dedupeKey string) (store.Job, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keys == nil {
		f.keys = map[string]*store.Job{}
	}
	if j, ok := f.keys[dedupeKey]; ok && dedupeKey != "" {
		return *j, false, nil
	}
	now := time.Now().UTC()
	if runAfter.IsZero() {
		runAfter = now
	}
	j := &store.Job{ID: uuid.New(), Kind: kind, Payload: payload, DedupeKey: dedupeKey, RunAfter: runAfter, CreatedAt: now}
	f.jobs = append(f.jobs, j)
	if dedupeKey != "" {
		f.keys[dedupeKey] = j
	}
	return *j, true, nil
}

func (f *fakeQueue) ClaimJobs(_ context.Context, worker string, n int) ([]store.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	var out []store.Job
	for _, j := range f.jobs {
		if len(out) == n {
			break
		}
		if due(j.DoneAt, j.ClaimedAt, j.RunAfter, now) {
			j.ClaimedBy, j.ClaimedAt = worker, &now
			out = append(out, *j)
		}
	}
	return out, nil
}

func (f *fakeQueue) findJob(id uuid.UUID) (*store.Job, error) {
	for _, j := range f.jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return nil, fmt.Errorf("job %s: %w", id, store.ErrNotFound)
}

func (f *fakeQueue) CompleteJob(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, err := f.findJob(id)
	if err != nil {
		return err
	}
	if j.DoneAt == nil {
		now := time.Now().UTC()
		j.DoneAt = &now
	}
	return nil
}

func (f *fakeQueue) FailJob(_ context.Context, id uuid.UUID, cause error, retryAfter time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, err := f.findJob(id)
	if err != nil || j.DoneAt != nil {
		return fmt.Errorf("fail job %s: %w", id, store.ErrNotFound)
	}
	f.retries = append(f.retries, retryAfter)
	j.Attempts++
	j.LastError = cause.Error()
	j.ClaimedBy, j.ClaimedAt = "", nil
	j.RunAfter = time.Now().UTC().Add(retryAfter)
	return nil
}

func (f *fakeQueue) AbandonJob(_ context.Context, id uuid.UUID, cause error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, err := f.findJob(id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	j.Attempts++
	j.LastError = cause.Error()
	j.DoneAt = &now
	return nil
}

func (f *fakeQueue) ReleaseClaims(_ context.Context, worker string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, ev := range f.events {
		if ev.ClaimedBy == worker && ev.ClaimedAt != nil && ev.DoneAt == nil {
			ev.ClaimedBy, ev.ClaimedAt = "", nil
			n++
		}
	}
	for _, j := range f.jobs {
		if j.ClaimedBy == worker && j.ClaimedAt != nil && j.DoneAt == nil {
			j.ClaimedBy, j.ClaimedAt = "", nil
			n++
		}
	}
	if n > 0 {
		f.released = append(f.released, worker)
	}
	return n, nil
}

func (f *fakeQueue) ReleaseStaleClaims(_ context.Context, olderThan time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := time.Now().UTC().Add(-olderThan)
	var n int64
	for _, ev := range f.events {
		if ev.DoneAt == nil && ev.ClaimedAt != nil && ev.ClaimedAt.Before(cutoff) {
			ev.ClaimedBy, ev.ClaimedAt = "", nil
			ev.Attempts++
			ev.LastError = "claim expired"
			n++
		}
	}
	return n, nil
}

func (f *fakeQueue) QueueDepth(context.Context) (store.QueueDepth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.depths++
	now := time.Now().UTC()
	var d store.QueueDepth
	for _, ev := range f.events {
		if due(ev.DoneAt, ev.ClaimedAt, ev.RunAfter, now) {
			d.Events++
		}
	}
	for _, j := range f.jobs {
		if due(j.DoneAt, j.ClaimedAt, j.RunAfter, now) {
			d.Jobs++
		}
	}
	return d, nil
}
