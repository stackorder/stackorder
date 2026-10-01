//go:build integration

package runs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
	"github.com/stackorder/stackorder/internal/testutil/oidcfake"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

const (
	repoName   = "acme/infra"
	repoID     = int64(100)
	instID     = int64(1)
	mainSHA    = "1111111111111111111111111111111111111111"
	baseSHA    = "2222222222222222222222222222222222222222"
	headSHA    = "3333333333333333333333333333333333333333"
	newHeadSHA = "4444444444444444444444444444444444444444"
	mergeSHA   = "5555555555555555555555555555555555555555"
	author     = "author"
	applier    = "octocat"

	vpc     = "stacks/prod/vpc"
	eks     = "stacks/prod/eks"
	apps    = "stacks/prod/apps"
	staging = "stacks/staging/vpc"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}

type recMetrics struct {
	mu         sync.Mutex
	runs       []string
	stacks     map[v1.StackStatus]int
	dispatched map[bool]int
	drifted    int
	locks      int
	commands   map[string]int
}

func newRecMetrics() *recMetrics {
	return &recMetrics{stacks: map[v1.StackStatus]int{}, dispatched: map[bool]int{}, commands: map[string]int{}}
}

func (m *recMetrics) RunStatusChanged(s v1.RunStatus, t v1.Trigger, mode v1.RunMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, fmt.Sprintf("%s/%s/%s", mode, t, s))
}

func (m *recMetrics) StackFinished(_ v1.RunMode, s v1.StackStatus, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stacks[s]++
}

func (m *recMetrics) Dispatched(_ v1.RunMode, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dispatched[ok]++
}

func (m *recMetrics) SetDriftedStacks(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drifted = n
}

func (m *recMetrics) SetLocksHeld(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.locks = n
}

func (m *recMetrics) CommandReceived(verb string, accepted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commands[fmt.Sprintf("%s:%t", verb, accepted)]++
}

func (m *recMetrics) snapshot() (locks, drifted int, commands map[string]int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := map[string]int{}
	for k, v := range m.commands {
		cp[k] = v
	}
	return m.locks, m.drifted, cp
}

type queuedJob struct {
	Kind     string
	Payload  json.RawMessage
	RunAfter time.Time
	Dedupe   string
}

type jobQueue struct {
	mu   sync.Mutex
	jobs []queuedJob
	keys map[string]bool
}

func (q *jobQueue) enqueue(_ context.Context, kind string, payload any, runAfter time.Time, dedupeKey string) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if dedupeKey != "" {
		if q.keys[dedupeKey] {
			return nil
		}
		q.keys[dedupeKey] = true
	}
	q.jobs = append(q.jobs, queuedJob{Kind: kind, Payload: b, RunAfter: runAfter, Dedupe: dedupeKey})
	return nil
}

func (q *jobQueue) take() []queuedJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.jobs
	q.jobs = nil
	return out
}

type memArtifacts struct {
	mu    sync.Mutex
	items map[string][]byte
}

func (a *memArtifacts) Put(_ context.Context, key, _ string, body []byte) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.items[key] = append([]byte(nil), body...)
	return "https://artifacts.test/" + key, nil
}

type env struct {
	t         *testing.T
	ctx       context.Context
	st        *store.Store
	gh        *ghfake.Server
	app       *gh.App
	oidc      *oidcfake.Issuer
	clock     *fakeClock
	m         *recMetrics
	svc       *runs.Service
	repo      store.Repo
	queue     *jobQueue
	artifacts *memArtifacts

	mu       sync.Mutex
	noTitles bool
	noJobs   bool
}

type envOption func(*envSetup)

type envSetup struct {
	queue     bool
	artifacts bool
}

func withQueue() envOption     { return func(s *envSetup) { s.queue = true } }
func withArtifacts() envOption { return func(s *envSetup) { s.artifacts = true } }

func baseConfig() *v1.RepoConfig {
	return &v1.RepoConfig{
		Version:      1,
		Environments: map[string]string{"stacks/prod/": "production", "stacks/staging/": "staging"},
	}
}

func newEnv(t *testing.T, cfg *v1.RepoConfig, opts ...envOption) *env {
	t.Helper()
	var setup envSetup
	for _, o := range opts {
		o(&setup)
	}
	e := &env{
		t:     t,
		ctx:   t.Context(),
		st:    pgtest.New(t),
		gh:    ghfake.New(t),
		oidc:  oidcfake.New(t),
		clock: &fakeClock{now: time.Now().UTC().Truncate(time.Second)},
		m:     newRecMetrics(),
	}
	e.gh.SetRepo(repoName, gh.Repository{ID: repoID, DefaultBranch: "main", Private: true})
	e.gh.AddInstallation(instID, "acme", repoName)
	e.gh.SetRef(repoName, "heads/main", mainSHA)
	e.gh.SetCollaboratorPermission(repoName, applier, "write")
	e.gh.SetCollaboratorPermission(repoName, author, "write")
	e.gh.OnDispatch(e.titleRun)
	e.app = e.gh.NewApp()
	_, err := e.st.UpsertInstallation(e.ctx, store.Installation{ID: instID, Account: "acme", AccountType: "Organization"})
	require.NoError(t, err)
	e.repo, err = e.st.UpsertRepo(e.ctx, store.RepoParams{ID: repoID, InstallationID: instID, FullName: repoName, DefaultBranch: "main", Private: true})
	require.NoError(t, err)
	e.setConfig(cfg)
	var svcOpts []runs.Option
	if setup.queue {
		e.queue = &jobQueue{keys: map[string]bool{}}
		svcOpts = append(svcOpts, runs.WithEnqueue(e.queue.enqueue))
	}
	if setup.artifacts {
		e.artifacts = &memArtifacts{items: map[string][]byte{}}
		svcOpts = append(svcOpts, runs.WithArtifactStore(e.artifacts))
	}
	e.svc = runs.New(e.st, e.app, runs.Config{BaseURL: "https://stackorder.test", Clock: e.clock.Now}, nil, e.m, svcOpts...)
	return e
}

func (e *env) setConfig(cfg *v1.RepoConfig) {
	e.t.Helper()
	if cfg != nil {
		c := *cfg
		config.ApplyDefaults(&c)
		require.NoError(e.t, config.Validate(&c))
		cfg = &c
	}
	require.NoError(e.t, e.st.UpdateRepoConfig(e.ctx, e.repo.ID, cfg, mainSHA))
	var err error
	e.repo, err = e.st.GetRepo(e.ctx, e.repo.ID)
	require.NoError(e.t, err)
}

func (e *env) titleRun(d ghfake.Dispatch) {
	e.mu.Lock()
	noTitles, noJobs := e.noTitles, e.noJobs
	e.mu.Unlock()
	if !noJobs {
		e.gh.SetJobs(d.Repo, d.RunID, matrixJobs(d))
	}
	if noTitles {
		return
	}
	for _, r := range e.gh.WorkflowRuns(d.Repo) {
		if r.ID == d.RunID {
			r.DisplayTitle = fmt.Sprintf("stackorder %s %s wave %s", d.Inputs["mode"], d.Inputs["run_id"], d.Inputs["wave"])
			r.Name = "stackorder run"
			e.gh.AddWorkflowRun(d.Repo, r)
		}
	}
}

func matrixJobs(d ghfake.Dispatch) []gh.WorkflowJob {
	var es []v1.MatrixEntry
	if err := json.Unmarshal([]byte(d.Inputs["stacks"]), &es); err != nil {
		return nil
	}
	jobs := make([]gh.WorkflowJob, len(es))
	for i, en := range es {
		jobs[i] = gh.WorkflowJob{
			RunID:  d.RunID,
			Name:   fmt.Sprintf("run / %s (%s, %s, %s, %d)", d.Inputs["mode"], en.Stack, en.Key, en.Environment, en.Wave),
			Status: gh.RunStatusQueued,
		}
	}
	return jobs
}

func (e *env) setNoTitles(v bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.noTitles = v
}

func (e *env) setNoJobs(v bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.noJobs = v
}

func testGraph(sha string) v1.Graph {
	stack := func(key string, deps ...string) v1.Stack {
		s := v1.Stack{Key: key, Path: key, Backend: &v1.Backend{Type: "s3", Bucket: "state", Key: key + ".tfstate"}}
		if len(deps) > 0 {
			s.Config = &v1.StackConfig{DependsOn: deps}
		}
		return s
	}
	return v1.Graph{
		Repo: repoName, SHA: sha, TreeHash: "tree-" + sha,
		Stacks: []v1.Stack{stack(vpc), stack(staging), stack(eks, vpc), stack(apps), stack("stacks/dev/other")},
		Modules: []v1.Module{
			{Key: "acme/infra//modules/vpc", Kind: v1.ModuleLocal, Path: "modules/vpc", Source: "../../modules/vpc"},
			{Key: "acme/modules//dns@v1.0.0", Kind: v1.ModuleGit, Source: "git::https://github.com/acme/modules.git//dns?ref=v1.0.0", Ref: "v1.0.0"},
		},
		Edges: []v1.Edge{
			{From: v1.StackRef(vpc), To: v1.ModuleRef("acme/infra//modules/vpc"), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": "", "source": "../../modules/vpc"}},
			{From: v1.StackRef(staging), To: v1.ModuleRef("acme/infra//modules/vpc"), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": "", "source": "../../modules/vpc"}},
			{From: v1.StackRef("stacks/dev/other"), To: v1.ModuleRef("acme/modules//dns@v1.0.0"), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": "v1.0.0"}},
			{From: v1.StackRef(eks), To: v1.StackRef(vpc), Type: v1.EdgeDependsOn},
			{From: v1.StackRef(apps), To: v1.StackRef(eks), Type: v1.EdgeReadsState, Inferred: true, Meta: map[string]string{"bucket": "state", "key": eks + ".tfstate"}},
		},
	}
}

func (e *env) openPull(number int, head string) {
	e.gh.SetPull(repoName, gh.PullRequest{
		Number: number, Title: "change", State: gh.IssueOpen, HeadSHA: head, HeadRef: "feature", BaseSHA: baseSHA,
		User: gh.User{Login: author}, Mergeable: ptr(true), MergeableState: "clean",
	})
}

func ptr[T any](v T) *T { return &v }

type planJob struct {
	p principal.Principal
}

func (e *env) planJob(pr int) planJob {
	c := e.oidc.PlanClaims(repoName, fmt.Sprint(repoID), pr, mergeSHA)
	return planJob{p: principal.Principal{Kind: principal.OIDC, Login: c.Actor, Claims: &c}}
}

func (e *env) dispatchJob(workflowRunID int64, environment string) principal.Principal {
	c := e.oidc.DispatchClaims(repoName, fmt.Sprint(repoID), workflowRunID, environment, "main", mainSHA)
	return principal.Principal{Kind: principal.OIDC, Login: c.Actor, Claims: &c}
}

func apiKey() principal.Principal {
	return principal.Principal{Kind: principal.APIKey, Login: "ci", APIKeyID: uuid.NewString()}
}

func (e *env) startPlan(pr int, head string, changed ...string) (planJob, string, *v1.ResolveResponse) {
	e.t.Helper()
	return e.startPlanGraph(pr, testGraph(head), changed...)
}

func (e *env) startPlanGraph(pr int, g v1.Graph, changed ...string) (planJob, string, *v1.ResolveResponse) {
	e.t.Helper()
	head := g.SHA
	job := e.planJob(pr)
	created, err := e.svc.CreateRun(e.ctx, job.p, v1.CreateRunRequest{Repo: repoName, SHA: head, BaseSHA: baseSHA, PRNumber: pr, Mode: v1.ModePlan})
	require.NoError(e.t, err)
	if len(changed) == 0 {
		changed = []string{"modules/vpc/main.tf"}
	}
	resp, err := e.svc.UploadGraph(e.ctx, job.p, created.RunID, v1.GraphUploadRequest{Graph: g, ChangedPaths: changed})
	require.NoError(e.t, err)
	return job, created.RunID, resp
}

func planResult(key, sha string, adds int) v1.StackResult {
	return v1.StackResult{
		Mode: v1.ModePlan, Status: v1.ResultSuccess, HasChanges: adds > 0, ExitCode: 0,
		Summary:  &v1.PlanSummary{Adds: adds, Added: addresses(adds)},
		PlanText: "# plan of " + key, Artifact: v1.PlanArtifactName(key, sha),
		JobURL: "https://github.com/acme/infra/actions/runs/1/job/" + strings.ReplaceAll(key, "/", "-"), DurationMS: 1500,
	}
}

func addresses(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("aws_thing.t%d", i)
	}
	return out
}

func (e *env) planned(pr int, head string) string {
	e.t.Helper()
	return e.plannedGraph(pr, testGraph(head))
}

func (e *env) plannedGraph(pr int, g v1.Graph) string {
	e.t.Helper()
	return e.plannedGraphWith(pr, g, nil)
}

func (e *env) plannedGraphWith(pr int, g v1.Graph, hook func(job planJob, runID string)) string {
	e.t.Helper()
	head := g.SHA
	e.openPull(pr, head)
	job, runID, resp := e.startPlanGraph(pr, g)
	if hook != nil {
		hook(job, runID)
	}
	for _, a := range resp.Affected {
		_, err := e.svc.RecordResult(e.ctx, job.p, runID, a.Key, planResult(a.Key, head, 1))
		require.NoError(e.t, err)
	}
	return runID
}

func (e *env) run(id string) v1.Run {
	e.t.Helper()
	u, err := uuid.Parse(id)
	require.NoError(e.t, err)
	r, err := e.st.RunDetail(e.ctx, u)
	require.NoError(e.t, err)
	return r
}

func stackStatuses(r v1.Run) map[string]v1.StackStatus {
	out := map[string]v1.StackStatus{}
	for _, rs := range r.Stacks {
		out[rs.Key] = rs.Status
	}
	return out
}

func (e *env) check(name string) ghfake.CheckRun {
	e.t.Helper()
	var found *ghfake.CheckRun
	for _, c := range e.gh.CheckRuns(repoName) {
		if c.Name == name {
			c := c
			found = &c
		}
	}
	require.NotNil(e.t, found, "check run %q", name)
	return *found
}

func (e *env) checksNamed(name string) []ghfake.CheckRun {
	var out []ghfake.CheckRun
	for _, c := range e.gh.CheckRuns(repoName) {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func (e *env) comments(pr int) []string {
	cs := e.gh.Comments(repoName, pr)
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Body)
	}
	return out
}

func (e *env) lastComment(pr int) string {
	e.t.Helper()
	cs := e.comments(pr)
	require.NotEmpty(e.t, cs)
	return cs[len(cs)-1]
}

func (e *env) sticky(pr int) string {
	e.t.Helper()
	for _, c := range e.comments(pr) {
		if strings.HasPrefix(c, "<!-- stackorder:sticky -->") {
			return c
		}
	}
	e.t.Fatalf("no sticky comment on #%d", pr)
	return ""
}

func (e *env) runComments(pr int) []gh.Comment {
	var out []gh.Comment
	for _, c := range e.gh.Comments(repoName, pr) {
		if strings.HasPrefix(c.Body, "<!-- stackorder:run:") {
			out = append(out, c)
		}
	}
	return out
}

func (e *env) runComment(pr int, runID string) gh.Comment {
	e.t.Helper()
	var found []gh.Comment
	for _, c := range e.gh.Comments(repoName, pr) {
		if strings.HasPrefix(c.Body, report.RunMarker(runID)+"\n") {
			found = append(found, c)
		}
	}
	require.Len(e.t, found, 1, "one comment of run %s on #%d", runID, pr)
	return found[0]
}

func (e *env) storedRun(id string) store.Run {
	e.t.Helper()
	r, err := e.st.GetRun(e.ctx, uuid.MustParse(id))
	require.NoError(e.t, err)
	return r
}

func (e *env) patches(commentID int64) int {
	n := 0
	for _, r := range e.gh.Requests() {
		if r.Method == http.MethodPatch && strings.HasSuffix(r.Path, "/issues/comments/"+strconv.FormatInt(commentID, 10)) {
			n++
		}
	}
	return n
}

func (e *env) comment(pr int, login, body string) gh.Comment {
	e.t.Helper()
	ev := e.gh.IssueCommentEvent(repoName, pr, body, login)
	require.NoError(e.t, e.svc.HandleIssueComment(e.ctx, ev))
	return ev.Comment
}

func (e *env) dispatchesFrom(n int) []ghfake.Dispatch {
	all := e.gh.Dispatches()
	if n > len(all) {
		return nil
	}
	return all[n:]
}

func entries(t *testing.T, d ghfake.Dispatch) []v1.MatrixEntry {
	t.Helper()
	var out []v1.MatrixEntry
	require.NoError(t, json.Unmarshal([]byte(d.Inputs["stacks"]), &out))
	return out
}

func entryKeys(es []v1.MatrixEntry) []string {
	out := make([]string, len(es))
	for i, en := range es {
		out[i] = en.Key
	}
	slices.Sort(out)
	return out
}

func applyResult(ok bool) v1.StackResult {
	res := v1.StackResult{Mode: v1.ModeApply, Status: v1.ResultSuccess, Summary: &v1.PlanSummary{Adds: 1}, DurationMS: 2000}
	if !ok {
		res.Status, res.ExitCode, res.ErrorText = v1.ResultFailure, 1, "Error: boom"
	}
	return res
}

func (e *env) reportAll(d ghfake.Dispatch, results map[string]bool) {
	e.t.Helper()
	for _, en := range entries(e.t, d) {
		ok, found := results[en.Key]
		if !found {
			ok = true
		}
		p := e.dispatchJob(d.RunID, en.Environment)
		_, err := e.svc.GetRunForPrincipal(e.ctx, p, d.Inputs["run_id"])
		require.NoError(e.t, err)
		_, err = e.svc.RecordResult(e.ctx, p, d.Inputs["run_id"], en.Key, applyResult(ok))
		require.NoError(e.t, err, "result of %s", en.Key)
	}
}

func (e *env) locks() map[string]int {
	e.t.Helper()
	ls, err := e.st.ListLocks(e.ctx, e.repo.ID)
	require.NoError(e.t, err)
	out := map[string]int{}
	for _, l := range ls {
		out[l.StackKey] = l.PRNumber
	}
	return out
}

func (e *env) audit(action string) []store.AuditEntry {
	e.t.Helper()
	rows, _, err := e.st.ListAudit(e.ctx, store.AuditFilter{Action: action, Limit: 500})
	require.NoError(e.t, err)
	return rows
}

func wideGraph(sha string) v1.Graph {
	g := testGraph(sha)
	g.Stacks = append(g.Stacks, v1.Stack{Key: "stacks/prod/jobs", Path: "stacks/prod/jobs"}, v1.Stack{Key: "stacks/prod/cache", Path: "stacks/prod/cache"})
	g.Edges = append(g.Edges,
		v1.Edge{From: v1.StackRef("stacks/prod/jobs"), To: v1.StackRef(eks), Type: v1.EdgeDependsOn},
		v1.Edge{From: v1.StackRef("stacks/prod/cache"), To: v1.StackRef(vpc), Type: v1.EdgeDependsOn},
	)
	return g
}

func (e *env) applyRun(pr int) v1.Run {
	e.t.Helper()
	runs, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: repoID, PRNumber: pr, Mode: v1.ModeApply, Limit: 1})
	require.NoError(e.t, err)
	require.NotEmpty(e.t, runs, "an apply run of #%d", pr)
	return e.run(runs[0].ID.String())
}

func (e *env) stalledApply(pr int, planRunID string) string {
	e.t.Helper()
	planRun, err := e.st.GetRun(e.ctx, uuid.MustParse(planRunID))
	require.NoError(e.t, err)
	require.NotNil(e.t, planRun.GraphID)
	plans, err := e.st.GetRunStacks(e.ctx, planRun.ID)
	require.NoError(e.t, err)
	var id uuid.UUID
	require.NoError(e.t, e.st.InTx(e.ctx, func(tx *store.Store) error {
		run, err := tx.CreateRun(e.ctx, store.CreateRunParams{
			RepoID: repoID, SHA: planRun.SHA, BaseSHA: planRun.BaseSHA, PRNumber: pr,
			Trigger: v1.TriggerComment, Mode: v1.ModeApply, Status: v1.RunPending, RequestedBy: applier,
		})
		if err != nil {
			return err
		}
		id = run.ID
		rows := make([]store.RunStack, len(plans))
		ids := make([]uuid.UUID, len(plans))
		waves := 0
		for i, p := range plans {
			ids[i] = p.StackID
			waves = max(waves, p.Wave+1)
			rows[i] = store.RunStack{
				StackID: p.StackID, Mode: v1.ModeApply, Status: v1.StackPlanned, Wave: p.Wave, Reasons: p.Reasons,
				Environment: p.Environment, Summary: p.Summary, HasChanges: p.HasChanges,
				PlanArtifact: p.PlanArtifact, PlanRunID: p.PlanRunID,
			}
		}
		conflicts, err := tx.TryLockStacks(e.ctx, ids, run.ID, pr, fmt.Sprintf("apply of #%d by %s", pr, applier))
		if err != nil {
			return err
		}
		require.Empty(e.t, conflicts)
		if err := tx.UpsertRunStacks(e.ctx, run.ID, rows); err != nil {
			return err
		}
		return tx.SetRunGraph(e.ctx, run.ID, *planRun.GraphID, waves, nil)
	}))
	return id.String()
}

func (e *env) planRunIDs(runID string) map[string]int64 {
	e.t.Helper()
	rows, err := e.st.GetRunStacks(e.ctx, uuid.MustParse(runID))
	require.NoError(e.t, err)
	out := map[string]int64{}
	for _, rs := range rows {
		out[rs.Key] = rs.PlanRunID
	}
	return out
}

func (e *env) via(runID string) map[string][]string {
	e.t.Helper()
	rows, err := e.st.GetRunStacks(e.ctx, uuid.MustParse(runID))
	require.NoError(e.t, err)
	out := map[string][]string{}
	for _, rs := range rows {
		out[rs.Key] = rs.Via
	}
	return out
}
