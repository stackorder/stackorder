//go:build integration

package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/stateread"
	"github.com/stackorder/stackorder/internal/store"
)

const (
	canaryValue   = "vpc-canary"
	canarySecret  = "canary-secret"
	canaryLineage = "5c1e8a7d-2b2f-4f3a-9a52-3f0d2b8e6c11"

	networkRepo = "acme/network"
	networkID   = int64(101)
	globexRepo  = "globex/infra"
	globexID    = int64(200)
	globexInst  = int64(2)
	sharedVPC   = "stacks/shared/vpc"
	sharedDNS   = "stacks/shared/dns"

	pushSHA      = "8888888888888888888888888888888888888888"
	laterPushSHA = "9999999999999999999999999999999999999999"
)

type fakeState struct {
	mu    sync.Mutex
	state *stateread.State
	err   error
	locs  []stateread.Location
}

func newFakeState() *fakeState { return &fakeState{state: canaryState()} }

func (f *fakeState) Read(_ context.Context, loc stateread.Location) (*stateread.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locs = append(f.locs, loc)
	if f.err != nil {
		return nil, f.err
	}
	return f.state, nil
}

func (f *fakeState) set(st *stateread.State, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state, f.err = st, err
}

func (f *fakeState) reads() []stateread.Location {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]stateread.Location(nil), f.locs...)
}

func output(value string, sensitive bool) stateread.Output {
	return stateread.Output{Value: json.RawMessage(`"` + value + `"`), Type: json.RawMessage(`"string"`), Sensitive: sensitive}
}

func canaryState() *stateread.State {
	return &stateread.State{
		Serial: 42, Lineage: canaryLineage, TerraformVersion: "1.9.8",
		Outputs: map[string]stateread.Output{"vpc_id": output(canaryValue, false), "db_password": output(canarySecret, true)},
	}
}

func served(o stateread.Output) v1.StackOutput {
	return v1.StackOutput{Value: o.Value, Type: o.Type, Sensitive: o.Sensitive}
}

func locationOf(obj *v1.StateObject) stateread.Location {
	return stateread.Location{Bucket: obj.Bucket, Key: obj.Key, Region: obj.Region}
}

func (l *logRecorder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logRecorder) debugLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func newOutputsEnv(t *testing.T, cfg *v1.RepoConfig, opts ...envOption) (*env, *fakeState) {
	t.Helper()
	state := newFakeState()
	return newEnv(t, cfg, append(opts, withStateReader(state))...), state
}

func sharedConfig(readers ...string) *v1.RepoConfig {
	cfg := baseConfig()
	cfg.Outputs = &v1.OutputsConfig{Readers: readers}
	return cfg
}

func (e *env) setStackFile(repo, sha, dir, body string) {
	e.gh.SetContents(repo, sha, dir+"/.stackorder.yaml", []byte(body))
}

func (e *env) setStateObject(repo int64, key string, obj *v1.StateObject) {
	e.t.Helper()
	tag, err := e.st.Pool().Exec(e.ctx, `UPDATE stacks SET state_object = $3, state_object_at = now() WHERE repo_id = $1 AND key = $2`, repo, key, obj)
	require.NoError(e.t, err)
	require.EqualValues(e.t, 1, tag.RowsAffected(), "stack %s", key)
}

func (e *env) setRowStatus(runID, key string, status v1.StackStatus) {
	e.t.Helper()
	tag, err := e.st.Pool().Exec(e.ctx, `UPDATE run_stacks SET status = $3 WHERE run_id = $1 AND stack_id = $2`, runID, e.stack(key).ID, string(status))
	require.NoError(e.t, err)
	require.EqualValues(e.t, 1, tag.RowsAffected(), "row of %s", key)
}

func (e *env) readingPlan(pr int) (planJob, string) {
	e.t.Helper()
	e.openPull(pr, headSHA)
	job, runID, _ := e.startPlan(pr, headSHA)
	e.setStateObject(repoID, vpc, stateAt(vpc))
	return job, runID
}

func (e *env) outputs(p principal.Principal, key, runID, reader string) (*v1.StackOutputs, error) {
	return e.svc.StackOutputs(e.ctx, p, repoName, key, runID, reader)
}

func (e *env) addRepo(id, inst int64, name string, cfg *v1.RepoConfig, keys ...string) {
	e.t.Helper()
	_, err := e.st.UpsertRepo(e.ctx, store.RepoParams{ID: id, InstallationID: inst, FullName: name, DefaultBranch: "main", Private: true})
	require.NoError(e.t, err)
	require.NoError(e.t, e.st.UpdateRepoConfig(e.ctx, id, cfg, mainSHA))
	g := v1.Graph{Repo: name, SHA: mainSHA, TreeHash: "tree-" + name}
	for _, key := range keys {
		g.Stacks = append(g.Stacks, v1.Stack{Key: key, Path: key})
	}
	_, _, err = e.st.SaveGraph(e.ctx, id, &g)
	require.NoError(e.t, err)
	for _, key := range keys {
		e.setStateObject(id, key, stateAt(name+"/"+key))
	}
}

func requireCoded(t *testing.T, err error, kind error, code string) string {
	t.Helper()
	require.ErrorIs(t, err, kind)
	var coded *principal.CodedError
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, code, coded.Code)
	return coded.Message
}

func notSharedMessage(key, reader, dir string) string {
	return fmt.Sprintf("%s does not share its outputs with %s; list it under outputs.readers in %s/.stackorder.yaml or stackorder.yaml on the default branch", key, reader, dir)
}

func notFoundMessage(repo, key string) string {
	return fmt.Sprintf("%s has no stack %s whose outputs this caller may read", repo, key)
}

func TestOutputsAreNotSharedByDefault(t *testing.T) {
	tests := []struct {
		name      string
		root      *v1.OutputsConfig
		stackFile string
	}{
		{name: "nothing configured"},
		{name: "sensitive set without readers", stackFile: "outputs:\n  sensitive: share\n"},
		{name: "an empty list withdraws the root's grant", root: &v1.OutputsConfig{Readers: []string{"stacks/**"}}, stackFile: "outputs:\n  readers: []\n"},
		{name: "only another stack is listed", stackFile: "outputs:\n  readers:\n    - stacks/prod/apps\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Outputs = tt.root
			e, state := newOutputsEnv(t, cfg)
			if tt.stackFile != "" {
				e.setStackFile(repoName, mainSHA, vpc, tt.stackFile)
			}
			job, runID := e.readingPlan(7)

			_, err := e.outputs(job.p, vpc, runID, eks)
			message := requireCoded(t, err, principal.ErrForbidden, v1.ErrCodeOutputsNotShared)
			assert.Equal(t, notSharedMessage(vpc, eks, vpc), message)
			assert.Empty(t, state.reads(), "a refused read never touches the state")

			rows := e.audit("outputs_refused")
			require.Len(t, rows, 1)
			assert.Equal(t, job.p.Actor(), rows[0].Actor)
			assert.Equal(t, repoName+"//"+vpc, rows[0].Target)
			assert.Equal(t, map[string]any{
				"repo": repoName, "stack": vpc, "reader_repo": repoName, "reader": eks, "run_id": runID, "reason": v1.ErrCodeOutputsNotShared,
			}, rows[0].Details)
			assert.Empty(t, e.audit("outputs_read"))
		})
	}
}

func TestOutputsServedToAListedStackOfTheSameRepository(t *testing.T) {
	tests := []struct {
		name      string
		root      []string
		stackFile string
	}{
		{name: "listed by key in the stack file", stackFile: "outputs:\n  readers:\n    - stacks/prod/eks\n"},
		{name: "matched by a glob in the stack file", stackFile: "outputs:\n  readers:\n    - stacks/*/eks\n"},
		{name: "matched by a glob in stackorder.yaml", root: []string{"stacks/prod/*"}},
		{name: "every stack of the repository", root: []string{"stacks/**"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			if tt.root != nil {
				cfg = sharedConfig(tt.root...)
			}
			e, state := newOutputsEnv(t, cfg)
			if tt.stackFile != "" {
				e.setStackFile(repoName, mainSHA, vpc, tt.stackFile)
			}
			job, runID := e.readingPlan(7)

			got, err := e.outputs(job.p, vpc, runID, eks)
			require.NoError(t, err)
			want := &v1.StackOutputs{
				Repo: repoName, Stack: vpc, Serial: 42, Lineage: canaryLineage, TerraformVersion: "1.9.8",
				Outputs:  map[string]v1.StackOutput{"vpc_id": served(output(canaryValue, false))},
				Withheld: []string{"db_password"},
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("outputs (-want +got):\n%s", diff)
			}
			assert.Equal(t, []stateread.Location{locationOf(stateAt(vpc))}, state.reads(),
				"the recorded state object is read, not the backend of the uploaded graph")

			rows := e.audit("outputs_read")
			require.Len(t, rows, 1)
			assert.Equal(t, job.p.Actor(), rows[0].Actor)
			assert.Equal(t, repoName+"//"+vpc, rows[0].Target)
			assert.Equal(t, map[string]any{
				"repo": repoName, "stack": vpc, "reader_repo": repoName, "reader": eks, "run_id": runID,
				"outputs": float64(1), "sensitive": float64(0), "withheld": float64(1),
			}, rows[0].Details)
			assert.Empty(t, e.audit("outputs_refused"))
		})
	}
}

func TestOutputsReadersComeFromTheDefaultBranch(t *testing.T) {
	const listsEKS = "outputs:\n  readers:\n    - stacks/prod/eks\n"
	e, state := newOutputsEnv(t, baseConfig())
	widened := &v1.StackConfig{Outputs: &v1.OutputsConfig{Readers: []string{eks}, Sensitive: v1.OutputsShare}}
	withReaders := func(sha string) v1.Graph {
		g := testGraph(sha)
		for i := range g.Stacks {
			if g.Stacks[i].Key == vpc {
				g.Stacks[i].Config = widened
			}
		}
		return g
	}
	onMain := withReaders(mainSHA)
	gid, _, err := e.st.SaveGraph(e.ctx, repoID, &onMain)
	require.NoError(t, err)
	require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, gid))
	e.setStackFile(repoName, headSHA, vpc, listsEKS)

	e.openPull(7, headSHA)
	job := e.planJob(7)
	created, err := e.svc.CreateRun(e.ctx, job.p, v1.CreateRunRequest{Repo: repoName, SHA: headSHA, BaseSHA: baseSHA, PRNumber: 7, Mode: v1.ModePlan})
	require.NoError(t, err)
	runID := created.RunID
	_, err = e.svc.UploadGraph(e.ctx, job.p, runID, v1.GraphUploadRequest{
		Graph: withReaders(headSHA), ChangedPaths: []string{"modules/vpc/main.tf"}, Config: sharedConfig("stacks/**"),
	})
	require.NoError(t, err)
	require.Equal(t, widened, e.stack(vpc).Config, "the upload wrote its readers into the stack row")
	e.setStateObject(repoID, vpc, stateAt(vpc))

	_, err = e.outputs(job.p, vpc, runID, eks)
	requireCoded(t, err, principal.ErrForbidden, v1.ErrCodeOutputsNotShared)
	assert.Empty(t, state.reads(), "readers of the pull request, its graph and the default-branch graph are ignored")

	push := func(sha, stackFile string) {
		t.Helper()
		e.gh.SetContents(repoName, sha, "stackorder.yaml", []byte("version: 1\n"))
		e.setStackFile(repoName, sha, vpc, stackFile)
		require.NoError(t, e.svc.HandlePush(e.ctx, e.gh.PushEvent(repoName, "refs/heads/main", sha, vpc+"/.stackorder.yaml")))
		repo, err := e.st.GetRepo(e.ctx, repoID)
		require.NoError(t, err)
		require.Equal(t, sha, repo.ConfigSHA)
	}
	push(pushSHA, listsEKS)
	got, err := e.outputs(job.p, vpc, runID, eks)
	require.NoError(t, err, "a push that lists the reader shares the outputs")
	assert.Equal(t, []string{"db_password"}, got.Withheld, "sensitive comes from the default branch too")

	push(laterPushSHA, "outputs:\n  readers: []\n")
	_, err = e.outputs(job.p, vpc, runID, eks)
	requireCoded(t, err, principal.ErrForbidden, v1.ErrCodeOutputsNotShared)

	repo, err := e.st.GetRepo(e.ctx, repoID)
	require.NoError(t, err)
	require.NoError(t, e.st.UpdateRepoConfig(e.ctx, repoID, repo.Config, ""))
	e.setStackFile(repoName, "main", vpc, listsEKS)
	_, err = e.outputs(job.p, vpc, runID, eks)
	require.NoError(t, err, "without a recorded commit the default branch itself is read")
	e.setStackFile(repoName, "main", vpc, "outputs:\n  readers: []\n")
	_, err = e.outputs(job.p, vpc, runID, eks)
	require.NoError(t, err, "a read of the branch is cached for a minute")
	e.clock.Advance(61 * time.Second)
	_, err = e.outputs(job.p, vpc, runID, eks)
	requireCoded(t, err, principal.ErrForbidden, v1.ErrCodeOutputsNotShared)
}

func TestOutputsReaderMustBelongToTheRun(t *testing.T) {
	type read struct {
		p      principal.Principal
		runID  string
		reader string
	}
	claims := func(c oidc.Claims) principal.Principal {
		return principal.Principal{Kind: principal.OIDC, Login: c.Actor, Claims: &c}
	}
	tests := []struct {
		name    string
		prepare func(t *testing.T, e *env, job planJob, runID string) read
		kind    error
		reason  string
		scope   string
		audited *read
	}{
		{
			name:    "a pending stack of the run",
			prepare: func(_ *testing.T, _ *env, job planJob, runID string) read { return read{job.p, runID, eks} },
		},
		{
			name: "a stack the run does not hold",
			prepare: func(t *testing.T, e *env, job planJob, runID string) read {
				require.NotContains(t, stackStatuses(e.run(runID)), "stacks/dev/other")
				return read{job.p, runID, "stacks/dev/other"}
			},
			kind: principal.ErrForbidden, reason: "forbidden",
		},
		{
			name: "a stack the repository does not have",
			prepare: func(_ *testing.T, _ *env, job planJob, runID string) read {
				return read{job.p, runID, "stacks/prod/nope"}
			},
			kind: principal.ErrForbidden, reason: "forbidden",
		},
		{
			name:    "no reader",
			prepare: func(_ *testing.T, _ *env, job planJob, runID string) read { return read{job.p, runID, ""} },
			kind:    principal.ErrForbidden, reason: "forbidden",
		},
		{
			name:    "no run",
			prepare: func(_ *testing.T, _ *env, job planJob, _ string) read { return read{job.p, "", eks} },
			kind:    principal.ErrForbidden, reason: "forbidden",
		},
		{
			name:    "a malformed run id",
			prepare: func(_ *testing.T, _ *env, job planJob, _ string) read { return read{job.p, "not-a-run", eks} },
			kind:    principal.ErrForbidden, reason: "forbidden",
		},
		{
			name:    "a reader that is not a stack key",
			prepare: func(_ *testing.T, _ *env, job planJob, runID string) read { return read{job.p, runID, "../" + eks} },
			kind:    principal.ErrForbidden, reason: "forbidden",
		},
		{
			name: "an overlong run id and reader",
			prepare: func(_ *testing.T, _ *env, job planJob, _ string) read {
				return read{job.p, strings.Repeat("r", 1<<20), "x" + strings.Repeat("é", 1<<19)}
			},
			kind: principal.ErrForbidden, reason: "forbidden",
			audited: &read{runID: strings.Repeat("r", 256) + "…", reader: "x" + strings.Repeat("é", 127) + "…"},
		},
		{
			name:    "an unknown run",
			prepare: func(_ *testing.T, _ *env, job planJob, _ string) read { return read{job.p, uuid.NewString(), eks} },
			kind:    principal.ErrForbidden, reason: "forbidden",
		},
		{
			name: "a reader that already reported",
			prepare: func(t *testing.T, e *env, job planJob, runID string) read {
				_, err := e.svc.RecordResult(e.ctx, job.p, runID, eks, planResult(eks, headSHA, 1))
				require.NoError(t, err)
				return read{job.p, runID, eks}
			},
			kind: principal.ErrForbidden, reason: "forbidden",
		},
		{
			name: "a finished plan run",
			prepare: func(t *testing.T, e *env, job planJob, runID string) read {
				for key := range stackStatuses(e.run(runID)) {
					_, err := e.svc.RecordResult(e.ctx, job.p, runID, key, planResult(key, headSHA, 1))
					require.NoError(t, err)
				}
				require.Equal(t, v1.RunPlanned, e.run(runID).Status)
				return read{job.p, runID, eks}
			},
			kind: principal.ErrConflict, reason: "conflict",
		},
		{
			name: "a superseded run",
			prepare: func(t *testing.T, e *env, job planJob, runID string) read {
				e.openPull(7, newHeadSHA)
				e.startPlan(7, newHeadSHA)
				require.Equal(t, v1.RunSuperseded, e.run(runID).Status)
				return read{job.p, runID, eks}
			},
			kind: principal.ErrSuperseded, reason: "superseded",
		},
		{
			name: "a token of another repository",
			prepare: func(_ *testing.T, e *env, _ planJob, runID string) read {
				return read{claims(e.oidc.PlanClaims(networkRepo, fmt.Sprint(networkID), 7, mergeSHA)), runID, eks}
			},
			kind: principal.ErrForbidden, reason: "forbidden", scope: networkRepo,
		},
		{
			name: "a token of another pull request",
			prepare: func(_ *testing.T, e *env, _ planJob, runID string) read {
				return read{claims(e.oidc.PlanClaims(repoName, fmt.Sprint(repoID), 8, mergeSHA)), runID, eks}
			},
			kind: principal.ErrForbidden, reason: "forbidden",
		},
		{
			name: "a signed-in user",
			prepare: func(_ *testing.T, _ *env, _ planJob, runID string) read {
				return read{principal.Principal{Kind: principal.Session, Login: author}, runID, eks}
			},
			kind: principal.ErrForbidden, reason: "forbidden",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, state := newOutputsEnv(t, sharedConfig("stacks/**"))
			job, runID := e.readingPlan(7)
			rd := tt.prepare(t, e, job, runID)

			got, err := e.outputs(rd.p, vpc, rd.runID, rd.reader)
			if tt.kind == nil {
				require.NoError(t, err)
				assert.Equal(t, vpc, got.Stack)
				assert.Len(t, state.reads(), 1)
				return
			}
			require.ErrorIs(t, err, tt.kind)
			assert.Empty(t, state.reads())
			rows := e.audit("outputs_refused")
			require.Len(t, rows, 1)
			assert.Equal(t, tt.reason, rows[0].Details["reason"])
			audited := rd
			if tt.audited != nil {
				audited = *tt.audited
			}
			assert.Equal(t, audited.reader, rows[0].Details["reader"])
			assert.Equal(t, audited.runID, rows[0].Details["run_id"])
			scope := tt.scope
			if scope == "" {
				scope = repoName
			}
			assert.Equal(t, scope, rows[0].Details["repo"], "the row is visible to the caller's own organisation")
		})
	}
}

type chunkedApply struct {
	e                *env
	state            *fakeState
	apply            v1.Run
	eksJob, cacheJob principal.Principal
	eksRun, cacheRun int64
}

const (
	prodCache = "stacks/prod/cache"
	prodJobs  = "stacks/prod/jobs"
)

func newChunkedApply(t *testing.T) chunkedApply {
	t.Helper()
	cfg := sharedConfig("stacks/**")
	cfg.Apply.MaxParallel = 1
	e, state := newOutputsEnv(t, cfg)
	e.plannedGraph(7, wideGraph(headSHA))
	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	for _, d := range e.gh.Dispatches() {
		e.reportAll(d, nil)
	}
	e.setStateObject(repoID, vpc, stateAt(vpc))
	dEKS, dCache := e.newestDispatchOf(eks), e.newestDispatchOf(prodCache)
	require.NotEqual(t, dEKS.RunID, dCache.RunID, "max_parallel 1 puts eks and cache in chunks of their own")
	require.Equal(t, []string{eks}, entryKeys(entries(t, dEKS)))
	require.Equal(t, []string{prodCache}, entryKeys(entries(t, dCache)))
	c := chunkedApply{
		e: e, state: state, apply: apply, eksRun: dEKS.RunID, cacheRun: dCache.RunID,
		eksJob: e.dispatchJob(dEKS.RunID, "production"), cacheJob: e.dispatchJob(dCache.RunID, "production"),
	}
	c.requireUnbound(t, c.eksRun)
	c.requireUnbound(t, c.cacheRun)
	return c
}

func (c chunkedApply) requireUnbound(t *testing.T, workflowRunID int64) {
	t.Helper()
	_, err := c.e.st.FindDispatchByWorkflowRun(c.e.ctx, workflowRunID)
	require.ErrorIs(t, err, store.ErrNotFound, "workflow run %d is bound to no dispatch", workflowRunID)
}

func (c chunkedApply) read(p principal.Principal, reader string) error {
	_, err := c.e.svc.StackOutputs(c.e.ctx, p, repoName, vpc, c.apply.ID, reader)
	return err
}

func TestOutputsDispatchTokenBindsTheReaderToItsChunk(t *testing.T) {
	ownChunksStillRead := func(t *testing.T, c chunkedApply) {
		c.requireUnbound(t, c.eksRun)
		c.requireUnbound(t, c.cacheRun)
		require.NoError(t, c.read(c.eksJob, eks), "the eks job still binds to its own dispatch")
		require.NoError(t, c.read(c.cacheJob, prodCache), "the cache job still binds to its own dispatch")
		_, err := c.e.svc.RecordResult(c.e.ctx, c.eksJob, c.apply.ID, eks, applyResult(true))
		require.NoError(t, err, "the eks job still reports its result")
	}
	tests := []struct {
		name    string
		prepare func(t *testing.T, c chunkedApply)
		job     func(c chunkedApply) principal.Principal
		reader  string
		kind    error
		after   func(t *testing.T, c chunkedApply)
	}{
		{name: "the stack of the chunk", job: func(c chunkedApply) principal.Principal { return c.eksJob }, reader: eks},
		{name: "the other chunk's own job", job: func(c chunkedApply) principal.Principal { return c.cacheJob }, reader: prodCache},
		{
			name:   "a stack of another chunk, as the first call of a workflow run",
			job:    func(c chunkedApply) principal.Principal { return c.eksJob },
			reader: prodCache, kind: principal.ErrForbidden, after: ownChunksStillRead,
		},
		{
			name:   "a stack of another chunk, as the first call of the other workflow run",
			job:    func(c chunkedApply) principal.Principal { return c.cacheJob },
			reader: eks, kind: principal.ErrForbidden, after: ownChunksStillRead,
		},
		{
			name: "a stack of another chunk, once the workflow run is bound",
			prepare: func(t *testing.T, c chunkedApply) {
				_, err := c.e.svc.GetRunForPrincipal(c.e.ctx, c.eksJob, c.apply.ID)
				require.NoError(t, err)
				_, err = c.e.st.FindDispatchByWorkflowRun(c.e.ctx, c.eksRun)
				require.NoError(t, err)
			},
			job:    func(c chunkedApply) principal.Principal { return c.eksJob },
			reader: prodCache, kind: principal.ErrForbidden,
		},
		{
			name:   "another environment",
			job:    func(c chunkedApply) principal.Principal { return c.e.dispatchJob(c.eksRun, "staging") },
			reader: eks, kind: principal.ErrForbidden,
		},
		{
			name:    "an undispatched stack of the same wave",
			prepare: func(_ *testing.T, c chunkedApply) { c.e.undispatch(c.apply.ID, prodCache, 1) },
			job:     func(c chunkedApply) principal.Principal { return c.eksJob },
			reader:  prodCache, kind: principal.ErrForbidden,
		},
		{
			name: "an undispatched noop stack of the same wave",
			prepare: func(t *testing.T, c chunkedApply) {
				c.e.undispatch(c.apply.ID, prodCache, 1)
				c.e.setRowStatus(c.apply.ID, prodCache, v1.StackNoop)
				_, err := c.e.svc.RecordResult(c.e.ctx, c.eksJob, c.apply.ID, prodCache, applyResult(true))
				require.ErrorIs(t, err, principal.ErrConflict, "the binding alone accepts it; only its status refuses the result")
			},
			job:    func(c chunkedApply) principal.Principal { return c.eksJob },
			reader: prodCache, kind: principal.ErrForbidden,
		},
		{
			name:    "a skipped stack",
			prepare: func(_ *testing.T, c chunkedApply) { c.e.setRowStatus(c.apply.ID, prodJobs, v1.StackSkipped) },
			job:     func(c chunkedApply) principal.Principal { return c.eksJob },
			reader:  prodJobs, kind: principal.ErrForbidden,
		},
		{
			name:    "the stack of the chunk once blocked",
			prepare: func(_ *testing.T, c chunkedApply) { c.e.setRowStatus(c.apply.ID, eks, v1.StackBlocked) },
			job:     func(c chunkedApply) principal.Principal { return c.eksJob },
			reader:  eks, kind: principal.ErrForbidden,
		},
		{
			name:    "the stack of the chunk once noop",
			prepare: func(_ *testing.T, c chunkedApply) { c.e.setRowStatus(c.apply.ID, eks, v1.StackNoop) },
			job:     func(c chunkedApply) principal.Principal { return c.eksJob },
			reader:  eks, kind: principal.ErrForbidden,
		},
		{
			name:    "the stack of the chunk once applied",
			prepare: func(_ *testing.T, c chunkedApply) { c.e.setRowStatus(c.apply.ID, eks, v1.StackApplied) },
			job:     func(c chunkedApply) principal.Principal { return c.eksJob },
			reader:  eks, kind: principal.ErrForbidden,
		},
		{
			name: "a finished run",
			prepare: func(t *testing.T, c chunkedApply) {
				c.e.setRowStatus(c.apply.ID, eks, v1.StackApplying)
				_, err := c.e.st.Pool().Exec(c.e.ctx, `UPDATE runs SET status = 'failed' WHERE id = $1`, c.apply.ID)
				require.NoError(t, err)
			},
			job:    func(c chunkedApply) principal.Principal { return c.eksJob },
			reader: eks, kind: principal.ErrConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newChunkedApply(t)
			if tt.prepare != nil {
				tt.prepare(t, c)
			}
			err := c.read(tt.job(c), tt.reader)
			if tt.kind == nil {
				require.NoError(t, err)
				assert.Len(t, c.state.reads(), 1)
			} else {
				require.ErrorIs(t, err, tt.kind)
				assert.Empty(t, c.state.reads())
			}
			if tt.after != nil {
				tt.after(t, c)
			}
		})
	}
}

func TestOutputsAcrossRepositories(t *testing.T) {
	e, state := newOutputsEnv(t, baseConfig())
	e.gh.SetRepo(networkRepo, gh.Repository{ID: networkID, DefaultBranch: "main", Private: true})
	e.gh.AddInstallation(instID, "acme", networkRepo)
	e.gh.SetRepo(globexRepo, gh.Repository{ID: globexID, DefaultBranch: "main", Private: true})
	e.gh.AddInstallation(globexInst, "globex", globexRepo)
	_, err := e.st.UpsertInstallation(e.ctx, store.Installation{ID: globexInst, Account: "globex", AccountType: "Organization"})
	require.NoError(t, err)
	e.addRepo(networkID, instID, networkRepo, &v1.RepoConfig{Version: 1}, sharedVPC, sharedDNS)
	e.setStackFile(networkRepo, mainSHA, sharedVPC, "outputs:\n  readers:\n    - acme/infra//stacks/prod/*\n")
	e.setStackFile(networkRepo, mainSHA, sharedDNS, "outputs:\n  readers:\n    - acme/infra//stacks/staging/*\n    - stacks/prod/eks\n")
	e.addRepo(globexID, globexInst, globexRepo, &v1.RepoConfig{Version: 1, Outputs: &v1.OutputsConfig{Readers: []string{"acme/infra//stacks/**"}}}, sharedVPC)
	job, runID := e.readingPlan(7)

	tests := []struct {
		name string
		repo string
		key  string
		ok   bool
	}{
		{name: "listed as owner/repo//glob", repo: networkRepo, key: sharedVPC, ok: true},
		{name: "repository names ignore case", repo: "ACME/Network", key: sharedVPC, ok: true},
		{name: "not listed, and an unqualified entry names the producer's own stacks", repo: networkRepo, key: sharedDNS},
		{name: "another installation", repo: globexRepo, key: sharedVPC},
		{name: "no such repository", repo: "acme/nope", key: sharedVPC},
		{name: "no such stack", repo: networkRepo, key: "stacks/shared/nope"},
		{name: "an external key", repo: networkRepo, key: "acme/infra//" + vpc},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(state.reads())
			got, err := e.svc.StackOutputs(e.ctx, job.p, tt.repo, tt.key, runID, eks)
			if tt.ok {
				require.NoError(t, err)
				assert.Equal(t, networkRepo, got.Repo)
				assert.Equal(t, tt.key, got.Stack)
				assert.Equal(t, locationOf(stateAt(networkRepo+"/"+tt.key)), state.reads()[before])
				return
			}
			message := requireCoded(t, err, principal.ErrNotFound, "not_found")
			assert.Equal(t, notFoundMessage(tt.repo, tt.key), message, "every refusal across repositories reads alike")
			assert.Len(t, state.reads(), before)
		})
	}

	scopes := map[string]int{}
	for _, row := range e.audit("outputs_refused") {
		scopes[row.Details["repo"].(string)]++
		assert.Equal(t, repoName, row.Details["reader_repo"])
	}
	assert.Equal(t, map[string]int{networkRepo: 3, repoName: 2}, scopes,
		"a refusal is shown to the producer's organisation only when it shares the reader's installation")
	read := e.audit("outputs_read")
	require.Len(t, read, 2)
	for _, row := range read {
		assert.Equal(t, networkRepo, row.Details["repo"])
		assert.Equal(t, networkRepo+"//"+sharedVPC, row.Target)
	}
}

func TestOutputsInstanceOverridesStack(t *testing.T) {
	blue, green, red := v1.StackKey(vpc, "blue"), v1.StackKey(vpc, "green"), v1.StackKey(vpc, "red")
	e, state := newOutputsEnv(t, baseConfig())
	e.setStackFile(repoName, mainSHA, vpc, `outputs:
  readers:
    - stacks/prod/eks
instances:
  blue:
    outputs:
      sensitive: share
  green:
    outputs:
      readers:
        - "{{ dir .Path }}/apps"
  red:
    outputs:
      readers:
        - "../{{ .Name }}"
`)
	e.openPull(7, headSHA)
	job, runID, _ := e.startPlanGraph(7, instancesGraph(headSHA, "blue", "green", "red"))
	for _, key := range []string{blue, green, red} {
		e.setStateObject(repoID, key, stateAt(key))
	}

	tests := []struct {
		name     string
		key      string
		reader   string
		withheld []string
		code     string
		message  string
	}{
		{name: "an instance inherits the stack's readers and shares sensitive values", key: blue, reader: eks},
		{name: "an instance's readers replace the stack's", key: green, reader: eks, code: v1.ErrCodeOutputsNotShared,
			message: notSharedMessage(green, eks, vpc)},
		{name: "an instance's readers render with its template data", key: green, reader: apps, withheld: []string{"db_password"}},
		{name: "a stack the instance does not list", key: blue, reader: apps, code: v1.ErrCodeOutputsNotShared},
		{name: "readers that do not render share nothing", key: red, reader: eks, code: v1.ErrCodeOutputsNotShared,
			message: "the outputs configuration of " + red + " on the default branch does not resolve, so its outputs are not shared: " +
				"stacks/prod/vpc/.stackorder.yaml: instances.red.outputs.readers[0]: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(state.reads())
			got, err := e.outputs(job.p, tt.key, runID, tt.reader)
			if tt.code != "" {
				message := requireCoded(t, err, principal.ErrForbidden, tt.code)
				assert.True(t, strings.HasPrefix(message, tt.message), message)
				assert.Len(t, state.reads(), before)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.key, got.Stack)
			assert.Equal(t, tt.withheld, got.Withheld)
			assert.Equal(t, locationOf(stateAt(tt.key)), state.reads()[before])
		})
	}
}

func TestOutputsWithholdSensitiveValues(t *testing.T) {
	tests := []struct {
		name      string
		root      v1.OutputsSensitive
		stackFile string
		share     bool
	}{
		{name: "withheld by default", stackFile: "outputs:\n  readers: [stacks/prod/eks]\n"},
		{name: "withheld when the stack says so", stackFile: "outputs:\n  readers: [stacks/prod/eks]\n  sensitive: withhold\n"},
		{name: "shared when the stack says so", stackFile: "outputs:\n  readers: [stacks/prod/eks]\n  sensitive: share\n", share: true},
		{name: "shared by stackorder.yaml", root: v1.OutputsShare, stackFile: "outputs:\n  readers: [stacks/prod/eks]\n", share: true},
		{name: "the stack overrides stackorder.yaml", root: v1.OutputsShare, stackFile: "outputs:\n  readers: [stacks/prod/eks]\n  sensitive: withhold\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			if tt.root != "" {
				cfg.Outputs = &v1.OutputsConfig{Sensitive: tt.root}
			}
			e, state := newOutputsEnv(t, cfg)
			st := canaryState()
			st.Outputs["api_token"] = output("canary-token", true)
			state.set(st, nil)
			e.setStackFile(repoName, mainSHA, vpc, tt.stackFile)
			job, runID := e.readingPlan(7)

			got, err := e.outputs(job.p, vpc, runID, eks)
			require.NoError(t, err)
			want := map[string]v1.StackOutput{"vpc_id": served(st.Outputs["vpc_id"])}
			var withheld []string
			sensitive := 0
			if tt.share {
				want["db_password"], want["api_token"] = served(st.Outputs["db_password"]), served(st.Outputs["api_token"])
				sensitive = 2
			} else {
				withheld = []string{"api_token", "db_password"}
			}
			if diff := cmp.Diff(want, got.Outputs); diff != "" {
				t.Errorf("outputs (-want +got):\n%s", diff)
			}
			assert.Equal(t, withheld, got.Withheld, "withheld names are sorted")

			rows := e.audit("outputs_read")
			require.Len(t, rows, 1)
			assert.Equal(t, float64(len(want)), rows[0].Details["outputs"])
			assert.Equal(t, float64(sensitive), rows[0].Details["sensitive"])
			assert.Equal(t, float64(len(withheld)), rows[0].Details["withheld"])
		})
	}
}

func TestOutputsStateLocationUnknown(t *testing.T) {
	e, state := newOutputsEnv(t, sharedConfig("stacks/prod/eks"))
	g := testGraph(mainSHA)
	gid, _, err := e.st.SaveGraph(e.ctx, repoID, &g)
	require.NoError(t, err)
	require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, gid))
	e.openPull(7, headSHA)
	job, runID, _ := e.startPlan(7, headSHA)
	require.NotNil(t, e.stack(vpc).Backend, "the graph names a backend")

	_, err = e.outputs(job.p, vpc, runID, eks)
	message := requireCoded(t, err, principal.ErrConflict, v1.ErrCodeStateLocationUnknown)
	assert.Contains(t, message, "no apply or drift job")
	assert.Empty(t, state.reads(), "the backend of a graph is never read")
	rows := e.audit("outputs_refused")
	require.Len(t, rows, 1)
	assert.Equal(t, v1.ErrCodeStateLocationUnknown, rows[0].Details["reason"])

	require.NoError(t, e.svc.RunDriftStack(e.ctx, repoID, e.stack(vpc).ID.String()))
	d := e.newestDispatchOf(vpc)
	drift := v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, Summary: &v1.PlanSummary{}, State: stateAt(vpc)}
	_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(d.RunID, v1.DefaultEnvironment), d.Inputs["run_id"], vpc, drift)
	require.NoError(t, err)

	_, err = e.outputs(job.p, vpc, runID, eks)
	require.NoError(t, err, "a drift job recorded where the state is")
	assert.Equal(t, []stateread.Location{locationOf(stateAt(vpc))}, state.reads())
}

func TestOutputsLocationComesFromApplyAndDriftResults(t *testing.T) {
	applied := &v1.StateObject{Bucket: "acme-state", Key: "applied/" + vpc + ".tfstate", Region: "eu-west-1"}
	drifted := &v1.StateObject{Bucket: "acme-state", Key: "env:/blue/" + vpc + ".tfstate", Region: "eu-west-1"}
	elsewhere := &v1.Backend{Type: "s3", Bucket: "acme-other", Key: eks + ".tfstate", Region: "us-east-1"}
	tests := []struct {
		name   string
		record func(t *testing.T, e *env)
		want   *v1.StateObject
	}{
		{
			name: "an apply result",
			record: func(t *testing.T, e *env) {
				e.planned(7, headSHA)
				e.comment(7, applier, "stackorder apply")
				apply := e.applyRun(7)
				prod := e.dispatchJob(e.newestDispatchOf(vpc).RunID, "production")
				_, err := e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withState(applyResult(true), applied))
				require.NoError(t, err)
			},
			want: applied,
		},
		{
			name: "a drift result",
			record: func(t *testing.T, e *env) {
				g := testGraph(mainSHA)
				gid, _, err := e.st.SaveGraph(e.ctx, repoID, &g)
				require.NoError(t, err)
				require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, gid))
				require.NoError(t, e.svc.RunDriftStack(e.ctx, repoID, e.stack(vpc).ID.String()))
				d := e.newestDispatchOf(vpc)
				drift := v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, Summary: &v1.PlanSummary{}}
				_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(d.RunID, v1.DefaultEnvironment), d.Inputs["run_id"], vpc, withState(drift, drifted))
				require.NoError(t, err)
			},
			want: drifted,
		},
		{name: "neither", record: func(*testing.T, *env) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, state := newOutputsEnv(t, sharedConfig("stacks/**"))
			tt.record(t, e)

			g := testGraph(newHeadSHA)
			for i := range g.Stacks {
				if g.Stacks[i].Key == vpc {
					g.Stacks[i].Backend = elsewhere
				}
			}
			e.openPull(8, newHeadSHA)
			job, runID, _ := e.startPlanGraph(8, g)
			require.Equal(t, elsewhere, e.stack(vpc).Backend, "the pull request repointed the producer's backend")
			repointed := &v1.StateObject{Bucket: elsewhere.Bucket, Key: elsewhere.Key, Region: elsewhere.Region}
			_, err := e.svc.RecordResult(e.ctx, job.p, runID, vpc, withState(planResult(vpc, newHeadSHA, 1), repointed))
			require.NoError(t, err)

			_, err = e.outputs(job.p, vpc, runID, eks)
			if tt.want == nil {
				requireCoded(t, err, principal.ErrConflict, v1.ErrCodeStateLocationUnknown)
				assert.Empty(t, state.reads(), "neither the pull request's backend nor its plan result locates the state")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []stateread.Location{locationOf(tt.want)}, state.reads(),
				"the object the last apply or drift job reported is read, not the one the pull request names")
		})
	}
}

func TestOutputsProducerRemovedFromTheDefaultBranch(t *testing.T) {
	e, state := newOutputsEnv(t, sharedConfig("stacks/**"))
	job, runID := e.readingPlan(7)
	setDefault := func(sha string, keep func(key string) bool) {
		t.Helper()
		g := testGraph(sha)
		var stacks []v1.Stack
		for _, st := range g.Stacks {
			if keep(st.Key) {
				stacks = append(stacks, st)
			}
		}
		var edges []v1.Edge
		for _, ed := range g.Edges {
			if keep(ed.From.Key) && (ed.To.Kind != v1.NodeStack || keep(ed.To.Key)) {
				edges = append(edges, ed)
			}
		}
		g.Stacks, g.Edges = stacks, edges
		gid, _, err := e.st.SaveGraph(e.ctx, repoID, &g)
		require.NoError(t, err)
		require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, gid))
	}
	tests := []struct {
		name    string
		prepare func()
		ok      bool
	}{
		{name: "no default-branch graph is known yet", prepare: func() {}, ok: true},
		{name: "the default branch no longer has the stack", prepare: func() { setDefault(mainSHA, func(key string) bool { return key != vpc }) }},
		{name: "the default branch has it again", prepare: func() { setDefault(mergeSHA, func(string) bool { return true }) }, ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.prepare()
			before := len(state.reads())
			_, err := e.outputs(job.p, vpc, runID, eks)
			if tt.ok {
				require.NoError(t, err)
				assert.Len(t, state.reads(), before+1)
				return
			}
			message := requireCoded(t, err, principal.ErrNotFound, "not_found")
			assert.Equal(t, notFoundMessage(repoName, vpc), message)
			assert.Len(t, state.reads(), before)
		})
	}
}

func TestOutputsAPIKeyBypassesReadersAndIsAudited(t *testing.T) {
	someRun := uuid.NewString()
	tests := []struct {
		name    string
		key     string
		runID   string
		reader  string
		kind    error
		reason  string
		audited string
	}{
		{name: "no run and no reader", key: vpc},
		{name: "a run and a reader are only checked for form", key: vpc, runID: someRun, reader: eks},
		{name: "a malformed run id", key: vpc, runID: "not-a-run", kind: principal.ErrInvalid, reason: "invalid"},
		{name: "a malformed reader", key: vpc, reader: "../eks", kind: principal.ErrInvalid, reason: "invalid"},
		{name: "a stack that does not exist", key: "stacks/prod/nope", kind: principal.ErrNotFound, reason: "not_found"},
		{
			name: "an overlong stack key", key: strings.Repeat("k", 1<<20), kind: principal.ErrNotFound, reason: "not_found",
			audited: strings.Repeat("k", 256) + "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, state := newOutputsEnv(t, baseConfig())
			e.readingPlan(7)
			admin := apiKey()

			got, err := e.outputs(admin, tt.key, tt.runID, tt.reader)
			readerRepo := ""
			if tt.reader != "" {
				readerRepo = repoName
			}
			base := map[string]any{"repo": repoName, "stack": tt.key, "reader_repo": readerRepo, "reader": tt.reader, "run_id": tt.runID}
			if tt.kind != nil {
				require.ErrorIs(t, err, tt.kind)
				assert.Empty(t, state.reads())
				rows := e.audit("outputs_refused")
				require.Len(t, rows, 1)
				base["reason"] = tt.reason
				if tt.audited != "" {
					base["stack"] = tt.audited
					assert.Equal(t, repoName+"//"+tt.audited, rows[0].Target)
				}
				assert.Equal(t, base, rows[0].Details)
				return
			}
			require.NoError(t, err, "nothing lists the API key, which reads anyway")
			assert.Equal(t, []string{"db_password"}, got.Withheld, "an API key gets what readers get")
			rows := e.audit("outputs_read")
			require.Len(t, rows, 1)
			assert.Equal(t, "apikey:ci", rows[0].Actor)
			assert.Equal(t, repoName+"//"+vpc, rows[0].Target)
			base["outputs"], base["sensitive"], base["withheld"] = float64(1), float64(0), float64(1)
			assert.Equal(t, base, rows[0].Details)
		})
	}
}

func TestOutputsAuditCarriesNoValues(t *testing.T) {
	logs := &logRecorder{}
	e, state := newOutputsEnv(t, baseConfig(), withLogger(logs.debugLogger()))
	e.setStackFile(repoName, mainSHA, vpc, "outputs:\n  readers: [stacks/prod/eks]\n  sensitive: share\n")
	job, runID := e.readingPlan(7)

	got, err := e.outputs(job.p, vpc, runID, eks)
	require.NoError(t, err)
	body, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(body), canaryValue)
	require.Contains(t, string(body), canarySecret)
	_, err = e.outputs(apiKey(), vpc, "", "")
	require.NoError(t, err)
	_, err = e.outputs(job.p, vpc, runID, apps)
	requireCoded(t, err, principal.ErrForbidden, v1.ErrCodeOutputsNotShared)
	state.set(nil, errors.New("SlowDown: reduce your request rate"))
	_, err = e.outputs(job.p, vpc, runID, eks)
	require.ErrorIs(t, err, principal.ErrUnavailable)

	rows, _, err := e.st.ListAudit(e.ctx, store.AuditFilter{Limit: 500})
	require.NoError(t, err)
	assert.Len(t, e.audit("outputs_read"), 2)
	assert.Len(t, e.audit("outputs_refused"), 1)
	audit, err := json.Marshal(rows)
	require.NoError(t, err)
	assert.NotContains(t, string(audit), "canary")
	require.Len(t, logs.records(t, "read stack outputs"), 1, "the failed read is logged")
	assert.NotContains(t, logs.String(), "canary")
}

func TestOutputsDisabledWithoutStateReader(t *testing.T) {
	tests := []struct {
		name string
		opts []envOption
	}{
		{name: "no state reader"},
		{name: "a nil *stateread.Reader", opts: []envOption{withStateReader((*stateread.Reader)(nil))}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, sharedConfig("stacks/**"), tt.opts...)
			job, runID := e.readingPlan(7)
			for _, p := range []principal.Principal{job.p, apiKey()} {
				_, err := e.outputs(p, vpc, runID, eks)
				requireCoded(t, err, principal.ErrNotFound, v1.ErrCodeOutputsDisabled)
			}
			assert.Empty(t, e.audit("outputs_read"))
			assert.Empty(t, e.audit("outputs_refused"))
		})
	}
}

func TestOutputsStateErrorsAreCoded(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind error
		code string
	}{
		{name: "bucket not granted", err: stateread.ErrNotAllowed, kind: principal.ErrForbidden, code: v1.ErrCodeStateBucketNotAllowed},
		{name: "object missing", err: stateread.ErrNotFound, kind: principal.ErrNotFound, code: v1.ErrCodeStateNotFound},
		{name: "access denied", err: stateread.ErrAccessDenied, kind: principal.ErrForbidden, code: v1.ErrCodeStateAccessDenied},
		{name: "encrypted", err: stateread.ErrEncrypted, kind: principal.ErrUnprocessable, code: v1.ErrCodeStateEncrypted},
		{name: "unsupported version", err: stateread.ErrUnsupported, kind: principal.ErrUnprocessable, code: v1.ErrCodeStateUnsupported},
		{name: "too large", err: stateread.ErrTooLarge, kind: principal.ErrUnprocessable, code: v1.ErrCodeStateTooLarge},
		{name: "malformed", err: fmt.Errorf("%w at byte 1234", stateread.ErrMalformed), kind: principal.ErrUnprocessable, code: v1.ErrCodeStateUnreadable},
		{name: "unreadable", err: fmt.Errorf("get object: %w", stateread.ErrUnreadable), kind: principal.ErrUnprocessable, code: v1.ErrCodeStateUnreadable},
		{name: "no state and no error", kind: principal.ErrUnprocessable, code: v1.ErrCodeStateUnreadable},
		{name: "anything else", err: errors.New("InternalError: we encountered an internal error"), kind: principal.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, state := newOutputsEnv(t, baseConfig())
			e.readingPlan(7)
			state.set(nil, tt.err)

			_, err := e.outputs(apiKey(), vpc, "", "")
			require.ErrorIs(t, err, tt.kind)
			if tt.code == "" {
				var coded *principal.CodedError
				assert.False(t, errors.As(err, &coded), "an unexpected failure is no coded refusal")
			} else {
				assert.Contains(t, requireCoded(t, err, tt.kind, tt.code), vpc)
			}
			if tt.err != nil {
				assert.NotContains(t, err.Error(), tt.err.Error(), "the reader's error stays on the server")
			}
			assert.Len(t, state.reads(), 1)
			assert.Empty(t, e.audit("outputs_read"))
			assert.Empty(t, e.audit("outputs_refused"), "a failed read of an allowed stack is no refusal")
		})
	}
}
