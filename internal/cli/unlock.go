package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

type unlockOptions struct {
	reason     string
	forceState bool
}

func (a *app) unlockCommand() *cobra.Command {
	var o unlockOptions
	cmd := &cobra.Command{
		Use:   "unlock <key>...",
		Short: "Release orchestration locks through the server, audited",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUnlock(cmd.Context(), args, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.reason, "reason", "", "why the lock is released, recorded in the audit log")
	f.BoolVar(&o.forceState, "force-state", false, "release a lock left by a job that died mid-apply")
	return cmd
}

func (a *app) runUnlock(ctx context.Context, keys []string, o unlockOptions) error {
	if err := a.requireFormat(false); err != nil {
		return err
	}
	if a.server == "" {
		return failed("unlock: %w", errNoServer)
	}
	if strings.TrimSpace(os.Getenv(EnvAPIKey)) == "" {
		return failed("unlock needs an automation API key in %s", EnvAPIKey)
	}
	gh, err := a.github(ctx)
	if err != nil {
		return err
	}
	if gh.Repository == "" {
		return failed("cannot tell the repository; add a git remote named origin or set GITHUB_REPOSITORY")
	}
	cl, err := a.newClient(gh, true)
	if err != nil {
		return failed("%w", err)
	}
	all := v1.UnlockResponse{Released: []v1.LockInfo{}}
	var errs []error
	for _, k := range keys {
		key := config.NormalizePath(strings.TrimSpace(k))
		if key == "" {
			errs = append(errs, failed("unlock: empty stack key"))
			continue
		}
		resp, err := cl.Unlock(ctx, v1.UnlockRequest{Repo: gh.Repository, StackKey: key, Reason: o.reason, ForceState: o.forceState})
		if err != nil {
			errs = append(errs, fmt.Errorf("unlocking %s: %w", key, err))
			continue
		}
		if len(resp.Released) == 0 && a.format != formatJSON {
			_, _ = fmt.Fprintf(a.stdout, "%s: no lock held\n", key)
		}
		for _, l := range resp.Released {
			if l.StackKey == "" {
				l.StackKey = key
			}
			all.Released = append(all.Released, l)
			if a.format != formatJSON {
				_, _ = fmt.Fprintln(a.stdout, describeLock(l))
			}
		}
	}
	if a.format == formatJSON {
		if err := a.writeJSON(all); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func describeLock(l v1.LockInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "released %s", l.StackKey)
	var parts []string
	if l.RunID != "" {
		parts = append(parts, "run "+l.RunID)
	}
	if l.PRNumber != 0 {
		parts = append(parts, fmt.Sprintf("PR #%d", l.PRNumber))
	}
	if !l.TakenAt.IsZero() {
		parts = append(parts, "taken "+l.TakenAt.UTC().Format(time.RFC3339))
	}
	if len(parts) > 0 {
		b.WriteString(" (" + strings.Join(parts, ", ") + ")")
	}
	return b.String()
}
