package runs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
)

const hourSlot = "2006010215"

// ScheduleDrift fans a repository's drift check out into one JobDrift per
// stack of its current graph, spread evenly across the next hour. The
// dedupe key carries the hour, so scheduling twice in an hour enqueues
// nothing new. A repository without drift.schedule is skipped.
func (s *Service) ScheduleDrift(ctx context.Context, repoID int64, enqueue func(ctx context.Context, kind string, payload any, runAfter time.Time, dedupeKey string) error) error {
	repo, err := s.st.GetRepo(ctx, repoID)
	if err != nil {
		return storeErr(err, "repository %d", repoID)
	}
	if repo.Suspended || repoConfig(repo).Drift.Schedule == "" {
		return nil
	}
	g, _, err := s.currentGraph(ctx, repoID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return storeErr(err, "graph of %s", repo.FullName)
	}
	live, err := s.st.ListStacks(ctx, repoID, false)
	if err != nil {
		return storeErr(err, "stacks of %s", repo.FullName)
	}
	inGraph := map[string]bool{}
	for _, st := range g.Stacks {
		if !st.External {
			inGraph[st.Key] = true
		}
	}
	var stacks []store.Stack
	for _, st := range live {
		if inGraph[st.Key] {
			stacks = append(stacks, st)
		}
	}
	now := s.now()
	slot := now.Format(hourSlot)
	for i, st := range stacks {
		at := now.Add(time.Duration(i) * time.Hour / time.Duration(len(stacks)))
		key := fmt.Sprintf("drift:%s:%s", st.ID, slot)
		if err := enqueue(ctx, JobDrift, DriftJob{RepoID: repoID, StackID: st.ID.String()}, at, key); err != nil {
			return fmt.Errorf("runs: schedule drift of %s: %w", st.Key, err)
		}
	}
	return nil
}

// RunDriftStack dispatches a drift check of one stack on the head of the
// default branch, reusing the drift run it already created this hour.
func (s *Service) RunDriftStack(ctx context.Context, repoID int64, stackID string) error {
	id, err := uuid.Parse(stackID)
	if err != nil {
		return fmt.Errorf("runs: drift of stack %q: %w", stackID, err)
	}
	repo, err := s.st.GetRepo(ctx, repoID)
	if err != nil {
		return storeErr(err, "repository %d", repoID)
	}
	if repo.Suspended {
		return nil
	}
	stack, err := s.st.GetStack(ctx, id)
	if err != nil {
		return storeErr(err, "stack %s", stackID)
	}
	if stack.RepoID != repoID || stack.RemovedAt != nil {
		return nil
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	head, err := c.GetRef(ctx, repo.FullName, "heads/"+repo.DefaultBranch)
	if err != nil {
		return fmt.Errorf("runs: head of %s: %w", repo.FullName, err)
	}
	graphID, err := s.currentGraphID(ctx, repoID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return storeErr(err, "graph of %s", repo.FullName)
	}
	now := s.now()
	hour := now.Truncate(time.Hour)
	var run store.Run
	err = s.st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockKey(ctx, "drift:"+stack.ID.String()+":"+now.Format(hourSlot)); err != nil {
			return err
		}
		existing, err := tx.FindRunForStack(ctx, repoID, stack.ID, v1.ModeDrift, hour)
		if err == nil {
			run = existing
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		run, err = tx.CreateRun(ctx, store.CreateRunParams{
			RepoID: repoID, SHA: head, Trigger: v1.TriggerSchedule, Mode: v1.ModeDrift,
			Status: v1.RunPending, RequestedBy: schedulerActor,
		})
		if err != nil {
			return err
		}
		if err := tx.UpsertRunStacks(ctx, run.ID, []store.RunStack{{
			StackID: stack.ID, Mode: v1.ModeDrift, Status: v1.StackPending, Environment: stack.Environment,
		}}); err != nil {
			return err
		}
		if graphID != uuid.Nil {
			return tx.SetRunGraph(ctx, run.ID, graphID, 1, nil)
		}
		return nil
	})
	if err != nil {
		return storeErr(err, "drift run of %s", stack.Key)
	}
	return s.dispatchWave(ctx, run.ID, 0)
}

func (s *Service) recordDrift(ctx context.Context, repo store.Repo, run store.Run, stack store.Stack, res v1.StackResult) error {
	d, err := s.st.RecordDrift(ctx, store.Drift{StackID: stack.ID, RunID: &run.ID, Drifted: res.HasChanges, Summary: res.Summary})
	if err != nil {
		return storeErr(err, "record drift of %s", stack.Key)
	}
	s.refreshDriftGauge(ctx)
	if !repoConfig(repo).Drift.OpenIssue {
		return nil
	}
	if err := s.syncDriftIssue(ctx, repo, stack, d); err != nil {
		s.log.WarnContext(ctx, "update drift issue", "repo", repo.FullName, "stack", stack.Key, "error", err)
	}
	return nil
}

func (s *Service) syncDriftIssue(ctx context.Context, repo store.Repo, stack store.Stack, d store.Drift) error {
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	detail := stack.Detail()
	if last, err := s.st.LatestRunStackForStack(ctx, stack.ID, v1.ModeApply, v1.StackApplied); err == nil {
		ref := last.ToV1()
		detail.LastApply = &ref
	}
	opts := report.Options{BaseURL: s.cfg.BaseURL, RepoURL: repoWebURL(repo)}
	title, body := report.DriftIssue(detail, d.ToV1(), opts)
	issues, err := c.ListIssues(ctx, repo.FullName, []string{driftLabel}, gh.IssueOpen)
	if err != nil {
		return fmt.Errorf("runs: drift issues of %s: %w", repo.FullName, err)
	}
	idx := slices.IndexFunc(issues, func(is gh.Issue) bool { return is.Title == title })
	switch {
	case d.Drifted && idx >= 0:
		if issues[idx].Body != body {
			if _, err := c.UpdateIssue(ctx, repo.FullName, issues[idx].Number, gh.IssueParams{Body: body}); err != nil {
				return fmt.Errorf("runs: update drift issue #%d: %w", issues[idx].Number, err)
			}
		}
		return s.st.SetDriftIssue(ctx, d.ID, issues[idx].Number)
	case d.Drifted:
		is, err := c.CreateIssue(ctx, repo.FullName, gh.IssueParams{Title: title, Body: body, Labels: []string{driftLabel}})
		if err != nil {
			return fmt.Errorf("runs: open drift issue for %s: %w", stack.Key, err)
		}
		return s.st.SetDriftIssue(ctx, d.ID, is.Number)
	case idx >= 0:
		n := issues[idx].Number
		if _, err := c.CreateIssueComment(ctx, repo.FullName, n, body); err != nil {
			return fmt.Errorf("runs: comment on drift issue #%d: %w", n, err)
		}
		if _, err := c.UpdateIssue(ctx, repo.FullName, n, gh.IssueParams{State: gh.IssueClosed}); err != nil {
			return fmt.Errorf("runs: close drift issue #%d: %w", n, err)
		}
		return s.st.SetDriftIssue(ctx, d.ID, n)
	}
	return nil
}
