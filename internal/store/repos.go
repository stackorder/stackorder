package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Repo is a repository the App is installed on.
type Repo struct {
	ID             int64          `db:"id"`
	InstallationID int64          `db:"installation_id"`
	Account        string         `db:"account"`
	FullName       string         `db:"full_name"`
	DefaultBranch  string         `db:"default_branch"`
	Config         *v1.RepoConfig `db:"config"`
	ConfigSHA      string         `db:"config_sha"`
	Private        bool           `db:"private"`
	Suspended      bool           `db:"suspended"`
	CreatedAt      time.Time      `db:"created_at"`
	UpdatedAt      time.Time      `db:"updated_at"`
}

// RepoParams are the GitHub-owned fields of a repository.
type RepoParams struct {
	ID             int64
	InstallationID int64
	FullName       string
	// DefaultBranch keeps the stored value when empty; new rows default to
	// "main".
	DefaultBranch string
	Private       bool
}

const repoSelect = `
	SELECT r.id, r.installation_id, i.account, r.full_name, r.default_branch, r.config,
	       r.config_sha, r.private, i.suspended_at IS NOT NULL AS suspended,
	       r.created_at, r.updated_at
	FROM repos r JOIN installations i ON i.id = r.installation_id`

// UpsertRepo creates the repository or refreshes its GitHub fields, keeping
// the stored configuration. GitHub ids are stable across renames, so a
// different stale row holding the same full name is renamed out of the way.
func (s *Store) UpsertRepo(ctx context.Context, p RepoParams) (Repo, error) {
	const op = "upsert repo"
	if p.ID == 0 || p.FullName == "" {
		return Repo{}, invalid(op, "id and full name are required")
	}
	var out Repo
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE repos SET full_name = full_name || '~' || id::text, updated_at = now()
			WHERE lower(full_name) = lower($2) AND id <> $1`, p.ID, p.FullName); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO repos (id, installation_id, full_name, default_branch, private)
			VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'main'), $5)
			ON CONFLICT (id) DO UPDATE SET
				installation_id = EXCLUDED.installation_id,
				full_name = EXCLUDED.full_name,
				default_branch = CASE WHEN $4 = '' THEN repos.default_branch ELSE EXCLUDED.default_branch END,
				private = EXCLUDED.private,
				updated_at = now()`,
			p.ID, p.InstallationID, p.FullName, p.DefaultBranch, p.Private); err != nil {
			return err
		}
		var err error
		out, err = queryOne[Repo](ctx, tx, repoSelect+` WHERE r.id = $1`, p.ID)
		return err
	})
	return out, wrap(op, err)
}

// GetRepo returns one repository by GitHub id.
func (s *Store) GetRepo(ctx context.Context, id int64) (Repo, error) {
	out, err := queryOne[Repo](ctx, s.db, repoSelect+` WHERE r.id = $1`, id)
	return out, wrap("get repo", err)
}

// GetRepoByName returns one repository by "owner/repo", ignoring case.
func (s *Store) GetRepoByName(ctx context.Context, fullName string) (Repo, error) {
	out, err := queryOne[Repo](ctx, s.db, repoSelect+` WHERE lower(r.full_name) = lower($1)`, fullName)
	return out, wrap("get repo by name", err)
}

// ListRepos returns repositories ordered by full name. When accounts are
// given, only repositories of installations on those accounts are listed;
// an empty account name matches nothing.
func (s *Store) ListRepos(ctx context.Context, accounts ...string) ([]Repo, error) {
	out, err := queryAll[Repo](ctx, s.db, repoSelect+`
		WHERE $1::boolean OR lower(i.account) = ANY($2::text[])
		ORDER BY r.full_name`, len(accounts) == 0, lowerAll(accounts))
	return out, wrap("list repos", err)
}

// UpdateRepoConfig stores the parsed default branch stackorder.yaml and the
// commit it was read at.
func (s *Store) UpdateRepoConfig(ctx context.Context, id int64, cfg *v1.RepoConfig, sha string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE repos SET config = $2, config_sha = $3, updated_at = now() WHERE id = $1`,
		id, cfg, sha)
	if err != nil {
		return wrap("update repo config", err)
	}
	if tag.RowsAffected() == 0 {
		return notFound("update repo config")
	}
	return nil
}

// DeleteRepo removes a repository and everything recorded for it. Deleting
// an unknown repository is not an error.
func (s *Store) DeleteRepo(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM repos WHERE id = $1`, id)
	return wrap("delete repo", err)
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v != "" {
			out = append(out, strings.ToLower(v))
		}
	}
	return out
}
