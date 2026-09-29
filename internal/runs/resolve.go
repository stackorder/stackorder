package runs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/graph"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

// UploadGraph stores the graph a plan run's resolve job scanned, resolves
// the affected stacks against it, records them on the run and creates the
// run's check runs and sticky comment. A dependency cycle fails the run and
// is reported in the response with a nil error.
//
// When the uploaded stackorder.yaml differs from the stored default-branch
// configuration, the graph is resolved under both and the affected sets
// are united, so a pull request cannot shrink its own affected set by
// editing its copy. Stacks of the default-branch graph (the latest graph
// before the first merge) that only the default-branch configuration
// discovers are added to the stored graph when that configuration finds
// them affected, unless the pull request deletes their directory, which is
// warned about instead. Every stack affected only under the default-branch
// configuration is planned too, with a warning naming the keys that
// differ.
func (s *Service) UploadGraph(ctx context.Context, p principal.Principal, runID string, req v1.GraphUploadRequest) (*v1.ResolveResponse, error) {
	run, repo, err := s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	c, err := claimsOf(p)
	if err != nil {
		return nil, err
	}
	if c.EventName != eventPullRequest {
		return nil, principal.Wrap(principal.ErrForbidden, "only the resolve job of a pull request uploads graphs; run %s was resolved by the server", run.ID)
	}
	if err := checkRepoClaims(c, repo); err != nil {
		return nil, err
	}
	if err := checkPullClaims(c, run); err != nil {
		return nil, err
	}
	if run.Mode != v1.ModePlan || run.Status != v1.RunPending && run.Status != v1.RunPlanning {
		return nil, principal.Wrap(principal.ErrConflict, "run %s is a %s run in status %s and takes no graph", run.ID, run.Mode, run.Status)
	}
	g := req.Graph
	switch {
	case g.SHA == "":
		g.SHA = run.SHA
	case g.SHA != run.SHA:
		return nil, &principal.InvalidError{Field: "graph.sha", Reason: fmt.Sprintf("is %s, the run is for %s", g.SHA, run.SHA)}
	}
	switch {
	case g.Repo == "":
		g.Repo = repo.FullName
	case !strings.EqualFold(g.Repo, repo.FullName):
		return nil, &principal.InvalidError{Field: "graph.repo", Reason: fmt.Sprintf("is %s, the run belongs to %s", g.Repo, repo.FullName)}
	}
	cfg, err := resolutionConfig(req.Config, repo)
	if err != nil {
		return nil, err
	}
	base := repoConfig(repo)
	var differs []string
	if req.Config != nil {
		differs = configDiff(base, cfg)
	}
	var baseline *v1.Graph
	if len(differs) > 0 {
		baseline = s.baselineGraph(ctx, repo)
	}
	stored, graphID, stackIDs, cached, err := s.storeGraph(ctx, repo, &g)
	if err != nil {
		return nil, err
	}
	locks, err := s.lockMap(ctx, repo.ID, run.PRNumber)
	if err != nil {
		return nil, err
	}
	in := graph.Input{ChangedPaths: req.ChangedPaths, Config: cfg, Requested: req.Stacks, Locks: locks}
	resolved := s.withExternalDependents(ctx, repo, stored)
	resp, rerr := graph.Resolve(resolved, in)
	var removed []string
	if rerr == nil && len(differs) > 0 {
		extended := withDefaultBranchStacks(stored, baseline, in, cfg, base)
		extended, removed, err = s.withoutDeletedStacks(ctx, repo, run.SHA, stored, baseline, extended, req.ChangedPaths)
		if err != nil {
			return nil, err
		}
		if extended != nil {
			extended.SHA = run.SHA
			id, ids, err := s.st.SaveGraph(ctx, repo.ID, extended)
			if err != nil {
				return nil, storeErr(err, "save graph of %s at %s", repo.FullName, shortSHA(run.SHA))
			}
			stored, graphID, stackIDs, cached = extended, id, ids, false
			resolved = s.withExternalDependents(ctx, repo, stored)
		}
		baseIn := in
		baseIn.Config = base
		baseResp, baseErr := graph.Resolve(resolved, baseIn)
		resp, rerr = unionResolution(resolved, resp, baseResp, baseErr, differs)
		if len(removed) > 0 && resp != nil {
			resp.Warnings = append(slices.Clone(resp.Warnings), fmt.Sprintf(
				"the stackorder.yaml of this pull request differs from the default branch's in %s; %s, which only the default branch's configuration discovers, no longer exists at %s and is recorded as removed",
				strings.Join(differs, ", "), strings.Join(removed, ", "), shortSHA(run.SHA)))
			slices.Sort(resp.Warnings)
		}
	}
	switch {
	case errors.Is(rerr, graph.ErrCycle):
		resp.RunID, resp.Cached = run.ID.String(), cached
		if err := s.st.SetRunGraph(ctx, run.ID, graphID, 0, resp.Warnings); err != nil {
			return nil, storeErr(err, "record graph of run %s", run.ID)
		}
		if _, err := s.transition(ctx, run, v1.RunFailed, v1.RunPending, v1.RunPlanning); err != nil {
			return nil, err
		}
		s.renderQuiet(ctx, run.ID, renderOpts{resolve: resp})
		return resp, nil
	case rerr != nil:
		if _, err := s.transition(ctx, run, v1.RunFailed, v1.RunPending, v1.RunPlanning); err != nil {
			return nil, err
		}
		s.renderQuiet(ctx, run.ID, renderOpts{resolveErr: rerr})
		return nil, &principal.InvalidError{Field: "graph", Reason: rerr.Error()}
	}
	if err := s.enforcePlanOutput(ctx, repo, resp.Affected); err != nil {
		return nil, err
	}
	rows := make([]store.RunStack, 0, len(resp.Affected))
	var ids []uuid.UUID
	for _, a := range resp.Affected {
		id, ok := stackIDs[a.Key]
		if !ok {
			continue
		}
		ids = append(ids, id)
		rows = append(rows, store.RunStack{
			StackID: id, Wave: a.Wave, Status: v1.StackPending, Reasons: a.Reasons, Via: a.Via,
			Environment: a.Environment, PlanOutput: a.PlanOutput,
		})
	}
	if err := s.st.UpsertRunStacks(ctx, run.ID, rows); err != nil {
		return nil, storeErr(err, "record stacks of run %s", run.ID)
	}
	warnings := slices.Clone(resp.Warnings)
	for _, e := range resp.External {
		warnings = append(warnings, "external dependent "+e+" is not scheduled by this run")
	}
	if err := s.st.SetRunGraph(ctx, run.ID, graphID, len(resp.Waves), warnings); err != nil {
		return nil, storeErr(err, "record graph of run %s", run.ID)
	}
	target := v1.RunPlanning
	if len(rows) == 0 {
		target = v1.RunPlanned
	}
	if _, err := s.transition(ctx, run, target, v1.RunPending, v1.RunPlanning); err != nil {
		return nil, err
	}
	resp.RunID, resp.Cached = run.ID.String(), cached
	resp.Matrix = graph.BuildMatrix(resp.Affected, run.SHA)
	s.renderQuiet(ctx, run.ID, renderOpts{resolve: resp, stacks: ids})
	return resp, nil
}

func (s *Service) enforcePlanOutput(ctx context.Context, repo store.Repo, affected []v1.AffectedStack) error {
	cfg := repoConfig(repo)
	keys := make([]string, len(affected))
	for i, a := range affected {
		keys[i] = a.Key
	}
	defaults, err := s.defaultStackConfigs(ctx, repo, keys)
	if err != nil {
		return err
	}
	for i, a := range affected {
		summary, err := summaryOnDefaultBranch(cfg, defaults, a)
		if err != nil {
			s.log.WarnContext(ctx, "default-branch configuration does not render; plan output falls back to summary",
				"repo", repo.FullName, "stack", a.Key, "error", err)
		}
		if summary {
			affected[i].PlanOutput = string(v1.PlanOutputSummary)
		}
	}
	return nil
}

func summaryOnDefaultBranch(cfg *v1.RepoConfig, defaults map[string]*v1.StackConfig, a v1.AffectedStack) (bool, error) {
	eff, err := defaultEffective(cfg, defaults, a.Key, a.Path)
	if err != nil {
		return true, err
	}
	return eff.PlanOutput == v1.PlanOutputSummary, nil
}

func resolutionConfig(uploaded *v1.RepoConfig, repo store.Repo) (*v1.RepoConfig, error) {
	if uploaded == nil {
		return repoConfig(repo), nil
	}
	c := *uploaded
	config.ApplyDefaults(&c)
	if err := config.Validate(&c); err != nil {
		return nil, &principal.InvalidError{Field: "config", Reason: err.Error()}
	}
	return &c, nil
}

func (s *Service) storeGraph(ctx context.Context, repo store.Repo, g *v1.Graph) (*v1.Graph, uuid.UUID, map[string]uuid.UUID, bool, error) {
	if g.TreeHash != "" {
		found, id, err := s.st.FindGraphByTreeHash(ctx, repo.ID, g.TreeHash)
		switch {
		case err == nil:
			ids, err := s.st.GraphStackIDs(ctx, id)
			if err != nil {
				return nil, uuid.Nil, nil, false, storeErr(err, "stacks of graph %s", id)
			}
			return found, id, ids, true, nil
		case !errors.Is(err, store.ErrNotFound):
			return nil, uuid.Nil, nil, false, storeErr(err, "graph by tree hash")
		}
	}
	id, ids, err := s.st.SaveGraph(ctx, repo.ID, g)
	if err != nil {
		return nil, uuid.Nil, nil, false, storeErr(err, "save graph of %s at %s", repo.FullName, shortSHA(g.SHA))
	}
	return g, id, ids, false, nil
}

func (s *Service) lockMap(ctx context.Context, repoID int64, pr int) (map[string]v1.LockInfo, error) {
	locks, err := s.st.ListLocks(ctx, repoID)
	if err != nil {
		return nil, storeErr(err, "locks")
	}
	out := map[string]v1.LockInfo{}
	for _, l := range locks {
		if pr == 0 || l.PRNumber != pr {
			out[l.StackKey] = l.ToV1()
		}
	}
	return out, nil
}

func (s *Service) withExternalDependents(ctx context.Context, repo store.Repo, g *v1.Graph) *v1.Graph {
	deps, err := s.st.ExternalDependents(ctx, repo.ID)
	if err != nil {
		s.log.WarnContext(ctx, "list external dependents", "repo", repo.FullName, "error", err)
		return g
	}
	return augmentExternal(g, deps)
}

func augmentExternal(g *v1.Graph, deps []store.ExternalDependent) *v1.Graph {
	if len(deps) == 0 {
		return g
	}
	local := map[string]bool{}
	known := map[string]bool{}
	for _, st := range g.Stacks {
		known[st.Key] = true
		if !st.External && (st.Repo == "" || strings.EqualFold(st.Repo, g.Repo)) {
			local[st.Key] = true
		}
	}
	out := *g
	out.Stacks = slices.Clone(g.Stacks)
	out.Edges = slices.Clone(g.Edges)
	for _, d := range deps {
		if !local[d.ToKey] {
			continue
		}
		key := v1.QualifiedStackKey(d.Repo, d.FromKey)
		if !known[key] {
			known[key] = true
			path, instance := v1.SplitStackKey(d.FromKey)
			out.Stacks = append(out.Stacks, v1.Stack{Key: key, Path: path, Instance: instance, Repo: d.Repo, External: true})
		}
		out.Edges = append(out.Edges, v1.Edge{From: v1.StackRef(key), To: v1.StackRef(d.ToKey), Type: d.Type})
	}
	return &out
}

func (s *Service) transition(ctx context.Context, run store.Run, to v1.RunStatus, from ...v1.RunStatus) (bool, error) {
	moved, err := s.st.UpdateRunStatus(ctx, run.ID, to, from...)
	switch {
	case errors.Is(err, store.ErrConflict):
		return false, nil
	case err != nil:
		return false, storeErr(err, "move run %s to %s", run.ID, to)
	}
	if moved.Status != run.Status {
		s.m.RunStatusChanged(moved.Status, moved.Trigger, moved.Mode)
	}
	return true, nil
}
