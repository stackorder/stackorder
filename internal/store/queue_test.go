//go:build integration

package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/store"
)

func TestInsertEventDedup(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name    string
		id      string
		kind    string
		payload string
		want    bool
		wantErr error
	}{
		{"new delivery", "d1", "push", `{"ref":"refs/heads/main"}`, true, nil},
		{"redelivery", "d1", "push", `{"ref":"other"}`, false, nil},
		{"empty payload", "d2", "ping", ``, true, nil},
		{"missing id", "", "push", `{}`, false, store.ErrInvalid},
		{"invalid json", "d3", "push", `{`, false, store.ErrInvalid},
		{"nul escape jsonb refuses", "d4", "push", `{"msg":"a\u0000b"}`, false, store.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inserted, err := f.s.InsertEvent(f.ctx, tc.id, tc.kind, json.RawMessage(tc.payload))
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, inserted)
		})
	}
	e, err := f.s.GetEvent(f.ctx, "d1")
	require.NoError(t, err)
	assert.JSONEq(t, `{"ref":"refs/heads/main"}`, string(e.Payload), "the first delivery wins")
	e, err = f.s.GetEvent(f.ctx, "d2")
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(e.Payload))
}

func TestClaimEventsExclusive(t *testing.T) {
	f := newFixture(t)
	const (
		events   = 200
		claimers = 8
	)
	for i := range events {
		inserted, err := f.s.InsertEvent(f.ctx, fmt.Sprintf("delivery-%03d", i), "push", json.RawMessage(`{"n":`+fmt.Sprint(i)+`}`))
		require.NoError(t, err)
		require.True(t, inserted)
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		claims = map[string][]string{}
	)
	for w := range claimers {
		worker := fmt.Sprintf("worker-%d", w)
		wg.Go(func() {
			for {
				batch, err := f.s.ClaimEvents(f.ctx, worker, 7)
				if !assert.NoError(t, err) || len(batch) == 0 {
					return
				}
				for _, e := range batch {
					assert.Equal(t, worker, e.ClaimedBy)
					assert.NotNil(t, e.ClaimedAt)
					mu.Lock()
					claims[e.ID] = append(claims[e.ID], worker)
					mu.Unlock()
					assert.NoError(t, f.s.CompleteEvent(f.ctx, e.ID))
				}
			}
		})
	}
	wg.Wait()

	require.Len(t, claims, events, "every event was claimed")
	for id, workers := range claims {
		assert.Len(t, workers, 1, "event %s claimed more than once", id)
	}
	var pending int
	require.NoError(t, f.s.Pool().QueryRow(f.ctx, `SELECT count(*) FROM events WHERE done_at IS NULL`).Scan(&pending))
	assert.Zero(t, pending, "every event was completed")
	left, err := f.s.ClaimEvents(f.ctx, "late", 10)
	require.NoError(t, err)
	assert.Empty(t, left)
}

func TestFailEventBackoffAndAttempts(t *testing.T) {
	f := newFixture(t)
	_, err := f.s.InsertEvent(f.ctx, "e1", "pull_request", nil)
	require.NoError(t, err)

	claimed, err := f.s.ClaimEvents(f.ctx, "w1", 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	require.NoError(t, f.s.FailEvent(f.ctx, "e1", errors.New("github 502"), time.Hour))
	e, err := f.s.GetEvent(f.ctx, "e1")
	require.NoError(t, err)
	assert.Equal(t, 1, e.Attempts)
	assert.Equal(t, "github 502", e.LastError)
	assert.Empty(t, e.ClaimedBy)
	assert.Nil(t, e.ClaimedAt)
	assert.WithinDuration(t, time.Now().Add(time.Hour), e.RunAfter, time.Minute)

	none, err := f.s.ClaimEvents(f.ctx, "w1", 10)
	require.NoError(t, err)
	assert.Empty(t, none, "a failed event waits out its backoff")

	require.NoError(t, f.s.FailEvent(f.ctx, "e1", errors.New(strings.Repeat("x", 10000)), -time.Second))
	again, err := f.s.ClaimEvents(f.ctx, "w2", 10)
	require.NoError(t, err)
	require.Len(t, again, 1, "due again once the backoff elapsed")
	assert.Equal(t, 2, again[0].Attempts)
	assert.Len(t, again[0].LastError, 4096, "error text is capped")

	require.NoError(t, f.s.AbandonEvent(f.ctx, "e1", errors.New("gave up")))
	e, err = f.s.GetEvent(f.ctx, "e1")
	require.NoError(t, err)
	assert.NotNil(t, e.DoneAt)
	assert.Equal(t, 3, e.Attempts)
	assert.Equal(t, "gave up", e.LastError)
	require.ErrorIs(t, f.s.FailEvent(f.ctx, "e1", nil, 0), store.ErrNotFound, "done events cannot fail")
	require.ErrorIs(t, f.s.CompleteEvent(f.ctx, "nope"), store.ErrNotFound)
}

func TestReleaseStaleClaims(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"stale", "fresh", "done"} {
		_, err := f.s.InsertEvent(f.ctx, id, "push", nil)
		require.NoError(t, err)
	}
	job, _, err := f.s.EnqueueJob(f.ctx, "reconcile", nil, time.Time{}, "")
	require.NoError(t, err)
	claimed, err := f.s.ClaimEvents(f.ctx, "crashed", 10)
	require.NoError(t, err)
	require.Len(t, claimed, 3)
	_, err = f.s.ClaimJobs(f.ctx, "crashed", 10)
	require.NoError(t, err)
	require.NoError(t, f.s.CompleteEvent(f.ctx, "done"))
	f.exec(`UPDATE events SET claimed_at = now() - interval '10 minutes' WHERE id IN ('stale', 'done')`)
	f.exec(`UPDATE jobs SET claimed_at = now() - interval '10 minutes'`)

	n, err := f.s.ReleaseStaleClaims(f.ctx, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "the stale event and the stale job")

	reclaimed, err := f.s.ClaimEvents(f.ctx, "survivor", 10)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	assert.Equal(t, "stale", reclaimed[0].ID)
	assert.Equal(t, 1, reclaimed[0].Attempts)
	assert.Equal(t, "claim expired", reclaimed[0].LastError)

	jobs, err := f.s.ClaimJobs(f.ctx, "survivor", 10)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, job.ID, jobs[0].ID)
}

func TestJobs(t *testing.T) {
	f := newFixture(t)
	later := time.Now().Add(time.Hour)

	cases := []struct {
		name      string
		kind      string
		runAfter  time.Time
		dedupe    string
		wantNew   bool
		wantErr   error
		wantDedup string
	}{
		{name: "plain", kind: "reconcile", wantNew: true},
		{name: "plain twice is two jobs", kind: "reconcile", wantNew: true},
		{name: "keyed", kind: "drift", dedupe: "drift:stack:1:0600", wantNew: true, wantDedup: "drift:stack:1:0600"},
		{name: "keyed again", kind: "drift", dedupe: "drift:stack:1:0600", wantNew: false, wantDedup: "drift:stack:1:0600"},
		{name: "scheduled", kind: "reminder", runAfter: later, wantNew: true},
		{name: "missing kind", wantErr: store.ErrInvalid},
	}
	jobs := map[string]store.Job{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j, inserted, err := f.s.EnqueueJob(f.ctx, tc.kind, json.RawMessage(`{"a":1}`), tc.runAfter, tc.dedupe)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantNew, inserted)
			assert.Equal(t, tc.wantDedup, j.DedupeKey)
			jobs[tc.name] = j
		})
	}
	assert.Equal(t, jobs["keyed"].ID, jobs["keyed again"].ID)

	claimed, err := f.s.ClaimJobs(f.ctx, "w", 10)
	require.NoError(t, err)
	require.Len(t, claimed, 3, "the scheduled job is not due yet")
	assert.JSONEq(t, `{"a":1}`, string(claimed[0].Payload))

	require.NoError(t, f.s.CompleteJob(f.ctx, claimed[0].ID))
	require.NoError(t, f.s.FailJob(f.ctx, claimed[1].ID, errors.New("boom"), -time.Second))
	require.NoError(t, f.s.AbandonJob(f.ctx, claimed[2].ID, errors.New("poison")))

	got, err := f.s.GetJob(f.ctx, claimed[1].ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Attempts)
	assert.Equal(t, "boom", got.LastError)
	retry, err := f.s.ClaimJobs(f.ctx, "w", 10)
	require.NoError(t, err)
	require.Len(t, retry, 1)
	assert.Equal(t, claimed[1].ID, retry[0].ID)

	abandoned, err := f.s.GetJob(f.ctx, claimed[2].ID)
	require.NoError(t, err)
	assert.NotNil(t, abandoned.DoneAt)

	require.ErrorIs(t, f.s.CompleteJob(f.ctx, uuid.New()), store.ErrNotFound)
	_, err = f.s.GetJob(f.ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
	none, err := f.s.ClaimJobs(f.ctx, "w", 0)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestPruneQueue(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"old-done", "old-open", "old-claimed", "new"} {
		_, err := f.s.InsertEvent(f.ctx, id, "push", nil)
		require.NoError(t, err)
	}
	require.NoError(t, f.s.CompleteEvent(f.ctx, "old-done"))
	f.exec(`UPDATE events SET claimed_by = 'w', claimed_at = now() WHERE id = 'old-claimed'`)
	f.exec(`UPDATE events SET received_at = now() - interval '8 days' WHERE id LIKE 'old-%'`)

	oldJob, _, err := f.s.EnqueueJob(f.ctx, "k", nil, time.Time{}, "old")
	require.NoError(t, err)
	openJob, _, err := f.s.EnqueueJob(f.ctx, "k", nil, time.Time{}, "open")
	require.NoError(t, err)
	require.NoError(t, f.s.CompleteJob(f.ctx, oldJob.ID))
	f.exec(`UPDATE jobs SET done_at = now() - interval '8 days' WHERE id = $1`, oldJob.ID)
	f.exec(`UPDATE jobs SET created_at = now() - interval '30 days' WHERE id = $1`, openJob.ID)

	events, jobs, err := f.s.PruneQueue(f.ctx, 7*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(2), events, "old done and old unclaimed events")
	assert.Equal(t, int64(1), jobs, "only completed jobs")

	_, err = f.s.GetEvent(f.ctx, "old-claimed")
	require.NoError(t, err, "in-flight events are kept")
	_, inserted, err := f.s.EnqueueJob(f.ctx, "k", nil, time.Time{}, "old")
	require.NoError(t, err)
	assert.True(t, inserted, "a pruned job frees its dedupe key")
}
