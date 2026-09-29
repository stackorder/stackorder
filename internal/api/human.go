package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

var knownRunStatuses = []v1.RunStatus{
	v1.RunPending, v1.RunPlanning, v1.RunPlanned, v1.RunApplying,
	v1.RunApplied, v1.RunFailed, v1.RunUnconfirmed, v1.RunSuperseded,
}

var knownModes = []v1.RunMode{v1.ModePlan, v1.ModeApply, v1.ModeDrift}

func (s *server) me(w http.ResponseWriter, r *http.Request, id identity) error {
	if id.Kind == principal.APIKey {
		s.writeJSON(w, r, http.StatusOK, v1.Whoami{Login: id.Actor(), Orgs: []string{}, Admin: true})
		return nil
	}
	s.writeJSON(w, r, http.StatusOK, id.session.ToV1())
	return nil
}

func (s *server) visibleRepo(r *http.Request, id identity) (store.Repo, error) {
	name := r.PathValue("owner") + "/" + r.PathValue("repo")
	repo, err := s.db.GetRepoByName(r.Context(), name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Repo{}, fmt.Errorf("get repository: %w", err)
	}
	if err != nil || !id.sees(repo.Account) {
		return store.Repo{}, notFound("repository " + name + " not found")
	}
	return repo, nil
}

func (s *server) overview(w http.ResponseWriter, r *http.Request, id identity) error {
	ov, err := s.db.Overview(r.Context(), id.accounts()...)
	if err != nil {
		return fmt.Errorf("overview: %w", err)
	}
	for i := range ov.RecentRuns {
		ov.RecentRuns[i].HTMLURL = s.runURL(ov.RecentRuns[i].ID)
	}
	s.writeJSON(w, r, http.StatusOK, ov)
	return nil
}

func (s *server) repos(w http.ResponseWriter, r *http.Request, id identity) error {
	limit, cursor, err := pageParams(r)
	if err != nil {
		return err
	}
	all, err := s.db.RepoSummaries(r.Context(), id.accounts()...)
	if err != nil {
		return fmt.Errorf("repo summaries: %w", err)
	}
	items, next, err := pageByKey(all, func(rs v1.RepoSummary) string { return rs.FullName }, limit, cursor)
	if err != nil {
		return err
	}
	s.writeJSON(w, r, http.StatusOK, v1.Page[v1.RepoSummary]{Items: items, NextCursor: next, Total: len(all)})
	return nil
}

func (s *server) repoRuns(w http.ResponseWriter, r *http.Request, id identity) error {
	repo, err := s.visibleRepo(r, id)
	if err != nil {
		return err
	}
	limit, cursor, err := pageParams(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := store.RunFilter{RepoID: repo.ID, Limit: limit, Cursor: cursor}
	if v := q.Get("status"); v != "" {
		if !slices.Contains(knownRunStatuses, v1.RunStatus(v)) {
			return &principal.InvalidError{Field: "status", Reason: "unknown run status " + strconv.Quote(v)}
		}
		f.Status = v1.RunStatus(v)
	}
	if v := q.Get("mode"); v != "" {
		if !slices.Contains(knownModes, v1.RunMode(v)) {
			return &principal.InvalidError{Field: "mode", Reason: "unknown run mode " + strconv.Quote(v)}
		}
		f.Mode = v1.RunMode(v)
	}
	if v := q.Get("pr"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return &principal.InvalidError{Field: "pr", Reason: "must be a positive pull request number"}
		}
		f.PRNumber = n
	}
	runs, next, err := s.db.ListRuns(r.Context(), f)
	if err != nil {
		return storeCursorError(err)
	}
	page := v1.Page[v1.Run]{Items: make([]v1.Run, len(runs)), NextCursor: next}
	for i, run := range runs {
		page.Items[i] = run.ToV1()
		page.Items[i].HTMLURL = s.runURL(page.Items[i].ID)
	}
	s.writeJSON(w, r, http.StatusOK, page)
	return nil
}

func (s *server) repoGraph(w http.ResponseWriter, r *http.Request, id identity) error {
	ctx := r.Context()
	repo, err := s.visibleRepo(r, id)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	ref := q.Get("ref")
	var run *store.Run
	if raw := q.Get("run"); raw != "" {
		runID, err := uuid.Parse(raw)
		if err != nil {
			return notFound(fmt.Sprintf("run %q not found in %s", raw, repo.FullName))
		}
		got, err := s.db.GetRun(ctx, runID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("get run: %w", err)
		}
		if err != nil || got.RepoID != repo.ID {
			return notFound(fmt.Sprintf("run %q not found in %s", raw, repo.FullName))
		}
		run = &got
	}

	var (
		g       *v1.Graph
		graphID uuid.UUID
	)
	switch {
	case ref != "":
		g, graphID, err = s.graphAtRef(ctx, repo, ref)
		if err != nil {
			return err
		}
	case run != nil && run.GraphID != nil:
		graphID = *run.GraphID
		g, err = s.db.GetGraphByID(ctx, graphID)
		if errors.Is(err, store.ErrNotFound) {
			return notFound("the graph of run " + run.ID.String() + " is no longer stored")
		}
	default:
		g, graphID, err = s.currentGraph(ctx, repo.ID)
		if err == nil && g == nil {
			return notFound("no graph recorded for " + repo.FullName + " yet")
		}
	}
	if err != nil {
		return fmt.Errorf("load graph: %w", err)
	}

	ids, err := s.db.GraphStackIDs(ctx, graphID)
	if err != nil {
		return fmt.Errorf("graph stack ids: %w", err)
	}
	view := v1.GraphView{Repo: repo.FullName, SHA: g.SHA, Graph: *g, StackIDs: make(map[string]string, len(ids))}
	for k, v := range ids {
		view.StackIDs[k] = v.String()
	}
	if run != nil {
		rows, err := s.db.GetRunStacks(ctx, run.ID)
		if err != nil {
			return fmt.Errorf("run stacks: %w", err)
		}
		view.Affected, view.Waves = replay(*run, rows, g)
	}
	s.writeJSON(w, r, http.StatusOK, view)
	return nil
}

const (
	defaultGraphRef = "default"
	minSHAPrefix    = 7
	maxSHALength    = 64
)

func (s *server) graphAtRef(ctx context.Context, repo store.Repo, ref string) (*v1.Graph, uuid.UUID, error) {
	if ref == defaultGraphRef {
		g, id, err := s.db.GetDefaultGraph(ctx, repo.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, uuid.Nil, notFound("no default-branch graph recorded for " + repo.FullName + " yet")
		}
		if err != nil {
			return nil, uuid.Nil, fmt.Errorf("load default graph: %w", err)
		}
		return g, id, nil
	}
	prefix := strings.ToLower(ref)
	if len(prefix) < minSHAPrefix || len(prefix) > maxSHALength || strings.Trim(prefix, "0123456789abcdef") != "" {
		return nil, uuid.Nil, &principal.InvalidError{Field: "ref",
			Reason: fmt.Sprintf("%q is not a commit SHA, a SHA prefix of at least %d characters or %q; branch names are not supported", ref, minSHAPrefix, defaultGraphRef)}
	}
	shas, err := s.db.GraphSHAsWithPrefix(ctx, repo.ID, prefix, 2)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("graph shas: %w", err)
	}
	if len(shas) == 0 {
		return nil, uuid.Nil, notFound("no graph recorded for " + repo.FullName + " at " + ref)
	}
	if len(shas) > 1 {
		return nil, uuid.Nil, &principal.InvalidError{Field: "ref",
			Reason: fmt.Sprintf("%s is ambiguous: graphs are recorded for %s and %s at least; give more characters", ref, shas[0], shas[1])}
	}
	g, id, err := s.db.GetGraph(ctx, repo.ID, shas[0])
	if errors.Is(err, store.ErrNotFound) {
		return nil, uuid.Nil, notFound("no graph recorded for " + repo.FullName + " at " + ref)
	}
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("load graph: %w", err)
	}
	return g, id, nil
}

func replay(run store.Run, rows []store.RunStack, g *v1.Graph) ([]v1.AffectedStack, [][]string) {
	nodes := make(map[string]v1.Stack, len(g.Stacks))
	for _, st := range g.Stacks {
		nodes[st.Key] = st
	}
	affected := make([]v1.AffectedStack, 0, len(rows))
	waves := run.Waves
	for _, rs := range rows {
		a := v1.AffectedStack{
			Key:         rs.Key,
			Path:        rs.Path,
			Workspace:   rs.Workspace,
			Wave:        rs.Wave,
			Reasons:     append([]v1.Reason{}, rs.Reasons...),
			Environment: rs.Environment,
		}
		if n, ok := nodes[rs.Key]; ok {
			a.Tool, a.ToolVersion, a.PlanOutput = n.Tool, n.ToolVersion, n.PlanOutput
		}
		affected = append(affected, a)
		waves = max(waves, rs.Wave+1)
	}
	byWave := make([][]string, waves)
	for i := range byWave {
		byWave[i] = []string{}
	}
	for _, a := range affected {
		if a.Wave >= 0 {
			byWave[a.Wave] = append(byWave[a.Wave], a.Key)
		}
	}
	return affected, byWave
}

func (s *server) audit(w http.ResponseWriter, r *http.Request, id identity) error {
	limit, cursor, err := pageParams(r)
	if err != nil {
		return err
	}
	entries, next, err := s.db.ListAudit(r.Context(), store.AuditFilter{Limit: limit, Cursor: cursor})
	if err != nil {
		return storeCursorError(err)
	}
	page := v1.Page[v1.AuditEntry]{Items: []v1.AuditEntry{}, NextCursor: next}
	for _, e := range entries {
		if !auditVisible(id, e) {
			continue
		}
		page.Items = append(page.Items, v1.AuditEntry{At: e.At.UTC(), Actor: e.Actor, Action: e.Action, Target: e.Target, Details: e.Details})
	}
	s.writeJSON(w, r, http.StatusOK, page)
	return nil
}

func auditVisible(id identity, e store.AuditEntry) bool {
	if id.Kind == principal.APIKey || strings.EqualFold(e.Actor, id.Login) {
		return true
	}
	scope := e.Target
	if repo, ok := e.Details["repo"].(string); ok && repo != "" {
		scope = repo
	}
	if kind, rest, ok := strings.Cut(scope, ":"); ok && !strings.Contains(kind, "/") {
		scope = rest
	}
	owner, _, ok := strings.Cut(scope, "/")
	return ok && owner != "" && id.sees(owner)
}
