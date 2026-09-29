//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/cli"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
	"github.com/stackorder/stackorder/internal/tf"
)

const (
	commandTimeout = 5 * time.Minute
	runTimeout     = 5 * time.Minute
	planWorkflow   = ".github/workflows/stackorder-plan.yml"
	runWorkflow    = "stackorder-run.yml"
	planDir        = ".stackorder/plans"
	backendConfig  = "use_path_style=true,skip_credentials_validation=true,skip_requesting_account_id=true,skip_metadata_api_check=true"
)

var scrubbedPrefixes = []string{"GITHUB_", "ACTIONS_", "RUNNER_", "STACKORDER_", "AWS_", "TF_", "GIT_"}

var keptVariables = []string{tf.EnvTerraformBin, tf.EnvTofuBin}

func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		scrub := slices.ContainsFunc(scrubbedPrefixes, func(p string) bool { return strings.HasPrefix(name, p) })
		if !scrub || slices.Contains(keptVariables, name) {
			out = append(out, kv)
		}
	}
	return out
}

type cliResult struct {
	args    []string
	code    int
	stdout  string
	stderr  string
	outputs map[string]string
}

func (r cliResult) String() string {
	return fmt.Sprintf("stackorder %s exited %d\nstdout:\n%s\nstderr:\n%s", strings.Join(r.args, " "), r.code, tail(r.stdout), tail(r.stderr))
}

func tail(s string) string {
	const limit = 16 * 1024
	if len(s) <= limit {
		return s
	}
	return "[...]\n" + s[len(s)-limit:]
}

type runner struct {
	cli  string
	ls   *localStack
	cp   *controlPlane
	repo *gitRepo

	store     string
	mu        sync.Mutex
	artifacts map[string]string
	handled   map[int64]bool
}

func (rn *runner) baseEnv() map[string]string {
	return map[string]string{
		"AWS_ACCESS_KEY_ID":         awsAccessKey,
		"AWS_SECRET_ACCESS_KEY":     awsSecretKey,
		"AWS_REGION":                awsRegion,
		"AWS_DEFAULT_REGION":        awsRegion,
		"AWS_ENDPOINT_URL":          rn.ls.endpoint,
		"AWS_ENDPOINT_URL_S3":       rn.ls.s3Endpoint,
		"AWS_ENDPOINT_URL_STS":      rn.ls.endpoint,
		"AWS_EC2_METADATA_DISABLED": "true",
		cli.EnvServerURL:            rn.cp.baseURL,
		cli.EnvBackendConfig:        backendConfig,
	}
}

func (rn *runner) exec(t *testing.T, dir string, env map[string]string, args ...string) cliResult {
	t.Helper()
	tmp := t.TempDir()
	outputs := filepath.Join(tmp, "github_output")
	full := rn.baseEnv()
	full["GITHUB_OUTPUT"] = outputs
	full["GITHUB_STEP_SUMMARY"] = filepath.Join(tmp, "step_summary.md")
	full["RUNNER_TEMP"] = tmp
	for k, v := range env {
		full[k] = v
	}
	rn.cp.syncClock()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, rn.cli, args...)
	cmd.Dir = dir
	cmd.Env = cleanEnv()
	for k, v := range full {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := cliResult{args: args, stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		res.code = exitErr.ExitCode()
	case err != nil:
		t.Errorf("e2e: run stackorder %s: %v", strings.Join(args, " "), err)
		res.code = -1
	}
	res.outputs = readOutputs(t, outputs)
	return res
}

func readOutputs(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out
	}
	if err != nil {
		t.Errorf("e2e: read the job outputs: %v", err)
		return out
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if name, delim, ok := strings.Cut(line, "<<"); ok && !strings.Contains(name, "=") {
			var value []string
			for sc.Scan() && sc.Text() != delim {
				value = append(value, sc.Text())
			}
			out[name] = strings.Join(value, "\n")
			continue
		}
		if name, value, ok := strings.Cut(line, "="); ok {
			out[name] = value
		}
	}
	if err := sc.Err(); err != nil {
		t.Errorf("e2e: parse the job outputs: %v", err)
	}
	return out
}

func writeEvent(t *testing.T, payload any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func (rn *runner) jobEnv(t *testing.T, claims oidc.Claims, workspace string, event any) map[string]string {
	t.Helper()
	ts := rn.cp.oidc.TokenRequestServer(t)
	ts.SetClaims(claims)
	env := ts.Env()
	env["GITHUB_WORKSPACE"] = workspace
	env["GITHUB_EVENT_PATH"] = writeEvent(t, event)
	env["GITHUB_API_URL"] = rn.cp.gh.URL()
	env["GITHUB_SERVER_URL"] = "https://github.com"
	return env
}

type pullRequest struct {
	number   int
	branch   string
	base     string
	head     string
	merge    string
	pull     gh.PullRequest
	workflow gh.WorkflowRun
	runID    string
	matrix   v1.Matrix
	waves    [][]string
	affected []string
	plans    map[string]cliResult
}

func (rn *runner) openPull(t *testing.T, number int, branch, base, head, title string) *pullRequest {
	t.Helper()
	pr := &pullRequest{number: number, branch: branch, base: base, head: head, pull: gh.PullRequest{
		Number: number, Title: title, State: gh.IssueOpen, HeadSHA: head, HeadRef: branch,
		BaseSHA: base, BaseRef: "main", User: gh.User{Login: author},
		Mergeable: new(true), MergeableState: "clean",
	}}
	pr.merge = rn.repo.mergeCommit(t, base, head, number)
	rn.cp.gh.SetFiles(repoName, number, rn.repo.changedPaths(t, base, head))
	rn.cp.deliver(t, gh.EventPullRequest, rn.cp.gh.PullRequestEvent("opened", repoName, pr.pull))
	pr.workflow = rn.cp.gh.AddWorkflowRun(repoName, gh.WorkflowRun{
		Name: "stackorder plan", Path: planWorkflow, Event: "pull_request", Status: gh.RunStatusInProgress,
		HeadSHA: head, HeadBranch: branch,
	})
	return pr
}

func (rn *runner) pullEvent(pr *pullRequest) *gh.PullRequestEvent {
	return rn.cp.gh.PullRequestEvent("opened", repoName, pr.pull)
}

func (rn *runner) planClaims(pr *pullRequest) oidc.Claims {
	c := rn.cp.oidc.PlanClaims(repoName, itoa(repoID), pr.number, pr.merge)
	c.RunID = itoa(pr.workflow.ID)
	c.Actor = pr.pull.User.Login
	return c
}

func (rn *runner) resolve(t *testing.T, pr *pullRequest) cliResult {
	t.Helper()
	ws := rn.repo.checkout(t, pr.merge)
	env := rn.jobEnv(t, rn.planClaims(pr), ws, rn.pullEvent(pr))
	res := rn.exec(t, ws, env, "resolve", "--server", rn.cp.baseURL)
	require.Equal(t, 0, res.code, "%s", res)
	pr.runID = res.outputs["run-id"]
	require.NotEmpty(t, pr.runID, "resolve reports the run id")
	require.Equal(t, "false", res.outputs["unconfirmed"], "the server confirmed the resolution")
	require.NoError(t, json.Unmarshal([]byte(res.outputs["matrix"]), &pr.matrix))
	require.NoError(t, json.Unmarshal([]byte(res.outputs["waves"]), &pr.waves))
	require.NoError(t, json.Unmarshal([]byte(res.outputs["affected"]), &pr.affected))
	return res
}

func (rn *runner) plan(t *testing.T, pr *pullRequest) map[string]cliResult {
	t.Helper()
	event := rn.pullEvent(pr)
	results := make([]cliResult, len(pr.matrix.Include))
	var wg sync.WaitGroup
	for i, entry := range pr.matrix.Include {
		ws := rn.repo.checkout(t, entry.SHA)
		env := rn.jobEnv(t, rn.planClaims(pr), ws, event)
		env[cli.EnvRunID] = pr.runID
		env[cli.EnvTool] = string(entry.Tool)
		env[cli.EnvPlanDir] = filepath.Join(ws, planDir)
		wg.Go(func() {
			results[i] = rn.exec(t, ws, env, "plan", "--stack", entry.Key, "--run-id", pr.runID, "--server", rn.cp.baseURL)
		})
	}
	wg.Wait()
	pr.plans = map[string]cliResult{}
	for i, entry := range pr.matrix.Include {
		res := results[i]
		require.Equal(t, 0, res.code, "plan of %s: %s", entry.Key, res)
		rn.upload(t, pr.workflow.ID, res.outputs["artifact"], res.outputs["plan-file"])
		pr.plans[entry.Key] = res
	}
	rn.cp.gh.CompleteWorkflowRun(repoName, pr.workflow.ID, gh.ConclusionSuccess)
	return pr.plans
}

func artifactKey(runID int64, name string) string { return fmt.Sprintf("%d/%s", runID, name) }

func (rn *runner) upload(t *testing.T, runID int64, name, planFile string) {
	t.Helper()
	require.NotEmpty(t, name, "the plan step names its artifact")
	require.FileExists(t, planFile, "the plan step wrote the plan file it reports")
	data, err := os.ReadFile(planFile)
	require.NoError(t, err)
	dst := filepath.Join(rn.store, itoa(runID), name, filepath.Base(planFile))
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o750))
	require.NoError(t, os.WriteFile(dst, data, 0o600))
	rn.mu.Lock()
	defer rn.mu.Unlock()
	rn.artifacts[artifactKey(runID, name)] = dst
}

func (rn *runner) download(t *testing.T, entry v1.MatrixEntry, workspace string) string {
	t.Helper()
	rn.mu.Lock()
	src, ok := rn.artifacts[artifactKey(entry.PlanRunID, entry.Artifact)]
	rn.mu.Unlock()
	require.True(t, ok, "artifact %s of workflow run %d was uploaded", entry.Artifact, entry.PlanRunID)
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	dst := planFile(workspace, entry.Artifact)
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o750))
	require.NoError(t, os.WriteFile(dst, data, 0o600))
	return dst
}

func planFile(workspace, artifact string) string {
	return filepath.Join(workspace, planDir, artifact+".tfplan")
}

type dispatchJob struct {
	dispatch     ghfake.Dispatch
	entry        v1.MatrixEntry
	workspace    string
	round        int
	skipDownload bool
	result       cliResult
}

type jobHook func(t *testing.T, j *dispatchJob)

func (rn *runner) onDispatch(d ghfake.Dispatch) {
	var entries []v1.MatrixEntry
	_ = json.Unmarshal([]byte(d.Inputs["stacks"]), &entries)
	jobs := make([]gh.WorkflowJob, len(entries))
	for i, en := range entries {
		jobs[i] = gh.WorkflowJob{
			RunID:  d.RunID,
			Name:   fmt.Sprintf("run / %s wave %s %s", d.Inputs["mode"], d.Inputs["wave"], en.Key),
			Status: gh.RunStatusQueued,
		}
	}
	rn.cp.gh.SetJobs(d.Repo, d.RunID, jobs)
	for _, r := range rn.cp.gh.WorkflowRuns(d.Repo) {
		if r.ID == d.RunID {
			r.Name = "stackorder run"
			r.DisplayTitle = fmt.Sprintf("stackorder %s %s wave %s", d.Inputs["mode"], d.Inputs["run_id"], d.Inputs["wave"])
			rn.cp.gh.AddWorkflowRun(d.Repo, r)
		}
	}
}

func (rn *runner) pending(runID string) []ghfake.Dispatch {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	var out []ghfake.Dispatch
	for _, d := range rn.cp.gh.Dispatches() {
		if d.Workflow == runWorkflow && d.Inputs["run_id"] == runID && !rn.handled[d.RunID] {
			out = append(out, d)
		}
	}
	return out
}

func (rn *runner) isHandled(id int64) bool {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return rn.handled[id]
}

func (rn *runner) markHandled(d ghfake.Dispatch) {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	rn.handled[d.RunID] = true
}

func entriesOf(t *testing.T, d ghfake.Dispatch) []v1.MatrixEntry {
	t.Helper()
	var out []v1.MatrixEntry
	require.NoError(t, json.Unmarshal([]byte(d.Inputs["stacks"]), &out), "stacks input of dispatch %d", d.RunID)
	return out
}

func (rn *runner) drive(t *testing.T, runID string, hook jobHook) []*dispatchJob {
	t.Helper()
	var all []*dispatchJob
	deadline := time.Now().Add(runTimeout)
	for round := 0; ; {
		ds := rn.pending(runID)
		if len(ds) == 0 {
			var run v1.Run
			rn.cp.getJSON(t, "/v1/runs/"+runID, &run)
			if run.Status.Terminal() {
				return all
			}
			if time.Now().After(deadline) {
				t.Fatalf("e2e: run %s is still %s after %s and nothing is dispatched", runID, run.Status, runTimeout)
			}
			time.Sleep(pollInterval)
			continue
		}
		for _, d := range ds {
			rn.markHandled(d)
			all = append(all, rn.runDispatch(t, d, round, hook)...)
		}
		round++
	}
}

func (rn *runner) dispatchEnv(t *testing.T, d ghfake.Dispatch, entry v1.MatrixEntry, environment, workspace string) map[string]string {
	t.Helper()
	main := strings.TrimSpace(rn.repo.git(t, "rev-parse", "main"))
	event := map[string]any{
		"inputs":   d.Inputs,
		"ref":      "refs/heads/" + d.Ref,
		"workflow": ".github/workflows/" + runWorkflow,
		"repository": map[string]any{
			"id": repoID, "full_name": repoName, "default_branch": "main",
		},
	}
	claims := rn.cp.oidc.DispatchClaims(repoName, itoa(repoID), d.RunID, environment, d.Ref, main)
	claims.Actor = applier
	env := rn.jobEnv(t, claims, workspace, event)
	env[cli.EnvRunID] = d.Inputs["run_id"]
	env[cli.EnvTool] = string(entry.Tool)
	return env
}

func (rn *runner) runDispatch(t *testing.T, d ghfake.Dispatch, round int, hook jobHook) []*dispatchJob {
	t.Helper()
	entries := entriesOf(t, d)
	jobs := make([]*dispatchJob, len(entries))
	var wg sync.WaitGroup
	for i, entry := range entries {
		j := &dispatchJob{dispatch: d, entry: entry, round: round, workspace: rn.repo.checkout(t, d.Inputs["sha"])}
		jobs[i] = j
		if hook != nil {
			hook(t, j)
		}
		env := rn.dispatchEnv(t, d, entry, entry.Environment, j.workspace)
		args := rn.stepArgs(t, d.Inputs["mode"], j)
		wg.Go(func() { j.result = rn.exec(t, j.workspace, env, args...) })
	}
	wg.Wait()
	conclusion := gh.ConclusionSuccess
	for _, j := range jobs {
		if !jobSucceeded(d.Inputs["mode"], j.result.code) {
			conclusion = gh.ConclusionFailure
		}
	}
	run := rn.cp.gh.CompleteWorkflowRun(repoName, d.RunID, conclusion)
	rn.cp.deliver(t, gh.EventWorkflowRun, rn.cp.gh.WorkflowRunEvent("completed", repoName, run))
	return jobs
}

func (rn *runner) applyAs(t *testing.T, j *dispatchJob, environment string) cliResult {
	t.Helper()
	workspace := rn.repo.checkout(t, j.dispatch.Inputs["sha"])
	rn.download(t, j.entry, workspace)
	env := rn.dispatchEnv(t, j.dispatch, j.entry, environment, workspace)
	return rn.exec(t, workspace, env, "apply", "--stack", j.entry.Key, "--run-id", j.dispatch.Inputs["run_id"],
		"--server", rn.cp.baseURL, "--plan-file", planFile(workspace, j.entry.Artifact))
}

func jobSucceeded(mode string, code int) bool {
	return code == cli.ExitSuccess || mode == string(v1.ModeDrift) && code == cli.ExitChanges
}

func (rn *runner) stepArgs(t *testing.T, mode string, j *dispatchJob) []string {
	t.Helper()
	base := []string{"--stack", j.entry.Key, "--run-id", j.dispatch.Inputs["run_id"], "--server", rn.cp.baseURL}
	switch v1.RunMode(mode) {
	case v1.ModeApply:
		if !j.skipDownload {
			rn.download(t, j.entry, j.workspace)
		}
		return append(append([]string{"apply"}, base...), "--plan-file", planFile(j.workspace, j.entry.Artifact))
	case v1.ModeDrift:
		return append([]string{"drift"}, base...)
	}
	return append([]string{"plan"}, base...)
}

func (rn *runner) local(t *testing.T, workspace string, args ...string) cliResult {
	t.Helper()
	return rn.exec(t, workspace, map[string]string{
		cli.EnvAPIKey:       rn.cp.apiKey,
		"GITHUB_REPOSITORY": repoName,
	}, args...)
}

func keysOf(entries []v1.MatrixEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Key
	}
	sort.Strings(out)
	return out
}
