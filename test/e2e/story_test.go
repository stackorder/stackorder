//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/cli"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

const (
	repoOwner      = "acme"
	repoName       = repoOwner + "/example-infra"
	repoID         = int64(424242)
	installationID = int64(4242)
	author         = "alice"
	reviewer       = "bob"
	applier        = "carol"

	prodVPC     = "stacks/prod/vpc"
	prodEKS     = "stacks/prod/eks"
	prodApps    = "stacks/prod/apps"
	stagingVPC  = "stacks/staging/vpc"
	stagingApps = "stacks/staging/apps"
	legacyDNS   = "stacks/legacy/dns"
	sandboxBlue = "stacks/sandbox/blue:blue"

	plaintextSecret = "e2e-plaintext-hunter2"
	stickyMarker    = "<!-- stackorder:sticky -->"
	commentLimit    = 65536
	planTextCap     = 256 * 1024
)

var stateKeys = map[string]string{
	prodVPC:     "prod/vpc.tfstate",
	prodEKS:     "prod/eks.tfstate",
	prodApps:    "prod/apps.tfstate",
	stagingVPC:  "staging/vpc.tfstate",
	stagingApps: "staging/apps.tfstate",
	legacyDNS:   "legacy/dns.tfstate",
}

var bootstrapOrder = []string{prodVPC, stagingVPC, prodEKS, prodApps, stagingApps, legacyDNS}

var vpcChange = []string{prodApps, prodEKS, prodVPC, stagingApps, stagingVPC}

const routeTableHCL = `
resource "terraform_data" "route_table" {
  input = {
    id             = "rtb-${substr(sha256("${var.name}/main"), 0, 17)}"
    vpc_id         = terraform_data.vpc.input.id
    admin_password = "` + plaintextSecret + `"
    routes         = join("\n", [for index in range(1000) : format("%s via %s", cidrsubnet(var.cidr, 10, index), join("", [for round in range(4) : sha256("${var.name}/${index}/${round}")]))])
  }
}
`

const routeTableOutputHCL = `
output "route_table_id" {
  description = "Fake route table id."
  value       = terraform_data.route_table.input.id
}
`

const stackRouteTableOutputHCL = `
output "route_table_id" {
  description = "Route table id, read by dependent stacks."
  value       = module.vpc.route_table_id
}
`

const natGatewayHCL = `
resource "terraform_data" "nat_gateway" {
  input = {
    vpc_id = module.vpc.vpc_id
  }
}
`

const dhcpOptionsHCL = `
resource "terraform_data" "dhcp_options" {
  input = {
    vpc_id      = terraform_data.vpc.input.id
    domain_name = "${var.name}.internal.example"
  }
}
`

const flowLogHCL = `
resource "terraform_data" "flow_log" {
  input = {
    vpc_id = module.vpc.vpc_id
  }
}
`

const sandboxBackendHCL = `terraform {
  backend "s3" {
    bucket       = "stackorder-example-state"
    key          = "sandbox/blue.tfstate"
    region       = "us-east-1"
    use_lockfile = true
  }
}
`

const sandboxMainHCL = `resource "terraform_data" "marker" {
  input = {
    workspace = terraform.workspace
  }
}

output "workspace" {
  description = "Workspace the stack was applied in."
  value       = terraform.workspace
}
`

type story struct {
	tool    v1.Tool
	ls      *localStack
	cp      *controlPlane
	repo    *gitRepo
	rn      *runner
	main    string
	serials map[string]int
	pulls   map[int]*pullRequest
}

func newStory(t *testing.T, tool v1.Tool, ls *localStack, cliBin, src string) *story {
	t.Helper()
	ls.resetBucket(t, stateBucket)
	ls.resetBucket(t, artifactBucket)
	repo, base := newExampleRepo(t, src, tool)
	cp := startControlPlane(t, ls, func(g *ghfake.Server) {
		g.SetRepo(repoName, gh.Repository{ID: repoID, DefaultBranch: "main", Private: true})
		g.AddInstallation(installationID, repoOwner, repoName)
		g.SetRef(repoName, "heads/main", base)
		repo.mirror(t, g, repoName, base)
		for _, login := range []string{author, reviewer, applier} {
			g.SetCollaboratorPermission(repoName, login, "write")
		}
	})
	s := &story{
		tool: tool, ls: ls, cp: cp, repo: repo, main: base,
		serials: map[string]int{}, pulls: map[int]*pullRequest{},
		rn: &runner{
			cli: cliBin, ls: ls, cp: cp, repo: repo, store: t.TempDir(),
			artifacts: map[string]string{}, handled: map[int64]bool{},
		},
	}
	cp.gh.OnDispatch(s.rn.onDispatch)
	return s
}

func (s *story) run(t *testing.T) {
	t.Helper()
	steps := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"adopt", s.adopt},
		{"bootstrap", s.bootstrap},
		{"pr_plan", s.prPlan},
		{"apply", s.apply},
		{"lock_safety", s.lockSafety},
		{"expired_artifact", s.expiredArtifact},
		{"drift", s.drift},
		{"workspace_stack", s.workspaceStack},
	}
	for _, step := range steps {
		if !t.Run(step.name, step.fn) {
			t.Fatalf("e2e: %s failed; the later steps build on it", step.name)
		}
	}
}

func (s *story) runJSON(t *testing.T, id string) v1.Run {
	t.Helper()
	var run v1.Run
	s.cp.getJSON(t, "/v1/runs/"+id, &run)
	return run
}

func runStack(t *testing.T, run v1.Run, key string) v1.RunStack {
	t.Helper()
	for _, rs := range run.Stacks {
		if rs.Key == key {
			return rs
		}
	}
	t.Fatalf("e2e: run %s has no stack %s", run.ID, key)
	return v1.RunStack{}
}

func statuses(run v1.Run) map[string]v1.StackStatus {
	out := map[string]v1.StackStatus{}
	for _, rs := range run.Stacks {
		out[rs.Key] = rs.Status
	}
	return out
}

func (s *story) stacks(t *testing.T) map[string]v1.StackDetail {
	t.Helper()
	var page v1.Page[v1.StackDetail]
	s.cp.getJSON(t, "/v1/repos/"+repoName+"/stacks?limit=100", &page)
	out := map[string]v1.StackDetail{}
	for _, st := range page.Items {
		out[st.Key] = st
	}
	return out
}

func (s *story) locks(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	for key, st := range s.stacks(t) {
		if st.Lock != nil {
			out[key] = st.Lock.PRNumber
		}
	}
	return out
}

func (s *story) checkRun(t *testing.T, name, sha string) ghfake.CheckRun {
	t.Helper()
	var found *ghfake.CheckRun
	for _, c := range s.cp.gh.CheckRuns(repoName) {
		if c.Name == name && c.HeadSHA == sha {
			found = &c
		}
	}
	require.NotNil(t, found, "check run %q on %s", name, sha)
	return *found
}

func (s *story) comments(pr int) []gh.Comment {
	return s.cp.gh.Comments(repoName, pr)
}

func (s *story) stickies(pr int) []string {
	var out []string
	for _, c := range s.comments(pr) {
		if strings.HasPrefix(c.Body, stickyMarker) {
			out = append(out, c.Body)
		}
	}
	return out
}

func (s *story) commentsContaining(pr int, text string) []string {
	var out []string
	for _, c := range s.comments(pr) {
		if strings.Contains(c.Body, text) {
			out = append(out, c.Body)
		}
	}
	return out
}

func (s *story) noLockObjects(t *testing.T) {
	t.Helper()
	assert.Empty(t, s.ls.lockObjects(t), "every S3 state lock was released")
}

func (s *story) pushMain(t *testing.T, sha string, paths []string) {
	t.Helper()
	s.repo.setBranch(t, "main", sha)
	s.repo.mirror(t, s.cp.gh, repoName, sha)
	s.cp.deliver(t, gh.EventPush, s.cp.gh.PushEvent(repoName, "refs/heads/main", sha, paths...))
	s.main = sha
}

func (s *story) mergePull(t *testing.T, pr *pullRequest) {
	t.Helper()
	require.Equal(t, s.main, pr.base, "#%d merges by fast-forward", pr.number)
	s.pushMain(t, pr.head, s.repo.changedPaths(t, pr.base, pr.head))
	now := time.Now().UTC()
	pr.pull.State, pr.pull.Merged, pr.pull.MergeCommitSHA, pr.pull.MergedAt = gh.IssueClosed, true, pr.head, &now
	s.cp.deliver(t, gh.EventPullRequest, s.cp.gh.PullRequestEvent("closed", repoName, pr.pull))
}

func (s *story) closePull(t *testing.T, pr *pullRequest) {
	t.Helper()
	pr.pull.State = gh.IssueClosed
	s.cp.deliver(t, gh.EventPullRequest, s.cp.gh.PullRequestEvent("closed", repoName, pr.pull))
}

func (s *story) approve(t *testing.T, pr *pullRequest) {
	t.Helper()
	ev := s.cp.gh.PullRequestReviewEvent("submitted", repoName, pr.number,
		gh.Review{User: gh.User{Login: reviewer}, State: gh.ReviewApproved, CommitID: pr.head})
	s.cp.deliver(t, gh.EventPullRequestReview, ev)
}

func (s *story) command(t *testing.T, pr *pullRequest, body string) gh.Comment {
	t.Helper()
	ev := s.cp.gh.IssueCommentEvent(repoName, pr.number, body, applier)
	s.cp.deliver(t, gh.EventIssueComment, ev)
	return ev.Comment
}

func (s *story) applyDispatches() []ghfake.Dispatch {
	var out []ghfake.Dispatch
	for _, d := range s.cp.gh.Dispatches() {
		if d.Inputs["mode"] == string(v1.ModeApply) {
			out = append(out, d)
		}
	}
	return out
}

func (s *story) applyRunOf(t *testing.T, before int) string {
	t.Helper()
	ds := s.applyDispatches()
	require.Greater(t, len(ds), before, "the apply command dispatched %s", runWorkflow)
	id := ds[before].Inputs["run_id"]
	require.NotEmpty(t, id)
	return id
}

func (s *story) openAndPlan(t *testing.T, number int, branch, base, head, title string) *pullRequest {
	t.Helper()
	pr := s.rn.openPull(t, number, branch, base, head, title)
	s.pulls[number] = pr
	s.rn.resolve(t, pr)
	if len(pr.matrix.Include) > 0 {
		s.rn.plan(t, pr)
	}
	return pr
}

func (s *story) adopt(t *testing.T) {
	head := s.repo.commit(t, "adopt", s.main, "docs: say who operates this repository",
		appendFile("README.md", "\nThis copy is operated by the Stackorder end-to-end suite.\n"))
	pr := s.openAndPlan(t, 1, "adopt", s.main, head, "docs: adopt Stackorder")
	assert.Empty(t, pr.affected, "a docs-only change affects nothing")
	assert.Empty(t, pr.matrix.Include)

	run := s.runJSON(t, pr.runID)
	assert.Equal(t, v1.RunPlanned, run.Status)
	assert.Empty(t, run.Stacks)
	assert.Equal(t, gh.ConclusionSuccess, s.checkRun(t, report.CheckApply, head).Conclusion,
		"a before_merge pull request that affects nothing gets a green apply check so branch protection can pass")
	known := s.stacks(t)
	for _, key := range bootstrapOrder {
		assert.Contains(t, known, key, "the resolve job registered the graph")
	}

	s.mergePull(t, pr)
	var view v1.GraphView
	s.cp.getJSON(t, "/v1/repos/"+repoName+"/graph", &view)
	assert.Equal(t, head, view.SHA, "the merged pull request's graph is the default-branch graph")
}

func (s *story) bootstrap(t *testing.T) {
	ws := s.repo.checkout(t, s.main)
	for _, key := range bootstrapOrder {
		res := s.rn.local(t, ws, "apply", "--stack", key, "--local")
		require.Equal(t, 0, res.code, "apply --local %s: %s", key, res)
		assert.Contains(t, res.stdout, key+": applied:", "apply --local reports the apply")
	}
	for _, key := range bootstrapOrder {
		st := s.ls.requireState(t, stateKeys[key])
		assert.Positive(t, st.Serial, "%s has a state serial", key)
		assert.NotEmpty(t, st.Resources, "%s state holds resources", key)
		s.serials[key] = st.Serial
	}
	vpc := s.ls.requireState(t, stateKeys[prodVPC])
	assert.Equal(t, "vpc-6754af9632a2745e8", vpc.Outputs["vpc_id"].Value)
	apps := s.ls.requireState(t, stateKeys[prodApps])
	assert.Equal(t, "vpc-6754af9632a2745e8", apps.input("terraform_data.app")["vpc_id"], "prod/apps read the vpc id from prod/vpc's state")

	var page v1.Page[v1.Run]
	s.cp.getJSON(t, "/v1/repos/"+repoName+"/runs?mode=apply&limit=50", &page)
	require.Len(t, page.Items, len(bootstrapOrder), "one manual run per stack")
	for _, r := range page.Items {
		assert.Equal(t, v1.TriggerManual, r.Trigger)
		assert.Equal(t, v1.RunApplied, r.Status, "manual run %s", r.ID)
		assert.Equal(t, "apikey:e2e", r.RequestedBy)
	}
	assert.Empty(t, s.locks(t), "manual runs release their locks when their results arrive")
	s.noLockObjects(t)
}

func (s *story) prPlan(t *testing.T) {
	head := s.repo.commit(t, "vpc-route-table", s.main, "feat(vpc): add a route table",
		appendFile("modules/vpc/main.tf", routeTableHCL),
		appendFile("modules/vpc/outputs.tf", routeTableOutputHCL),
		appendFile("stacks/prod/vpc/outputs.tf", stackRouteTableOutputHCL),
		replaceIn("stacks/prod/apps/variables.tf", "default     = 3", "default     = 4"))
	pr := s.openAndPlan(t, 2, "vpc-route-table", s.main, head, "feat(vpc): add a route table")

	laptop := s.rn.local(t, s.repo.checkout(t, head), "resolve", "--base", "origin/main")
	require.Equal(t, 0, laptop.code, "resolve on a laptop with the automation key set: %s", laptop)
	for _, key := range vpcChange {
		assert.Contains(t, laptop.stdout, key, "the local preview lists %s", key)
	}

	assert.ElementsMatch(t, vpcChange, pr.affected)
	assert.Equal(t, [][]string{{prodVPC, stagingVPC}, {prodApps, prodEKS, stagingApps}}, sortedWaves(pr.waves))
	envs := map[string]string{}
	for _, e := range pr.matrix.Include {
		envs[e.Key] = e.Environment
		assert.Equal(t, s.tool, e.Tool, "%s runs with the repository's tool", e.Key)
		assert.Equal(t, head, e.SHA, "%s plans the head commit", e.Key)
	}
	assert.Equal(t, map[string]string{
		prodVPC: "production", prodEKS: "production", prodApps: "production",
		stagingVPC: "staging", stagingApps: "staging",
	}, envs)

	for key, res := range pr.plans {
		want := key == prodVPC || key == stagingVPC || key == prodApps
		assert.Equal(t, fmt.Sprint(want), res.outputs["has-changes"], "has-changes of %s", key)
		assert.Equal(t, v1.PlanArtifactName(key, head), res.outputs["artifact"], "artifact of %s", key)
		assert.Equal(t, "false", res.outputs["unconfirmed"], "the server confirmed the plan of %s", key)
	}
	vpcLog := pr.plans[prodVPC].stdout
	banner := map[v1.Tool]string{v1.ToolTerraform: "Terraform will perform", v1.ToolTofu: "OpenTofu will perform"}
	assert.Contains(t, vpcLog, banner[s.tool], "the plan job ran %s", s.tool)
	assert.Contains(t, vpcLog, "::add-mask::"+plaintextSecret, "the plan job masks the secret in the job log")
	assert.NotContains(t, withoutMaskCommands(vpcLog), plaintextSecret, "the job log never shows the secret")

	run := s.runJSON(t, pr.runID)
	require.Equal(t, v1.RunPlanned, run.Status)
	require.Len(t, run.Stacks, len(vpcChange))
	for _, rs := range run.Stacks {
		assert.Equal(t, v1.StackPlanned, rs.Status, rs.Key)
		assert.Equal(t, v1.PlanArtifactName(rs.Key, head), rs.PlanArtifact, rs.Key)
		require.NotNil(t, rs.Summary, rs.Key)
	}
	vpc := runStack(t, run, prodVPC)
	assert.Equal(t, 1, vpc.Summary.Adds)
	assert.Equal(t, []string{"module.vpc.terraform_data.route_table"}, vpc.Summary.Added)
	assert.Equal(t, 1, vpc.Summary.OutputChanges, "prod/vpc exposes the route table id")
	assert.ElementsMatch(t, []v1.Reason{v1.ReasonChanged, v1.ReasonModule}, vpc.Reasons)
	staging := runStack(t, run, stagingVPC)
	assert.Equal(t, 1, staging.Summary.Adds)
	assert.Equal(t, []string{"module.vpc.terraform_data.route_table"}, staging.Summary.Added)
	assert.Equal(t, []v1.Reason{v1.ReasonModule}, staging.Reasons)
	apps := runStack(t, run, prodApps)
	assert.Equal(t, 1, apps.Summary.Changes, "prod/apps read prod/vpc's remote state and plans its replica change")
	assert.Equal(t, []string{"terraform_data.app"}, apps.Summary.Changed)
	assert.Contains(t, apps.Reasons, v1.ReasonReadsState)
	for _, key := range []string{prodEKS, stagingApps} {
		rs := runStack(t, run, key)
		assert.True(t, rs.Summary.Empty(), "%s plans clean: %+v", key, rs.Summary)
		assert.Equal(t, []v1.Reason{v1.ReasonDependent}, rs.Reasons, key)
	}

	assert.True(t, vpc.Truncated, "the plan text of prod/vpc exceeds the cap")
	assert.Equal(t, fmt.Sprintf("s3://%s/runs/%s/stacks-prod-vpc/plan.txt", artifactBucket, run.ID), vpc.PlanURL)
	summaryOnly := runStack(t, run, stagingApps)
	assert.Equal(t, string(v1.PlanOutputSummary), summaryOnly.PlanOutput)
	assert.Empty(t, summaryOnly.PlanURL, "plan_output: summary keeps plan text off the server")

	stored := s.storedPlanText(t, run.ID)
	assert.Contains(t, stored[prodVPC], `admin_password = "***"`, "Postgres holds the redacted beginning of the plan")
	assert.Less(t, len(stored[prodVPC]), 64*1024, "with an artifact bucket Postgres keeps only the beginning")
	assert.Empty(t, stored[stagingApps], "plan_output: summary stores no plan text")
	for key, text := range stored {
		assert.NotContains(t, text, plaintextSecret, key)
	}

	objects := s.ls.keys(t, artifactBucket, "runs/"+run.ID+"/")
	for _, key := range []string{prodVPC, stagingVPC, prodApps, prodEKS} {
		prefix := "runs/" + run.ID + "/" + strings.ReplaceAll(key, "/", "-") + "/"
		assert.Contains(t, objects, prefix+"plan.txt", key)
		assert.Contains(t, objects, prefix+"plan.json", key)
	}
	assert.NotContains(t, objects, "runs/"+run.ID+"/stacks-staging-apps/plan.txt")
	planJSON, ok := s.ls.get(t, artifactBucket, "runs/"+run.ID+"/stacks-prod-vpc/plan.json")
	require.True(t, ok)
	var bucketSummary v1.PlanSummary
	require.NoError(t, json.Unmarshal(planJSON, &bucketSummary), "the bucket holds the plan summary as JSON")
	assert.Equal(t, []string{"module.vpc.terraform_data.route_table"}, bucketSummary.Added)
	assert.NotContains(t, string(planJSON), plaintextSecret)
	full, ok := s.ls.get(t, artifactBucket, "runs/"+run.ID+"/stacks-prod-vpc/plan.txt")
	require.True(t, ok)
	assert.LessOrEqual(t, len(full), planTextCap, "the CLI caps the plan text it sends at 256 KB")
	assert.Greater(t, len(full), len(stored[prodVPC]), "the bucket keeps more than Postgres")
	assert.Contains(t, string(full), "module.vpc.terraform_data.route_table will be created")
	assert.Contains(t, string(full), `admin_password = "***"`)
	assert.NotContains(t, string(full), plaintextSecret)

	for _, key := range vpcChange {
		c := s.checkRun(t, "stackorder/plan: "+key, head)
		assert.Equal(t, gh.CheckRunCompleted, c.Status, key)
		assert.Equal(t, gh.ConclusionSuccess, c.Conclusion, key)
		assert.NotContains(t, c.Output.Text, plaintextSecret, key)
		assert.LessOrEqual(t, len(c.Output.Text), commentLimit, key)
	}
	assert.Contains(t, s.checkRun(t, "stackorder/plan: "+prodVPC, head).Output.Text, `admin_password = "***"`)
	rollup := s.checkRun(t, report.CheckPlan, head)
	assert.Equal(t, gh.ConclusionSuccess, rollup.Conclusion)
	stickies := s.stickies(pr.number)
	require.Len(t, stickies, 1, "one sticky comment")
	for _, key := range vpcChange {
		assert.Contains(t, stickies[0], key)
	}
	assert.NotContains(t, stickies[0], plaintextSecret)
	assert.LessOrEqual(t, len(stickies[0]), commentLimit)
	s.noLockObjects(t)
}

func (s *story) storedPlanText(t *testing.T, runID string) map[string]string {
	t.Helper()
	rows, err := s.cp.store.GetRunStacksWithText(t.Context(), uuid.MustParse(runID))
	require.NoError(t, err)
	out := map[string]string{}
	for _, rs := range rows {
		out[rs.Key] = rs.PlanText
	}
	return out
}

func withoutMaskCommands(log string) string {
	var b strings.Builder
	for line := range strings.Lines(log) {
		if !strings.HasPrefix(line, "::add-mask::") {
			b.WriteString(line)
		}
	}
	return b.String()
}

func sortedWaves(waves [][]string) [][]string {
	out := make([][]string, len(waves))
	for i, w := range waves {
		out[i] = slices.Sorted(slices.Values(w))
	}
	return out
}

func (s *story) apply(t *testing.T) {
	pr := s.pulls[2]
	s.approve(t, pr)
	before := len(s.applyDispatches())
	c := s.command(t, pr, "stackorder apply")
	assert.Equal(t, []string{"eyes", "rocket"}, s.cp.gh.Reactions(c.ID))
	runID := s.applyRunOf(t, before)

	var impostor cliResult
	jobs := s.rn.drive(t, runID, func(t *testing.T, j *dispatchJob) {
		t.Helper()
		if j.entry.Key == prodVPC {
			impostor = s.rn.applyAs(t, j, v1.DefaultEnvironment)
			assert.Equal(t, s.serials[prodVPC], s.ls.requireState(t, stateKeys[prodVPC]).Serial, "the refused job applied nothing")
		}
	})
	assert.Equal(t, cli.ExitRefused, impostor.code, "a job outside every dispatched environment is refused: %s", impostor)
	assert.Contains(t, impostor.stderr, "in environment default")

	type dispatched struct {
		round       int
		wave        string
		environment string
		stacks      []string
	}
	var got []dispatched
	seen := map[int64]bool{}
	for _, j := range jobs {
		assert.Equal(t, 0, j.result.code, "apply of %s: %s", j.entry.Key, j.result)
		assert.NotContains(t, j.result.stderr, "planning again", "%s applies the saved plan", j.entry.Key)
		assert.Equal(t, "main", j.dispatch.Ref, "%s is dispatched on the default branch", j.entry.Key)
		assert.Equal(t, pr.head, j.dispatch.Inputs["sha"], "%s applies the head commit of #2 before it merges", j.entry.Key)
		if !seen[j.dispatch.RunID] {
			seen[j.dispatch.RunID] = true
			got = append(got, dispatched{j.round, j.dispatch.Inputs["wave"], j.entry.Environment, keysOf(entriesOf(t, j.dispatch))})
		}
	}
	assert.ElementsMatch(t, []dispatched{
		{0, "0", "production", []string{prodVPC}},
		{0, "0", "staging", []string{stagingVPC}},
		{1, "1", "production", []string{prodApps}},
	}, got, "one dispatch per wave and environment, wave 1 only after wave 0 finished; propagated no-op stacks are skipped")

	run := s.runJSON(t, runID)
	assert.Equal(t, v1.RunApplied, run.Status)
	assert.Equal(t, map[string]v1.StackStatus{
		prodVPC: v1.StackApplied, stagingVPC: v1.StackApplied, prodApps: v1.StackApplied,
		prodEKS: v1.StackNoop, stagingApps: v1.StackNoop,
	}, statuses(run))
	assert.Equal(t, gh.ConclusionSuccess, s.checkRun(t, report.CheckApply, pr.head).Conclusion)
	stickies := s.stickies(pr.number)
	require.Len(t, stickies, 1, "the apply updates the one sticky comment instead of adding another")
	assert.Contains(t, strings.SplitN(stickies[0], "\n", 3)[1], string(v1.RunApplied), "the sticky comment shows the applied run")

	for _, key := range []string{prodVPC, stagingVPC, prodApps} {
		st := s.ls.requireState(t, stateKeys[key])
		assert.Greater(t, st.Serial, s.serials[key], "%s has a new state serial", key)
		s.serials[key] = st.Serial
	}
	for _, key := range []string{prodEKS, stagingApps, legacyDNS} {
		assert.Equal(t, s.serials[key], s.ls.requireState(t, stateKeys[key]).Serial, "%s was not applied", key)
	}
	vpc := s.ls.requireState(t, stateKeys[prodVPC])
	assert.Contains(t, vpc.addresses(), "module.vpc.terraform_data.route_table")
	assert.Regexp(t, `^rtb-[0-9a-f]{17}$`, vpc.Outputs["route_table_id"].Value)
	assert.Equal(t, "vpc-6754af9632a2745e8", vpc.Outputs["vpc_id"].Value)
	apps := s.ls.requireState(t, stateKeys[prodApps])
	assert.InDelta(t, 4, apps.input("terraform_data.app")["replicas"], 0)

	locks := s.locks(t)
	for _, key := range vpcChange {
		assert.Equal(t, 2, locks[key], "#2 holds the lock on %s until it merges", key)
	}
	s.noLockObjects(t)
}

func (s *story) lockSafety(t *testing.T) {
	head := s.repo.commit(t, "staging-nat", s.main, "feat(staging): add a NAT gateway",
		appendFile("stacks/staging/vpc/main.tf", natGatewayHCL))
	pr := s.openAndPlan(t, 3, "staging-nat", s.main, head, "feat(staging): add a NAT gateway")
	assert.ElementsMatch(t, []string{stagingApps, stagingVPC}, pr.affected)
	run := s.runJSON(t, pr.runID)
	nat := runStack(t, run, stagingVPC)
	assert.Equal(t, []string{"module.vpc.terraform_data.route_table"}, nat.Summary.Destroyed,
		"planned against the state #2 applied, the branch would remove its route table")
	assert.Equal(t, []string{"terraform_data.nat_gateway"}, nat.Summary.Added)

	resolveCheck := s.checkRun(t, report.CheckResolve, head)
	assert.Contains(t, resolveCheck.Output.Summary+resolveCheck.Output.Text, "[#2]", "the resolve check shows the locks #2 holds")
	for _, key := range pr.affected {
		c := s.checkRun(t, "stackorder/plan: "+key, head)
		assert.Equal(t, gh.ConclusionSuccess, c.Conclusion, "the plan of the locked stack %s still runs", key)
		assert.Contains(t, c.Output.Summary+c.Output.Text, "locked by [#2]", "the plan check of %s warns that #2 holds its lock", key)
	}

	s.approve(t, pr)
	before := len(s.applyDispatches())
	s.command(t, pr, "stackorder apply")
	assert.Len(t, s.applyDispatches(), before, "a refused apply dispatches nothing")
	refusals := s.commentsContaining(pr.number, "locked by #2")
	require.NotEmpty(t, refusals, "the refusal names the pull request holding the locks")
	assert.Contains(t, refusals[len(refusals)-1], report.LayerName(report.LayerLocks))
	assert.Contains(t, refusals[len(refusals)-1], stagingVPC)

	ws := s.repo.checkout(t, s.main)
	const reason = "e2e: release the staging locks of #2"
	res := s.rn.local(t, ws, "unlock", stagingVPC, stagingApps, "--reason", reason)
	require.Equal(t, 0, res.code, "%s", res)
	assert.Contains(t, res.stdout, "released "+stagingVPC)
	assert.Contains(t, res.stdout, "released "+stagingApps)
	assert.Contains(t, res.stdout, "PR #2")
	assert.Equal(t, map[string]int{prodVPC: 2, prodEKS: 2, prodApps: 2}, s.locks(t), "only the named locks were released")

	var audit v1.Page[v1.AuditEntry]
	s.cp.getJSON(t, "/v1/audit?limit=100", &audit)
	var unlocked []string
	for _, e := range audit.Items {
		if e.Action == "unlock" && e.Actor == "apikey:e2e" && e.Details["reason"] == reason {
			unlocked = append(unlocked, fmt.Sprint(e.Details["stack"]))
			assert.InDelta(t, 2, e.Details["pr"], 0)
		}
	}
	assert.ElementsMatch(t, []string{stagingVPC, stagingApps}, unlocked, "the unlock is audited")

	s.closePull(t, pr)
	seen := len(s.comments(2))
	s.mergePull(t, s.pulls[2])
	assert.Empty(t, s.locks(t), "merging #2 released its remaining locks")
	var posted []string
	for _, c := range s.comments(2)[seen:] {
		if !strings.HasPrefix(c.Body, stickyMarker) {
			posted = append(posted, c.Body)
		}
	}
	require.Len(t, posted, 1, "the merge posts one comment about the locks it released")
	for _, key := range []string{prodVPC, prodEKS, prodApps} {
		assert.Contains(t, posted[0], key, "the merge comment lists the released lock on %s", key)
	}
	for _, key := range []string{stagingVPC, stagingApps} {
		assert.NotContains(t, posted[0], key, "the lock on %s was already released by unlock", key)
	}
}

func (s *story) expiredArtifact(t *testing.T) {
	head := s.repo.commit(t, "vpc-dhcp", s.main, "feat(vpc): add DHCP options",
		appendFile("modules/vpc/main.tf", dhcpOptionsHCL),
		replaceIn("stacks/staging/apps/variables.tf", "default     = 1", "default     = 3"),
		replaceIn("stacks/prod/apps/variables.tf", "default     = 4", "default     = 5"))
	pr := s.openAndPlan(t, 4, "vpc-dhcp", s.main, head, "feat(vpc): add DHCP options")
	assert.ElementsMatch(t, vpcChange, pr.affected)
	plan := s.runJSON(t, pr.runID)
	for _, key := range []string{prodVPC, stagingVPC} {
		assert.Equal(t, []string{"module.vpc.terraform_data.dhcp_options"}, runStack(t, plan, key).Summary.Added, key)
	}
	assert.Equal(t, []string{"terraform_data.app"}, runStack(t, plan, stagingApps).Summary.Changed, "staging/apps has a change of its own behind staging/vpc")
	assert.Equal(t, []string{"terraform_data.app"}, runStack(t, plan, prodApps).Summary.Changed, "prod/apps has a change of its own behind prod/vpc")
	stagingSerial := s.ls.requireState(t, stateKeys[stagingVPC]).Serial
	appsSerial := s.ls.requireState(t, stateKeys[stagingApps]).Serial
	prodAppsSerial := s.ls.requireState(t, stateKeys[prodApps]).Serial

	s.approve(t, pr)
	before := len(s.applyDispatches())
	s.command(t, pr, "stackorder apply")
	runID := s.applyRunOf(t, before)
	jobs := s.rn.drive(t, runID, func(t *testing.T, j *dispatchJob) {
		t.Helper()
		j.skipDownload = true
		if j.entry.Key == stagingVPC {
			appendFile("stacks/staging/vpc/main.tf", flowLogHCL)(t, j.workspace)
		}
	})
	results := map[string]cliResult{}
	for _, j := range jobs {
		results[j.entry.Key] = j.result
	}
	require.Contains(t, results, prodVPC)
	require.Contains(t, results, stagingVPC)
	assert.Len(t, results, 2, "the run ends after the wave in which a stack failed, so prod/apps in wave 1 is never dispatched")

	vpc := results[prodVPC]
	require.Equal(t, 0, vpc.code, "%s", vpc)
	assert.Contains(t, vpc.stderr, "planning again", "the CLI re-plans when the artifact expired")

	staging := results[stagingVPC]
	assert.Equal(t, cli.ExitRefused, staging.code, "%s", staging)
	assert.Contains(t, staging.stderr, "does not match the plan recorded")
	assert.Contains(t, staging.stderr, "terraform_data.flow_log")

	run := s.runJSON(t, runID)
	assert.Equal(t, v1.RunFailed, run.Status)
	assert.Equal(t, v1.StackApplied, runStack(t, run, prodVPC).Status)
	failed := runStack(t, run, stagingVPC)
	assert.Equal(t, v1.StackFailed, failed.Status)
	require.NotNil(t, failed.ExitCode)
	assert.Equal(t, cli.ExitRefused, *failed.ExitCode)
	assert.Equal(t, gh.ConclusionFailure, s.checkRun(t, "stackorder/apply: "+stagingVPC, head).Conclusion)
	blocked := runStack(t, run, stagingApps)
	assert.Equal(t, v1.StackBlocked, blocked.Status, "the dependent of the failed stack is blocked")
	assert.Equal(t, []string{stagingVPC}, blocked.BlockedBy)
	unrelated := runStack(t, run, prodApps)
	assert.NotEqual(t, v1.StackApplied, unrelated.Status, "prod/apps depends only on prod/vpc, which applied, but its wave never starts")
	assert.Empty(t, unrelated.BlockedBy, "prod/apps is not a dependent of the failed stack")
	assert.Equal(t, v1.StackNoop, runStack(t, run, prodEKS).Status)
	assert.Contains(t, s.ls.requireState(t, stateKeys[prodVPC]).addresses(), "module.vpc.terraform_data.dhcp_options")
	assert.Equal(t, stagingSerial, s.ls.requireState(t, stateKeys[stagingVPC]).Serial, "the refused apply changed nothing")
	assert.Equal(t, appsSerial, s.ls.requireState(t, stateKeys[stagingApps]).Serial, "the blocked stack was not applied")
	assert.Equal(t, prodAppsSerial, s.ls.requireState(t, stateKeys[prodApps]).Serial, "the stack of the wave after the failure was not applied")
	locks := s.locks(t)
	for _, key := range vpcChange {
		assert.Equal(t, 4, locks[key], "#4 keeps the lock on %s after its apply failed", key)
	}
	s.noLockObjects(t)
}

func (s *story) driftCheck(t *testing.T, key string, round int) (*dispatchJob, v1.Run) {
	t.Helper()
	stack, ok := s.stacks(t)[key]
	require.True(t, ok, "the server knows %s", key)
	_, err := s.cp.store.Pool().Exec(t.Context(),
		`UPDATE runs SET created_at = created_at - interval '1 hour' WHERE repo_id = $1 AND mode = 'drift'`, repoID)
	require.NoError(t, err, "age the earlier drift runs by an hour, as the next scheduled check sees them")
	before := len(s.cp.gh.Dispatches())
	payload, err := json.Marshal(runs.DriftJob{RepoID: repoID, StackID: stack.ID})
	require.NoError(t, err)
	_, _, err = s.cp.store.EnqueueJob(t.Context(), runs.JobDrift, payload, time.Time{}, fmt.Sprintf("e2e-drift:%s:%d", stack.ID, round))
	require.NoError(t, err)
	var d ghfake.Dispatch
	waitUntil(t, time.Minute, "the drift check of "+key+" is dispatched", func() bool {
		for _, cand := range s.cp.gh.Dispatches()[before:] {
			if cand.Inputs["mode"] != string(v1.ModeDrift) || s.rn.isHandled(cand.RunID) {
				continue
			}
			if slices.Contains(keysOf(entriesOf(t, cand)), key) {
				d = cand
				return true
			}
		}
		return false
	})
	assert.Equal(t, s.main, d.Inputs["sha"], "drift checks the head of the default branch")
	s.rn.markHandled(d)
	jobs := s.rn.runDispatch(t, d, 0, nil)
	require.Len(t, jobs, 1)
	assert.Equal(t, v1.DefaultEnvironment, jobs[0].entry.Environment)
	return jobs[0], s.runJSON(t, d.Inputs["run_id"])
}

func (s *story) driftIssues() []gh.Issue {
	var out []gh.Issue
	for _, is := range s.cp.gh.Issues(repoName) {
		if is.Title == report.DriftIssueTitle(stagingApps) {
			out = append(out, is)
		}
	}
	return out
}

func (s *story) drift(t *testing.T) {
	drifted := s.repo.commit(t, "main", s.main, "feat(staging): scale the app to two replicas",
		replaceIn("stacks/staging/apps/variables.tf", "default     = 1", "default     = 2"))
	s.pushMain(t, drifted, []string{"stacks/staging/apps/variables.tf"})

	job, run := s.driftCheck(t, stagingApps, 1)
	assert.Equal(t, cli.ExitChanges, job.result.code, "drift exits 2: %s", job.result)
	assert.Equal(t, "true", job.result.outputs["drifted"])
	rs := runStack(t, run, stagingApps)
	require.NotNil(t, rs.Summary)
	assert.Equal(t, []string{"terraform_data.app"}, rs.Summary.Changed)
	stack := s.stacks(t)[stagingApps]
	require.NotNil(t, stack.Drift)
	assert.True(t, stack.Drift.Drifted)
	issues := s.driftIssues()
	require.Len(t, issues, 1, "one drift issue for the stack")
	assert.Equal(t, gh.IssueOpen, issues[0].State)
	assert.Equal(t, issues[0].Number, stack.Drift.IssueNumber)

	reverted := s.repo.commit(t, "main", s.main, "revert: scale the app back to one replica",
		replaceIn("stacks/staging/apps/variables.tf", "default     = 2", "default     = 1"))
	s.pushMain(t, reverted, []string{"stacks/staging/apps/variables.tf"})

	job, second := s.driftCheck(t, stagingApps, 2)
	assert.NotEqual(t, run.ID, second.ID, "the second check is a run of its own")
	assert.Equal(t, 0, job.result.code, "no drift after the revert: %s", job.result)
	assert.Equal(t, "false", job.result.outputs["drifted"])
	stack = s.stacks(t)[stagingApps]
	require.NotNil(t, stack.Drift)
	assert.False(t, stack.Drift.Drifted)
	issues = s.driftIssues()
	require.Len(t, issues, 1)
	assert.Equal(t, gh.IssueClosed, issues[0].State, "the drift issue is closed once the stack matches again")
	assert.NotEmpty(t, s.cp.gh.Comments(repoName, issues[0].Number), "the issue says why it was closed")
	s.noLockObjects(t)
}

func (s *story) workspaceStack(t *testing.T) {
	head := s.repo.commit(t, "sandbox-blue", s.main, "feat(sandbox): add the blue workspace stack",
		writeFile("stacks/sandbox/blue/.stackorder.yaml", "workspace: blue\n"),
		writeFile("stacks/sandbox/blue/backend.tf", sandboxBackendHCL),
		writeFile("stacks/sandbox/blue/main.tf", sandboxMainHCL))
	pr := s.openAndPlan(t, 5, "sandbox-blue", s.main, head, "feat(sandbox): add the blue workspace stack")
	require.Equal(t, []string{sandboxBlue}, pr.affected)
	entry := pr.matrix.Include[0]
	assert.Equal(t, "blue", entry.Workspace)
	assert.Equal(t, "stacks/sandbox/blue", entry.Stack)
	assert.Equal(t, v1.DefaultEnvironment, entry.Environment, "stacks/sandbox/ matches no environment prefix")
	planned := pr.plans[sandboxBlue]
	assert.Contains(t, planned.stdout, `workspace "blue"`, "the plan job selects the workspace")
	assert.Equal(t, "stackorder-plan-stacks-sandbox-blue-blue-"+head, planned.outputs["artifact"])

	s.approve(t, pr)
	before := len(s.applyDispatches())
	s.command(t, pr, "stackorder apply")
	runID := s.applyRunOf(t, before)
	jobs := s.rn.drive(t, runID, nil)
	require.Len(t, jobs, 1)
	assert.Equal(t, 0, jobs[0].result.code, "%s", jobs[0].result)
	assert.Equal(t, v1.RunApplied, s.runJSON(t, runID).Status)

	st := s.ls.requireState(t, "env:/blue/sandbox/blue.tfstate")
	assert.Equal(t, "blue", st.Outputs["workspace"].Value)
	assert.Equal(t, []string{"terraform_data.marker"}, st.addresses())
	_, inDefault := s.ls.state(t, "sandbox/blue.tfstate")
	assert.False(t, inDefault, "nothing is written to the default workspace's key")
	stack := s.stacks(t)[sandboxBlue]
	assert.Equal(t, "blue", stack.Workspace)
	require.NotNil(t, stack.Backend)
	assert.Equal(t, "sandbox/blue.tfstate", stack.Backend.Key)
	s.noLockObjects(t)
}
