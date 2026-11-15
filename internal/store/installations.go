package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Installation is one installation of the GitHub App on an account.
type Installation struct {
	ID          int64      `db:"id"`
	Account     string     `db:"account"`
	AccountType string     `db:"account_type"`
	SuspendedAt *time.Time `db:"suspended_at"`
	CreatedAt   time.Time  `db:"created_at"`
}

const installationColumns = `id, account, account_type, suspended_at, created_at`

// UpsertInstallation creates the installation or refreshes its account
// fields. The suspension state is left untouched.
func (s *Store) UpsertInstallation(ctx context.Context, in Installation) (Installation, error) {
	out, err := queryOne[Installation](ctx, s.db, `
		INSERT INTO installations (id, account, account_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET account = EXCLUDED.account, account_type = EXCLUDED.account_type
		RETURNING `+installationColumns,
		in.ID, in.Account, in.AccountType)
	return out, wrap("upsert installation", err)
}

// GetInstallation returns one installation.
func (s *Store) GetInstallation(ctx context.Context, id int64) (Installation, error) {
	out, err := queryOne[Installation](ctx, s.db,
		`SELECT `+installationColumns+` FROM installations WHERE id = $1`, id)
	return out, wrap("get installation", err)
}

// ListInstallations returns every installation ordered by account and id,
// suspended ones included.
func (s *Store) ListInstallations(ctx context.Context) ([]Installation, error) {
	out, err := queryAll[Installation](ctx, s.db,
		`SELECT `+installationColumns+` FROM installations ORDER BY lower(account), id`)
	return out, wrap("list installations", err)
}

// SuspendInstallation marks the installation suspended, keeping the first
// suspension time, or clears the mark when suspended is false.
func (s *Store) SuspendInstallation(ctx context.Context, id int64, suspended bool) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE installations
		SET suspended_at = CASE WHEN $2 THEN COALESCE(suspended_at, now()) ELSE NULL END
		WHERE id = $1`, id, suspended)
	if err != nil {
		return wrap("suspend installation", err)
	}
	if tag.RowsAffected() == 0 {
		return notFound("suspend installation")
	}
	return nil
}

// DeleteInstallation removes the installation together with its
// repositories and everything recorded for them, queueing the plan objects
// their runs kept in the artifact store for removal in the same
// transaction. The stack rows are deleted explicitly first and their
// objects queued from the deleted rows, so a plan_url a concurrent result
// records up to the moment of deletion is captured rather than escaping
// through the cascade. Deleting an unknown installation is not an error.
func (s *Store) DeleteInstallation(ctx context.Context, id int64) error {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			WITH deleted AS (
				DELETE FROM run_stacks rs
				USING runs r, repos p, stacks s
				WHERE rs.run_id = r.id AND r.repo_id = p.id
				  AND p.installation_id = $1 AND s.id = rs.stack_id
				RETURNING rs.run_id, s.key AS stack_key, rs.plan_url
			)
			INSERT INTO object_deletions (run_id, stack_key)
			SELECT run_id, stack_key FROM deleted WHERE plan_url <> ''
			ON CONFLICT (run_id, stack_key) DO NOTHING`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM installations WHERE id = $1`, id)
		return err
	})
	return wrap("delete installation", err)
}
