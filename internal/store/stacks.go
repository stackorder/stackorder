package store

import (
	"context"
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
}

// ToV1 converts the row to a graph node.
func (s Stack) ToV1() v1.Stack {
	return v1.Stack{
		Key:         s.Key,
		Path:        s.Path,
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
		Workspace:   s.Workspace,
		Environment: s.Environment,
		Backend:     s.Backend,
		Tool:        s.Tool,
	}
}

const stackSelect = `
	SELECT s.id, s.repo_id, r.full_name AS repo, s.key, s.path, s.workspace, s.backend,
	       s.environment, s.tool, s.config, s.first_seen_at, s.last_seen_at, s.removed_at
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
