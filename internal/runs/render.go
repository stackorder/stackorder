package runs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
)

type renderOpts struct {
	stacks       []uuid.UUID
	allStacks    bool
	resolve      *v1.ResolveResponse
	resolveErr   error
	supersededBy string
}

func renderKey(run store.Run) string {
	if run.PRNumber > 0 {
		return "render:pr:" + strconv.FormatInt(run.RepoID, 10) + ":" + strconv.Itoa(run.PRNumber)
	}
	return "render:run:" + run.ID.String()
}

func (s *Service) renderQuiet(ctx context.Context, id uuid.UUID, o renderOpts) {
	if err := s.render(ctx, id, o); err != nil {
		s.log.WarnContext(ctx, "update github for run", "run_id", id, "error", err)
	}
}

func (s *Service) render(ctx context.Context, id uuid.UUID, o renderOpts) error {
	run, err := s.st.GetRun(ctx, id)
	if err != nil {
		return storeErr(err, "run %s", id)
	}
	if run.Trigger == v1.TriggerManual || run.Mode == v1.ModeDrift && run.PRNumber == 0 {
		return nil
	}
	repo, err := s.st.GetRepo(ctx, run.RepoID)
	if err != nil {
		return storeErr(err, "repository of run %s", id)
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	var ghErrs []error
	err = s.st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockKey(ctx, renderKey(run)); err != nil {
			return err
		}
		ghErrs, err = s.renderLocked(ctx, tx, c, id, o)
		return err
	})
	if err != nil {
		return err
	}
	return errors.Join(ghErrs...)
}

func (s *Service) renderLocked(ctx context.Context, tx *store.Store, c *gh.Client, id uuid.UUID, o renderOpts) ([]error, error) {
	run, err := tx.GetRun(ctx, id)
	if err != nil {
		return nil, storeErr(err, "run %s", id)
	}
	repo, err := tx.GetRepo(ctx, run.RepoID)
	if err != nil {
		return nil, storeErr(err, "repository of run %s", id)
	}
	w := &checkWriter{s: s, tx: tx, c: c, repo: repo}
	if run.Status == v1.RunSuperseded {
		if o.supersededBy != "" {
			w.superseded(ctx, &run, o.supersededBy)
		}
		return w.errs, nil
	}
	opts, err := s.reportOptions(ctx, tx, repo, run.PRNumber)
	if err != nil {
		return nil, err
	}
	detail, err := runView(ctx, tx, run.ID)
	if err != nil {
		return nil, err
	}
	if o.resolve != nil || o.resolveErr != nil {
		w.write(ctx, &run, report.CheckResolve, report.ResolveCheck(o.resolve, o.resolveErr, opts))
	}
	if run.Mode != v1.ModeDrift {
		rollup := rollupName(run.Mode)
		for _, rs := range detail.Stacks {
			if o.allStacks || slices.Contains(o.stacks, uuid.MustParse(rs.StackID)) {
				w.write(ctx, &run, report.StackCheckName(rollup, rs.Key), report.StackCheck(detail, rs, opts))
			}
		}
		if err := s.renderRollup(ctx, w, run, detail, opts); err != nil {
			return w.errs, err
		}
	}
	if run.Mode == v1.ModeApply && run.PRNumber > 0 {
		s.renderRunComment(ctx, w, &run, detail, opts)
	}
	if run.PRNumber > 0 {
		if err := s.renderSticky(ctx, w, repo, run.PRNumber, opts); err != nil {
			return w.errs, err
		}
	}
	return w.errs, nil
}

func rollupName(mode v1.RunMode) string {
	if mode == v1.ModeApply {
		return report.CheckApply
	}
	return report.CheckPlan
}

func (s *Service) renderRollup(ctx context.Context, w *checkWriter, run store.Run, detail v1.Run, opts report.Options) error {
	if run.Mode != v1.ModePlan || run.PRNumber == 0 {
		w.write(ctx, &run, rollupName(run.Mode), report.RollupCheck(detail, opts))
		return nil
	}
	view, err := planViewAt(ctx, w.tx, run.RepoID, run.PRNumber, run.SHA)
	if err != nil {
		return err
	}
	owner := run
	if len(view.runs) > 0 {
		owner = view.runs[0]
	}
	rollup := report.RollupCheck(view.v1(), opts)
	w.write(ctx, &owner, report.CheckPlan, rollup)
	if view.nothingAffected() && repoConfig(w.repo).Apply.Mode != v1.ApplyOnMerge {
		w.write(ctx, &owner, report.CheckApply, rollup)
	}
	return nil
}

func (s *Service) renderSticky(ctx context.Context, w *checkWriter, repo store.Repo, pr int, opts report.Options) error {
	plan, ok, err := latestPlanRun(ctx, w.tx, repo.ID, pr)
	if err != nil || !ok {
		return err
	}
	pv, err := planViewAt(ctx, w.tx, repo.ID, pr, plan.SHA)
	if err != nil {
		return err
	}
	applies, _, err := w.tx.ListRuns(ctx, store.RunFilter{RepoID: repo.ID, PRNumber: pr, Mode: v1.ModeApply, Limit: 50})
	if err != nil {
		return storeErr(err, "apply runs of pull request %d", pr)
	}
	for _, a := range applies {
		opts.Applies = append(opts.Applies, report.ApplyRef{Run: a.ToV1(), CommentURL: commentURL(repo, pr, a.CommentID)})
	}
	body := report.StickyComment(pv.v1(), opts)
	if _, err := w.c.UpsertStickyComment(ctx, repo.FullName, pr, report.Marker, body); err != nil {
		w.errs = append(w.errs, fmt.Errorf("runs: sticky comment on %s#%d: %w", repo.FullName, pr, err))
	}
	return nil
}

func (s *Service) renderRunComment(ctx context.Context, w *checkWriter, run *store.Run, detail v1.Run, opts report.Options) {
	if run.CommentID != nil && *run.CommentID == 0 {
		return
	}
	if run.Status == v1.RunApplying {
		opts.PendingApprovals = s.pendingApprovals(ctx, w, *run)
	}
	if err := w.runComment(ctx, run, report.RunComment(detail, opts)); err != nil {
		w.errs = append(w.errs, fmt.Errorf("runs: comment of run %s: %w", run.ID, err))
	}
}

func (s *Service) pendingApprovals(ctx context.Context, w *checkWriter, run store.Run) []report.Approval {
	dispatches, err := w.tx.ListDispatches(ctx, run.ID)
	if err != nil {
		s.log.WarnContext(ctx, "list dispatches", "run_id", run.ID, "error", err)
		return nil
	}
	var out []report.Approval
	for _, d := range dispatches {
		if d.WorkflowRunID == nil || d.CompletedAt != nil || d.Environment == v1.DefaultEnvironment {
			continue
		}
		pending, err := w.c.ListPendingDeployments(ctx, w.repo.FullName, *d.WorkflowRunID)
		if err != nil {
			s.log.WarnContext(ctx, "list pending deployments", "run_id", run.ID, "error", err)
			continue
		}
		for _, p := range pending {
			out = append(out, report.Approval{
				Environment: p.Environment.Name,
				URL:         strings.TrimRight(repoWebURL(w.repo), "/") + "/actions/runs/" + strconv.FormatInt(*d.WorkflowRunID, 10),
			})
		}
	}
	return out
}

func runView(ctx context.Context, db *store.Store, id uuid.UUID) (v1.Run, error) {
	detail, err := db.RunDetail(ctx, id)
	if err != nil {
		return v1.Run{}, storeErr(err, "run %s", id)
	}
	rows, err := db.GetRunStacksWithText(ctx, id)
	if err != nil {
		return v1.Run{}, storeErr(err, "plan text of run %s", id)
	}
	text := make(map[string]string, len(rows))
	for _, rs := range rows {
		text[rs.Key] = rs.PlanText
	}
	for i := range detail.Stacks {
		detail.Stacks[i].PlanText = text[detail.Stacks[i].Key]
	}
	return detail, nil
}

func repoWebURL(repo store.Repo) string { return "https://github.com/" + repo.FullName }

func commentURL(repo store.Repo, pr int, id *int64) string {
	if id == nil || *id <= 0 {
		return ""
	}
	return repoWebURL(repo) + "/pull/" + strconv.Itoa(pr) + "#issuecomment-" + strconv.FormatInt(*id, 10)
}

func latestPlanRun(ctx context.Context, db *store.Store, repoID int64, pr int) (store.Run, bool, error) {
	runs, _, err := db.ListRuns(ctx, store.RunFilter{RepoID: repoID, PRNumber: pr, Mode: v1.ModePlan, Limit: 50})
	if err != nil {
		return store.Run{}, false, storeErr(err, "plan runs of pull request %d", pr)
	}
	for _, r := range runs {
		if r.Status != v1.RunSuperseded {
			return r, true, nil
		}
	}
	return store.Run{}, false, nil
}

func (s *Service) reportOptions(ctx context.Context, db *store.Store, repo store.Repo, pr int) (report.Options, error) {
	locks, err := db.ListLocks(ctx, repo.ID)
	if err != nil {
		return report.Options{}, storeErr(err, "locks of %s", repo.FullName)
	}
	o := report.Options{BaseURL: s.cfg.BaseURL, RepoURL: repoWebURL(repo)}
	for _, l := range locks {
		if pr == 0 || l.PRNumber != pr {
			o.Locks = append(o.Locks, l.ToV1())
		}
	}
	return o, nil
}

type checkWriter struct {
	s    *Service
	tx   *store.Store
	c    *gh.Client
	repo store.Repo
	errs []error
}

func (w *checkWriter) params(run *store.Run, name string, out report.CheckOutput) gh.CheckRunParams {
	p := gh.CheckRunParams{
		Name:       name,
		HeadSHA:    run.SHA,
		Status:     out.Status,
		DetailsURL: w.s.runURL(run.ID),
		ExternalID: run.ID.String(),
		Output:     gh.CheckRunOutput{Title: out.Title, Summary: out.Summary, Text: out.Text},
	}
	if p.Output.Summary == "" {
		p.Output.Summary = out.Title
	}
	if out.Status == report.StatusCompleted {
		p.Conclusion = out.Conclusion
		p.CompletedAt = w.s.now()
	}
	return p
}

func (w *checkWriter) write(ctx context.Context, run *store.Run, name string, out report.CheckOutput) {
	if err := w.upsert(ctx, run, name, w.params(run, name, out)); err != nil {
		w.errs = append(w.errs, err)
	}
}

func (w *checkWriter) upsert(ctx context.Context, run *store.Run, name string, p gh.CheckRunParams) error {
	if id, ok := run.CheckRuns[name]; ok {
		_, err := w.c.UpdateCheckRun(ctx, w.repo.FullName, id, p)
		if err == nil || !errors.Is(err, gh.ErrNotFound) {
			return wrapCheck(name, err)
		}
	}
	cr, err := w.c.CreateCheckRun(ctx, w.repo.FullName, p)
	if err != nil {
		return wrapCheck(name, err)
	}
	if err := w.tx.SetRunCheckRun(ctx, run.ID, name, cr.ID); err != nil {
		return err
	}
	if run.CheckRuns == nil {
		run.CheckRuns = map[string]int64{}
	}
	run.CheckRuns[name] = cr.ID
	return nil
}

func (w *checkWriter) runComment(ctx context.Context, run *store.Run, body string) error {
	if run.CommentID != nil {
		if *run.CommentID == 0 {
			return nil
		}
		_, err := w.c.UpdateIssueComment(ctx, w.repo.FullName, *run.CommentID, body)
		if !errors.Is(err, gh.ErrNotFound) {
			return err
		}
		return w.setRunComment(ctx, run, 0)
	}
	found, err := w.c.OwnComments(ctx, w.repo.FullName, run.PRNumber, report.RunMarker(run.ID.String()))
	if err != nil {
		return err
	}
	var id int64
	switch {
	case len(found) > 0:
		id = found[0].ID
		for _, dup := range found[1:] {
			if err := w.c.DeleteIssueComment(ctx, w.repo.FullName, dup.ID); err != nil && !errors.Is(err, gh.ErrNotFound) {
				return err
			}
		}
		if found[0].Body != body {
			if _, err := w.c.UpdateIssueComment(ctx, w.repo.FullName, id, body); err != nil {
				return err
			}
		}
	case !run.Status.Terminal():
		created, err := w.c.CreateIssueComment(ctx, w.repo.FullName, run.PRNumber, body)
		if err != nil {
			return err
		}
		id = created.ID
	}
	return w.setRunComment(ctx, run, id)
}

func (w *checkWriter) setRunComment(ctx context.Context, run *store.Run, id int64) error {
	if err := w.tx.SetRunComment(ctx, run.ID, id); err != nil {
		return err
	}
	run.CommentID = &id
	return nil
}

func wrapCheck(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("runs: check run %q: %w", name, err)
}

func (w *checkWriter) superseded(ctx context.Context, run *store.Run, by string) {
	names := make([]string, 0, len(run.CheckRuns))
	for name := range run.CheckRuns {
		if strings.HasPrefix(name, report.CheckPlan) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	out := report.CheckOutput{
		Title:      "Superseded by " + shortSHA(by),
		Summary:    "Commit `" + by + "` is now the head of this pull request, so the results of `" + shortSHA(run.SHA) + "` are informational only.",
		Status:     report.StatusCompleted,
		Conclusion: report.ConclusionNeutral,
	}
	for _, name := range names {
		p := w.params(run, name, out)
		if _, err := w.c.UpdateCheckRun(ctx, w.repo.FullName, run.CheckRuns[name], p); err != nil && !errors.Is(err, gh.ErrNotFound) {
			w.errs = append(w.errs, wrapCheck(name, err))
		}
	}
}

type planView struct {
	runs        []store.Run
	rows        []v1.RunStack
	stored      map[string]store.RunStack
	newestEmpty bool
}

func planViewAt(ctx context.Context, db *store.Store, repoID int64, pr int, sha string) (planView, error) {
	runs, _, err := db.ListRuns(ctx, store.RunFilter{RepoID: repoID, PRNumber: pr, SHA: sha, Mode: v1.ModePlan, Limit: 50})
	if err != nil {
		return planView{}, storeErr(err, "plan runs of pull request %d", pr)
	}
	v := planView{stored: map[string]store.RunStack{}}
	seen := map[string]bool{}
	for _, r := range runs {
		if r.Status == v1.RunSuperseded {
			continue
		}
		v.runs = append(v.runs, r)
		detail, err := db.RunDetail(ctx, r.ID)
		if err != nil {
			return planView{}, storeErr(err, "run %s", r.ID)
		}
		if len(v.runs) == 1 {
			v.newestEmpty = len(detail.Stacks) == 0
		}
		rows, err := db.GetRunStacksWithText(ctx, r.ID)
		if err != nil {
			return planView{}, storeErr(err, "stacks of run %s", r.ID)
		}
		for _, rs := range rows {
			if _, ok := v.stored[rs.Key]; !ok && !neverPlanned(r, rs.Status) {
				v.stored[rs.Key] = rs
			}
		}
		for _, rs := range detail.Stacks {
			if !seen[rs.Key] && !neverPlanned(r, rs.Status) {
				seen[rs.Key] = true
				rs.PlanText = v.stored[rs.Key].PlanText
				v.rows = append(v.rows, rs)
			}
		}
	}
	slices.SortStableFunc(v.rows, func(a, b v1.RunStack) int {
		if a.Wave != b.Wave {
			return a.Wave - b.Wave
		}
		return strings.Compare(a.Key, b.Key)
	})
	return v, nil
}

func neverPlanned(r store.Run, st v1.StackStatus) bool {
	return r.Status == v1.RunFailed && st == v1.StackPending
}

func (v planView) row(key string) (v1.RunStack, bool) {
	for _, rs := range v.rows {
		if rs.Key == key {
			return rs, true
		}
	}
	return v1.RunStack{}, false
}

func (v planView) graphID() (uuid.UUID, bool) {
	for _, r := range v.runs {
		if r.GraphID != nil {
			return *r.GraphID, true
		}
	}
	return uuid.Nil, false
}

func (v planView) nothingAffected() bool {
	return len(v.runs) > 0 && len(v.rows) == 0 && v.status() == v1.RunPlanned
}

func (v planView) keys() []string {
	out := make([]string, 0, len(v.rows))
	for _, rs := range v.rows {
		out = append(out, rs.Key)
	}
	return out
}

func (v planView) status() v1.RunStatus {
	switch {
	case len(v.runs) == 0:
		return v1.RunPending
	case len(v.rows) == 0 || v.newestEmpty && v.runs[0].Status == v1.RunFailed:
		return v.runs[0].Status
	}
	rows := make([]store.RunStack, len(v.rows))
	for i, rs := range v.rows {
		rows[i] = store.RunStack{Status: rs.Status}
	}
	return planTarget(rows)
}

func (v planView) v1() v1.Run {
	if len(v.runs) == 0 {
		return v1.Run{Mode: v1.ModePlan, Status: v1.RunPending}
	}
	out := v.runs[0].ToV1()
	out.Stacks = slices.Clone(v.rows)
	out.Status = v.status()
	waves := 0
	for _, rs := range v.rows {
		waves = max(waves, rs.Wave+1)
	}
	out.Waves = max(out.Waves, waves)
	var warnings []string
	for _, r := range v.runs {
		warnings = append(warnings, r.Warnings...)
	}
	slices.Sort(warnings)
	out.Warnings = slices.Compact(warnings)
	if len(out.Warnings) == 0 {
		out.Warnings = nil
	}
	return out
}
