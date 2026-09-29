//go:build integration

package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/oidcfake"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

type world struct {
	t       *testing.T
	st      *store.Store
	runs    *fakeRuns
	issuer  *oidcfake.Issuer
	srv     *server
	h       http.Handler
	infra   store.Repo
	modRepo store.Repo
	globex  store.Repo

	mergedGraph, prGraph uuid.UUID
	stacks               map[string]uuid.UUID
	globexStack          uuid.UUID
	planRun, applyRun    store.Run
	globexRun            store.Run
	eksPinned, eksFamily store.Module
	globexFamily         store.Module

	key     string
	session *http.Cookie
}

const (
	mergedSHA = "abcdef0111111111111111111111111111111111"
	prHeadSHA = "abcdef0222222222222222222222222222222222"
)

func ptr[T any](v T) *T { return &v }

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := t.Context()
	w := &world{t: t, st: pgtest.New(t), runs: &fakeRuns{}, issuer: oidcfake.New(t)}
	verifier, err := oidc.New(w.issuer.Config(testBaseURL))
	require.NoError(t, err)
	w.srv = newServer(Config{BaseURL: testBaseURL, SessionKey: testSessionKey}, Deps{
		Store:    w.st,
		Runs:     w.runs,
		Verifier: verifier,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, w.st)
	w.h = w.srv.routes()

	for _, in := range []store.Installation{{ID: 1, Account: "acme", AccountType: "Organization"}, {ID: 2, Account: "globex", AccountType: "Organization"}} {
		_, err = w.st.UpsertInstallation(ctx, in)
		require.NoError(t, err)
	}
	w.infra, err = w.st.UpsertRepo(ctx, store.RepoParams{ID: 100, InstallationID: 1, FullName: "acme/infra", DefaultBranch: "main"})
	require.NoError(t, err)
	w.modRepo, err = w.st.UpsertRepo(ctx, store.RepoParams{ID: 101, InstallationID: 1, FullName: "acme/modules", DefaultBranch: "main"})
	require.NoError(t, err)
	w.globex, err = w.st.UpsertRepo(ctx, store.RepoParams{ID: 200, InstallationID: 2, FullName: "globex/platform", DefaultBranch: "main"})
	require.NoError(t, err)

	eks := func(ref string) v1.Module {
		return v1.Module{Key: "acme/modules//eks@" + ref, Kind: v1.ModuleGit, Ref: ref, Source: "git::https://github.com/acme/modules.git//eks?ref=" + ref}
	}
	graph := func(sha, eksRef string, extra ...v1.Stack) *v1.Graph {
		return &v1.Graph{
			Repo: "acme/infra", SHA: sha, TreeHash: "tree-" + sha,
			Stacks: append([]v1.Stack{
				{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc", Environment: "production", Tool: v1.ToolTofu,
					Backend: &v1.Backend{Type: "s3", Bucket: "state", Key: "prod/vpc.tfstate", Region: "eu-west-1"}},
				{Key: "stacks/prod/eks", Path: "stacks/prod/eks", Environment: "production", Tool: v1.ToolTofu, ToolVersion: "1.9.0"},
				{Key: "stacks/prod/apps:blue", Path: "stacks/prod/apps", Workspace: "blue", PlanOutput: "summary"},
				{Key: "acme/network//stacks/prod/tgw", Path: "stacks/prod/tgw", Repo: "acme/network", External: true},
			}, extra...),
			Modules: []v1.Module{
				{Key: "acme/infra//modules/vpc", Kind: v1.ModuleLocal, Path: "modules/vpc", Source: "../../modules/vpc"},
				eks(eksRef),
			},
			Edges: []v1.Edge{
				{From: v1.StackRef("stacks/prod/vpc"), To: v1.ModuleRef("acme/infra//modules/vpc"), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": "", "source": "../../modules/vpc"}},
				{From: v1.StackRef("stacks/prod/eks"), To: v1.ModuleRef(eks(eksRef).Key), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": eksRef}},
				{From: v1.StackRef("stacks/prod/eks"), To: v1.StackRef("stacks/prod/vpc"), Type: v1.EdgeDependsOn},
				{From: v1.StackRef("stacks/prod/apps:blue"), To: v1.StackRef("stacks/prod/eks"), Type: v1.EdgeReadsState, Inferred: true, Meta: map[string]string{"bucket": "state", "key": "env:/blue/prod/eks.tfstate"}},
				{From: v1.StackRef("stacks/prod/vpc"), To: v1.StackRef("acme/network//stacks/prod/tgw"), Type: v1.EdgeDependsOn},
			},
		}
	}
	var ids map[string]uuid.UUID
	w.mergedGraph, w.stacks, err = w.st.SaveGraph(ctx, w.infra.ID, graph(mergedSHA, "v1.2.0"))
	require.NoError(t, err)
	w.prGraph, ids, err = w.st.SaveGraph(ctx, w.infra.ID, graph(prHeadSHA, "v1.4.1", v1.Stack{Key: "stacks/prod/new", Path: "stacks/prod/new"}))
	require.NoError(t, err)
	w.stacks["stacks/prod/new"] = ids["stacks/prod/new"]
	require.NoError(t, w.st.SetDefaultGraph(ctx, w.infra.ID, w.mergedGraph))

	globexMod := v1.Module{Key: "globex/mods//net@v1.0.0", Kind: v1.ModuleGit, Ref: "v1.0.0", Source: "git::https://github.com/globex/mods.git//net?ref=v1.0.0"}
	_, ids, err = w.st.SaveGraph(ctx, w.globex.ID, &v1.Graph{
		Repo: "globex/platform", SHA: "g1",
		Stacks:  []v1.Stack{{Key: "stacks/app", Path: "stacks/app"}},
		Modules: []v1.Module{globexMod},
		Edges:   []v1.Edge{{From: v1.StackRef("stacks/app"), To: v1.ModuleRef(globexMod.Key), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": "v1.0.0"}}},
	})
	require.NoError(t, err)
	w.globexStack = ids["stacks/app"]

	w.eksPinned, err = w.st.GetModuleByKey(ctx, "acme/modules//eks@v1.2.0")
	require.NoError(t, err)
	w.eksFamily, err = w.st.GetModuleByKey(ctx, "acme/modules//eks")
	require.NoError(t, err)
	w.globexFamily, err = w.st.GetModuleByKey(ctx, "globex/mods//net")
	require.NoError(t, err)
	day := func(n int) time.Time { return time.Date(2026, 9, 1+n, 12, 0, 0, 0, time.UTC) }
	for _, v := range []struct {
		version string
		at      time.Time
	}{{"v1.2.0", day(0)}, {"v1.4.1", day(1)}, {"v2.0.0-rc.1", day(2)}, {"v1.3.0", day(3)}} {
		require.NoError(t, w.st.RecordModuleVersion(ctx, w.eksPinned.ID, v.version, "sha-"+v.version, v.at))
	}

	w.planRun, err = w.st.CreateRun(ctx, store.CreateRunParams{RepoID: w.infra.ID, SHA: prHeadSHA, PRNumber: 7, Trigger: v1.TriggerPullRequest, Mode: v1.ModePlan, Status: v1.RunPlanning, RequestedBy: "octocat"})
	require.NoError(t, err)
	require.NoError(t, w.st.SetRunGraph(ctx, w.planRun.ID, w.prGraph, 3, nil))
	require.NoError(t, w.st.UpsertRunStacks(ctx, w.planRun.ID, []store.RunStack{
		{StackID: w.stacks["stacks/prod/vpc"], Wave: 0, Reasons: []v1.Reason{v1.ReasonModule}, Environment: "production"},
		{StackID: w.stacks["stacks/prod/eks"], Wave: 1, Reasons: []v1.Reason{v1.ReasonDependent}, Environment: "production"},
		{StackID: w.stacks["stacks/prod/apps:blue"], Wave: 2, Reasons: []v1.Reason{v1.ReasonReadsState}},
	}))
	for _, key := range []string{"stacks/prod/vpc", "stacks/prod/eks"} {
		_, err = w.st.UpdateRunStack(ctx, w.planRun.ID, w.stacks[key], store.RunStackPatch{
			Status: ptr(v1.StackPlanned), Summary: &v1.PlanSummary{Adds: 1}, HasChanges: ptr(true), JobURL: ptr("https://github.com/acme/infra/actions/runs/1/job/" + key),
		})
		require.NoError(t, err)
	}

	time.Sleep(5 * time.Millisecond)
	w.applyRun, err = w.st.CreateRun(ctx, store.CreateRunParams{RepoID: w.infra.ID, SHA: mergedSHA, PRNumber: 5, Trigger: v1.TriggerComment, Mode: v1.ModeApply, Status: v1.RunApplying, RequestedBy: "hubot"})
	require.NoError(t, err)
	require.NoError(t, w.st.SetRunGraph(ctx, w.applyRun.ID, w.mergedGraph, 2, nil))
	require.NoError(t, w.st.UpsertRunStacks(ctx, w.applyRun.ID, []store.RunStack{
		{StackID: w.stacks["stacks/prod/vpc"], Wave: 0, Environment: "production"},
		{StackID: w.stacks["stacks/prod/eks"], Wave: 1, Environment: "production"},
	}))
	_, err = w.st.UpdateRunStack(ctx, w.applyRun.ID, w.stacks["stacks/prod/vpc"], store.RunStackPatch{Status: ptr(v1.StackApplied)})
	require.NoError(t, err)
	_, err = w.st.UpdateRunStack(ctx, w.applyRun.ID, w.stacks["stacks/prod/eks"], store.RunStackPatch{Status: ptr(v1.StackFailed), ExitCode: ptr(1)})
	require.NoError(t, err)
	conflicts, err := w.st.TryLockStacks(ctx, []uuid.UUID{w.stacks["stacks/prod/eks"]}, w.applyRun.ID, 5, "apply")
	require.NoError(t, err)
	require.Empty(t, conflicts)
	_, err = w.st.RecordDrift(ctx, store.Drift{StackID: w.stacks["stacks/prod/vpc"], Drifted: true, Summary: &v1.PlanSummary{Changes: 2}, IssueNumber: 12})
	require.NoError(t, err)

	w.globexRun, err = w.st.CreateRun(ctx, store.CreateRunParams{RepoID: w.globex.ID, SHA: "g1", Trigger: v1.TriggerSchedule, Mode: v1.ModeDrift})
	require.NoError(t, err)

	_, err = w.st.RecordAudit(ctx, store.AuditEntry{Actor: "octocat", Action: "unlock", Target: "acme/infra//stacks/prod/eks", Details: map[string]any{"reason": "reverted"}})
	require.NoError(t, err)
	_, err = w.st.RecordAudit(ctx, store.AuditEntry{Actor: "apikey:ci", Action: "rerun", Target: w.globexRun.ID.String(), Details: map[string]any{"repo": "globex/platform"}})
	require.NoError(t, err)

	w.key, _, err = w.st.CreateAPIKey(ctx, "ci", "admin")
	require.NoError(t, err)
	token, _, err := w.st.CreateSession(ctx, store.NewSession{Login: "octocat", UserID: 1, Orgs: []string{"acme"}, TTL: time.Hour})
	require.NoError(t, err)
	w.session = &http.Cookie{Name: sessionCookie, Value: w.srv.sessionCookieValue(token)}
	return w
}

func (w *world) get(target string, asSession bool) *httptest.ResponseRecorder {
	w.t.Helper()
	r := newRequest(w.t, http.MethodGet, target, nil)
	if asSession {
		r.AddCookie(w.session)
	} else {
		r.Header.Set("Authorization", "Bearer "+w.key)
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

func (w *world) ok(target string, asSession bool) *httptest.ResponseRecorder {
	w.t.Helper()
	rec := w.get(target, asSession)
	require.Equal(w.t, http.StatusOK, rec.Code, "%s: %s", target, rec.Body.String())
	return rec
}

func TestOverviewAndRepos(t *testing.T) {
	w := newWorld(t)

	ov := decodeBody[v1.Overview](t, w.ok("/v1/overview", true))
	assert.Equal(t, 2, ov.Repos)
	assert.Equal(t, 4, ov.Stacks)
	assert.Equal(t, 1, ov.Drifted)
	assert.Equal(t, 1, ov.LocksHeld)
	assert.Equal(t, map[v1.RunStatus]int{v1.RunPlanning: 1, v1.RunApplying: 1}, ov.RunsByStatus)
	require.Len(t, ov.RecentRuns, 2)
	assert.Equal(t, w.applyRun.ID.String(), ov.RecentRuns[0].ID)
	assert.Equal(t, testBaseURL+"/runs/"+w.applyRun.ID.String(), ov.RecentRuns[0].HTMLURL)

	all := decodeBody[v1.Overview](t, w.ok("/v1/overview", false))
	assert.Equal(t, 3, all.Repos, "API keys see every installation")

	repos := decodeBody[v1.Page[v1.RepoSummary]](t, w.ok("/v1/repos", true))
	require.Len(t, repos.Items, 2)
	assert.Equal(t, 2, repos.Total)
	assert.Equal(t, "acme/infra", repos.Items[0].FullName)
	assert.Equal(t, 4, repos.Items[0].Stacks)
	assert.Equal(t, 1, repos.Items[0].LocksHeld)
	assert.Equal(t, 1, repos.Items[0].Drifted)
	assert.Equal(t, "acme/modules", repos.Items[1].FullName)

	first := decodeBody[v1.Page[v1.RepoSummary]](t, w.ok("/v1/repos?limit=2", false))
	require.Len(t, first.Items, 2)
	require.NotEmpty(t, first.NextCursor)
	assert.Equal(t, 3, first.Total)
	second := decodeBody[v1.Page[v1.RepoSummary]](t, w.ok("/v1/repos?limit=2&cursor="+first.NextCursor, false))
	require.Len(t, second.Items, 1)
	assert.Equal(t, "globex/platform", second.Items[0].FullName)
	assert.Empty(t, second.NextCursor)

	rec := w.get("/v1/repos?cursor=!!!", true)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRepoGraph(t *testing.T) {
	w := newWorld(t)

	view := decodeBody[v1.GraphView](t, w.ok("/v1/repos/acme/infra/graph", true))
	assert.Equal(t, "acme/infra", view.Repo)
	assert.Equal(t, mergedSHA, view.SHA, "the default-branch graph wins over the newer PR graph")
	assert.Equal(t, mergedSHA, view.Graph.SHA)
	assert.Len(t, view.Graph.Stacks, 4)
	assert.Len(t, view.Graph.Edges, 5)
	assert.Equal(t, map[string]string{
		"stacks/prod/vpc":       w.stacks["stacks/prod/vpc"].String(),
		"stacks/prod/eks":       w.stacks["stacks/prod/eks"].String(),
		"stacks/prod/apps:blue": w.stacks["stacks/prod/apps:blue"].String(),
	}, view.StackIDs)
	assert.Empty(t, view.Affected)

	view = decodeBody[v1.GraphView](t, w.ok("/v1/repos/acme/infra/graph?ref="+prHeadSHA, true))
	assert.Equal(t, prHeadSHA, view.SHA)
	assert.Contains(t, view.StackIDs, "stacks/prod/new")
	for ref, sha := range map[string]string{"abcdef02": prHeadSHA, "ABCDEF01": mergedSHA, "abcdef0111": mergedSHA, "default": mergedSHA} {
		view = decodeBody[v1.GraphView](t, w.ok("/v1/repos/acme/infra/graph?ref="+ref, true))
		assert.Equal(t, sha, view.SHA, "ref %s", ref)
	}
	for ref, reason := range map[string]string{
		"abcdef0":         "is ambiguous: graphs are recorded for " + mergedSHA + " and " + prHeadSHA,
		"abcdef":          "a SHA prefix of at least 7 characters",
		"main":            "branch names are not supported",
		"release/2026-09": "branch names are not supported",
	} {
		rec := w.get("/v1/repos/acme/infra/graph?ref="+url.QueryEscape(ref), true)
		require.Equal(t, http.StatusBadRequest, rec.Code, ref)
		body := decodeBody[v1.Error](t, rec)
		assert.Equal(t, "invalid", body.Code, ref)
		details, _ := body.Details.(map[string]any)
		assert.Equal(t, "ref", details["field"], ref)
		assert.Contains(t, details["reason"], reason, ref)
	}

	view = decodeBody[v1.GraphView](t, w.ok("/v1/repos/acme/infra/graph?run="+w.planRun.ID.String(), true))
	assert.Equal(t, prHeadSHA, view.SHA, "a replay shows the graph the run resolved against")
	assert.Equal(t, [][]string{{"stacks/prod/vpc"}, {"stacks/prod/eks"}, {"stacks/prod/apps:blue"}}, view.Waves)
	require.Len(t, view.Affected, 3)
	assert.Equal(t, v1.AffectedStack{
		Key: "stacks/prod/eks", Path: "stacks/prod/eks", Wave: 1, Reasons: []v1.Reason{v1.ReasonDependent},
		Environment: "production", Tool: v1.ToolTofu, ToolVersion: "1.9.0",
	}, view.Affected[1])
	assert.Equal(t, "blue", view.Affected[2].Workspace)
	assert.Equal(t, "summary", view.Affected[2].PlanOutput)

	view = decodeBody[v1.GraphView](t, w.ok("/v1/repos/acme/infra/graph?ref="+mergedSHA[:10]+"&run="+w.planRun.ID.String(), false))
	assert.Equal(t, mergedSHA, view.SHA)
	assert.Len(t, view.Affected, 3)

	for target, status := range map[string]int{
		"/v1/repos/acme/infra/graph?ref=0123456":                       404,
		"/v1/repos/acme/modules/graph?ref=default":                     404,
		"/v1/repos/acme/infra/graph?run=" + w.globexRun.ID.String():    404,
		"/v1/repos/acme/infra/graph?run=not-a-uuid":                    404,
		"/v1/repos/acme/infra/graph?run=" + uuid.NewString():           404,
		"/v1/repos/acme/modules/graph":                                 404,
		"/v1/repos/globex/platform/graph":                              404,
		"/v1/repos/acme/unknown/graph":                                 404,
		"/v1/repos/globex/platform/graph?run=" + w.planRun.ID.String(): 404,
		"/v1/repos/acme/infra/graph?run=" + w.applyRun.ID.String():     200,
		"/v1/repos/ACME/Infra/graph":                                   200,
	} {
		assert.Equal(t, status, w.get(target, true).Code, target)
	}
	assert.Equal(t, http.StatusOK, w.get("/v1/repos/globex/platform/graph", false).Code, "API keys see every repository")
}

func TestRepoRuns(t *testing.T) {
	w := newWorld(t)
	page := decodeBody[v1.Page[v1.Run]](t, w.ok("/v1/repos/acme/infra/runs", true))
	require.Len(t, page.Items, 2)
	assert.Equal(t, w.applyRun.ID.String(), page.Items[0].ID, "newest first")
	assert.Equal(t, w.planRun.ID.String(), page.Items[1].ID)
	assert.Equal(t, testBaseURL+"/runs/"+w.planRun.ID.String(), page.Items[1].HTMLURL)
	assert.Equal(t, "octocat", page.Items[1].RequestedBy)
	assert.Equal(t, 3, page.Items[1].Waves)

	byPR := decodeBody[v1.Page[v1.Run]](t, w.ok("/v1/repos/acme/infra/runs?pr=7", true))
	require.Len(t, byPR.Items, 1)
	assert.Equal(t, w.planRun.ID.String(), byPR.Items[0].ID)

	byStatus := decodeBody[v1.Page[v1.Run]](t, w.ok("/v1/repos/acme/infra/runs?status=applying", true))
	require.Len(t, byStatus.Items, 1)
	assert.Equal(t, w.applyRun.ID.String(), byStatus.Items[0].ID)

	byMode := decodeBody[v1.Page[v1.Run]](t, w.ok("/v1/repos/acme/infra/runs?mode=plan", true))
	require.Len(t, byMode.Items, 1)

	first := decodeBody[v1.Page[v1.Run]](t, w.ok("/v1/repos/acme/infra/runs?limit=1", true))
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	second := decodeBody[v1.Page[v1.Run]](t, w.ok("/v1/repos/acme/infra/runs?limit=1&cursor="+first.NextCursor, true))
	require.Len(t, second.Items, 1)
	assert.Equal(t, w.planRun.ID.String(), second.Items[0].ID)

	empty := decodeBody[v1.Page[v1.Run]](t, w.ok("/v1/repos/acme/modules/runs", true))
	assert.Equal(t, []v1.Run{}, empty.Items)

	for target, field := range map[string]string{
		"/v1/repos/acme/infra/runs?status=bogus": "status",
		"/v1/repos/acme/infra/runs?mode=destroy": "mode",
		"/v1/repos/acme/infra/runs?pr=seven":     "pr",
		"/v1/repos/acme/infra/runs?pr=0":         "pr",
		"/v1/repos/acme/infra/runs?cursor=zzzz":  "cursor",
		"/v1/repos/acme/infra/runs?limit=-1":     "limit",
	} {
		rec := w.get(target, true)
		require.Equal(t, http.StatusBadRequest, rec.Code, target)
		assert.Equal(t, field, errorOf(t, rec).Details.(map[string]any)["field"], target)
	}
	assert.Equal(t, http.StatusNotFound, w.get("/v1/repos/globex/platform/runs", true).Code)
}

func TestStackDetail(t *testing.T) {
	w := newWorld(t)
	eks := decodeBody[v1.StackDetail](t, w.ok("/v1/stacks/"+w.stacks["stacks/prod/eks"].String(), true))
	assert.Equal(t, w.stacks["stacks/prod/eks"].String(), eks.ID)
	assert.Equal(t, "acme/infra", eks.Repo)
	assert.Equal(t, "stacks/prod/eks", eks.Key)
	assert.Equal(t, "production", eks.Environment)
	assert.Equal(t, v1.ToolTofu, eks.Tool)
	require.NotNil(t, eks.LastApply)
	assert.Equal(t, w.applyRun.ID.String(), eks.LastApply.RunID)
	assert.Equal(t, v1.StackFailed, eks.LastApply.Status)
	assert.Equal(t, 5, eks.LastApply.PRNumber)
	require.NotNil(t, eks.LastPlan)
	assert.Equal(t, w.planRun.ID.String(), eks.LastPlan.RunID)
	assert.Equal(t, v1.StackPlanned, eks.LastPlan.Status)
	assert.Equal(t, &v1.PlanSummary{Adds: 1}, eks.LastPlan.Summary)
	require.NotNil(t, eks.Lock)
	assert.Equal(t, w.applyRun.ID.String(), eks.Lock.RunID)
	assert.Equal(t, 5, eks.Lock.PRNumber)
	assert.Equal(t, "apply", eks.Lock.Reason)
	assert.Nil(t, eks.Drift)
	assert.Equal(t, []string{"stacks/prod/vpc"}, eks.DependsOn)
	assert.Equal(t, []string{"stacks/prod/apps:blue"}, eks.Dependents)
	assert.Equal(t, []v1.ModuleConsume{{ModuleKey: "acme/modules//eks", Ref: "v1.2.0", Latest: "v1.4.1", Behind: 2}}, eks.Modules,
		"the default-branch graph pins v1.2.0; v1.3.0 and v1.4.1 are newer by semver even though v1.3.0 was tagged last")

	vpc := decodeBody[v1.StackDetail](t, w.ok("/v1/stacks/"+w.stacks["stacks/prod/vpc"].String(), true))
	require.NotNil(t, vpc.Drift)
	assert.True(t, vpc.Drift.Drifted)
	assert.Equal(t, &v1.PlanSummary{Changes: 2}, vpc.Drift.Summary)
	assert.Equal(t, 12, vpc.Drift.IssueNumber)
	assert.Equal(t, "https://github.com/acme/infra/issues/12", vpc.Drift.IssueURL)
	assert.Equal(t, &v1.Backend{Type: "s3", Bucket: "state", Key: "prod/vpc.tfstate", Region: "eu-west-1"}, vpc.Backend)
	assert.Equal(t, []string{"acme/network//stacks/prod/tgw"}, vpc.DependsOn)
	assert.Equal(t, []string{"stacks/prod/eks"}, vpc.Dependents)
	assert.Equal(t, []v1.ModuleConsume{{ModuleKey: "acme/infra//modules/vpc"}}, vpc.Modules)
	require.NotNil(t, vpc.LastApply)
	assert.Equal(t, v1.StackApplied, vpc.LastApply.Status)
	assert.Nil(t, vpc.Lock)

	fresh := decodeBody[v1.StackDetail](t, w.ok("/v1/stacks/"+w.stacks["stacks/prod/new"].String(), true))
	assert.Nil(t, fresh.LastApply)
	assert.Nil(t, fresh.LastPlan)
	assert.Empty(t, fresh.DependsOn, "the stack only exists in the PR graph")
	assert.Empty(t, fresh.Modules)

	assert.Equal(t, http.StatusNotFound, w.get("/v1/stacks/"+w.globexStack.String(), true).Code)
	assert.Equal(t, http.StatusOK, w.get("/v1/stacks/"+w.globexStack.String(), false).Code)
	assert.Equal(t, http.StatusNotFound, w.get("/v1/stacks/"+uuid.NewString(), false).Code)
	assert.Equal(t, http.StatusNotFound, w.get("/v1/stacks/stacks%2Fprod%2Fvpc", false).Code)
}

func TestRepoStacksAndHistory(t *testing.T) {
	w := newWorld(t)
	page := decodeBody[v1.Page[v1.StackDetail]](t, w.ok("/v1/repos/acme/infra/stacks", true))
	require.Len(t, page.Items, 4)
	assert.Equal(t, 4, page.Total)
	keys := make([]string, len(page.Items))
	for i, d := range page.Items {
		keys[i] = d.Key
	}
	assert.Equal(t, []string{"stacks/prod/apps:blue", "stacks/prod/eks", "stacks/prod/new", "stacks/prod/vpc"}, keys)
	single := decodeBody[v1.StackDetail](t, w.ok("/v1/stacks/"+w.stacks["stacks/prod/eks"].String(), true))
	assert.Equal(t, single, page.Items[1], "list items are the full stack detail")
	vpc := page.Items[3]
	require.NotNil(t, vpc.Drift)
	assert.Equal(t, "https://github.com/acme/infra/issues/12", vpc.Drift.IssueURL)

	first := decodeBody[v1.Page[v1.StackDetail]](t, w.ok("/v1/repos/acme/infra/stacks?limit=3", true))
	require.Len(t, first.Items, 3)
	rest := decodeBody[v1.Page[v1.StackDetail]](t, w.ok("/v1/repos/acme/infra/stacks?limit=3&cursor="+first.NextCursor, true))
	require.Len(t, rest.Items, 1)
	assert.Equal(t, "stacks/prod/vpc", rest.Items[0].Key)
	assert.Equal(t, http.StatusNotFound, w.get("/v1/repos/globex/platform/stacks", true).Code)

	history := decodeBody[v1.Page[v1.RunStackRef]](t, w.ok("/v1/stacks/"+w.stacks["stacks/prod/vpc"].String()+"/runs", true))
	require.Len(t, history.Items, 2)
	assert.Equal(t, w.applyRun.ID.String(), history.Items[0].RunID, "newest first")
	assert.Equal(t, v1.StackApplied, history.Items[0].Status)
	assert.Equal(t, mergedSHA, history.Items[0].SHA)
	assert.Equal(t, w.planRun.ID.String(), history.Items[1].RunID)
	assert.Equal(t, "https://github.com/acme/infra/actions/runs/1/job/stacks/prod/vpc", history.Items[1].JobURL)

	paged := decodeBody[v1.Page[v1.RunStackRef]](t, w.ok("/v1/stacks/"+w.stacks["stacks/prod/vpc"].String()+"/runs?limit=1", true))
	require.Len(t, paged.Items, 1)
	next := decodeBody[v1.Page[v1.RunStackRef]](t, w.ok("/v1/stacks/"+w.stacks["stacks/prod/vpc"].String()+"/runs?limit=1&cursor="+paged.NextCursor, true))
	require.Len(t, next.Items, 1)
	assert.Equal(t, w.planRun.ID.String(), next.Items[0].RunID)
	assert.Equal(t, http.StatusNotFound, w.get("/v1/stacks/"+w.globexStack.String()+"/runs", true).Code)
}

func TestModules(t *testing.T) {
	w := newWorld(t)
	page := decodeBody[v1.Page[v1.ModuleDetail]](t, w.ok("/v1/modules", true))
	require.Len(t, page.Items, 2, "the globex module is not visible to an acme member")
	assert.Zero(t, page.Total)
	local, eks := page.Items[0], page.Items[1]
	assert.Equal(t, "acme/infra//modules/vpc", local.Key)
	assert.Equal(t, v1.ModuleLocal, local.Kind)
	assert.Equal(t, "../../modules/vpc", local.Source)
	assert.Empty(t, local.Versions)
	assert.Empty(t, local.Latest)
	require.Len(t, local.Consumers, 1)
	assert.Equal(t, v1.ModuleConsumer{StackID: w.stacks["stacks/prod/vpc"].String(), Repo: "acme/infra", StackKey: "stacks/prod/vpc"}, local.Consumers[0])

	assert.Equal(t, w.eksFamily.ID.String(), eks.ID)
	assert.Equal(t, "acme/modules//eks", eks.Key, "modules are keyed by their ref-less family")
	assert.Equal(t, v1.ModuleGit, eks.Kind)
	assert.Equal(t, "git::https://github.com/acme/modules.git//eks?ref=v1.4.1", eks.Source)
	versions := make([]string, len(eks.Versions))
	for i, v := range eks.Versions {
		versions[i] = v.Version
	}
	assert.Equal(t, []string{"v1.3.0", "v2.0.0-rc.1", "v1.4.1", "v1.2.0"}, versions)
	assert.Equal(t, "v1.4.1", eks.Latest, "the latest version is the newest stable one by semver, not the last tagged")
	assert.Equal(t, []v1.ModuleConsumer{{StackID: w.stacks["stacks/prod/eks"].String(), Repo: "acme/infra", StackKey: "stacks/prod/eks", Ref: "v1.2.0", Behind: 2}}, eks.Consumers)

	all := decodeBody[v1.Page[v1.ModuleDetail]](t, w.ok("/v1/modules", false))
	require.Len(t, all.Items, 3)
	assert.Equal(t, 3, all.Total)
	assert.Equal(t, "globex/mods//net", all.Items[2].Key)

	filtered := decodeBody[v1.Page[v1.ModuleDetail]](t, w.ok("/v1/modules?q=EKS", true))
	require.Len(t, filtered.Items, 1)
	assert.Equal(t, "acme/modules//eks", filtered.Items[0].Key)
	none := decodeBody[v1.Page[v1.ModuleDetail]](t, w.ok("/v1/modules?q=net", true))
	assert.Equal(t, []v1.ModuleDetail{}, none.Items)

	first := decodeBody[v1.Page[v1.ModuleDetail]](t, w.ok("/v1/modules?limit=1", false))
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	second := decodeBody[v1.Page[v1.ModuleDetail]](t, w.ok("/v1/modules?limit=1&cursor="+first.NextCursor, false))
	require.Len(t, second.Items, 1)
	assert.Equal(t, "acme/modules//eks", second.Items[0].Key)
	third := decodeBody[v1.Page[v1.ModuleDetail]](t, w.ok("/v1/modules?limit=1&cursor="+second.NextCursor, false))
	require.Len(t, third.Items, 1)
	assert.Empty(t, third.NextCursor)

	byPinned := decodeBody[v1.ModuleDetail](t, w.ok("/v1/modules/"+w.eksPinned.ID.String(), true))
	assert.Equal(t, eks, byPinned, "a pinned module id resolves to its family")
	byFamily := decodeBody[v1.ModuleDetail](t, w.ok("/v1/modules/"+w.eksFamily.ID.String(), true))
	assert.Equal(t, eks, byFamily)

	assert.Equal(t, http.StatusNotFound, w.get("/v1/modules/"+w.globexFamily.ID.String(), true).Code)
	assert.Equal(t, http.StatusOK, w.get("/v1/modules/"+w.globexFamily.ID.String(), false).Code)
	assert.Equal(t, http.StatusNotFound, w.get("/v1/modules/"+uuid.NewString(), false).Code)
	assert.Equal(t, http.StatusNotFound, w.get("/v1/modules/acme", false).Code)
}

func TestAuditLog(t *testing.T) {
	w := newWorld(t)
	all := decodeBody[v1.Page[v1.AuditEntry]](t, w.ok("/v1/audit", false))
	require.Len(t, all.Items, 2)
	assert.Equal(t, "rerun", all.Items[0].Action, "newest first")
	assert.Equal(t, "apikey:ci", all.Items[0].Actor)
	assert.Equal(t, map[string]any{"repo": "globex/platform"}, all.Items[0].Details)
	assert.Equal(t, time.UTC, all.Items[1].At.Location())

	mine := decodeBody[v1.Page[v1.AuditEntry]](t, w.ok("/v1/audit", true))
	require.Len(t, mine.Items, 1, "an acme member does not see globex entries")
	assert.Equal(t, v1.AuditEntry{At: mine.Items[0].At, Actor: "octocat", Action: "unlock", Target: "acme/infra//stacks/prod/eks", Details: map[string]any{"reason": "reverted"}}, mine.Items[0])

	first := decodeBody[v1.Page[v1.AuditEntry]](t, w.ok("/v1/audit?limit=1", false))
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	second := decodeBody[v1.Page[v1.AuditEntry]](t, w.ok("/v1/audit?limit=1&cursor="+first.NextCursor, false))
	require.Len(t, second.Items, 1)
	assert.Equal(t, "unlock", second.Items[0].Action)
	assert.Equal(t, http.StatusBadRequest, w.get("/v1/audit?cursor=abc", false).Code)
}

func TestRunnerAuthWithTheRealStore(t *testing.T) {
	w := newWorld(t)
	post := func(token string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		w.h.ServeHTTP(rec, bearer(newRequest(t, http.MethodPost, "/v1/runs", createRunBody()), token))
		return rec
	}

	token := w.issuer.Token(w.issuer.PlanClaims("acme/infra", "100", 7, testSHA))
	rec := post(token)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "octocat", w.runs.last().p.Login)

	rec = post(token)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "the jti is recorded in Postgres")
	assert.Contains(t, errorOf(t, rec).Message, "token replayed")

	rec = post(w.issuer.Token(w.issuer.PlanClaims("acme/unknown", "999", 7, testSHA)))
	assert.Equal(t, http.StatusForbidden, rec.Code)

	require.NoError(t, w.st.SuspendInstallation(t.Context(), 1, true))
	rec = post(w.issuer.Token(w.issuer.PlanClaims("acme/infra", "100", 7, testSHA)))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, errorOf(t, rec).Message, "suspended")

	rec = post(w.key)
	assert.Equal(t, http.StatusCreated, rec.Code, "API keys verify against the store")
	var revoked store.APIKey
	keys, err := w.st.ListAPIKeys(t.Context())
	require.NoError(t, err)
	revoked = keys[0]
	require.NoError(t, w.st.RevokeAPIKey(t.Context(), revoked.ID))
	rec = post(w.key)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = httptest.NewRecorder()
	w.h.ServeHTTP(rec, withCookie(newRequest(t, http.MethodGet, "/v1/me", nil), w.session))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, v1.Whoami{Login: "octocat", Orgs: []string{"acme"}}, decodeBody[v1.Whoami](t, rec))
	rec = httptest.NewRecorder()
	w.h.ServeHTTP(rec, withCookie(newRequest(t, http.MethodGet, "/v1/me", nil), &http.Cookie{Name: sessionCookie, Value: w.srv.sessionCookieValue("unknown")}))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestPlanText(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	const vpc, eks = "stacks/prod/vpc", "stacks/prod/eks"
	full := strings.Repeat("  # aws_route_table.private will be updated in-place\n", 400)
	_, err := w.st.UpdateRunStack(ctx, w.planRun.ID, w.stacks[vpc], store.RunStackPatch{
		PlanText: ptr(full[:512]), PlanTextTruncated: ptr(true),
		PlanURL: ptr("s3://stackorder-artifacts/" + store.PlanTextArtifactKey(w.planRun.ID, vpc)),
	})
	require.NoError(t, err)
	mem := &memArtifacts{items: map[string]string{store.PlanTextArtifactKey(w.planRun.ID, vpc): full}}
	w.srv.artifacts = mem

	for _, asSession := range []bool{true, false} {
		rec := w.ok(planTarget(w.planRun.ID, vpc), asSession)
		assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, full, rec.Body.String(), "the whole plan, not the stored beginning")
	}

	for name, tc := range map[string]struct {
		target    string
		asSession bool
		status    int
		message   string
	}{
		"a stack without plan_url":    {planTarget(w.planRun.ID, eks), true, http.StatusNotFound, "keeps no full plan text"},
		"a stack outside the run":     {planTarget(w.planRun.ID, "stacks/prod/new"), true, http.StatusNotFound, "is not part of run"},
		"a run the person cannot see": {planTarget(w.globexRun.ID, vpc), true, http.StatusNotFound, "not found"},
		"an unknown run":              {planTarget(uuid.New(), vpc), false, http.StatusNotFound, "not found"},
	} {
		rec := w.get(tc.target, tc.asSession)
		require.Equal(t, tc.status, rec.Code, name)
		assert.Contains(t, errorOf(t, rec).Message, tc.message, name)
	}

	r := newRequest(t, http.MethodGet, planTarget(w.planRun.ID, vpc), nil)
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "sessions and API keys only")
	r = newRequest(t, http.MethodGet, planTarget(w.planRun.ID, vpc), nil)
	r.Header.Set("Authorization", "Bearer "+w.issuer.Token(w.issuer.PlanClaims("acme/infra", "100", 7, prHeadSHA)))
	rec = httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "runner tokens do not read plan text")

	delete(mem.items, store.PlanTextArtifactKey(w.planRun.ID, vpc))
	rec = w.get(planTarget(w.planRun.ID, vpc), true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, errorOf(t, rec).Message, "no longer in the artifact bucket")
	mem.err = errors.New("s3 is down")
	assert.Equal(t, http.StatusInternalServerError, w.get(planTarget(w.planRun.ID, vpc), true).Code)
	w.srv.artifacts = nil
	rec = w.get(planTarget(w.planRun.ID, vpc), true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, errorOf(t, rec).Message, "no artifact bucket")
}
