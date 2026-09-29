package runs

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/store"
)

// DispatchWave dispatches stackorder-run.yml for one wave of a run: once
// per environment for an apply, chunked to apply.max_parallel stacks per
// dispatch, and once under the default environment for every pending stack
// of a plan or drift run, which only has wave 0. Calling it again for a
// wave already dispatched, for a wave behind the run, or before the wave
// before it finished cleanly does nothing. A wave with nothing to dispatch
// advances the run at once.
func (s *Service) DispatchWave(ctx context.Context, runID string, wave int) error {
	id, err := parseRunID(runID)
	if err != nil {
		return err
	}
	return s.dispatchWave(ctx, id, wave)
}

func (s *Service) dispatchWave(ctx context.Context, id uuid.UUID, wave int) error {
	run, err := s.st.GetRun(ctx, id)
	if err != nil {
		return storeErr(err, "run %s", id)
	}
	if run.Status.Terminal() || wave < 0 {
		return nil
	}
	rows, err := s.st.GetRunStacks(ctx, id)
	if err != nil {
		return storeErr(err, "stacks of run %s", id)
	}
	if run.Mode != v1.ModeApply {
		if wave != 0 {
			return nil
		}
		return s.dispatchStacks(ctx, run, 0, pendingPlans(rows))
	}
	if wave < run.CurrentWave {
		return nil
	}
	if wave > run.CurrentWave {
		for w := run.CurrentWave; w < wave; w++ {
			if !waveDone(rows, w) {
				return nil
			}
		}
		if applyBroken(rows) {
			return nil
		}
		if _, err := s.st.AdvanceRunWave(ctx, id, wave); err != nil {
			return storeErr(err, "advance run %s to wave %d", id, wave)
		}
		run.CurrentWave = wave
	}
	var targets []store.RunStack
	for _, rs := range rows {
		if rs.Wave == wave && rs.Status == v1.StackPlanned {
			targets = append(targets, rs)
		}
	}
	return s.dispatchStacks(ctx, run, wave, targets)
}

func pendingPlans(rows []store.RunStack) []store.RunStack {
	var out []store.RunStack
	for _, rs := range rows {
		if rs.Status == v1.StackPending {
			out = append(out, rs)
		}
	}
	return out
}

func (s *Service) dispatchStacks(ctx context.Context, run store.Run, wave int, targets []store.RunStack) error {
	started := v1.RunApplying
	if run.Mode != v1.ModeApply {
		started = v1.RunPlanning
	}
	if len(targets) == 0 {
		if run.Mode == v1.ModeApply {
			if _, err := s.transition(ctx, run, started, v1.RunPending, v1.RunPlanned); err != nil {
				return err
			}
		}
		if err := s.advance(ctx, run.ID); err != nil {
			return err
		}
		s.renderQuiet(ctx, run.ID, renderOpts{})
		return nil
	}
	moved, err := s.transition(ctx, run, started, v1.RunPending, v1.RunPlanned)
	if err != nil {
		return err
	}
	if !moved {
		current, err := s.st.GetRun(ctx, run.ID)
		if err != nil {
			return storeErr(err, "run %s", run.ID)
		}
		if current.Status.Terminal() {
			return nil
		}
	}
	repo, err := s.st.GetRepo(ctx, run.RepoID)
	if err != nil {
		return storeErr(err, "repository of run %s", run.ID)
	}
	nodes, err := s.graphNodes(ctx, run)
	if err != nil {
		return err
	}
	cfg := repoConfig(repo)
	type pendingSend struct {
		d    store.Dispatch
		rows []store.RunStack
	}
	var pending []pendingSend
	err = s.st.InTx(ctx, func(tx *store.Store) error {
		for _, g := range dispatchGroups(run.Mode, targets, cfg.Apply.MaxParallel) {
			d, created, err := tx.CreateDispatchChunk(ctx, run.ID, wave, g.env, run.Mode, g.chunk)
			if err != nil {
				return storeErr(err, "record dispatch of run %s", run.ID)
			}
			if !created {
				continue
			}
			for _, rs := range g.rows {
				if _, err := tx.UpdateRunStack(ctx, run.ID, rs.StackID, store.RunStackPatch{DispatchID: &d.ID}); err != nil {
					return storeErr(err, "record dispatch of %s", rs.Key)
				}
			}
			pending = append(pending, pendingSend{d: d, rows: g.rows})
		}
		return nil
	})
	if err != nil {
		return err
	}
	var sent []uuid.UUID
	var sendErr error
	for _, p := range pending {
		if err := s.send(ctx, repo, run, p.d, p.rows, nodes, cfg); err != nil {
			if permanentGitHubError(err) {
				return s.failDispatch(ctx, repo, run, err)
			}
			sendErr = cmp.Or(sendErr, err)
			continue
		}
		for _, rs := range p.rows {
			sent = append(sent, rs.StackID)
		}
	}
	s.renderQuiet(ctx, run.ID, renderOpts{stacks: sent, allStacks: wave == 0})
	return sendErr
}

type dispatchGroup struct {
	env   string
	chunk int
	rows  []store.RunStack
}

func dispatchGroups(mode v1.RunMode, rows []store.RunStack, maxParallel int) []dispatchGroup {
	sorted := slices.Clone(rows)
	slices.SortFunc(sorted, func(a, b store.RunStack) int { return strings.Compare(a.Key, b.Key) })
	if mode != v1.ModeApply {
		return []dispatchGroup{{env: v1.DefaultEnvironment, rows: sorted}}
	}
	byEnv := map[string][]store.RunStack{}
	for _, rs := range sorted {
		env := firstNonEmpty(rs.Environment, v1.DefaultEnvironment)
		byEnv[env] = append(byEnv[env], rs)
	}
	envs := make([]string, 0, len(byEnv))
	for env := range byEnv {
		envs = append(envs, env)
	}
	slices.Sort(envs)
	var out []dispatchGroup
	for _, env := range envs {
		for i, part := range chunk(byEnv[env], maxParallel) {
			out = append(out, dispatchGroup{env: env, chunk: i, rows: part})
		}
	}
	return out
}

func (s *Service) graphNodes(ctx context.Context, run store.Run) (map[string]v1.Stack, error) {
	out := map[string]v1.Stack{}
	if run.GraphID == nil {
		return out, nil
	}
	g, err := s.st.GetGraphByID(ctx, *run.GraphID)
	if err != nil {
		return nil, storeErr(err, "graph of run %s", run.ID)
	}
	for _, st := range g.Stacks {
		out[st.Key] = st
	}
	return out, nil
}

func matrixEntries(run store.Run, rows []store.RunStack, nodes map[string]v1.Stack, cfg *v1.RepoConfig) []v1.MatrixEntry {
	out := make([]v1.MatrixEntry, 0, len(rows))
	for _, rs := range rows {
		node := nodes[rs.Key]
		sc := node.Config
		if sc == nil {
			sc = &v1.StackConfig{}
		}
		path, ws := rs.Path, rs.Workspace
		if path == "" {
			path, ws = v1.SplitStackKey(rs.Key)
		}
		env := v1.DefaultEnvironment
		if run.Mode == v1.ModeApply {
			env = firstNonEmpty(rs.Environment, v1.DefaultEnvironment)
		}
		e := v1.MatrixEntry{
			Stack:       path,
			Key:         rs.Key,
			Workspace:   ws,
			Environment: env,
			Wave:        rs.Wave,
			Tool:        v1.Tool(firstNonEmpty(string(node.Tool), string(sc.Tool), string(cfg.Tool))),
			ToolVersion: firstNonEmpty(node.ToolVersion, sc.ToolVersion, cfg.ToolVersion),
			PlanOutput:  firstNonEmpty(rs.PlanOutput, node.PlanOutput, string(sc.PlanOutput), string(cfg.PlanOutput)),
			SHA:         run.SHA,
		}
		if run.Mode == v1.ModeApply {
			e.PlanRunID, e.Artifact = rs.PlanRunID, rs.PlanArtifact
		}
		out = append(out, e)
	}
	return out
}

func (s *Service) send(ctx context.Context, repo store.Repo, run store.Run, d store.Dispatch, rows []store.RunStack, nodes map[string]v1.Stack, cfg *v1.RepoConfig) error {
	entries, err := json.Marshal(matrixEntries(run, rows, nodes, cfg))
	if err != nil {
		return fmt.Errorf("runs: encode stacks of run %s: %w", run.ID, err)
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	inputs := map[string]string{
		"run_id": run.ID.String(),
		"mode":   string(run.Mode),
		"wave":   strconv.Itoa(d.Wave),
		"sha":    run.SHA,
		"stacks": string(entries),
	}
	err = c.DispatchWorkflow(ctx, repo.FullName, s.cfg.WorkflowFile, repo.DefaultBranch, inputs)
	s.m.Dispatched(run.Mode, err == nil)
	if err != nil {
		return fmt.Errorf("runs: dispatch %s wave %d of run %s: %w", run.Mode, d.Wave, run.ID, err)
	}
	if _, err := s.st.MarkDispatchSent(ctx, d.ID); err != nil {
		return storeErr(err, "mark dispatch %s sent", d.ID)
	}
	return nil
}

func (s *Service) resend(ctx context.Context, d store.Dispatch) error {
	run, err := s.st.GetRun(ctx, d.RunID)
	if err != nil {
		return storeErr(err, "run %s", d.RunID)
	}
	if run.Status.Terminal() {
		return nil
	}
	repo, err := s.st.GetRepo(ctx, run.RepoID)
	if err != nil {
		return storeErr(err, "repository of run %s", run.ID)
	}
	rows, err := s.st.GetRunStacks(ctx, run.ID)
	if err != nil {
		return storeErr(err, "stacks of run %s", run.ID)
	}
	var carried []store.RunStack
	for _, rs := range rows {
		if rs.DispatchID != nil && *rs.DispatchID == d.ID {
			carried = append(carried, rs)
		}
	}
	if len(carried) == 0 {
		return nil
	}
	nodes, err := s.graphNodes(ctx, run)
	if err != nil {
		return err
	}
	if err := s.send(ctx, repo, run, d, carried, nodes, repoConfig(repo)); err != nil {
		if permanentGitHubError(err) {
			return s.failDispatch(ctx, repo, run, err)
		}
		return err
	}
	return nil
}

func dispatchWarning(workflow string, err error) string {
	return fmt.Sprintf("workflow dispatch failed: %v; check that .github/workflows/%s exists on the default branch and declares the inputs run_id, mode, wave, sha and stacks", err, workflow)
}

func (s *Service) failDispatch(ctx context.Context, repo store.Repo, run store.Run, cause error) error {
	s.log.WarnContext(ctx, "dispatch refused by github", "run_id", run.ID, "error", cause)
	warning := dispatchWarning(s.cfg.WorkflowFile, cause)
	if err := s.st.AddRunWarning(ctx, run.ID, warning); err != nil {
		return storeErr(err, "record warning on run %s", run.ID)
	}
	moved, err := s.transition(ctx, run, v1.RunFailed, nonTerminalRunStatuses...)
	if err != nil {
		return err
	}
	if moved {
		s.onFinished(ctx, repo, run, v1.RunFailed)
		if run.PRNumber > 0 {
			var apiErr *gh.APIError
			reason := cause.Error()
			if errors.As(cause, &apiErr) {
				reason = apiErr.Error()
			}
			body := "**Stackorder could not start the " + string(run.Mode) + ".** GitHub refused to dispatch `.github/workflows/" +
				s.cfg.WorkflowFile + "` on `" + repo.DefaultBranch + "`: " + reason + "\n\n" +
				"Check that the workflow exists on the default branch and declares the inputs `run_id`, `mode`, `wave`, `sha` and `stacks`, then comment `stackorder " + string(run.Mode) + "` again.\n"
			s.comment(ctx, repo, run.PRNumber, body)
		}
	}
	s.renderQuiet(ctx, run.ID, renderOpts{allStacks: true})
	return nil
}

func (s *Service) comment(ctx context.Context, repo store.Repo, pr int, body string) {
	c, err := s.client(ctx, repo)
	if err == nil {
		_, err = c.CreateIssueComment(ctx, repo.FullName, pr, body)
	}
	if err != nil {
		s.log.WarnContext(ctx, "post comment", "repo", repo.FullName, "pr", pr, "error", err)
	}
}
