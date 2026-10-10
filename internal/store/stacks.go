package store

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Stack is the stable identity of a stack inside a repository. Its id
// survives commits; the descriptive fields reflect the most recently saved
// graph that contained the stack. Environment is never empty: a stack the
// graph maps to no environment has v1.DefaultEnvironment.
type Stack struct {
	ID          uuid.UUID       `db:"id"`
	RepoID      int64           `db:"repo_id"`
	Repo        string          `db:"repo"`
	Key         string          `db:"key"`
	Path        string          `db:"path"`
	Workspace   string          `db:"workspace"`
	Backend     *v1.Backend     `db:"backend"`
	Environment string          `db:"environment"`
	Tool        v1.Tool         `db:"tool"`
	Config      *v1.StackConfig `db:"config"`
	FirstSeenAt time.Time       `db:"first_seen_at"`
	LastSeenAt  time.Time       `db:"last_seen_at"`
	RemovedAt   *time.Time      `db:"removed_at"`
	// StateObject is where an apply or drift job of the newest run that
	// reported one found the stack's state; nil until then. Saving a graph
	// never changes it, unlike Backend.
	StateObject *v1.StateObject `db:"state_object"`
	// StateObjectRunID is the run of that job, nil once the run is deleted.
	StateObjectRunID *uuid.UUID `db:"state_object_run_id"`
	// StateObjectAt is when StateObject was recorded.
	StateObjectAt *time.Time `db:"state_object_at"`
}

// KeyInstance returns the instance a stack key names: the suffix after the
// stack's path and ":", or, when path is empty or not a prefix of key, the
// suffix v1.SplitStackKey finds. A key equal to its path names none.
func KeyInstance(key, path string) string {
	switch {
	case path != "" && key == path:
		return ""
	case path != "" && strings.HasPrefix(key, path+":"):
		return key[len(path)+1:]
	}
	_, instance := v1.SplitStackKey(key)
	return instance
}

// Instance returns the instance the stack's key names.
func (s Stack) Instance() string { return KeyInstance(s.Key, s.Path) }

// ToV1 converts the row to a graph node.
func (s Stack) ToV1() v1.Stack {
	return v1.Stack{
		Key:         s.Key,
		Path:        s.Path,
		Instance:    s.Instance(),
		Workspace:   s.Workspace,
		Repo:        s.Repo,
		Backend:     s.Backend,
		Environment: s.Environment,
		Tool:        s.Tool,
		Config:      s.Config,
	}
}

// Detail converts the row to the identity part of a stack page; the caller
// fills in runs, drift, lock and graph neighbours.
func (s Stack) Detail() v1.StackDetail {
	return v1.StackDetail{
		ID:          s.ID.String(),
		Repo:        s.Repo,
		Key:         s.Key,
		Path:        s.Path,
		Instance:    s.Instance(),
		Workspace:   s.Workspace,
		Environment: s.Environment,
		Backend:     s.Backend,
		Tool:        s.Tool,
	}
}

const stackSelect = `
	SELECT s.id, s.repo_id, r.full_name AS repo, s.key, s.path, s.workspace, s.backend,
	       s.environment, s.tool, s.config, s.first_seen_at, s.last_seen_at, s.removed_at,
	       s.state_object, s.state_object_run_id, s.state_object_at
	FROM stacks s JOIN repos r ON r.id = s.repo_id`

// GetStack returns one stack by id.
func (s *Store) GetStack(ctx context.Context, id uuid.UUID) (Stack, error) {
	out, err := queryOne[Stack](ctx, s.db, stackSelect+` WHERE s.id = $1`, id)
	return out, wrap("get stack", err)
}

// GetStackByKey returns one stack by repository and stack key.
func (s *Store) GetStackByKey(ctx context.Context, repoID int64, key string) (Stack, error) {
	out, err := queryOne[Stack](ctx, s.db, stackSelect+` WHERE s.repo_id = $1 AND s.key = $2`, repoID, key)
	return out, wrap("get stack by key", err)
}

// ListStacks returns the stacks of a repository ordered by key. Stacks
// marked removed are included only when includeRemoved is set.
func (s *Store) ListStacks(ctx context.Context, repoID int64, includeRemoved bool) ([]Stack, error) {
	out, err := queryAll[Stack](ctx, s.db, stackSelect+`
		WHERE s.repo_id = $1 AND ($2 OR s.removed_at IS NULL)
		ORDER BY s.key`, repoID, includeRemoved)
	return out, wrap("list stacks", err)
}

// MarkStacksRemoved stamps removed_at on every stack of the repository whose
// key is not in keepKeys and returns how many were marked. Stacks already
// marked keep their original removal time.
func (s *Store) MarkStacksRemoved(ctx context.Context, repoID int64, keepKeys []string) (int, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE stacks SET removed_at = now()
		WHERE repo_id = $1 AND removed_at IS NULL AND NOT (key = ANY($2::text[]))`,
		repoID, nonNil(keepKeys))
	if err != nil {
		return 0, wrap("mark stacks removed", err)
	}
	return int(tag.RowsAffected()), nil
}

// SetStackStateObject records the state object an apply or drift job of run
// reported for the stack. The run must be an apply or drift run of the
// stack's repository. A report from a run created before the run already
// recorded is ignored, so a late drift job cannot undo the location a newer
// apply recorded; the same run may report again. It returns ErrInvalid when
// obj has no bucket or key or the run may not report for the stack, and
// ErrNotFound when the stack or the run does not exist.
func (s *Store) SetStackStateObject(ctx context.Context, stackID uuid.UUID, obj v1.StateObject, runID uuid.UUID) error {
	const op = "set stack state object"
	if obj.Bucket == "" || obj.Key == "" {
		return invalid(op, "state object with a bucket and key is required")
	}
	var allowed bool
	err := s.db.QueryRow(ctx, `
		WITH report AS (
			SELECT s.id AS stack_id, r.id AS run_id, r.created_at,
			       s.repo_id = r.repo_id AND r.mode IN ('apply', 'drift') AS allowed
			FROM stacks s CROSS JOIN runs r
			WHERE s.id = $1 AND r.id = $3
		), recorded AS (
			UPDATE stacks s SET state_object = $2, state_object_run_id = p.run_id, state_object_at = now()
			FROM report p
			WHERE s.id = p.stack_id AND p.allowed
			  AND (s.state_object_run_id IS NULL
			       OR p.created_at >= (SELECT created_at FROM runs WHERE id = s.state_object_run_id))
		)
		SELECT allowed FROM report`, stackID, obj, runID).Scan(&allowed)
	if err != nil {
		return wrap(op, err)
	}
	if !allowed {
		return invalid(op, "run is not an apply or drift run of the stack's repository")
	}
	return nil
}

// DefaultGraphHasStack reports whether the repository has a default-branch
// graph and whether that graph holds the local, non-external stack key. It
// returns ErrNotFound when the repository does not exist.
func (s *Store) DefaultGraphHasStack(ctx context.Context, repoID int64, key string) (known, has bool, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT r.default_graph_id IS NOT NULL, EXISTS (
			SELECT 1 FROM graph_stacks gs JOIN stacks s ON s.id = gs.stack_id
			WHERE gs.graph_id = r.default_graph_id AND s.repo_id = r.id AND s.key = $2
			  AND NOT COALESCE((gs.node->>'external')::boolean, false))
		FROM repos r WHERE r.id = $1`, repoID, key).Scan(&known, &has)
	if err != nil {
		return false, false, wrap("default graph has stack", err)
	}
	return known, has, nil
}
