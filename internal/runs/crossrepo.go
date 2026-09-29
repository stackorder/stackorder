package runs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func (s *Service) propagateCrossRepo(ctx context.Context, repo store.Repo, run store.Run, rows []store.RunStack) error {
	if repoConfig(repo).Propagate.CrossRepo != v1.CrossRepoPlan {
		return nil
	}
	applied := map[string]bool{}
	for _, rs := range rows {
		if rs.Status == v1.StackApplied {
			applied[rs.Key] = true
		}
	}
	deps, err := s.st.ExternalDependents(ctx, repo.ID)
	if err != nil {
		return storeErr(err, "external dependents of %s", repo.FullName)
	}
	byRepo := map[string][]string{}
	for _, d := range deps {
		if applied[d.ToKey] && !slices.Contains(byRepo[d.Repo], d.FromKey) {
			byRepo[d.Repo] = append(byRepo[d.Repo], d.FromKey)
		}
	}
	repos := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repos = append(repos, r)
	}
	slices.Sort(repos)
	for _, r := range repos {
		keys := byRepo[r]
		slices.Sort(keys)
		job := CrossRepoPlanJob{Repo: r, StackKeys: keys, UpstreamRunID: run.ID.String()}
		err := s.later(ctx, JobCrossRepoPlan, job, "crossrepo:"+run.ID.String()+":"+strings.ToLower(r),
			func(ctx context.Context) error { return s.RunCrossRepoPlan(ctx, job) })
		if err != nil {
			return err
		}
	}
	return nil
}

// RunCrossRepoPlan dispatches a plan-only run of the named stacks of a
// downstream repository on the head of its default branch, once per
// upstream run.
func (s *Service) RunCrossRepoPlan(ctx context.Context, job CrossRepoPlanJob) error {
	repo, err := s.st.GetRepoByName(ctx, job.Repo)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return storeErr(err, "repository %s", job.Repo)
	}
	if repo.Suspended || len(job.StackKeys) == 0 {
		return nil
	}
	g, graphID, err := s.currentGraph(ctx, repo.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return storeErr(err, "graph of %s", repo.FullName)
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	head, err := c.GetRef(ctx, repo.FullName, "heads/"+repo.DefaultBranch)
	if err != nil {
		return fmt.Errorf("runs: head of %s: %w", repo.FullName, err)
	}
	target := "run:" + job.UpstreamRunID + ":" + strings.ToLower(repo.FullName)
	claim := func(ctx context.Context, tx *store.Store) (bool, error) {
		if err := tx.LockKey(ctx, "crossrepo:"+target); err != nil {
			return false, err
		}
		n, err := tx.CountAudit(ctx, "cross_repo_plan", target, time.Time{})
		if err != nil || n > 0 {
			return false, err
		}
		_, err = tx.RecordAudit(ctx, store.AuditEntry{Actor: schedulerActor, Action: "cross_repo_plan", Target: target,
			Details: map[string]any{"stacks": job.StackKeys}})
		return err == nil, err
	}
	_, refusal, err := s.startServerPlan(ctx, serverPlan{
		repo: repo, sha: head, graph: g, graphID: graphID, keys: job.StackKeys,
		trigger: v1.TriggerPush, requester: schedulerActor, claim: claim,
		warnings: []string{"planned because upstream run " + job.UpstreamRunID + " applied stacks these depend on"},
	})
	if err != nil {
		return err
	}
	if refusal != "" {
		s.log.InfoContext(ctx, "cross-repo plan skipped", "repo", repo.FullName, "upstream_run", job.UpstreamRunID, "reason", refusal)
	}
	return nil
}
