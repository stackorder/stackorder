package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// MaxPlanTextBytes caps the plan text stored per stack; longer text is cut
// at a UTF-8 boundary and flagged as truncated.
const MaxPlanTextBytes = 256 << 10

var (
	runStatuses = []v1.RunStatus{
		v1.RunPending, v1.RunPlanning, v1.RunPlanned, v1.RunApplying,
		v1.RunApplied, v1.RunFailed, v1.RunUnconfirmed, v1.RunSuperseded,
	}
	stackStatuses = []v1.StackStatus{
		v1.StackPending, v1.StackPlanning, v1.StackPlanned, v1.StackApplying, v1.StackApplied,
		v1.StackFailed, v1.StackBlocked, v1.StackNoop, v1.StackUnconfirmed, v1.StackUnknown, v1.StackSkipped,
	}
	startedStatuses     = []string{string(v1.RunPlanning), string(v1.RunApplying)}
	terminalRunStatuses = statusesWhere(runStatuses, v1.RunStatus.Terminal)
	finishedRunStatuses = slices.Concat([]string{string(v1.RunPlanned)}, terminalRunStatuses)
	finishedStackStates = slices.Concat([]string{string(v1.StackPlanned)}, statusesWhere(stackStatuses, v1.StackStatus.Terminal))
)

func statusesWhere[T ~string](all []T, keep func(T) bool) []string {
	var out []string
	for _, s := range all {
		if keep(s) {
			out = append(out, string(s))
		}
	}
	return out
}

// Run is one run of a repository at a commit.
type Run struct {
	ID                 uuid.UUID    `db:"id"`
	RepoID             int64        `db:"repo_id"`
	Repo               string       `db:"repo"`
	SHA                string       `db:"sha"`
	BaseSHA            string       `db:"base_sha"`
	PRNumber           int          `db:"pr_number"`
	Trigger            v1.Trigger   `db:"trigger"`
	Mode               v1.RunMode   `db:"mode"`
	Status             v1.RunStatus `db:"status"`
	RequestedBy        string       `db:"requested_by"`
	GraphID            *uuid.UUID   `db:"graph_id"`
	Waves              int          `db:"waves"`
	CurrentWave        int          `db:"current_wave"`
	Warnings           []string     `db:"warnings"`
	WorkflowRunID      int64        `db:"workflow_run_id"`
	WorkflowRunAttempt int          `db:"workflow_run_attempt"`
	// CheckRuns maps the names of the GitHub check runs created for the
	// run to their ids.
	CheckRuns  map[string]int64 `db:"check_runs"`
	CreatedAt  time.Time        `db:"created_at"`
	StartedAt  *time.Time       `db:"started_at"`
	FinishedAt *time.Time       `db:"finished_at"`
}

// ToV1 converts the row without its stacks; see RunDetail for the full
// view.
func (r Run) ToV1() v1.Run {
	out := v1.Run{
		ID:          r.ID.String(),
		Repo:        r.Repo,
		SHA:         r.SHA,
		BaseSHA:     r.BaseSHA,
		PRNumber:    r.PRNumber,
		Trigger:     r.Trigger,
		Mode:        r.Mode,
		Status:      r.Status,
		RequestedBy: r.RequestedBy,
		CreatedAt:   r.CreatedAt.UTC(),
		StartedAt:   utc(r.StartedAt),
		FinishedAt:  utc(r.FinishedAt),
		Waves:       r.Waves,
		CurrentWave: r.CurrentWave,
	}
	if len(r.Warnings) > 0 {
		out.Warnings = r.Warnings
	}
	return out
}

// CreateRunParams describes a new run. Status defaults to pending.
type CreateRunParams struct {
	RepoID             int64
	SHA                string
	BaseSHA            string
	PRNumber           int
	Trigger            v1.Trigger
	Mode               v1.RunMode
	Status             v1.RunStatus
	RequestedBy        string
	WorkflowRunID      int64
	WorkflowRunAttempt int
}

// RunFilter narrows ListRuns. Zero fields match everything.
type RunFilter struct {
	RepoID   int64
	PRNumber int
	SHA      string
	Status   v1.RunStatus
	Mode     v1.RunMode
	// Limit defaults to 50 and is capped at 500.
	Limit int
	// Cursor is the value returned by the previous page.
	Cursor string
}

const runCols = `r.id, r.repo_id, p.full_name AS repo, r.sha, r.base_sha, r.pr_number, r.trigger,
	r.mode, r.status, r.requested_by, r.graph_id, r.waves, r.current_wave, r.warnings,
	r.workflow_run_id, r.workflow_run_attempt, r.check_runs, r.created_at, r.started_at, r.finished_at`

const runSelect = `SELECT ` + runCols + ` FROM runs r JOIN repos p ON p.id = r.repo_id`

const runFromCTE = ` SELECT ` + runCols + ` FROM r JOIN repos p ON p.id = r.repo_id`

// CreateRun inserts a run. started_at and finished_at are stamped when the
// initial status implies them.
func (s *Store) CreateRun(ctx context.Context, p CreateRunParams) (Run, error) {
	const op = "create run"
	if p.RepoID == 0 || p.SHA == "" || p.Trigger == "" || p.Mode == "" {
		return Run{}, invalid(op, "repo, sha, trigger and mode are required")
	}
	if p.Status == "" {
		p.Status = v1.RunPending
	}
	out, err := queryOne[Run](ctx, s.db, `
		WITH r AS (
			INSERT INTO runs (repo_id, sha, base_sha, pr_number, trigger, mode, status, requested_by,
			                  workflow_run_id, workflow_run_attempt, started_at, finished_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7::text, $8, $9, $10,
			        CASE WHEN $7::text = ANY($11::text[]) THEN now() END,
			        CASE WHEN $7::text = ANY($12::text[]) THEN now() END)
			RETURNING *
		)`+runFromCTE,
		p.RepoID, p.SHA, p.BaseSHA, p.PRNumber, p.Trigger, p.Mode, p.Status, p.RequestedBy,
		p.WorkflowRunID, p.WorkflowRunAttempt, startedStatuses, finishedRunStatuses)
	return out, wrap(op, err)
}

// FindOpenRun returns the newest non-terminal run for the repository,
// commit, pull request (0 for none) and mode.
func (s *Store) FindOpenRun(ctx context.Context, repoID int64, sha string, prNumber int, mode v1.RunMode) (Run, error) {
	out, err := queryOne[Run](ctx, s.db, runSelect+`
		WHERE r.repo_id = $1 AND r.sha = $2 AND r.pr_number = $3 AND r.mode = $4
		  AND NOT (r.status = ANY($5::text[]))
		ORDER BY r.created_at DESC, r.id DESC LIMIT 1`,
		repoID, sha, prNumber, mode, terminalRunStatuses)
	return out, wrap("find open run", err)
}

// FindOrCreateRun returns the open run matching the parameters' repository,
// commit, pull request and mode, or creates one. Concurrent callers with the
// same identity serialise on a transaction-level advisory lock, so exactly
// one run is created.
func (s *Store) FindOrCreateRun(ctx context.Context, p CreateRunParams) (Run, bool, error) {
	var (
		out     Run
		created bool
	)
	key := fmt.Sprintf("run:%d:%s:%d:%s", p.RepoID, p.SHA, p.PRNumber, p.Mode)
	err := s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
			return wrap("find or create run: lock", err)
		}
		run, err := tx.FindOpenRun(ctx, p.RepoID, p.SHA, p.PRNumber, p.Mode)
		switch {
		case err == nil:
			out = run
			return nil
		case !errors.Is(err, ErrNotFound):
			return err
		}
		out, err = tx.CreateRun(ctx, p)
		created = err == nil
		return err
	})
	return out, created, err
}

// GetRun returns one run without its stacks.
func (s *Store) GetRun(ctx context.Context, id uuid.UUID) (Run, error) {
	out, err := queryOne[Run](ctx, s.db, runSelect+` WHERE r.id = $1`, id)
	return out, wrap("get run", err)
}

// ListRuns returns runs newest first and the cursor of the next page, empty
// on the last page.
func (s *Store) ListRuns(ctx context.Context, f RunFilter) ([]Run, string, error) {
	const op = "list runs"
	c, err := decodeCursor(op, f.Cursor)
	if err != nil {
		return nil, "", err
	}
	at, id := c.args()
	limit := pageSize(f.Limit)
	out, err := queryAll[Run](ctx, s.db, runSelect+`
		WHERE ($1::bigint = 0 OR r.repo_id = $1::bigint)
		  AND ($2::integer = 0 OR r.pr_number = $2::integer)
		  AND ($3::text = '' OR r.sha = $3::text)
		  AND ($4::text = '' OR r.status = $4::text)
		  AND ($5::text = '' OR r.mode = $5::text)
		  AND ($6::timestamptz IS NULL OR (r.created_at, r.id) < ($6::timestamptz, $7::uuid))
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT $8`,
		f.RepoID, f.PRNumber, f.SHA, string(f.Status), string(f.Mode), at, id, limit+1)
	if err != nil {
		return nil, "", wrap(op, err)
	}
	var next string
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

// UpdateRunStatus moves a run to status. started_at is stamped the first
// time the run enters planning or applying; finished_at is stamped when it
// enters planned or a terminal status and cleared when it starts again.
// When from is given the update only happens if the current status is one
// of them, and ErrConflict is returned otherwise.
func (s *Store) UpdateRunStatus(ctx context.Context, id uuid.UUID, status v1.RunStatus, from ...v1.RunStatus) (Run, error) {
	const op = "update run status"
	out, err := queryOne[Run](ctx, s.db, `
		WITH r AS (
			UPDATE runs SET
				status = $2::text,
				started_at = CASE WHEN $2::text = ANY($3::text[]) THEN COALESCE(started_at, now()) ELSE started_at END,
				finished_at = CASE WHEN $2::text = ANY($4::text[]) THEN COALESCE(finished_at, now()) ELSE NULL END
			WHERE id = $1 AND (cardinality($5::text[]) = 0 OR status = ANY($5::text[]))
			RETURNING *
		)`+runFromCTE,
		id, status, startedStatuses, finishedRunStatuses, strs(from))
	if errors.Is(err, pgx.ErrNoRows) && len(from) > 0 {
		return out, s.guardError(ctx, op, `SELECT 1 FROM runs WHERE id = $1`, id)
	}
	return out, wrap(op, err)
}

func (s *Store) guardError(ctx context.Context, op, existsQuery string, args ...any) error {
	var one int
	err := s.db.QueryRow(ctx, existsQuery, args...).Scan(&one)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return notFound(op)
	case err != nil:
		return wrap(op, err)
	}
	return fmt.Errorf("store: %s: status guard failed: %w", op, ErrConflict)
}

// SetRunGraph records the graph a run was resolved against, its number of
// waves and the resolution warnings.
func (s *Store) SetRunGraph(ctx context.Context, id, graphID uuid.UUID, waves int, warnings []string) error {
	return s.execOne(ctx, "set run graph", `
		UPDATE runs SET graph_id = $2, waves = $3, warnings = $4 WHERE id = $1`,
		id, graphID, waves, nonNil(warnings))
}

// SetRunWave records the wave currently being applied.
func (s *Store) SetRunWave(ctx context.Context, id uuid.UUID, wave int) error {
	return s.execOne(ctx, "set run wave", `UPDATE runs SET current_wave = $2 WHERE id = $1`, id, wave)
}

// AdvanceRunWave moves the run's current wave forward to wave and reports
// whether it moved; a wave at or behind the current one is left alone, so
// concurrent callers can never move a run back.
func (s *Store) AdvanceRunWave(ctx context.Context, id uuid.UUID, wave int) (bool, error) {
	const op = "advance run wave"
	tag, err := s.db.Exec(ctx, `UPDATE runs SET current_wave = $2 WHERE id = $1 AND current_wave < $2`, id, wave)
	if err != nil {
		return false, wrap(op, err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	var one int
	if err := s.db.QueryRow(ctx, `SELECT 1 FROM runs WHERE id = $1`, id).Scan(&one); err != nil {
		return false, wrap(op, err)
	}
	return false, nil
}

// AddRunWarning appends a warning to a run unless it already carries it.
func (s *Store) AddRunWarning(ctx context.Context, id uuid.UUID, warning string) error {
	const op = "add run warning"
	if warning == "" {
		return invalid(op, "warning is required")
	}
	return s.execOne(ctx, op, `
		UPDATE runs SET warnings = CASE WHEN warnings @> jsonb_build_array($2::text) THEN warnings
		                                ELSE warnings || jsonb_build_array($2::text) END
		WHERE id = $1`, id, warning)
}

// SetRunWorkflowRun records the Actions workflow run and attempt that
// reported for a run.
func (s *Store) SetRunWorkflowRun(ctx context.Context, id uuid.UUID, workflowRunID int64, attempt int) error {
	return s.execOne(ctx, "set run workflow run", `
		UPDATE runs SET workflow_run_id = $2, workflow_run_attempt = $3 WHERE id = $1`,
		id, workflowRunID, attempt)
}

// SetRunCheckRun records the id of a GitHub check run created for a run
// under its check run name, replacing an id recorded before under the same
// name.
func (s *Store) SetRunCheckRun(ctx context.Context, id uuid.UUID, name string, checkRunID int64) error {
	const op = "set run check run"
	if name == "" || checkRunID <= 0 {
		return invalid(op, "name and a positive check run id are required")
	}
	return s.execOne(ctx, op, `
		UPDATE runs SET check_runs = check_runs || jsonb_build_object($2::text, $3::bigint) WHERE id = $1`,
		id, name, checkRunID)
}

// FindRunByWorkflowRun returns the newest run of the repository that
// recorded workflowRunID as its Actions workflow run, or ErrNotFound.
func (s *Store) FindRunByWorkflowRun(ctx context.Context, repoID, workflowRunID int64) (Run, error) {
	const op = "find run by workflow run"
	if workflowRunID <= 0 {
		return Run{}, notFound(op)
	}
	out, err := queryOne[Run](ctx, s.db, runSelect+`
		WHERE r.repo_id = $1 AND r.workflow_run_id = $2
		ORDER BY r.created_at DESC, r.id DESC LIMIT 1`, repoID, workflowRunID)
	return out, wrap(op, err)
}

// FindRunForStack returns the newest run of the repository in the given
// mode that was created at or after since and has a row for the stack.
func (s *Store) FindRunForStack(ctx context.Context, repoID int64, stackID uuid.UUID, mode v1.RunMode, since time.Time) (Run, error) {
	out, err := queryOne[Run](ctx, s.db, runSelect+`
		WHERE r.repo_id = $1 AND r.mode = $3 AND r.created_at >= $4
		  AND EXISTS (SELECT 1 FROM run_stacks rs WHERE rs.run_id = r.id AND rs.stack_id = $2)
		ORDER BY r.created_at DESC, r.id DESC LIMIT 1`, repoID, stackID, mode, since)
	return out, wrap("find run for stack", err)
}

// SupersedeRuns moves every non-terminal run of a pull request whose SHA
// differs from exceptSHA to superseded and returns how many moved. A zero
// prNumber matches nothing.
func (s *Store) SupersedeRuns(ctx context.Context, repoID int64, prNumber int, exceptSHA string) (int, error) {
	if prNumber <= 0 {
		return 0, nil
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE runs SET status = 'superseded', finished_at = COALESCE(finished_at, now())
		WHERE repo_id = $1 AND pr_number = $2 AND sha <> $3 AND NOT (status = ANY($4::text[]))`,
		repoID, prNumber, exceptSHA, terminalRunStatuses)
	if err != nil {
		return 0, wrap("supersede runs", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) execOne(ctx context.Context, op, query string, args ...any) error {
	tag, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return notFound(op)
	}
	return nil
}

// RunStack is one stack's row inside a run, joined with the stack's
// identity.
type RunStack struct {
	RunID             uuid.UUID       `db:"run_id"`
	StackID           uuid.UUID       `db:"stack_id"`
	Key               string          `db:"key"`
	Path              string          `db:"path"`
	Workspace         string          `db:"workspace"`
	Wave              int             `db:"wave"`
	Mode              v1.RunMode      `db:"mode"`
	Status            v1.StackStatus  `db:"status"`
	Reasons           []v1.Reason     `db:"reasons"`
	Environment       string          `db:"environment"`
	Adds              int             `db:"adds"`
	Changes           int             `db:"changes"`
	Destroys          int             `db:"destroys"`
	Replaces          int             `db:"replaces"`
	HasChanges        bool            `db:"has_changes"`
	ExitCode          *int            `db:"exit_code"`
	JobURL            string          `db:"job_url"`
	PlanArtifact      string          `db:"plan_artifact"`
	PlanRunID         int64           `db:"plan_run_id"`
	Summary           *v1.PlanSummary `db:"summary"`
	PlanText          string          `db:"plan_text"`
	PlanTextTruncated bool            `db:"plan_text_truncated"`
	ErrorText         string          `db:"error_text"`
	// PlanURL points at the full plan text in the optional artifact store.
	PlanURL string `db:"plan_url"`
	// PlanOutput is the stack's effective plan_output setting.
	PlanOutput string `db:"plan_output"`
	// BlockedBy lists the failed predecessors of a blocked stack.
	BlockedBy []string `db:"blocked_by"`
	// DispatchID is the workflow dispatch the stack was sent in, if any.
	DispatchID *uuid.UUID `db:"dispatch_id"`
	StartedAt  *time.Time `db:"started_at"`
	FinishedAt *time.Time `db:"finished_at"`
	UpdatedAt  time.Time  `db:"updated_at"`
}

// ToV1 converts the row; checks are attached by RunDetail.
func (rs RunStack) ToV1() v1.RunStack {
	out := v1.RunStack{
		StackID:      rs.StackID.String(),
		Key:          rs.Key,
		Path:         rs.Path,
		Workspace:    rs.Workspace,
		Environment:  rs.Environment,
		Wave:         rs.Wave,
		Status:       rs.Status,
		Summary:      rs.Summary,
		ExitCode:     rs.ExitCode,
		JobURL:       rs.JobURL,
		PlanArtifact: rs.PlanArtifact,
		PlanText:     rs.PlanText,
		Truncated:    rs.PlanTextTruncated,
		StartedAt:    utc(rs.StartedAt),
		FinishedAt:   utc(rs.FinishedAt),
		PlanOutput:   rs.PlanOutput,
		PlanURL:      rs.PlanURL,
	}
	if len(rs.Reasons) > 0 {
		out.Reasons = rs.Reasons
	}
	if len(rs.BlockedBy) > 0 {
		out.BlockedBy = rs.BlockedBy
	}
	return out
}

// RunStackPatch lists the fields UpdateRunStack changes; nil fields are
// kept.
type RunStackPatch struct {
	Status *v1.StackStatus
	// IfStatus, when non-empty, applies the patch only if the current status
	// is one of these; otherwise UpdateRunStack returns ErrConflict.
	IfStatus []v1.StackStatus
	Mode     *v1.RunMode
	Wave     *int
	// Environment set to "" stores v1.DefaultEnvironment.
	Environment  *string
	Summary      *v1.PlanSummary
	HasChanges   *bool
	ExitCode     *int
	JobURL       *string
	PlanArtifact *string
	PlanRunID    *int64
	// PlanText replaces the stored text, cut to MaxPlanTextBytes.
	PlanText *string
	// PlanTextTruncated says the sender already truncated PlanText.
	PlanTextTruncated *bool
	ErrorText         *string
	// PlanURL replaces the link to the full plan text.
	PlanURL *string
	// BlockedBy replaces the list of failed predecessors.
	BlockedBy *[]string
	// DispatchID records the dispatch the stack was sent in.
	DispatchID *uuid.UUID
	// StartedAt sets started_at when the row has none and the patch does
	// not start the stack itself.
	StartedAt *time.Time
}

const runStackColsHead = `rs.run_id, rs.stack_id, s.key, s.path, s.workspace, rs.wave, rs.mode, rs.status,
	rs.reasons, rs.environment, rs.adds, rs.changes, rs.destroys, rs.replaces, rs.has_changes,
	rs.exit_code, rs.job_url, rs.plan_artifact, rs.plan_run_id, rs.summary, `

const runStackColsTail = `, rs.plan_text_truncated, rs.error_text, rs.plan_url, rs.plan_output, rs.blocked_by,
	rs.dispatch_id, rs.started_at, rs.finished_at, rs.updated_at`

const runStackCols = runStackColsHead + `COALESCE(rs.plan_text, '') AS plan_text` + runStackColsTail

const runStackColsNoText = runStackColsHead + `'' AS plan_text` + runStackColsTail

// UpsertRunStacks records the affected stacks of a run. New rows take the
// given status (pending by default) and mode (the run's by default), and
// the given summary, change flag, plan artifact and plan run, which is how
// an apply run inherits its plans; existing rows only have their wave,
// reasons, environment and plan output refreshed, so results already
// reported are kept. An empty environment is v1.DefaultEnvironment.
func (s *Store) UpsertRunStacks(ctx context.Context, runID uuid.UUID, stacks []RunStack) error {
	if len(stacks) == 0 {
		return nil
	}
	sorted := slices.Clone(stacks)
	slices.SortFunc(sorted, func(a, b RunStack) int { return cmp.Compare(a.StackID.String(), b.StackID.String()) })
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		b := &pgx.Batch{}
		for _, rs := range sorted {
			b.Queue(`
				INSERT INTO run_stacks (run_id, stack_id, wave, mode, status, reasons, environment, plan_output,
				                        summary, adds, changes, destroys, replaces, has_changes, plan_artifact, plan_run_id)
				VALUES ($1, $2, $3,
				        COALESCE(NULLIF($4::text, ''), (SELECT mode FROM runs WHERE id = $1), 'plan'),
				        COALESCE(NULLIF($5::text, ''), 'pending'), $6::text[], $7, $8,
				        $9::jsonb, $10, $11, $12, $13, $14, $15, $16)
				ON CONFLICT (run_id, stack_id) DO UPDATE SET
					wave = EXCLUDED.wave,
					reasons = EXCLUDED.reasons,
					environment = EXCLUDED.environment,
					plan_output = EXCLUDED.plan_output,
					updated_at = now()`,
				runID, rs.StackID, rs.Wave, string(rs.Mode), string(rs.Status), strs(nonNil(rs.Reasons)),
				environmentOrDefault(rs.Environment), rs.PlanOutput,
				rs.Summary, rs.Adds, rs.Changes, rs.Destroys, rs.Replaces, rs.HasChanges, rs.PlanArtifact, rs.PlanRunID)
		}
		return tx.SendBatch(ctx, b).Close()
	})
	return wrap("upsert run stacks", err)
}

// UpdateRunStack applies a patch to one stack row and returns it with its
// plan text. Entering planning or applying stamps started_at and clears
// finished_at; entering planned or a terminal status stamps finished_at.
func (s *Store) UpdateRunStack(ctx context.Context, runID, stackID uuid.UUID, p RunStackPatch) (RunStack, error) {
	const op = "update run stack"
	var adds, changes, destroys, replaces *int
	if p.Summary != nil {
		adds, changes, destroys, replaces = &p.Summary.Adds, &p.Summary.Changes, &p.Summary.Destroys, &p.Summary.Replaces
	}
	if p.Environment != nil {
		env := environmentOrDefault(*p.Environment)
		p.Environment = &env
	}
	truncated := p.PlanTextTruncated
	var text *string
	if p.PlanText != nil {
		t, cut := truncateUTF8(*p.PlanText, MaxPlanTextBytes)
		flag := cut || (p.PlanTextTruncated != nil && *p.PlanTextTruncated)
		text, truncated = &t, &flag
	}
	out, err := queryOne[RunStack](ctx, s.db, `
		WITH rs AS (
			UPDATE run_stacks SET
				status = COALESCE($3::text, status),
				mode = COALESCE($4::text, mode),
				wave = COALESCE($5::integer, wave),
				environment = COALESCE($6::text, environment),
				summary = COALESCE($7::jsonb, summary),
				adds = COALESCE($8::integer, adds),
				changes = COALESCE($9::integer, changes),
				destroys = COALESCE($10::integer, destroys),
				replaces = COALESCE($11::integer, replaces),
				has_changes = COALESCE($12::boolean, has_changes),
				exit_code = COALESCE($13::integer, exit_code),
				job_url = COALESCE($14::text, job_url),
				plan_artifact = COALESCE($15::text, plan_artifact),
				plan_run_id = COALESCE($16::bigint, plan_run_id),
				plan_text = COALESCE($17::text, plan_text),
				plan_text_truncated = COALESCE($18::boolean, plan_text_truncated),
				error_text = COALESCE($19::text, error_text),
				plan_url = COALESCE($23::text, plan_url),
				blocked_by = COALESCE($24::text[], blocked_by),
				dispatch_id = COALESCE($25::uuid, dispatch_id),
				started_at = CASE
					WHEN $3::text IS NOT NULL AND $3::text <> status AND $3::text = ANY($20::text[]) THEN now()
					WHEN $26::timestamptz IS NOT NULL THEN COALESCE(started_at, $26::timestamptz)
					ELSE started_at END,
				finished_at = CASE
					WHEN $3::text IS NULL OR $3::text = status THEN finished_at
					WHEN $3::text = ANY($21::text[]) THEN now()
					ELSE NULL END,
				updated_at = now()
			WHERE run_id = $1 AND stack_id = $2
			  AND (cardinality($22::text[]) = 0 OR status = ANY($22::text[]))
			RETURNING *
		)
		SELECT `+runStackCols+` FROM rs JOIN stacks s ON s.id = rs.stack_id`,
		runID, stackID, p.Status, p.Mode, p.Wave, p.Environment, p.Summary,
		adds, changes, destroys, replaces, p.HasChanges, p.ExitCode, p.JobURL, p.PlanArtifact,
		p.PlanRunID, text, truncated, p.ErrorText, startedStatuses, finishedStackStates, strs(p.IfStatus),
		p.PlanURL, p.BlockedBy, p.DispatchID, p.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) && len(p.IfStatus) > 0 {
		return out, s.guardError(ctx, op, `SELECT 1 FROM run_stacks WHERE run_id = $1 AND stack_id = $2`, runID, stackID)
	}
	return out, wrap(op, err)
}

func environmentOrDefault(env string) string {
	if env == "" {
		return v1.DefaultEnvironment
	}
	return env
}

func truncateUTF8(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// GetRunStack returns one stack row of a run including its plan text.
func (s *Store) GetRunStack(ctx context.Context, runID, stackID uuid.UUID) (RunStack, error) {
	out, err := queryOne[RunStack](ctx, s.db, `
		SELECT `+runStackCols+` FROM run_stacks rs JOIN stacks s ON s.id = rs.stack_id
		WHERE rs.run_id = $1 AND rs.stack_id = $2`, runID, stackID)
	return out, wrap("get run stack", err)
}

// GetRunStacks returns every stack row of a run ordered by wave and key.
// PlanText is left empty to keep the result small; use GetRunStack for it.
func (s *Store) GetRunStacks(ctx context.Context, runID uuid.UUID) ([]RunStack, error) {
	out, err := queryAll[RunStack](ctx, s.db, `
		SELECT `+runStackColsNoText+` FROM run_stacks rs JOIN stacks s ON s.id = rs.stack_id
		WHERE rs.run_id = $1 ORDER BY rs.wave, s.key`, runID)
	return out, wrap("get run stacks", err)
}

// GetRunStacksWithText returns every stack row of a run ordered by wave
// and key, plan text included, for rendering the sticky comment.
func (s *Store) GetRunStacksWithText(ctx context.Context, runID uuid.UUID) ([]RunStack, error) {
	out, err := queryAll[RunStack](ctx, s.db, `
		SELECT `+runStackCols+` FROM run_stacks rs JOIN stacks s ON s.id = rs.stack_id
		WHERE rs.run_id = $1 ORDER BY rs.wave, s.key`, runID)
	return out, wrap("get run stacks with text", err)
}

// RunDetail assembles the API view of a run: the run, its stack rows
// without plan text, and each stack's named checks.
func (s *Store) RunDetail(ctx context.Context, id uuid.UUID) (v1.Run, error) {
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return v1.Run{}, err
	}
	stacks, err := s.GetRunStacks(ctx, id)
	if err != nil {
		return v1.Run{}, err
	}
	checks, err := s.ListChecks(ctx, id)
	if err != nil {
		return v1.Run{}, err
	}
	byStack := map[uuid.UUID][]v1.Check{}
	for _, c := range checks {
		byStack[c.StackID] = append(byStack[c.StackID], c.ToV1())
	}
	out := run.ToV1()
	out.Stacks = make([]v1.RunStack, len(stacks))
	for i, rs := range stacks {
		out.Stacks[i] = rs.ToV1()
		out.Stacks[i].Checks = byStack[rs.StackID]
	}
	return out, nil
}

// StackRun is a stack row together with the run it belongs to, as listed
// in a stack's history.
type StackRun struct {
	RunStack
	SHA          string       `db:"sha"`
	PRNumber     int          `db:"pr_number"`
	Trigger      v1.Trigger   `db:"trigger"`
	RunMode      v1.RunMode   `db:"run_mode"`
	RunStatus    v1.RunStatus `db:"run_status"`
	RunCreatedAt time.Time    `db:"run_created_at"`
}

// ToV1 converts the row to a reference for the stack page.
func (sr StackRun) ToV1() v1.RunStackRef {
	return v1.RunStackRef{
		RunID:      sr.RunID.String(),
		SHA:        sr.SHA,
		PRNumber:   sr.PRNumber,
		Status:     sr.Status,
		Summary:    sr.Summary,
		FinishedAt: utc(sr.FinishedAt),
		JobURL:     sr.JobURL,
	}
}

const stackRunSelect = `
	SELECT ` + runStackColsNoText + `, r.sha, r.pr_number, r.trigger, r.mode AS run_mode,
	       r.status AS run_status, r.created_at AS run_created_at
	FROM run_stacks rs
	JOIN stacks s ON s.id = rs.stack_id
	JOIN runs r ON r.id = rs.run_id`

// LatestRunStackForStack returns the stack's row in its newest run whose
// last operation on the stack had the given mode, optionally restricted to
// some statuses.
func (s *Store) LatestRunStackForStack(ctx context.Context, stackID uuid.UUID, mode v1.RunMode, statuses ...v1.StackStatus) (StackRun, error) {
	out, err := queryOne[StackRun](ctx, s.db, stackRunSelect+`
		WHERE rs.stack_id = $1 AND rs.mode = $2
		  AND (cardinality($3::text[]) = 0 OR rs.status = ANY($3::text[]))
		ORDER BY r.created_at DESC, r.id DESC LIMIT 1`, stackID, mode, strs(statuses))
	return out, wrap("latest run stack for stack", err)
}

// StackHistory returns the stack's rows across runs, newest run first, and
// the cursor of the next page.
func (s *Store) StackHistory(ctx context.Context, stackID uuid.UUID, limit int, cursor string) ([]StackRun, string, error) {
	const op = "stack history"
	c, err := decodeCursor(op, cursor)
	if err != nil {
		return nil, "", err
	}
	at, id := c.args()
	size := pageSize(limit)
	out, err := queryAll[StackRun](ctx, s.db, stackRunSelect+`
		WHERE rs.stack_id = $1
		  AND ($2::timestamptz IS NULL OR (r.created_at, r.id) < ($2::timestamptz, $3::uuid))
		ORDER BY r.created_at DESC, r.id DESC LIMIT $4`, stackID, at, id, size+1)
	if err != nil {
		return nil, "", wrap(op, err)
	}
	var next string
	if len(out) > size {
		out = out[:size]
		last := out[size-1]
		next = encodeCursor(last.RunCreatedAt, last.RunID)
	}
	return out, next, nil
}
