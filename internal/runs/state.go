package runs

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
)

func (s *Service) advance(ctx context.Context, id uuid.UUID) error {
	run, err := s.st.GetRun(ctx, id)
	if err != nil {
		return storeErr(err, "run %s", id)
	}
	rows, err := s.st.GetRunStacks(ctx, id)
	if err != nil {
		return storeErr(err, "stacks of run %s", id)
	}
	if run.Mode == v1.ModeApply {
		return s.advanceApply(ctx, run, rows)
	}
	return s.advancePlan(ctx, run, rows)
}

func (s *Service) advancePlan(ctx context.Context, run store.Run, rows []store.RunStack) error {
	target := planTarget(rows)
	if run.Status == target || run.Status == v1.RunSuperseded {
		return nil
	}
	from := make([]v1.RunStatus, 0, 3)
	from = append(from, v1.RunPending)
	if target != v1.RunPlanning {
		from = append(from, v1.RunPlanning)
	}
	if target == v1.RunPlanned && run.Mode == v1.ModePlan {
		from = append(from, v1.RunFailed)
	}
	if run.Status.Terminal() && !slices.Contains(from, run.Status) {
		return nil
	}
	moved, err := s.transition(ctx, run, target, from...)
	if err != nil || !moved {
		return err
	}
	repo, err := s.st.GetRepo(ctx, run.RepoID)
	if err != nil {
		return storeErr(err, "repository of run %s", run.ID)
	}
	if target != v1.RunPlanning {
		s.onFinished(ctx, repo, run, target)
	}
	return nil
}

func (s *Service) advanceApply(ctx context.Context, run store.Run, rows []store.RunStack) error {
	if run.Status.Terminal() {
		return nil
	}
	w := run.CurrentWave
	if !waveDone(rows, w) {
		return nil
	}
	repo, err := s.st.GetRepo(ctx, run.RepoID)
	if err != nil {
		return storeErr(err, "repository of run %s", run.ID)
	}
	if applyBroken(rows) {
		return s.finishApply(ctx, repo, run, rows, v1.RunFailed)
	}
	if w >= lastWave(rows) {
		return s.finishApply(ctx, repo, run, rows, v1.RunApplied)
	}
	next := w + 1
	return s.later(ctx, JobDispatchWave, DispatchWaveJob{RunID: run.ID.String(), Wave: next},
		fmt.Sprintf("wave:%s:%d", run.ID, next),
		func(ctx context.Context) error { return s.dispatchWave(ctx, run.ID, next) })
}

func (s *Service) finishApply(ctx context.Context, repo store.Repo, run store.Run, rows []store.RunStack, status v1.RunStatus) error {
	moved, err := s.transition(ctx, run, status, v1.RunPending, v1.RunPlanned, v1.RunApplying)
	if err != nil || !moved {
		return err
	}
	s.onFinished(ctx, repo, run, status)
	if run.PRNumber > 0 {
		switch status {
		case v1.RunFailed:
			s.comment(ctx, repo, run.PRNumber, applyFailedComment(run, rows, releasesOnCompletion(run)))
		case v1.RunApplied:
			s.comment(ctx, repo, run.PRNumber, report.AppliedComment(appliedView(run, rows), releasesOnCompletion(run),
				report.Options{BaseURL: s.cfg.BaseURL, RepoURL: repoWebURL(repo)}))
		}
	}
	return s.propagateCrossRepo(ctx, repo, run, rows)
}

func appliedView(run store.Run, rows []store.RunStack) v1.Run {
	view := run.ToV1()
	view.Status = v1.RunApplied
	for _, rs := range rows {
		view.Stacks = append(view.Stacks, rs.ToV1())
	}
	return view
}

func releasesOnCompletion(run store.Run) bool {
	return run.Mode == v1.ModeApply && run.Trigger != v1.TriggerComment
}

func (s *Service) onFinished(ctx context.Context, repo store.Repo, run store.Run, status v1.RunStatus) {
	if releasesOnCompletion(run) {
		released, err := s.st.ReleaseLocksForRun(ctx, run.ID)
		if err != nil {
			s.log.WarnContext(ctx, "release locks of finished run", "run_id", run.ID, "error", err)
			return
		}
		for _, l := range released {
			s.audit(ctx, run.RequestedBy, "unlock", v1.QualifiedStackKey(repo.FullName, l.StackKey), map[string]any{
				"repo": repo.FullName, "stack": l.StackKey, "stack_id": l.StackID.String(), "run_id": run.ID.String(),
				"reason": "run " + string(status),
			})
		}
		if len(released) > 0 {
			s.refreshLocksGauge(ctx)
		}
	}
}

func applyFailedComment(run store.Run, rows []store.RunStack, released bool) string {
	var failed, blocked []string
	for _, rs := range rows {
		switch {
		case rs.Status == v1.StackBlocked:
			blocked = append(blocked, "`"+rs.Key+"`")
		case brokenStatus(rs.Status) || rs.Status == v1.StackUnconfirmed:
			failed = append(failed, "`"+rs.Key+"` ("+string(rs.Status)+")")
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Apply failed** in wave %d of `%s`: %s.", run.CurrentWave, shortSHA(run.SHA), strings.Join(failed, ", "))
	if len(blocked) > 0 {
		b.WriteString(" Blocked dependents: " + strings.Join(blocked, ", ") + ".")
	}
	b.WriteString(" Later waves were not dispatched.\n\n")
	if released {
		b.WriteString("The run's orchestration locks were released.\n")
	} else {
		b.WriteString("The orchestration locks stay held, because the default branch no longer matches what is deployed. " +
			"Fix the failure and comment `stackorder apply` again, or release them with `stackorder unlock`.\n")
	}
	return b.String()
}
