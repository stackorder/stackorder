//go:build integration

package runs_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

func TestApplyGateLayers(t *testing.T) {
	tests := []struct {
		name     string
		cfg      func(*v1.RepoConfig)
		postPlan func(e *env, job planJob, runID string)
		setup    func(e *env)
		by       string
		want     []string
	}{
		{
			name: "layer 1 names the team",
			cfg:  func(c *v1.RepoConfig) { c.Apply.AllowedTeams = []string{"platform"} },
			want: []string{"**Layer 1, authorization**", "`acme/platform`", "octocat is not an active member"},
		},
		{
			name: "layer 1 reads a stack's narrower teams from the default branch",
			cfg:  func(c *v1.RepoConfig) { c.Apply.AllowedTeams = []string{"platform"} },
			setup: func(e *env) {
				e.gh.SetTeamMembership("acme", "platform", applier, gh.MembershipActive)
				g := testGraph(mainSHA)
				for i := range g.Stacks {
					if g.Stacks[i].Key == vpc {
						g.Stacks[i].Config = &v1.StackConfig{Apply: &v1.StackApplyConfig{AllowedTeams: []string{"acme/platform-prod"}}}
					}
				}
				id, _, err := e.st.SaveGraph(e.ctx, repoID, &g)
				require.NoError(e.t, err)
				require.NoError(e.t, e.st.SetDefaultGraph(e.ctx, repoID, id))
			},
			want: []string{"**Layer 1, authorization** (`stacks/prod/vpc`)", "`acme/platform-prod`"},
		},
		{
			name: "layer 1 reads a stack's teams through the Contents API before the first merge",
			cfg:  func(c *v1.RepoConfig) { c.Apply.AllowedTeams = []string{"platform"} },
			setup: func(e *env) {
				e.gh.SetTeamMembership("acme", "platform", applier, gh.MembershipActive)
				e.gh.SetContents(repoName, "main", "stacks/prod/vpc/.stackorder.yaml", []byte("apply:\n  allowed_teams: [acme/platform-prod]\n"))
			},
			want: []string{"**Layer 1, authorization** (`stacks/prod/vpc`)", "`acme/platform-prod`"},
		},
		{
			name: "layer 1 refuses a stack whose default-branch configuration does not render",
			setup: func(e *env) {
				e.gh.SetContents(repoName, "main", "stacks/prod/vpc/.stackorder.yaml",
					[]byte("environment: '{{ if eq .Path \"stacks/prod/vpc\" }}{{ .Nope }}{{ end }}'\n"))
			},
			want: []string{"**Layer 1, authorization** (`stacks/prod/vpc`)", "apply.allowed_teams is unknown", "Nope"},
		},
		{
			name: "layer 2 counts approvals on the head commit",
			cfg:  func(c *v1.RepoConfig) { c.Apply.RequireApprovals = 1 },
			setup: func(e *env) {
				e.gh.SetReviews(repoName, 7, []gh.Review{{User: gh.User{Login: "bob"}, State: gh.ReviewApproved, CommitID: baseSHA}})
			},
			want: []string{"**Layer 2, approvals**", "0 of 1 required approvals on the head commit 3333333"},
		},
		{
			name: "layer 2 counts only approvals from users with write access",
			cfg:  func(c *v1.RepoConfig) { c.Apply.RequireApprovals = 2 },
			setup: func(e *env) {
				e.gh.SetCollaboratorPermission(repoName, "bob", "write")
				e.gh.SetCollaboratorPermission(repoName, "reader", "read")
				e.gh.SetReviews(repoName, 7, []gh.Review{
					{User: gh.User{Login: "bob"}, State: gh.ReviewApproved, CommitID: headSHA},
					{User: gh.User{Login: "reader"}, State: gh.ReviewApproved, CommitID: headSHA},
					{User: gh.User{Login: "passer-by"}, State: gh.ReviewApproved, CommitID: headSHA},
				})
			},
			want: []string{"**Layer 2, approvals**", "1 of 2 required approvals on the head commit 3333333"},
		},
		{
			name: "layer 2 four eyes",
			cfg:  func(c *v1.RepoConfig) { c.Apply.FourEyes = true },
			by:   author,
			want: []string{"**Layer 2, four eyes**", "author opened this pull request"},
		},
		{
			name: "layer 2 mergeability",
			setup: func(e *env) {
				e.gh.SetPull(repoName, gh.PullRequest{Number: 7, State: gh.IssueOpen, HeadSHA: headSHA, BaseSHA: baseSHA,
					User: gh.User{Login: author}, Mergeable: ptr(false), MergeableState: "dirty"})
			},
			want: []string{"**Layer 2, approvals**", "not mergeable"},
		},
		{
			name: "layer 2 refuses a closed pull request",
			setup: func(e *env) {
				e.gh.SetPull(repoName, gh.PullRequest{Number: 7, State: gh.IssueClosed, HeadSHA: headSHA, BaseSHA: baseSHA,
					User: gh.User{Login: author}, Mergeable: ptr(true), MergeableState: "clean"})
			},
			want: []string{"**Layer 2, approvals**", "the pull request is closed"},
		},
		{
			name: "layer 2 refuses a merged pull request",
			setup: func(e *env) {
				e.gh.SetPull(repoName, gh.PullRequest{Number: 7, State: gh.IssueClosed, Merged: true, HeadSHA: headSHA, BaseSHA: baseSHA,
					User: gh.User{Login: author}, MergeableState: "unknown"})
			},
			want: []string{"**Layer 2, approvals**", "the pull request is already merged"},
		},
		{
			name: "layer 2 code owner review",
			cfg:  func(c *v1.RepoConfig) { c.Apply.RequireCodeownerReview = true },
			setup: func(e *env) {
				e.gh.SetContents(repoName, "main", ".github/CODEOWNERS", []byte("/stacks/prod/ @acme/platform-prod\n/stacks/staging/ @carol\n"))
				e.gh.SetTeamMembership("acme", "platform-prod", "dave", gh.MembershipActive)
				for _, login := range []string{"bob", "carol", "dave"} {
					e.gh.SetCollaboratorPermission(repoName, login, "write")
				}
				e.gh.SetReviews(repoName, 7, []gh.Review{
					{User: gh.User{Login: "bob"}, State: gh.ReviewApproved, CommitID: headSHA},
					{User: gh.User{Login: "carol"}, State: gh.ReviewApproved, CommitID: headSHA},
					{User: gh.User{Login: "dave"}, State: gh.ReviewApproved, CommitID: headSHA},
					{User: gh.User{Login: "dave"}, State: gh.ReviewChangesRequested, CommitID: headSHA},
				})
			},
			want: []string{"**Layer 2, code owner review** (`stacks/prod/vpc`)", "@acme/platform-prod", "(`stacks/prod/eks`)"},
		},
		{
			name: "layer 2 code owner review under a catch-all rule",
			cfg:  func(c *v1.RepoConfig) { c.Apply.RequireCodeownerReview = true },
			setup: func(e *env) {
				e.gh.SetContents(repoName, "main", ".github/CODEOWNERS", []byte("* @acme/infra\n"))
			},
			want: []string{"**Layer 2, code owner review** (`stacks/prod/vpc`)", "@acme/infra", "(`stacks/staging/vpc`)"},
		},
		{
			name: "layer 2 code owner review follows the rule of the stack's files",
			cfg:  func(c *v1.RepoConfig) { c.Apply.RequireCodeownerReview = true },
			setup: func(e *env) {
				e.gh.SetContents(repoName, "main", ".github/CODEOWNERS", []byte("/stacks/ @acme/a\n*.tf @acme/b\n"))
				e.gh.SetTeamMembership("acme", "a", "dave", gh.MembershipActive)
				e.gh.SetCollaboratorPermission(repoName, "dave", "write")
				e.gh.SetReviews(repoName, 7, []gh.Review{{User: gh.User{Login: "dave"}, State: gh.ReviewApproved, CommitID: headSHA}})
			},
			want: []string{"**Layer 2, code owner review** (`stacks/prod/vpc`)", "code owner: @acme/b"},
		},
		{
			name: "layer 3 needs a plan of the head commit",
			setup: func(e *env) {
				e.openPull(7, newHeadSHA)
			},
			want: []string{"**Layer 3, plans for the head commit**", "no plan exists for the head commit 4444444"},
		},
		{
			name: "layer 3 refuses a failed plan",
			setup: func(e *env) {
				e.openPull(7, newHeadSHA)
				job, runID, _ := e.startPlan(7, newHeadSHA)
				for _, key := range []string{vpc, staging, eks} {
					_, err := e.svc.RecordResult(e.ctx, job.p, runID, key, planResult(key, newHeadSHA, 1))
					require.NoError(e.t, err)
				}
				_, err := e.svc.RecordResult(e.ctx, job.p, runID, apps, v1.StackResult{Mode: v1.ModePlan, Status: v1.ResultFailure, ExitCode: 1})
				require.NoError(e.t, err)
			},
			want: []string{"**Layer 3, plans for the head commit** (`stacks/prod/apps`)", "the plan did not succeed"},
		},
		{
			name: "layer 4 refuses a failing policy check",
			postPlan: func(e *env, job planJob, runID string) {
				_, err := e.svc.RecordCheck(e.ctx, job.p, runID, vpc, "policy",
					v1.CheckVerdict{Status: v1.CheckFail, Summary: "public bucket"})
				require.NoError(e.t, err)
				c := e.check(report.PolicyCheckName("policy", vpc))
				assert.Equal(e.t, gh.ConclusionFailure, c.Conclusion)
				_, err = e.svc.RecordCheck(e.ctx, job.p, runID, eks, "cost", v1.CheckVerdict{Status: v1.CheckWarn})
				require.NoError(e.t, err)
			},
			want: []string{"**Layer 4, policy checks** (`stacks/prod/vpc`)", "check `policy` failed: public bucket"},
		},
		{
			name: "layer 5 refuses a stack locked by another pull request",
			setup: func(e *env) {
				other, err := e.st.CreateRun(e.ctx, store.CreateRunParams{RepoID: repoID, SHA: baseSHA, PRNumber: 8, Trigger: v1.TriggerComment, Mode: v1.ModeApply})
				require.NoError(e.t, err)
				st, err := e.st.GetStackByKey(e.ctx, repoID, vpc)
				require.NoError(e.t, err)
				conflicts, err := e.st.TryLockStacks(e.ctx, []uuid.UUID{st.ID}, other.ID, 8, "apply of #8")
				require.NoError(e.t, err)
				require.Empty(e.t, conflicts)
			},
			want: []string{"**Layer 5, locks** (`stacks/prod/vpc`)", "locked by #8"},
		},
		{
			name: "every failure is reported at once",
			cfg: func(c *v1.RepoConfig) {
				c.Apply.AllowedTeams = []string{"platform"}
				c.Apply.RequireApprovals = 2
			},
			want: []string{"**Layer 1, authorization**", "0 of 2 required approvals"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			if tt.cfg != nil {
				tt.cfg(cfg)
			}
			e := newEnv(t, cfg)
			var postPlan func(job planJob, runID string)
			if tt.postPlan != nil {
				postPlan = func(job planJob, runID string) { tt.postPlan(e, job, runID) }
			}
			e.plannedGraphWith(7, testGraph(headSHA), postPlan)
			if tt.setup != nil {
				tt.setup(e)
			}
			by := tt.by
			if by == "" {
				by = applier
			}
			cm := e.comment(7, by, "stackorder apply")
			body := e.lastComment(7)
			assert.Contains(t, body, "**`stackorder apply` was refused.** Nothing was dispatched.")
			for _, w := range tt.want {
				assert.Contains(t, body, w)
			}
			assert.Empty(t, e.gh.Dispatches())
			assert.Equal(t, []string{gh.ReactionEyes}, e.gh.Reactions(cm.ID), "no rocket for a refusal")
			for key, pr := range e.locks() {
				assert.NotEqual(t, 7, pr, "a refused apply takes no lock on %s", key)
			}
			rows := e.audit("command")
			require.Len(t, rows, 1)
			assert.Equal(t, false, rows[0].Details["accepted"])
			_, _, cmds := e.m.snapshot()
			assert.Equal(t, 1, cmds["apply:false"])
		})
	}
}

func TestEvaluateApplyGatePasses(t *testing.T) {
	e := newEnv(t, baseConfig())
	runID := e.planned(7, headSHA)
	run, err := e.st.GetRun(e.ctx, uuid.MustParse(runID))
	require.NoError(t, err)
	failures, err := e.svc.EvaluateApplyGate(e.ctx, &run, applier, nil)
	require.NoError(t, err)
	assert.Empty(t, failures)
	failures, err = e.svc.EvaluateApplyGate(e.ctx, &run, "", nil)
	require.NoError(t, err)
	require.Len(t, failures, 1, "an apply nobody requested is not authorized")
	assert.Equal(t, report.LayerAuthorization, failures[0].Layer)
	cfg := baseConfig()
	cfg.Apply.RequireApprovals = 1
	e.setConfig(cfg)
	e.gh.SetCollaboratorPermission(repoName, "maintainer", "maintain")
	e.gh.SetReviews(repoName, 7, []gh.Review{{User: gh.User{Login: "maintainer"}, State: gh.ReviewApproved, CommitID: headSHA}})
	failures, err = e.svc.EvaluateApplyGate(e.ctx, &run, applier, nil)
	require.NoError(t, err)
	assert.Empty(t, failures, "an approval from a maintainer counts")
	failures, err = e.svc.EvaluateApplyGate(e.ctx, &run, applier, []string{vpc, "stacks/none"})
	require.NoError(t, err)
	require.Len(t, failures, 1)
	assert.Equal(t, report.LayerPlans, failures[0].Layer)
	assert.Equal(t, []string{"stacks/none"}, failures[0].Stacks)
}

func TestApplyAcrossThreeWaves(t *testing.T) {
	e := newEnv(t, baseConfig())
	planRun := e.planned(7, headSHA)
	planIDs := e.planRunIDs(planRun)

	cm := e.comment(7, applier, "stackorder apply")
	assert.Equal(t, []string{gh.ReactionEyes, gh.ReactionRocket}, e.gh.Reactions(cm.ID))
	apply := e.applyRun(7)
	assert.Equal(t, e.via(planRun)[eks], e.via(apply.ID)[eks], "the apply carries the via of its plan")
	assert.Equal(t, []string{vpc}, e.via(apply.ID)[eks])
	assert.Equal(t, v1.RunApplying, apply.Status)
	assert.Equal(t, v1.TriggerComment, apply.Trigger)
	assert.Equal(t, applier, apply.RequestedBy)
	assert.Equal(t, headSHA, apply.SHA)
	assert.Equal(t, 3, apply.Waves)
	assert.Equal(t, map[string]int{vpc: 7, staging: 7, eks: 7, apps: 7}, e.locks())

	wave0 := e.gh.Dispatches()
	require.Len(t, wave0, 2, "one dispatch per environment")
	byEnv := map[string]int{}
	for i, d := range wave0 {
		assert.Equal(t, runs.DefaultWorkflowFile, d.Workflow)
		assert.Equal(t, "main", d.Ref)
		assert.Equal(t, apply.ID, d.Inputs["run_id"])
		assert.Equal(t, "apply", d.Inputs["mode"])
		assert.Equal(t, "0", d.Inputs["wave"])
		assert.Equal(t, headSHA, d.Inputs["sha"])
		es := entries(t, d)
		require.Len(t, es, 1)
		byEnv[es[0].Environment] = i
		assert.Equal(t, headSHA, es[0].SHA)
		assert.Equal(t, v1.PlanArtifactName(es[0].Key, headSHA), es[0].Artifact)
		assert.Equal(t, planIDs[es[0].Key], es[0].PlanRunID)
		assert.NotZero(t, es[0].PlanRunID)
		assert.Equal(t, v1.ToolTerraform, es[0].Tool)
	}
	assert.Equal(t, []string{vpc}, entryKeys(entries(t, wave0[byEnv["production"]])))
	assert.Equal(t, []string{staging}, entryKeys(entries(t, wave0[byEnv["staging"]])))
	assert.Equal(t, gh.CheckRunInProgress, e.check(report.CheckApply).Status)
	assert.Equal(t, gh.CheckRunQueued, e.check(report.StackCheckName(report.CheckApply, apps)).Status)

	again := e.comment(7, applier, "stackorder apply")
	assert.Contains(t, e.lastComment(7), "apply run "+apply.ID+" of this pull request is still applying")
	assert.Equal(t, []string{gh.ReactionEyes}, e.gh.Reactions(again.ID), "a refused apply gets no rocket")
	assert.Len(t, e.gh.Dispatches(), 2, "a second apply while one runs dispatches nothing")
	assert.Equal(t, apply.ID, e.applyRun(7).ID)

	prod := wave0[byEnv["production"]]
	bound, err := e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(prod.RunID, "production"), apply.ID)
	require.NoError(t, err, "the first job of a dispatch binds it")
	assert.Equal(t, apply.ID, bound.ID)
	_, err = e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(prod.RunID+1000, "production"), apply.ID)
	require.ErrorIs(t, err, principal.ErrForbidden, "a duplicated dispatch's jobs are refused")
	_, err = e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(prod.RunID, "staging"), apply.ID)
	require.ErrorIs(t, err, principal.ErrForbidden, "the environment claim must match the dispatch")
	_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(prod.RunID, "production"), apply.ID, staging, applyResult(true))
	require.ErrorIs(t, err, principal.ErrForbidden, "a job cannot report a stack of another environment")

	e.gh.SetPendingDeployments(repoName, prod.RunID, []gh.PendingDeployment{{Environment: gh.Environment{Name: "production"}}})
	waiting := gh.WorkflowJob{RunID: prod.RunID, Name: "run / apply (" + vpc + ", " + vpc + ", production, 0)", Status: gh.RunStatusWaiting,
		HTMLURL: "https://github.com/acme/infra/actions/runs/9/job/1"}
	require.NoError(t, e.svc.HandleWorkflowJob(e.ctx, e.gh.WorkflowJobEvent("waiting", repoName, waiting)))
	sticky := e.sticky(7)
	assert.Contains(t, sticky, "**Waiting for approval**")
	assert.Contains(t, sticky, "https://github.com/acme/infra/actions/runs/")
	assert.Contains(t, sticky, "awaiting approval")
	assert.Equal(t, v1.StackApplying, stackStatuses(e.run(apply.ID))[vpc])
	e.gh.SetPendingDeployments(repoName, prod.RunID, nil)

	e.reportAll(wave0[byEnv["production"]], nil)
	assert.Len(t, e.gh.Dispatches(), 2, "wave 1 waits for all of wave 0")
	e.reportAll(wave0[byEnv["staging"]], nil)

	wave1 := e.dispatchesFrom(2)
	require.Len(t, wave1, 1)
	assert.Equal(t, "1", wave1[0].Inputs["wave"])
	assert.Equal(t, []string{eks}, entryKeys(entries(t, wave1[0])))
	require.NoError(t, e.svc.HandleWorkflowRun(e.ctx, e.gh.WorkflowRunEvent("requested", repoName, gh.WorkflowRun{ID: wave1[0].RunID})))
	d, err := e.st.FindDispatchByWorkflowRun(e.ctx, wave1[0].RunID)
	require.NoError(t, err, "the display title binds the dispatch")
	assert.Equal(t, 1, d.Wave)
	assert.Equal(t, 1, e.run(apply.ID).CurrentWave)

	e.reportAll(wave1[0], nil)
	wave2 := e.dispatchesFrom(3)
	require.Len(t, wave2, 1)
	assert.Equal(t, []string{apps}, entryKeys(entries(t, wave2[0])))
	e.reportAll(wave2[0], nil)
	assert.Len(t, e.gh.Dispatches(), 4)
	e.gh.CompleteWorkflowRun(repoName, wave2[0].RunID, gh.ConclusionSuccess)
	require.NoError(t, e.svc.HandleWorkflowRun(e.ctx, e.gh.WorkflowRunEvent("completed", repoName, gh.WorkflowRun{ID: wave2[0].RunID})))
	_, err = e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(wave2[0].RunID, "production"), apply.ID)
	require.ErrorIs(t, err, principal.ErrForbidden, "a token of a completed workflow run is refused")

	done := e.run(apply.ID)
	assert.Equal(t, v1.RunApplied, done.Status)
	for _, rs := range done.Stacks {
		assert.Equal(t, v1.StackApplied, rs.Status, rs.Key)
	}
	check := e.check(report.CheckApply)
	assert.Equal(t, gh.ConclusionSuccess, check.Conclusion)
	assert.Equal(t, headSHA, check.HeadSHA)
	assert.Contains(t, e.sticky(7), "### Stackorder: applied")
	assert.Equal(t, map[string]int{vpc: 7, staging: 7, eks: 7, apps: 7}, e.locks(), "before_merge keeps the locks until the merge")

	merged := e.gh.PullRequestEvent("closed", repoName, gh.PullRequest{
		Number: 7, State: gh.IssueClosed, Merged: true, MergeCommitSHA: mergeSHA, HeadSHA: headSHA, BaseSHA: baseSHA, User: gh.User{Login: author},
	})
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, merged))
	assert.Empty(t, e.locks())
	assert.Contains(t, e.lastComment(7), "Released 4 orchestration locks")
	assert.Len(t, e.audit("unlock"), 4)
	_, id, err := e.st.GetDefaultGraph(e.ctx, repoID)
	require.NoError(t, err, "the merged graph becomes the default-branch graph")
	planStored, err := e.st.GetRun(e.ctx, uuid.MustParse(planRun))
	require.NoError(t, err)
	assert.Equal(t, *planStored.GraphID, id)
	n := len(e.comments(7))
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, merged))
	assert.Len(t, e.comments(7), n, "a redelivered merge changes nothing")
}

func TestApplyEnvironmentIsReadFromTheDefaultBranchBeforeTheFirstMerge(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetContents(repoName, "main", eks+"/.stackorder.yaml", []byte("depends_on: [stacks/prod/vpc]\nenvironment: prod-eks\n"))
	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	envs := map[string]string{}
	for _, rs := range e.applyRun(7).Stacks {
		envs[rs.Key] = rs.Environment
	}
	assert.Equal(t, map[string]string{vpc: "production", staging: "staging", eks: "prod-eks", apps: "production"}, envs,
		"without a default-branch graph the stack's environment override is read at the default branch")
}

func TestConcurrentWorkersOnOneRun(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	req := v1.CreateRunRequest{Repo: repoName, SHA: headSHA, BaseSHA: baseSHA, PRNumber: 7, Mode: v1.ModePlan}
	ids := make([]string, 4)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			resp, err := e.svc.CreateRun(e.ctx, e.planJob(7).p, req)
			if assert.NoError(t, err) {
				ids[i] = resp.RunID
			}
		})
	}
	wg.Wait()
	for _, id := range ids {
		assert.Equal(t, ids[0], id, "jobs registering one head at once share its run")
	}
	job := e.planJob(7)
	resp, err := e.svc.UploadGraph(e.ctx, job.p, ids[0], v1.GraphUploadRequest{Graph: testGraph(headSHA), ChangedPaths: []string{"modules/vpc/main.tf"}})
	require.NoError(t, err)
	for _, a := range resp.Affected {
		wg.Go(func() {
			_, err := e.svc.RecordResult(e.ctx, job.p, ids[0], a.Key, planResult(a.Key, headSHA, 1))
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.Equal(t, v1.RunPlanned, e.run(ids[0]).Status)
	assert.Len(t, e.checksNamed(report.CheckPlan), 1, "concurrent renders create the roll-up once")

	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	wave0 := e.gh.Dispatches()
	require.Len(t, wave0, 2)
	for _, d := range wave0 {
		for _, en := range entries(t, d) {
			for range 3 {
				wg.Go(func() {
					_, err := e.svc.RecordResult(e.ctx, e.dispatchJob(d.RunID, en.Environment), apply.ID, en.Key, applyResult(true))
					assert.NoError(t, err, "a duplicated post of %s is idempotent", en.Key)
				})
			}
		}
	}
	wg.Wait()
	next := e.dispatchesFrom(2)
	require.Len(t, next, 1, "the last results of a wave arriving together dispatch the next wave once")
	assert.Equal(t, []string{eks}, entryKeys(entries(t, next[0])))
	r := e.run(apply.ID)
	assert.Equal(t, 1, r.CurrentWave)
	assert.Equal(t, v1.RunApplying, r.Status)
	assert.Len(t, e.checksNamed(report.CheckApply), 1)
}

func TestApplyFailureBlocksDependents(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.plannedGraph(7, wideGraph(headSHA))
	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	for _, d := range e.gh.Dispatches() {
		e.reportAll(d, nil)
	}
	wave1 := e.dispatchesFrom(2)
	require.Len(t, wave1, 1)
	assert.Equal(t, []string{"stacks/prod/cache", eks}, entryKeys(entries(t, wave1[0])))
	e.reportAll(wave1[0], map[string]bool{eks: false})

	assert.Len(t, e.gh.Dispatches(), 3, "wave 2 is never dispatched")
	r := e.run(apply.ID)
	assert.Equal(t, v1.RunFailed, r.Status)
	assert.Equal(t, map[string]v1.StackStatus{
		vpc: v1.StackApplied, staging: v1.StackApplied, "stacks/prod/cache": v1.StackApplied,
		eks: v1.StackFailed, apps: v1.StackBlocked, "stacks/prod/jobs": v1.StackBlocked,
	}, stackStatuses(r))
	for _, rs := range r.Stacks {
		if rs.Status == v1.StackBlocked {
			assert.Equal(t, []string{eks}, rs.BlockedBy)
		}
	}
	assert.Len(t, e.locks(), 6, "a failed before_merge apply keeps its locks")
	body := e.lastComment(7)
	assert.Contains(t, body, "**Apply failed** in wave 1")
	assert.Contains(t, body, "`stacks/prod/eks` (failed)")
	assert.Contains(t, body, "Blocked dependents: `stacks/prod/apps`, `stacks/prod/jobs`")
	assert.Contains(t, body, "stay held")
	assert.Equal(t, gh.ConclusionFailure, e.check(report.CheckApply).Conclusion)
	blocked := e.check(report.StackCheckName(report.CheckApply, apps))
	assert.Equal(t, gh.ConclusionFailure, blocked.Conclusion)
	assert.Equal(t, "Blocked by stacks/prod/eks", blocked.Output.Title)

	_, err := e.svc.RecordResult(e.ctx, e.dispatchJob(wave1[0].RunID, "production"), apply.ID, eks, applyResult(false))
	require.NoError(t, err, "a duplicate failure is accepted")
	assert.Len(t, e.gh.Dispatches(), 3)
}

func TestMergeIntoAnotherBranchIsNotADefaultBranchMerge(t *testing.T) {
	for _, mode := range []v1.ApplyMode{v1.ApplyOnMerge, v1.ApplyBeforeMerge} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := baseConfig()
			cfg.Apply.Mode = mode
			e := newEnv(t, cfg, withQueue())
			g := testGraph(mainSHA)
			defaultID, _, err := e.st.SaveGraph(e.ctx, repoID, &g)
			require.NoError(t, err)
			require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, defaultID))
			e.planned(7, headSHA)
			merged := e.gh.PullRequestEvent("closed", repoName, gh.PullRequest{
				Number: 7, State: gh.IssueClosed, Merged: true, MergeCommitSHA: mergeSHA, HeadSHA: headSHA, BaseSHA: baseSHA,
				BaseRef: "scratch", User: gh.User{Login: author},
			})
			require.NoError(t, e.svc.HandlePullRequest(e.ctx, merged))

			applyRuns, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: repoID, PRNumber: 7, Mode: v1.ModeApply})
			require.NoError(t, err)
			assert.Empty(t, applyRuns, "a merge into another branch applies nothing")
			assert.Empty(t, e.gh.Dispatches())
			_, id, err := e.st.GetDefaultGraph(e.ctx, repoID)
			require.NoError(t, err)
			assert.Equal(t, defaultID, id, "a merge into another branch leaves the default-branch graph alone")
		})
	}
}

func TestMergeIntoARenamedDefaultBranchApplies(t *testing.T) {
	cfg := baseConfig()
	cfg.Apply.Mode = v1.ApplyOnMerge
	e := newEnv(t, cfg, withQueue())
	e.planned(7, headSHA)
	e.gh.SetRef(repoName, "heads/trunk", mainSHA)
	merged := e.gh.PullRequestEvent("closed", repoName, gh.PullRequest{
		Number: 7, State: gh.IssueClosed, Merged: true, MergeCommitSHA: mergeSHA, HeadSHA: headSHA, BaseSHA: baseSHA,
		BaseRef: "trunk", User: gh.User{Login: author},
	})
	merged.Repository.DefaultBranch = "trunk"
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, merged))

	applyRuns, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: repoID, PRNumber: 7, Mode: v1.ModeApply})
	require.NoError(t, err)
	require.Len(t, applyRuns, 1, "the event names the default branch even before a push to it was seen")
	repo, err := e.st.GetRepo(e.ctx, repoID)
	require.NoError(t, err)
	assert.Equal(t, "trunk", repo.DefaultBranch)
	require.NotEmpty(t, e.gh.Dispatches())
	assert.Equal(t, "trunk", e.gh.Dispatches()[0].Ref)
}

func TestApplyOnMerge(t *testing.T) {
	cfg := baseConfig()
	cfg.Apply.Mode = v1.ApplyOnMerge
	e := newEnv(t, cfg, withQueue())
	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	assert.Contains(t, e.lastComment(7), "applies on merge", "merge protection is the approval gate of on_merge, so a comment cannot apply first")
	assert.Empty(t, e.gh.Dispatches())
	assert.Empty(t, e.locks())
	merged := e.gh.PullRequestEvent("closed", repoName, gh.PullRequest{
		Number: 7, State: gh.IssueClosed, Merged: true, MergeCommitSHA: mergeSHA, HeadSHA: headSHA, BaseSHA: baseSHA, User: gh.User{Login: author},
	})
	merged.Sender = gh.User{Login: applier}
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, merged))
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, merged))

	applyRuns, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: repoID, PRNumber: 7, Mode: v1.ModeApply})
	require.NoError(t, err)
	require.Len(t, applyRuns, 1, "a redelivered merge starts no second apply")
	apply := applyRuns[0]
	assert.Equal(t, mergeSHA, apply.SHA)
	assert.Equal(t, v1.TriggerPullRequest, apply.Trigger)
	assert.Equal(t, applier, apply.RequestedBy)
	assert.Len(t, e.locks(), 4)

	for wave := 0; wave < 3; wave++ {
		ds := e.gh.Dispatches()
		for _, d := range ds {
			if d.Inputs["wave"] != itoa(wave) {
				continue
			}
			assert.Equal(t, mergeSHA, d.Inputs["sha"])
			for _, en := range entries(t, d) {
				assert.Equal(t, mergeSHA, en.SHA)
				assert.Equal(t, v1.PlanArtifactName(en.Key, headSHA), en.Artifact, "the plan of the head commit is applied")
			}
			e.reportAll(d, nil)
		}
		jobs := e.queue.take()
		if wave < 2 {
			require.Len(t, jobs, 1)
			assert.Equal(t, runs.JobDispatchWave, jobs[0].Kind)
			assert.Equal(t, "wave:"+apply.ID.String()+":"+itoa(wave+1), jobs[0].Dedupe)
			var j runs.DispatchWaveJob
			require.NoError(t, json.Unmarshal(jobs[0].Payload, &j))
			assert.Equal(t, runs.DispatchWaveJob{RunID: apply.ID.String(), Wave: wave + 1}, j)
			require.NoError(t, e.svc.HandleJob(e.ctx, jobs[0].Kind, jobs[0].Payload))
			require.NoError(t, e.svc.HandleJob(e.ctx, jobs[0].Kind, jobs[0].Payload), "a repeated wave job dispatches nothing")
		} else {
			assert.Empty(t, jobs)
		}
	}
	assert.Len(t, e.gh.Dispatches(), 4)
	assert.Equal(t, v1.RunApplied, e.run(apply.ID.String()).Status)
	assert.Empty(t, e.locks(), "an on_merge apply releases its locks when it completes")
}

func TestClosedUnmergedKeepsLocks(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	require.Len(t, e.locks(), 4)
	closed := e.gh.PullRequestEvent("closed", repoName, gh.PullRequest{Number: 7, State: gh.IssueClosed, HeadSHA: headSHA, User: gh.User{Login: author}})
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, closed))
	body := e.lastComment(7)
	assert.Contains(t, body, "**Orchestration locks kept.**")
	assert.Contains(t, body, "4 stacks stay locked")
	n := len(e.comments(7))
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, closed))
	assert.Len(t, e.comments(7), n, "the warning is posted once")
	assert.Len(t, e.locks(), 4)

	require.NoError(t, e.svc.RemindStaleLocks(e.ctx))
	assert.Len(t, e.comments(7), n, "fresh locks need no reminder")
	e.clock.Advance(25 * time.Hour)
	require.NoError(t, e.svc.RemindStaleLocks(e.ctx))
	require.Len(t, e.comments(7), n+1, "a lock older than a day on a closed pull request is reminded")
	assert.Contains(t, e.lastComment(7), "**Orchestration locks kept.**")
	require.NoError(t, e.svc.RemindStaleLocks(e.ctx))
	assert.Len(t, e.comments(7), n+1, "once a day")
	assert.Len(t, e.audit("lock_reminder"), 4, "one audit row per lock and day")
	e.clock.Advance(25 * time.Hour)
	require.NoError(t, e.svc.RemindStaleLocks(e.ctx))
	assert.Len(t, e.comments(7), n+2, "and again the next day")
}

func TestManualRunLocks(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.planned(7, headSHA)
	req := v1.CreateRunRequest{Repo: repoName, SHA: mainSHA, Mode: v1.ModeApply, Trigger: v1.TriggerManual, Stacks: []string{"./" + vpc + "/"}}
	first, err := e.svc.CreateRun(e.ctx, apiKey(), req)
	require.NoError(t, err)
	assert.Equal(t, v1.RunApplying, first.Status)
	assert.Equal(t, v1.TriggerManual, first.Run.Trigger)
	assert.Equal(t, "apikey:ci", first.Run.RequestedBy)
	require.Len(t, first.Run.Stacks, 1)
	assert.Equal(t, v1.StackPlanned, first.Run.Stacks[0].Status)
	assert.Equal(t, map[string]int{vpc: 0}, e.locks())

	_, err = e.svc.CreateRun(e.ctx, apiKey(), req)
	var locked *principal.LockedError
	require.ErrorAs(t, err, &locked)
	require.ErrorIs(t, err, principal.ErrLocked)
	require.Len(t, locked.Conflicts, 1)
	assert.Equal(t, vpc, locked.Conflicts[0].StackKey)
	assert.Equal(t, first.RunID, locked.Conflicts[0].RunID)
	manual, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: repoID, Mode: v1.ModeApply})
	require.NoError(t, err)
	assert.Len(t, manual, 1, "a refused manual run leaves nothing behind")

	e.comment(7, applier, "stackorder apply")
	assert.Contains(t, e.lastComment(7), "locked by a manual run", "a pull request cannot apply over a manual lock")

	_, err = e.svc.RecordResult(e.ctx, e.planJob(7).p, first.RunID, vpc, applyResult(true))
	require.ErrorIs(t, err, principal.ErrForbidden)
	row, err := e.svc.RecordResult(e.ctx, apiKey(), first.RunID, vpc, applyResult(true))
	require.NoError(t, err)
	assert.Equal(t, v1.StackApplied, row.Status)
	assert.Equal(t, v1.RunApplied, e.run(first.RunID).Status)
	assert.Empty(t, e.locks(), "the result releases the manual run's locks")

	_, err = e.svc.CreateRun(e.ctx, apiKey(), req)
	require.NoError(t, err)
	_, err = e.svc.CreateRun(e.ctx, apiKey(), v1.CreateRunRequest{Repo: repoName, SHA: mainSHA, Mode: v1.ModeApply, Trigger: v1.TriggerManual, Stacks: []string{"stacks/none"}})
	require.ErrorIs(t, err, principal.ErrInvalid)
}

func TestDeploymentProtectionRule(t *testing.T) {
	cfg := baseConfig()
	cfg.Apply.AllowedTeams = []string{"platform"}
	e := newEnv(t, cfg)
	e.gh.SetTeamMembership("acme", "platform", applier, gh.MembershipActive)
	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	ds := e.gh.Dispatches()
	require.Len(t, ds, 2)
	var prod, stage int64
	for _, d := range ds {
		if entries(t, d)[0].Environment == "production" {
			prod = d.RunID
		} else {
			stage = d.RunID
		}
	}
	e.gh.SetJobs(repoName, prod, nil)
	e.gh.SetJobs(repoName, stage, nil)

	require.NoError(t, e.svc.HandleDeploymentProtectionRule(e.ctx, e.gh.DeploymentProtectionRuleEvent(repoName, prod, "production", mainSHA)))
	bound, err := e.st.FindDispatchByWorkflowRun(e.ctx, prod)
	require.NoError(t, err, "the environment of the rule tells two dispatches with one display title apart")
	assert.Equal(t, "production", bound.Environment)
	assert.Equal(t, apply.ID, bound.RunID.String())
	decisions := e.gh.ProtectionRuleDecisions()
	require.Len(t, decisions, 1)
	assert.Equal(t, gh.DeploymentApproved, decisions[0].State)
	assert.Equal(t, "production", decisions[0].EnvironmentName)
	assert.Contains(t, decisions[0].Comment, "layer 1")
	assert.Contains(t, decisions[0].Comment, "layer 2")
	assert.Contains(t, decisions[0].Comment, vpc)

	e.gh.SetTeamMembership("acme", "platform", applier, gh.MembershipNone)
	e.clock.Advance(61 * time.Second)
	require.NoError(t, e.svc.HandleDeploymentProtectionRule(e.ctx, e.gh.DeploymentProtectionRuleEvent(repoName, stage, "staging", mainSHA)))
	decisions = e.gh.ProtectionRuleDecisions()
	require.Len(t, decisions, 2)
	assert.Equal(t, gh.DeploymentRejected, decisions[1].State)
	assert.Contains(t, decisions[1].Comment, "**Layer 1, authorization** (`stacks/staging/vpc`)")
	_, err = e.st.FindDispatchByWorkflowRun(e.ctx, stage)
	require.NoError(t, err, "an unbound dispatch is bound by its display title first")

	stranger := e.gh.AddWorkflowRun(repoName, gh.WorkflowRun{Path: ".github/workflows/stackorder-run.yml", Event: "workflow_dispatch", DisplayTitle: "manual"})
	require.NoError(t, e.svc.HandleDeploymentProtectionRule(e.ctx, e.gh.DeploymentProtectionRuleEvent(repoName, stranger.ID, "production", mainSHA)))
	decisions = e.gh.ProtectionRuleDecisions()
	require.Len(t, decisions, 3)
	assert.Equal(t, gh.DeploymentRejected, decisions[2].State)
	assert.Contains(t, decisions[2].Comment, "did not dispatch")
}

func TestStacklessFirstContactBindsOnlyTheDispatchItsJobsCarry(t *testing.T) {
	const dns = "stacks/prod/dns"
	setup := func(t *testing.T) (*env, string, ghfake.Dispatch, ghfake.Dispatch) {
		cfg := baseConfig()
		cfg.Apply.MaxParallel = 1
		e := newEnv(t, cfg)
		g := testGraph(headSHA)
		g.Stacks = append(g.Stacks, v1.Stack{Key: dns, Path: dns})
		e.openPull(7, headSHA)
		job, runID, resp := e.startPlanGraph(7, g, "modules/vpc/main.tf", dns+"/main.tf")
		for _, a := range resp.Affected {
			_, err := e.svc.RecordResult(e.ctx, job.p, runID, a.Key, planResult(a.Key, headSHA, 1))
			require.NoError(t, err)
		}
		e.comment(7, applier, "stackorder apply")
		apply := e.applyRun(7)
		var p0, p1 *ghfake.Dispatch
		for _, d := range e.gh.Dispatches() {
			switch entryKeys(entries(t, d))[0] {
			case dns:
				p0 = &d
			case vpc:
				p1 = &d
			}
		}
		require.NotNil(t, p0)
		require.NotNil(t, p1)
		return e, apply.ID, *p0, *p1
	}
	duplicate := func(e *env, applyID string, of ghfake.Dispatch) int64 {
		dup := e.gh.AddWorkflowRun(repoName, gh.WorkflowRun{Path: ".github/workflows/stackorder-run.yml", Event: "workflow_dispatch",
			DisplayTitle: "stackorder apply " + applyID + " wave 0"})
		e.gh.SetJobs(repoName, dup.ID, matrixJobs(of))
		return dup.ID
	}
	boundTo := func(e *env, workflowRunID int64) []string {
		d, err := e.st.FindDispatchByWorkflowRun(e.ctx, workflowRunID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		require.NoError(e.t, err)
		rows, err := e.st.GetRunStacks(e.ctx, d.RunID)
		require.NoError(e.t, err)
		var keys []string
		for _, rs := range rows {
			if rs.DispatchID != nil && *rs.DispatchID == d.ID {
				keys = append(keys, rs.Key)
			}
		}
		return keys
	}

	t.Run("the sibling chunk is still unbound", func(t *testing.T) {
		e, applyID, p0, p1 := setup(t)
		require.NoError(t, e.svc.HandleWorkflowRun(e.ctx, e.gh.WorkflowRunEvent("requested", repoName, gh.WorkflowRun{ID: p0.RunID})))
		require.Equal(t, []string{dns}, boundTo(e, p0.RunID))
		dup := duplicate(e, applyID, p0)
		_, err := e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(dup, "production"), applyID)
		require.ErrorIs(t, err, principal.ErrForbidden, "a duplicated dispatch's jobs are refused")
		assert.Nil(t, boundTo(e, dup))
		_, err = e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(p1.RunID, "production"), applyID)
		require.NoError(t, err, "the real sibling chunk keeps its dispatch")
		assert.Equal(t, []string{vpc}, boundTo(e, p1.RunID))
	})

	t.Run("both chunks are unbound", func(t *testing.T) {
		e, applyID, p0, p1 := setup(t)
		_, err := e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(p1.RunID, "production"), applyID)
		require.NoError(t, err)
		assert.Equal(t, []string{vpc}, boundTo(e, p1.RunID), "the jobs of the workflow run choose its dispatch")
		_, err = e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(p0.RunID, "production"), applyID)
		require.NoError(t, err)
		assert.Equal(t, []string{dns}, boundTo(e, p0.RunID))
		dup := duplicate(e, applyID, p0)
		_, err = e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(dup, "production"), applyID)
		require.ErrorIs(t, err, principal.ErrForbidden)
	})
}

func TestDeploymentProtectionRuleVetsOnlyTheStacksOfASubset(t *testing.T) {
	cfg := baseConfig()
	cfg.Apply.AllowedTeams = []string{"platform"}
	e := newEnv(t, cfg)
	e.gh.SetTeamMembership("acme", "platform", applier, gh.MembershipActive)
	g := testGraph(mainSHA)
	for i := range g.Stacks {
		if g.Stacks[i].Key == eks {
			g.Stacks[i].Config = &v1.StackConfig{Apply: &v1.StackApplyConfig{AllowedTeams: []string{"acme/platform-prod"}}}
		}
	}
	id, _, err := e.st.SaveGraph(e.ctx, repoID, &g)
	require.NoError(t, err)
	require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, id))
	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply "+vpc)
	ds := e.gh.Dispatches()
	require.Len(t, ds, 1)
	require.Equal(t, []string{vpc}, entryKeys(entries(t, ds[0])))
	e.gh.SetJobs(repoName, ds[0].RunID, nil)

	require.NoError(t, e.svc.HandleDeploymentProtectionRule(e.ctx, e.gh.DeploymentProtectionRuleEvent(repoName, ds[0].RunID, "production", mainSHA)))
	decisions := e.gh.ProtectionRuleDecisions()
	require.Len(t, decisions, 1)
	assert.Equal(t, gh.DeploymentApproved, decisions[0].State, decisions[0].Comment)
	assert.Contains(t, decisions[0].Comment, "production deployment of "+vpc+" for run", "skipped stacks are not named as deployed")
}

func TestDeploymentProtectionRuleRefusesFinishedWork(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.planned(7, headSHA)
	decide := func(runID int64, env string) ghfake.ProtectionRuleDecision {
		t.Helper()
		n := len(e.gh.ProtectionRuleDecisions())
		require.NoError(t, e.svc.HandleDeploymentProtectionRule(e.ctx, e.gh.DeploymentProtectionRuleEvent(repoName, runID, env, mainSHA)))
		ds := e.gh.ProtectionRuleDecisions()
		require.Len(t, ds, n+1)
		return ds[n]
	}

	e.comment(7, applier, "stackorder plan "+eks)
	plan := e.gh.Dispatches()[0]
	assert.Equal(t, gh.DeploymentApproved, decide(plan.RunID, v1.DefaultEnvironment).State)
	assert.Equal(t, gh.DeploymentRejected, decide(plan.RunID, "production").State,
		"a plan dispatch deploys to the default environment only, even once it is bound")
	_, err := e.svc.RecordResult(e.ctx, e.dispatchJob(plan.RunID, v1.DefaultEnvironment), plan.Inputs["run_id"], eks, planResult(eks, headSHA, 1))
	require.NoError(t, err)

	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	ds := e.dispatchesFrom(1)
	require.Len(t, ds, 2)
	prod, stage := ds[0], ds[1]
	if entries(t, prod)[0].Environment != "production" {
		prod, stage = stage, prod
	}
	e.reportAll(stage, nil)
	e.gh.CompleteWorkflowRun(repoName, stage.RunID, gh.ConclusionSuccess)
	require.NoError(t, e.svc.HandleWorkflowRun(e.ctx, e.gh.WorkflowRunEvent("completed", repoName, gh.WorkflowRun{ID: stage.RunID})))
	rerun := decide(stage.RunID, "staging")
	assert.Equal(t, gh.DeploymentRejected, rerun.State, "a re-run of a completed dispatch is not vouched for")
	assert.Contains(t, rerun.Comment, "already completed")

	_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(prod.RunID, "production"), apply.ID, vpc, applyResult(false))
	require.NoError(t, err)
	require.Equal(t, v1.RunFailed, e.run(apply.ID).Status)
	late := decide(prod.RunID, "production")
	assert.Equal(t, gh.DeploymentRejected, late.State, "a finished run deploys nothing more")
	assert.Contains(t, late.Comment, "already failed")
}

func TestUnlock(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetCollaboratorPermission(repoName, "reader", "read")
	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	require.Len(t, e.locks(), 4)

	_, err := e.svc.UnlockByKey(e.ctx, "reader", v1.UnlockRequest{Repo: repoName, StackKey: vpc})
	require.ErrorIs(t, err, principal.ErrForbidden)
	_, err = e.svc.UnlockByKey(e.ctx, applier, v1.UnlockRequest{Repo: repoName})
	require.ErrorIs(t, err, principal.ErrInvalid)

	resp, err := e.svc.UnlockByKey(e.ctx, applier, v1.UnlockRequest{Repo: repoName, StackKey: vpc, Reason: "runner died", ForceState: true})
	require.NoError(t, err)
	require.Len(t, resp.Released, 1)
	assert.Equal(t, vpc, resp.Released[0].StackKey)
	assert.Equal(t, 7, resp.Released[0].PRNumber)
	rows := e.audit("unlock")
	require.Len(t, rows, 1)
	assert.Equal(t, applier, rows[0].Actor)
	assert.Equal(t, true, rows[0].Details["force_state"])
	assert.Equal(t, "runner died", rows[0].Details["reason"])
	body := e.lastComment(7)
	assert.Contains(t, body, "Released 1 orchestration lock at the request of @octocat")
	assert.Contains(t, body, "--force-state")

	again, err := e.svc.UnlockByKey(e.ctx, applier, v1.UnlockRequest{Repo: repoName, StackKey: vpc})
	require.NoError(t, err)
	assert.Empty(t, again.Released, "unlocking twice releases nothing")

	st, err := e.st.GetStackByKey(e.ctx, repoID, eks)
	require.NoError(t, err)
	byID, err := e.svc.Unlock(e.ctx, "apikey:ops", st.ID.String(), v1.UnlockRequest{})
	require.NoError(t, err)
	require.Len(t, byID.Released, 1)

	e.comment(7, applier, "stackorder unlock")
	assert.Contains(t, e.lastComment(7), "Released 2 orchestration locks")
	assert.Empty(t, e.locks())
	assert.Len(t, e.audit("unlock"), 4)
}

func TestHumanAuditRowsNameTheirRepository(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetCollaboratorPermission(repoName, "reader", "read")
	planRun := e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	e.comment(7, "reader", "stackorder help")
	_, err := e.svc.UnlockByKey(e.ctx, applier, v1.UnlockRequest{Repo: repoName, StackKey: vpc, Reason: "runner died"})
	require.NoError(t, err)
	st, err := e.st.GetStackByKey(e.ctx, repoID, eks)
	require.NoError(t, err)
	_, err = e.svc.Unlock(e.ctx, "apikey:ops", st.ID.String(), v1.UnlockRequest{})
	require.NoError(t, err)
	e.comment(7, applier, "stackorder unlock")
	_, err = e.svc.Rerun(e.ctx, applier, planRun)
	require.NoError(t, err)
	for range 11 {
		e.comment(8, applier, "stackorder help")
	}

	rows, _, err := e.st.ListAudit(e.ctx, store.AuditFilter{Limit: 500})
	require.NoError(t, err)
	seen := map[string]int{}
	for _, r := range rows {
		switch r.Action {
		case "unlock":
			key, _ := r.Details["stack"].(string)
			assert.Equal(t, v1.QualifiedStackKey(repoName, key), r.Target, "an unlock names the stack as owner/repo//key")
		case "rerun", "command", "command_ignored", "command_rate_limited":
			assert.Equal(t, repoName, r.Target, "%s names the repository", r.Action)
			assert.NotNil(t, r.Details["pr"], "%s keeps its pull request", r.Action)
		default:
			continue
		}
		assert.Equal(t, repoName, r.Details["repo"], "%s carries details.repo", r.Action)
		seen[r.Action]++
	}
	assert.Equal(t, map[string]int{"unlock": 4, "rerun": 1, "command": 13, "command_ignored": 1, "command_rate_limited": 1}, seen)

	help := e.comment(7, applier, "stackorder help")
	assert.Equal(t, []string{gh.ReactionEyes}, e.gh.Reactions(help.ID),
		"the rate limit of another pull request of the same repository leaves this one alone")
}

func TestCrossRepoPlanIsRetriedAfterAFailedFirstAttempt(t *testing.T) {
	e := newEnv(t, baseConfig())
	const downstream = "acme/apps"
	e.gh.SetRepo(downstream, gh.Repository{ID: 300, DefaultBranch: "main"})
	e.gh.AddInstallation(instID, "acme", downstream)
	e.gh.SetRef(downstream, "heads/main", baseSHA)
	down, err := e.st.UpsertRepo(e.ctx, store.RepoParams{ID: 300, InstallationID: instID, FullName: downstream, DefaultBranch: "main"})
	require.NoError(t, err)
	_, _, err = e.st.SaveGraph(e.ctx, down.ID, &v1.Graph{Repo: downstream, SHA: baseSHA, Stacks: []v1.Stack{{Key: "stacks/api", Path: "stacks/api"}}})
	require.NoError(t, err)
	job := runs.CrossRepoPlanJob{Repo: downstream, StackKeys: []string{"stacks/api"}, UpstreamRunID: uuid.NewString()}
	downstreamRuns := func() int {
		rs, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: down.ID, Mode: v1.ModePlan})
		require.NoError(t, err)
		return len(rs)
	}

	e.gh.FailNext("GET /repos/{owner}/{repo}/contents/{path...}", http.StatusServiceUnavailable, 5)
	require.Error(t, e.svc.RunCrossRepoPlan(e.ctx, job))
	assert.Zero(t, downstreamRuns())

	require.NoError(t, e.svc.RunCrossRepoPlan(e.ctx, job))
	assert.Equal(t, 1, downstreamRuns(), "the retry plans what the failed attempt did not")
	require.NoError(t, e.svc.RunCrossRepoPlan(e.ctx, job))
	assert.Equal(t, 1, downstreamRuns(), "the plan is still started once per upstream run")
}

func TestCrossRepoPlanFollowsTheStacksAFailedApplyApplied(t *testing.T) {
	cfg := baseConfig()
	cfg.Propagate.CrossRepo = v1.CrossRepoPlan
	e := newEnv(t, cfg, withQueue())
	const downstream = "acme/apps"
	e.gh.SetRepo(downstream, gh.Repository{ID: 300, DefaultBranch: "main"})
	e.gh.AddInstallation(instID, "acme", downstream)
	down, err := e.st.UpsertRepo(e.ctx, store.RepoParams{ID: 300, InstallationID: instID, FullName: downstream, DefaultBranch: "main"})
	require.NoError(t, err)
	_, _, err = e.st.SaveGraph(e.ctx, down.ID, &v1.Graph{
		Repo: downstream, SHA: baseSHA,
		Stacks: []v1.Stack{
			{Key: "stacks/api", Path: "stacks/api"},
			{Key: "acme/infra//" + vpc, Path: vpc, Repo: repoName, External: true},
		},
		Edges: []v1.Edge{{From: v1.StackRef("stacks/api"), To: v1.StackRef("acme/infra//" + vpc), Type: v1.EdgeDependsOn}},
	})
	require.NoError(t, err)

	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	var kept []queuedJob
	reported := map[int64]bool{}
	for progressed := true; progressed; {
		progressed = false
		for _, d := range e.gh.Dispatches() {
			if !reported[d.RunID] {
				reported[d.RunID] = true
				e.reportAll(d, map[string]bool{eks: false})
				progressed = true
			}
		}
		for _, j := range e.queue.take() {
			if j.Kind != runs.JobDispatchWave {
				kept = append(kept, j)
				continue
			}
			require.NoError(t, e.svc.HandleJob(e.ctx, j.Kind, j.Payload))
			progressed = true
		}
	}
	apply := e.applyRun(7)
	require.Equal(t, v1.RunFailed, apply.Status)
	require.Equal(t, v1.StackApplied, stackStatuses(apply)[vpc])
	require.Len(t, kept, 1, "vpc applied, so its downstream dependents are planned")
	assert.Equal(t, runs.JobCrossRepoPlan, kept[0].Kind)
	var cj runs.CrossRepoPlanJob
	require.NoError(t, json.Unmarshal(kept[0].Payload, &cj))
	assert.Equal(t, runs.CrossRepoPlanJob{Repo: downstream, StackKeys: []string{"stacks/api"}, UpstreamRunID: apply.ID}, cj)
}

func TestCrossRepoPlanJob(t *testing.T) {
	cfg := baseConfig()
	cfg.Propagate.CrossRepo = v1.CrossRepoPlan
	e := newEnv(t, cfg, withQueue())
	const downstream = "acme/apps"
	e.gh.SetRepo(downstream, gh.Repository{ID: 300, DefaultBranch: "main"})
	e.gh.AddInstallation(instID, "acme", downstream)
	e.gh.SetRef(downstream, "heads/main", baseSHA)
	down, err := e.st.UpsertRepo(e.ctx, store.RepoParams{ID: 300, InstallationID: instID, FullName: downstream, DefaultBranch: "main"})
	require.NoError(t, err)
	_, _, err = e.st.SaveGraph(e.ctx, down.ID, &v1.Graph{
		Repo: downstream, SHA: baseSHA,
		Stacks: []v1.Stack{
			{Key: "stacks/api", Path: "stacks/api"},
			{Key: "stacks/web", Path: "stacks/web"},
			{Key: "acme/infra//" + vpc, Path: vpc, Repo: repoName, External: true},
		},
		Edges: []v1.Edge{{From: v1.StackRef("stacks/api"), To: v1.StackRef("acme/infra//" + vpc), Type: v1.EdgeDependsOn}},
	})
	require.NoError(t, err)

	e.openPull(7, headSHA)
	job, runID, resp := e.startPlan(7, headSHA)
	assert.Equal(t, []string{"acme/apps//stacks/api"}, resp.External)
	assert.Contains(t, e.run(runID).Warnings, "external dependent acme/apps//stacks/api is not scheduled by this run")
	for _, a := range resp.Affected {
		_, err := e.svc.RecordResult(e.ctx, job.p, runID, a.Key, planResult(a.Key, headSHA, 1))
		require.NoError(t, err)
	}
	e.comment(7, applier, "stackorder apply")
	var kept []queuedJob
	reported := map[int64]bool{}
	for progressed := true; progressed; {
		progressed = false
		for _, d := range e.gh.Dispatches() {
			if d.Repo == repoName && !reported[d.RunID] {
				reported[d.RunID] = true
				e.reportAll(d, nil)
				progressed = true
			}
		}
		for _, j := range e.queue.take() {
			if j.Kind != runs.JobDispatchWave {
				kept = append(kept, j)
				continue
			}
			require.NoError(t, e.svc.HandleJob(e.ctx, j.Kind, j.Payload))
			progressed = true
		}
	}
	assert.Equal(t, v1.RunApplied, e.applyRun(7).Status)
	jobs := kept
	require.Len(t, jobs, 1)
	assert.Equal(t, runs.JobCrossRepoPlan, jobs[0].Kind)
	var cj runs.CrossRepoPlanJob
	require.NoError(t, json.Unmarshal(jobs[0].Payload, &cj))
	assert.Equal(t, runs.CrossRepoPlanJob{Repo: downstream, StackKeys: []string{"stacks/api"}, UpstreamRunID: e.applyRun(7).ID}, cj)

	require.NoError(t, e.svc.HandleJob(e.ctx, jobs[0].Kind, jobs[0].Payload))
	require.NoError(t, e.svc.HandleJob(e.ctx, jobs[0].Kind, jobs[0].Payload), "the plan is dispatched once per upstream run")
	var planned []string
	for _, d := range e.gh.Dispatches() {
		if d.Repo == downstream {
			assert.Equal(t, "plan", d.Inputs["mode"])
			assert.Equal(t, baseSHA, d.Inputs["sha"])
			planned = append(planned, entryKeys(entries(t, d))...)
		}
	}
	assert.Equal(t, []string{"stacks/api"}, planned)
	downChecks := e.gh.CheckRuns(downstream)
	names := make([]string, 0, len(downChecks))
	for _, c := range downChecks {
		names = append(names, c.Name)
	}
	assert.Contains(t, names, report.StackCheckName(report.CheckPlan, "stacks/api"))
	downRuns, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: down.ID})
	require.NoError(t, err)
	require.Len(t, downRuns, 1)
	assert.Equal(t, v1.TriggerPush, downRuns[0].Trigger)
	assert.True(t, strings.Contains(strings.Join(downRuns[0].Warnings, " "), e.applyRun(7).ID))
}

func itoa(n int) string { return strconv.Itoa(n) }
