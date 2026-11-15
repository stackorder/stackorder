package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/stackorder/stackorder/internal/server"
	"github.com/stackorder/stackorder/internal/store"
)

const migrateUsage = `usage: stackorder-server migrate [--to N | --status | --down --to N [--yes] | --force N]

Apply the embedded migrations to DATABASE_URL and exit, holding the same
migration lock as start-up so it is safe next to starting servers. It needs
only DATABASE_URL, not STACKORDER_BASE_URL or the GitHub App settings.

Flags:
  --to N     migrate up to version N instead of the latest; with --down, the
             version to revert the schema down to
  --status   print the current and target version, change nothing, and exit
             0 when up to date, 2 when migrations are pending, 1 when the
             database is dirty or ahead of the latest version this binary embeds
  --down     revert migrations down to --to N instead of migrating up, to roll
             the schema back before starting an older release. This permanently
             drops the data the reverted migrations own. Without --yes it only
             prints the plan and changes nothing.
  --yes      apply the --down plan instead of only printing it
  --force N  record version N and clear the dirty flag, without running any
             migration; only for a dirty database. A failed migration rolls
             back under this driver and leaves the schema at the previous
             version, so after migration N fails use --force N-1 and run
             migrate again to re-apply N. Use --force N only when you finished
             migration N by hand. --force 0 means no migration is applied.

Exit codes:
  0  migrated, already up to date, --status found the schema up to date,
     --down printed its plan or reverted the schema, or --force recorded the
     version
  1  a usage, configuration, connection or migration error, a dirty database
     (without --force), or a database ahead of the latest version this binary
     embeds
  2  --status found pending migrations
`

func migrateCmd(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, migrateUsage) }
	var (
		to     int
		status bool
		down   bool
		yes    bool
		force  int
	)
	fs.IntVar(&to, "to", 0, "migrate up to this version instead of the latest")
	fs.BoolVar(&status, "status", false, "report the current and target version without migrating")
	fs.BoolVar(&down, "down", false, "revert migrations down to --to N instead of migrating up")
	fs.BoolVar(&yes, "yes", false, "apply the --down plan instead of only printing it")
	fs.IntVar(&force, "force", 0, "record this version and clear the dirty flag without migrating")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: unexpected argument %q\n\n%s", fs.Arg(0), migrateUsage)
		return 1
	}
	var toSet, forceSet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "to":
			toSet = true
		case "force":
			forceSet = true
		}
	})
	switch {
	case status && toSet:
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: --status and --to cannot be combined\n")
		return 1
	case status && down:
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: --status and --down cannot be combined\n")
		return 1
	case status && forceSet:
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: --status and --force cannot be combined\n")
		return 1
	case toSet && forceSet:
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: --to and --force cannot be combined\n")
		return 1
	}
	if yes && !down {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: --yes applies only to --down\n")
		return 1
	}
	if down && !toSet {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: --down requires --to N, the version to revert the schema down to\n")
		return 1
	}
	if toSet && to < 0 {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: --to must not be negative, got %d\n", to)
		return 1
	}
	if forceSet && force < 0 {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: --force must not be negative, got %d\n", force)
		return 1
	}

	cfg, err := server.LoadMigrateConfig(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
		return 1
	}
	target, err := store.LatestMigration()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
		return 1
	}

	dest := target
	if toSet {
		if to == 0 || uint(to) > target {
			_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: no migration numbered %d; the latest is %d\n", to, target)
			return 1
		}
		dest = uint(to)
	}
	if forceSet && uint(force) > target {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: cannot force version %d; the latest this binary embeds is %d\n", force, target)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
		return 1
	}
	defer st.Close()

	current, dirty, err := st.SchemaVersion(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
		return 1
	}

	if forceSet {
		if !dirty {
			_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: database is not dirty (version %d); --force only recovers a dirty database\n", current)
			return 1
		}
		previous, err := st.ForceVersion(ctx, uint(force))
		if err != nil {
			var notDirty *store.SchemaNotDirtyError
			if errors.As(err, &notDirty) {
				_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: database is not dirty (version %d); --force only recovers a dirty database\n", notDirty.Version)
				return 1
			}
			_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "cleared the dirty flag; recorded version %d (was dirty at version %d)\n", force, previous)
		return 0
	}

	if status {
		_, _ = fmt.Fprintf(stdout, "current=%d\ntarget=%d\ndirty=%t\n", current, target, dirty)
		switch {
		case dirty:
			_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: database is dirty at version %d; a migration did not finish. Repair the schema, then run `stackorder-server migrate --force N` with the recovered version N; see docs/operations/troubleshooting.md\n", current)
			return 1
		case current > target:
			_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: database is at version %d, above the latest version %d this binary embeds; migrate does not revert migrations\n", current, target)
			return 1
		case current < target:
			return 2
		default:
			return 0
		}
	}

	if down {
		return migrateDown(ctx, st, stdout, stderr, current, target, dest, dirty, yes)
	}

	applied, err := st.MigrateUp(ctx, dest)
	if err != nil {
		var ahead *store.SchemaAheadError
		var dirtyErr *store.SchemaDirtyError
		switch {
		case !toSet && errors.As(err, &ahead):
			_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: database is at version %d, above the latest version %d this binary embeds; migrate does not revert migrations\n", ahead.Current, ahead.Target)
		case errors.As(err, &dirtyErr):
			_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: %v; run `stackorder-server migrate --force N` with the recovered version N; see docs/operations/troubleshooting.md\n", dirtyErr)
		default:
			_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
		}
		return 1
	}
	after, _, err := st.SchemaVersion(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
		return 1
	}
	if applied {
		_, _ = fmt.Fprintf(stdout, "migrated database from version %d to version %d\n", current, after)
	} else {
		_, _ = fmt.Fprintf(stdout, "database already at version %d\n", after)
	}
	return 0
}

func migrateDown(ctx context.Context, st *store.Store, stdout, stderr io.Writer, current, target, dest uint, dirty, yes bool) int {
	if dirty {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: schema is dirty at version %d; repair it before migrating\n", current)
		return 1
	}
	if current > target {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: database is at version %d, above the latest version %d this binary embeds; it cannot revert migrations it does not embed\n", current, target)
		return 1
	}
	if current < dest {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: database is at version %d, below the requested version %d; migrate --down only reverts migrations\n", current, dest)
		return 1
	}
	if current == dest {
		_, _ = fmt.Fprintf(stdout, "database already at version %d\n", current)
		return 0
	}

	all, err := store.Migrations()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "migrate --down reverts the schema from version %d to version %d, running these down migrations in order:\n", current, dest)
	for i := len(all) - 1; i >= 0; i-- {
		if m := all[i]; m.Version > dest && m.Version <= current {
			_, _ = fmt.Fprintf(stdout, "  %04d  %s — %s\n", m.Version, m.Name, migrationLoss(m.Version))
		}
	}
	_, _ = fmt.Fprint(stdout, "Reverting a migration permanently drops the tables, columns and indexes it added and the data in them; see the rollback table in the upgrades-and-backups guide for what each version loses.\n")
	_, _ = fmt.Fprint(stderr, "Stop every stackorder-server instance of the newer release first: one left running will fail once the columns and tables it uses are gone, and the older release refuses to start until the schema is at or below the version it embeds.\n")

	if !yes {
		_, _ = fmt.Fprint(stdout, "This was a dry run; nothing was changed. Re-run with --yes to apply.\n")
		return 0
	}

	reverted, err := st.MigrateDownTo(ctx, dest)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
		return 1
	}
	after, _, err := st.SchemaVersion(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
		return 1
	}
	if reverted {
		_, _ = fmt.Fprintf(stdout, "reverted database from version %d to version %d\n", current, after)
	} else {
		_, _ = fmt.Fprintf(stdout, "database already at version %d\n", after)
	}
	return 0
}

// migrationLoss names, for the plan printed by migrate --down, what reverting
// the migration at version loses, so an operator sees the cost of each step
// before applying it. Every embedded migration has an entry, asserted by a
// test; a new migration must add one.
func migrationLoss(version uint) string {
	switch version {
	case 1:
		return "drops the graph tables (repos, graphs, stacks, modules) — a full teardown"
	case 2:
		return "drops the run history (runs and run_stacks)"
	case 3:
		return "drops the work queue (events and jobs) and everything queued in it"
	case 4:
		return "drops the sessions, API keys and audit log"
	case 5:
		return "drops the run-service columns on run_stacks"
	case 6:
		return "drops run_stacks.via, the node keys a change reached each stack through"
	case 7:
		return "drops runs.comment_id; the next run opens a new pull-request comment instead of editing the old one"
	case 8:
		return "drops stacks.state_object; stacks that read shared outputs fail to plan until each producer's next apply or drift job records it again"
	case 9:
		return "drops stack_resources; each stack re-records its resource list on its next apply or drift job"
	case 10:
		return "no schema change and no data dropped; the one-time stacks.removed_at reconcile is simply not re-run"
	case 11:
		return "no data dropped; repos.default_graph_id becomes NOT DEFERRABLE again, so the single-transaction data-only restore no longer works"
	case 12:
		return "drops the object_deletions queue and every row in it; objects already unreferenced in Postgres are never deleted from the artifact bucket and stay orphaned"
	case 13:
		return "no data dropped; drops the run_stacks_stranded_idx index, re-created by the next upgrade"
	case 14:
		return "drops installations.suspend_observed_at; suspend/unsuspend ordering falls back to processing order until the daily installation sync settles it"
	case 15:
		return "drops events.trace_context and jobs.trace_context, the queued W3C trace context; observability only, no run or state data"
	case 16:
		return "drops the API-key scopes (api_keys.read_only, installation_id, expires_at): every read-only, installation-scoped or expiring key is then honoured as a FULL, never-expiring administrator. Revoke every scoped or expiring key BEFORE reverting — list them with `stackorder-server api-key list` and revoke each with `stackorder-server api-key revoke <id>`"
	default:
		return "drops the tables, columns and indexes this migration added and the data in them; see the rollback table in the upgrades-and-backups guide"
	}
}
