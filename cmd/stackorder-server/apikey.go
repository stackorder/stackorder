package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/stackorder/stackorder/internal/server"
	"github.com/stackorder/stackorder/internal/store"
)

const keyCommandUsage = `usage: stackorder-server api-key <command>

Commands:
  create <name>   mint a key and print its secret once
  list            list every key, revoked ones included
  revoke <id>     revoke a key by id

DATABASE_URL must point at the server's database. Keys created here are
equivalent to those inserted by hand and work with every endpoint.

Flags:
  --actor string   identity recorded in the audit log, and the creator of a
                   new key (default "operator")
`

const apiKeyTimeout = 15 * time.Second

func apiKey(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, keyCommandUsage)
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("api-key "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, keyCommandUsage) }
	actor := fs.String("actor", "operator", "identity recorded in the audit log")
	var rest []string
	for remaining := args[1:]; len(remaining) > 0; {
		if err := fs.Parse(remaining); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		remaining = fs.Args()[1:]
	}
	if strings.TrimSpace(*actor) == "" {
		_, _ = fmt.Fprintln(stderr, "stackorder-server api-key: --actor must not be empty")
		return 2
	}
	var revokeID uuid.UUID
	switch sub {
	case "create":
		if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" {
			_, _ = fmt.Fprintln(stderr, "stackorder-server api-key create: a single name is required")
			return 2
		}
	case "revoke":
		if len(rest) != 1 {
			_, _ = fmt.Fprintln(stderr, "stackorder-server api-key revoke: a single key id is required")
			return 2
		}
		var err error
		if revokeID, err = uuid.Parse(strings.TrimSpace(rest[0])); err != nil {
			_, _ = fmt.Fprintf(stderr, "stackorder-server api-key revoke: %q is not a key id\n", rest[0])
			return 2
		}
	case "list":
		if len(rest) != 0 {
			_, _ = fmt.Fprintln(stderr, "stackorder-server api-key list: takes no arguments")
			return 2
		}
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, keyCommandUsage)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "stackorder-server api-key: unknown command %q\n\n%s", sub, keyCommandUsage)
		return 2
	}

	dsn := strings.TrimSpace(getenv(server.EnvDatabaseURL))
	if dsn == "" {
		_, _ = fmt.Fprintf(stderr, "stackorder-server api-key: %s is required\n", server.EnvDatabaseURL)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiKeyTimeout)
	defer cancel()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server api-key:", err)
		return 1
	}
	defer st.Close()

	switch sub {
	case "create":
		return apiKeyCreate(ctx, st, rest[0], *actor, stdout, stderr)
	case "list":
		return apiKeyList(ctx, st, stdout, stderr)
	default:
		return apiKeyRevoke(ctx, st, revokeID, *actor, stdout, stderr)
	}
}

func apiKeyCreate(ctx context.Context, st *store.Store, rawName, actor string, stdout, stderr io.Writer) int {
	name := strings.TrimSpace(rawName)
	secret, key, err := st.CreateAPIKey(ctx, name, actor)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server api-key create:", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "Created API key %q (id %s).\nStore this secret now; it is not shown again:\n\n  %s\n\n", key.Name, key.ID, secret)
	return 0
}

func apiKeyList(ctx context.Context, st *store.Store, stdout, stderr io.Writer) int {
	keys, err := st.ListAPIKeys(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server api-key list:", err)
		return 1
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tPREFIX\tCREATED BY\tCREATED\tLAST USED\tREVOKED")
	for _, k := range keys {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			k.ID, k.Name, k.Prefix, k.CreatedBy, stamp(&k.CreatedAt), stamp(k.LastUsedAt), stamp(k.RevokedAt))
	}
	_ = tw.Flush()
	return 0
}

func apiKeyRevoke(ctx context.Context, st *store.Store, id uuid.UUID, actor string, stdout, stderr io.Writer) int {
	key, err := st.RevokeAPIKey(ctx, id, actor)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server api-key revoke:", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "Revoked API key %q (id %s).\n", key.Name, key.ID)
	return 0
}

func stamp(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
