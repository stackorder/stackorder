//go:build integration

package runs_test

import (
	"encoding/json"
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
		name  string
		cfg   func(*v1.RepoConfig)
		setup func(e *env)
		by    string
		want  []string
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
			setup: func(e *env) {
				runs, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: repoID, PRNumber: 7, Mode: v1.ModePlan})
				require.NoError(e.t, err)
				_, err = e.svc.RecordCheck(e.ctx, e.planJob(7).p, runs[0].ID.String(), vpc, "policy",
					v1.CheckVerdict{Status: v1.CheckFail, Summary: "public bucket"})
				require.NoError(e.t, err)
				c := e.check(report.PolicyCheckName("policy", vpc))
				assert.Equal(e.t, gh.ConclusionFailure, c.Conclusion)
				_, err = e.svc.RecordCheck(e.ctx, e.planJob(7).p, runs[0].ID.String(), eks, "cost", v1.CheckVerdict{Status: v1.CheckWarn})
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
			e.planned(7, headSHA)
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
