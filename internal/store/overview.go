package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const recentRuns = 10

const scopedRepos = `
	SELECT r.id FROM repos r JOIN installations i ON i.id = r.installation_id
	WHERE $1::boolean OR lower(i.account) = ANY($2::text[])`

type statusCount struct {
	Status string `db:"status"`
	Count  int    `db:"count"`
}

// Overview counts repositories, live stacks, drifted stacks, held locks
// (including those on stacks marked removed, as RepoSummaries does), runs by
// status and stacks by the status of their latest run, and lists the most
// recent runs. When accounts are given only their installations'
// repositories are counted; an empty account name matches nothing.
func (s *Store) Overview(ctx context.Context, accounts ...string) (v1.Overview, error) {
	const op = "overview"
	all, scope := len(accounts) == 0, lowerAll(accounts)
	out := v1.Overview{
		RunsByStatus:   map[v1.RunStatus]int{},
		StacksByStatus: map[v1.StackStatus]int{},
	}
	err := s.db.QueryRow(ctx, `
		WITH scope AS (`+scopedRepos+`),
		live AS (
			SELECT s.id FROM stacks s WHERE s.removed_at IS NULL AND s.repo_id IN (SELECT id FROM scope)
		)
		SELECT
			(SELECT count(*) FROM scope),
			(SELECT count(*) FROM live),
			(SELECT count(*) FROM live l JOIN LATERAL (
				SELECT d.drifted FROM drift d WHERE d.stack_id = l.id
				ORDER BY d.checked_at DESC, d.id DESC LIMIT 1) ld ON true
			 WHERE ld.drifted),
			(SELECT count(*) FROM locks l JOIN stacks s ON s.id = l.stack_id
			 WHERE s.repo_id IN (SELECT id FROM scope))`, all, scope).
		Scan(&out.Repos, &out.Stacks, &out.Drifted, &out.LocksHeld)
	if err != nil {
		return v1.Overview{}, wrap(op, err)
	}
	runCounts, err := queryAll[statusCount](ctx, s.db, `
		SELECT status, count(*) AS count FROM runs
		WHERE repo_id IN (`+scopedRepos+`) GROUP BY status`, all, scope)
	if err != nil {
		return v1.Overview{}, wrap(op, err)
	}
	for _, c := range runCounts {
		out.RunsByStatus[v1.RunStatus(c.Status)] = c.Count
	}
	stackCounts, err := queryAll[statusCount](ctx, s.db, `
		SELECT status, count(*) AS count FROM (
			SELECT DISTINCT ON (rs.stack_id) rs.status
			FROM run_stacks rs
			JOIN runs r ON r.id = rs.run_id
			JOIN stacks s ON s.id = rs.stack_id
			WHERE s.removed_at IS NULL AND s.repo_id IN (`+scopedRepos+`)
			ORDER BY rs.stack_id, r.created_at DESC, r.id DESC
		) latest GROUP BY status`, all, scope)
	if err != nil {
		return v1.Overview{}, wrap(op, err)
	}
	for _, c := range stackCounts {
		out.StacksByStatus[v1.StackStatus(c.Status)] = c.Count
	}
	runs, err := queryAll[Run](ctx, s.db, runSelect+`
		WHERE r.repo_id IN (`+scopedRepos+`)
		ORDER BY r.created_at DESC, r.id DESC LIMIT $3`, all, scope, recentRuns)
	if err != nil {
		return v1.Overview{}, wrap(op, err)
	}
	for _, r := range runs {
		out.RecentRuns = append(out.RecentRuns, r.ToV1())
	}
	return out, nil
}

type repoSummaryRow struct {
	ID            int64      `db:"id"`
	FullName      string     `db:"full_name"`
	DefaultBranch string     `db:"default_branch"`
	Stacks        int        `db:"stacks"`
	Drifted       int        `db:"drifted"`
	LocksHeld     int        `db:"locks_held"`
	LastRunAt     *time.Time `db:"last_run_at"`
}

// RepoSummaries returns one row per repository ordered by full name, with
// live stack, drifted stack and held lock counts and the time of the last
// run. When accounts are given only their installations' repositories are
// listed; an empty account name matches nothing.
func (s *Store) RepoSummaries(ctx context.Context, accounts ...string) ([]v1.RepoSummary, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id, r.full_name, r.default_branch,
		       (SELECT count(*) FROM stacks s WHERE s.repo_id = r.id AND s.removed_at IS NULL) AS stacks,
		       (SELECT count(*) FROM stacks s JOIN LATERAL (
		            SELECT d.drifted FROM drift d WHERE d.stack_id = s.id
		            ORDER BY d.checked_at DESC, d.id DESC LIMIT 1) ld ON true
		        WHERE s.repo_id = r.id AND s.removed_at IS NULL AND ld.drifted) AS drifted,
		       (SELECT count(*) FROM locks l JOIN stacks s ON s.id = l.stack_id
		        WHERE s.repo_id = r.id) AS locks_held,
		       (SELECT max(created_at) FROM runs WHERE repo_id = r.id) AS last_run_at
		FROM repos r JOIN installations i ON i.id = r.installation_id
		WHERE $1::boolean OR lower(i.account) = ANY($2::text[])
		ORDER BY r.full_name`, len(accounts) == 0, lowerAll(accounts))
	if err != nil {
		return nil, wrap("repo summaries", err)
	}
	summaries, err := pgx.CollectRows(rows, pgx.RowToStructByName[repoSummaryRow])
	if err != nil {
		return nil, wrap("repo summaries", err)
	}
	out := make([]v1.RepoSummary, len(summaries))
	for i, r := range summaries {
		out[i] = v1.RepoSummary{
			ID:            r.ID,
			FullName:      r.FullName,
			DefaultBranch: r.DefaultBranch,
			Stacks:        r.Stacks,
			Drifted:       r.Drifted,
			LocksHeld:     r.LocksHeld,
			LastRunAt:     utc(r.LastRunAt),
		}
	}
	return out, nil
}
