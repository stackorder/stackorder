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

const migrateUsage = `usage: stackorder-server migrate [--to N | --status]

Apply the embedded migrations to DATABASE_URL and exit, holding the same
migration lock as start-up so it is safe next to starting servers. It needs
only DATABASE_URL, not STACKORDER_BASE_URL or the GitHub App settings.

Flags:
  --to N     migrate up to version N instead of the latest
  --status   print the current and target version, change nothing, and exit
             0 when up to date, 2 when migrations are pending, 1 when the
             database is dirty or ahead of the latest version this binary embeds

Exit codes:
  0  migrated, already up to date, or --status found the schema up to date
  1  a usage, configuration, connection or migration error, a dirty database, or
     a database ahead of the latest version this binary embeds
  2  --status found pending migrations
`

func migrateCmd(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, migrateUsage) }
	var (
		to     int
		status bool
	)
	fs.IntVar(&to, "to", 0, "migrate up to this version instead of the latest")
	fs.BoolVar(&status, "status", false, "report the current and target version without migrating")
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
	toSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "to" {
			toSet = true
		}
	})
	if status && toSet {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: --status and --to cannot be combined\n")
		return 1
	}
	if toSet && to < 0 {
		_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: --to must not be negative, got %d\n", to)
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

	if status {
		_, _ = fmt.Fprintf(stdout, "current=%d\ntarget=%d\ndirty=%t\n", current, target, dirty)
		switch {
		case dirty:
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

	applied, err := st.MigrateUp(ctx, dest)
	if err != nil {
		var ahead *store.SchemaAheadError
		if !toSet && errors.As(err, &ahead) {
			_, _ = fmt.Fprintf(stderr, "stackorder-server migrate: database is at version %d, above the latest version %d this binary embeds; migrate does not revert migrations\n", ahead.Current, ahead.Target)
			return 1
		}
		_, _ = fmt.Fprintln(stderr, "stackorder-server migrate:", err)
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
