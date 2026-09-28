package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxErrorText = 4 << 10

// Event is a webhook delivery waiting in, or processed by, the queue. ID is
// the GitHub delivery id.
type Event struct {
	ID         string          `db:"id"`
	Kind       string          `db:"kind"`
	Payload    json.RawMessage `db:"payload"`
	ReceivedAt time.Time       `db:"received_at"`
	ClaimedBy  string          `db:"claimed_by"`
	ClaimedAt  *time.Time      `db:"claimed_at"`
	Attempts   int             `db:"attempts"`
	RunAfter   time.Time       `db:"run_after"`
	DoneAt     *time.Time      `db:"done_at"`
	LastError  string          `db:"last_error"`
}

// Job is a unit of internal work, such as a reconciliation or a scheduled
// drift dispatch. DedupeKey is empty when the job has none.
type Job struct {
	ID        uuid.UUID       `db:"id"`
	Kind      string          `db:"kind"`
	Payload   json.RawMessage `db:"payload"`
	DedupeKey string          `db:"dedupe_key"`
	RunAfter  time.Time       `db:"run_after"`
	ClaimedBy string          `db:"claimed_by"`
	ClaimedAt *time.Time      `db:"claimed_at"`
	Attempts  int             `db:"attempts"`
	DoneAt    *time.Time      `db:"done_at"`
	LastError string          `db:"last_error"`
	CreatedAt time.Time       `db:"created_at"`
}

const eventCols = `id, kind, payload, received_at, claimed_by, claimed_at, attempts, run_after, done_at, last_error`

const jobCols = `id, kind, payload, COALESCE(dedupe_key, '') AS dedupe_key, run_after, claimed_by, claimed_at,
	attempts, done_at, last_error, created_at`

// InsertEvent stores a webhook delivery and reports whether it was new; a
// delivery id seen before is ignored, which deduplicates redeliveries.
func (s *Store) InsertEvent(ctx context.Context, id, kind string, payload json.RawMessage) (bool, error) {
	const op = "insert event"
	if id == "" || kind == "" {
		return false, invalid(op, "id and kind are required")
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO events (id, kind, payload) VALUES ($1, $2, $3::jsonb)
		ON CONFLICT (id) DO NOTHING`, id, kind, jsonPayload(payload))
	if err != nil {
		return false, wrap(op, err)
	}
	return tag.RowsAffected() == 1, nil
}

// GetEvent returns one event.
func (s *Store) GetEvent(ctx context.Context, id string) (Event, error) {
	out, err := queryOne[Event](ctx, s.db, `SELECT `+eventCols+` FROM events WHERE id = $1`, id)
	return out, wrap("get event", err)
}

// ClaimEvents claims up to n due, unclaimed events for worker with SELECT
// ... FOR UPDATE SKIP LOCKED, so concurrent workers never claim the same
// event. Events come back oldest due first.
func (s *Store) ClaimEvents(ctx context.Context, worker string, n int) ([]Event, error) {
	if n <= 0 {
		return nil, nil
	}
	out, err := queryAll[Event](ctx, s.db, `
		WITH c AS (
			SELECT id FROM events
			WHERE done_at IS NULL AND claimed_at IS NULL AND run_after <= now()
			ORDER BY run_after, received_at, id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE events e SET claimed_by = $1, claimed_at = now()
		FROM c WHERE e.id = c.id
		RETURNING e.id, e.kind, e.payload, e.received_at, e.claimed_by, e.claimed_at, e.attempts,
		          e.run_after, e.done_at, e.last_error`, worker, n)
	if err != nil {
		return nil, wrap("claim events", err)
	}
	slices.SortFunc(out, func(a, b Event) int {
		if c := a.RunAfter.Compare(b.RunAfter); c != 0 {
			return c
		}
		return a.ReceivedAt.Compare(b.ReceivedAt)
	})
	return out, nil
}

// CompleteEvent marks an event done. Completing it again keeps the first
// completion time.
func (s *Store) CompleteEvent(ctx context.Context, id string) error {
	return s.execOne(ctx, "complete event",
		`UPDATE events SET done_at = COALESCE(done_at, now()) WHERE id = $1`, id)
}

// FailEvent records a failed attempt: attempts is incremented, the claim is
// released and the event becomes due again after retryAfter. It returns
// ErrNotFound for an unknown or already completed event.
func (s *Store) FailEvent(ctx context.Context, id string, cause error, retryAfter time.Duration) error {
	return s.execOne(ctx, "fail event", `
		UPDATE events SET attempts = attempts + 1, last_error = $2, claimed_by = '', claimed_at = NULL,
			run_after = now() + $3::bigint * interval '1 microsecond'
		WHERE id = $1 AND done_at IS NULL`, id, errorText(cause), micros(retryAfter))
}

// AbandonEvent gives up on an event: it is marked done with its last
// error recorded, and kept until pruned.
func (s *Store) AbandonEvent(ctx context.Context, id string, cause error) error {
	return s.execOne(ctx, "abandon event", `
		UPDATE events SET attempts = attempts + 1, last_error = $2, done_at = COALESCE(done_at, now())
		WHERE id = $1`, id, errorText(cause))
}

// EnqueueJob adds a job due at runAfter (now when zero). With a non-empty
// dedupeKey an existing job with that key, done or not, is returned instead
// with inserted == false; keys stay taken until the job is pruned.
func (s *Store) EnqueueJob(ctx context.Context, kind string, payload json.RawMessage, runAfter time.Time, dedupeKey string) (Job, bool, error) {
	const op = "enqueue job"
	if kind == "" {
		return Job{}, false, invalid(op, "kind is required")
	}
	var at *time.Time
	if !runAfter.IsZero() {
		at = &runAfter
	}
	out, err := queryOne[Job](ctx, s.db, `
		INSERT INTO jobs (kind, payload, run_after, dedupe_key)
		VALUES ($1, $2::jsonb, COALESCE($3::timestamptz, now()), NULLIF($4::text, ''))
		ON CONFLICT (dedupe_key) DO NOTHING
		RETURNING `+jobCols, kind, jsonPayload(payload), at, dedupeKey)
	if err == nil {
		return out, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) || dedupeKey == "" {
		return Job{}, false, wrap(op, err)
	}
	out, err = queryOne[Job](ctx, s.db, `SELECT `+jobCols+` FROM jobs WHERE dedupe_key = $1`, dedupeKey)
	return out, false, wrap(op, err)
}

// GetJob returns one job.
func (s *Store) GetJob(ctx context.Context, id uuid.UUID) (Job, error) {
	out, err := queryOne[Job](ctx, s.db, `SELECT `+jobCols+` FROM jobs WHERE id = $1`, id)
	return out, wrap("get job", err)
}

// ClaimJobs claims up to n due, unclaimed jobs for worker with SELECT ...
// FOR UPDATE SKIP LOCKED. Jobs come back oldest due first.
func (s *Store) ClaimJobs(ctx context.Context, worker string, n int) ([]Job, error) {
	if n <= 0 {
		return nil, nil
	}
	out, err := queryAll[Job](ctx, s.db, `
		WITH c AS (
			SELECT id FROM jobs
			WHERE done_at IS NULL AND claimed_at IS NULL AND run_after <= now()
			ORDER BY run_after, created_at, id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE jobs j SET claimed_by = $1, claimed_at = now()
		FROM c WHERE j.id = c.id
		RETURNING j.id, j.kind, j.payload, COALESCE(j.dedupe_key, '') AS dedupe_key, j.run_after,
		          j.claimed_by, j.claimed_at, j.attempts, j.done_at, j.last_error, j.created_at`, worker, n)
	if err != nil {
		return nil, wrap("claim jobs", err)
	}
	slices.SortFunc(out, func(a, b Job) int {
		if c := a.RunAfter.Compare(b.RunAfter); c != 0 {
			return c
		}
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return out, nil
}

// CompleteJob marks a job done.
func (s *Store) CompleteJob(ctx context.Context, id uuid.UUID) error {
	return s.execOne(ctx, "complete job",
		`UPDATE jobs SET done_at = COALESCE(done_at, now()) WHERE id = $1`, id)
}

// FailJob records a failed attempt like FailEvent.
func (s *Store) FailJob(ctx context.Context, id uuid.UUID, cause error, retryAfter time.Duration) error {
	return s.execOne(ctx, "fail job", `
		UPDATE jobs SET attempts = attempts + 1, last_error = $2, claimed_by = '', claimed_at = NULL,
			run_after = now() + $3::bigint * interval '1 microsecond'
		WHERE id = $1 AND done_at IS NULL`, id, errorText(cause), micros(retryAfter))
}

// AbandonJob gives up on a job like AbandonEvent.
func (s *Store) AbandonJob(ctx context.Context, id uuid.UUID, cause error) error {
	return s.execOne(ctx, "abandon job", `
		UPDATE jobs SET attempts = attempts + 1, last_error = $2, done_at = COALESCE(done_at, now())
		WHERE id = $1`, id, errorText(cause))
}

// ReleaseStaleClaims returns events and jobs claimed more than olderThan
// ago and still not done to the queue, counting the lost claim as an
// attempt, and reports how many rows were released in total.
func (s *Store) ReleaseStaleClaims(ctx context.Context, olderThan time.Duration) (int64, error) {
	var total int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		for _, q := range []string{
			`UPDATE events SET claimed_by = '', claimed_at = NULL, attempts = attempts + 1, last_error = 'claim expired'
			 WHERE done_at IS NULL AND claimed_at < now() - $1::bigint * interval '1 microsecond'`,
			`UPDATE jobs SET claimed_by = '', claimed_at = NULL, attempts = attempts + 1, last_error = 'claim expired'
			 WHERE done_at IS NULL AND claimed_at < now() - $1::bigint * interval '1 microsecond'`,
		} {
			tag, err := tx.Exec(ctx, q, micros(olderThan))
			if err != nil {
				return err
			}
			total += tag.RowsAffected()
		}
		return nil
	})
	return total, wrap("release stale claims", err)
}

// ReleaseClaims returns every unfinished event and job claimed by worker to
// the queue at once, without counting an attempt, and reports how many rows
// were released. A worker calls it when it stops before starting the work
// it claimed.
func (s *Store) ReleaseClaims(ctx context.Context, worker string) (int64, error) {
	const op = "release claims"
	if worker == "" {
		return 0, invalid(op, "worker is required")
	}
	var total int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		for _, q := range []string{
			`UPDATE events SET claimed_by = '', claimed_at = NULL
			 WHERE claimed_by = $1 AND claimed_at IS NOT NULL AND done_at IS NULL`,
			`UPDATE jobs SET claimed_by = '', claimed_at = NULL
			 WHERE claimed_by = $1 AND claimed_at IS NOT NULL AND done_at IS NULL`,
		} {
			tag, err := tx.Exec(ctx, q, worker)
			if err != nil {
				return err
			}
			total += tag.RowsAffected()
		}
		return nil
	})
	return total, wrap(op, err)
}

// QueueDepth counts the rows of each queue that are due, unclaimed and not
// done: the backlog no worker has picked up yet.
type QueueDepth struct {
	Events int64
	Jobs   int64
}

// QueueDepth returns the current backlog of the events and jobs queues.
func (s *Store) QueueDepth(ctx context.Context) (QueueDepth, error) {
	var d QueueDepth
	err := s.db.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM events WHERE done_at IS NULL AND claimed_at IS NULL AND run_after <= now()),
			(SELECT count(*) FROM jobs WHERE done_at IS NULL AND claimed_at IS NULL AND run_after <= now())`,
	).Scan(&d.Events, &d.Jobs)
	return d, wrap("queue depth", err)
}

// PruneEvents deletes events received more than olderThan ago that are done
// or not currently claimed, and returns how many were deleted.
func (s *Store) PruneEvents(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM events
		WHERE received_at < now() - $1::bigint * interval '1 microsecond'
		  AND (done_at IS NOT NULL OR claimed_at IS NULL)`, micros(olderThan))
	if err != nil {
		return 0, wrap("prune events", err)
	}
	return tag.RowsAffected(), nil
}

// PruneJobs deletes jobs completed more than olderThan ago and returns how
// many were deleted.
func (s *Store) PruneJobs(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM jobs WHERE done_at < now() - $1::bigint * interval '1 microsecond'`, micros(olderThan))
	if err != nil {
		return 0, wrap("prune jobs", err)
	}
	return tag.RowsAffected(), nil
}

// PruneQueue runs PruneEvents and PruneJobs in one transaction.
func (s *Store) PruneQueue(ctx context.Context, olderThan time.Duration) (events, jobs int64, err error) {
	err = s.InTx(ctx, func(tx *Store) error {
		var err error
		if events, err = tx.PruneEvents(ctx, olderThan); err != nil {
			return err
		}
		jobs, err = tx.PruneJobs(ctx, olderThan)
		return err
	})
	return events, jobs, err
}

func jsonPayload(p json.RawMessage) string {
	if len(p) == 0 {
		return "{}"
	}
	return string(p)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	msg, _ := truncateUTF8(err.Error(), maxErrorText)
	return msg
}
