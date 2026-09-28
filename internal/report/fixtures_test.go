package report

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

const (
	runID   = "5b0e6c1e-8a51-4c1a-9a3e-2f3c4d5e6f70"
	headSHA = "3f9a2c1d7e8b4a6f0c5d2e1b9a8c7d6e5f4a3b2c"
	jobBase = "https://github.com/acme/infra/actions/runs/1234567/job/"
)

var (
	t0       = time.Date(2026, 9, 28, 6, 0, 0, 0, time.UTC)
	testOpts = Options{BaseURL: "https://stackorder.example.com/", RepoURL: "https://github.com/acme/infra"}
)

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o750))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run `go test ./internal/report -update` to create %s", path)
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("%s differs (-want +got):\n%s", path, diff)
	}
}

func intp(i int) *int { return &i }

func tfPlan(symbol string, addrs ...string) string {
	verb := map[string]string{"+": "created", "~": "updated in-place", "-": "destroyed"}[symbol]
	var b strings.Builder
	b.WriteString("Terraform used the selected providers to generate the following execution\nplan. Resource actions are indicated with the following symbols:\n")
	b.WriteString("  + create\n  ~ update in-place\n  - destroy\n\nTerraform will perform the following actions:\n\n")
	for _, a := range addrs {
		typ, name, _ := strings.Cut(a, ".")
		fmt.Fprintf(&b, "  # %s will be %s\n  %s resource %q %q {\n      %s id   = (known after apply)\n      %s tags = {\n          %s \"team\" = \"platform\"\n        }\n    }\n\n", a, verb, symbol, typ, name, symbol, symbol, symbol)
	}
	counts := map[string]int{symbol: len(addrs)}
	fmt.Fprintf(&b, "Plan: %d to add, %d to change, %d to destroy.\n", counts["+"], counts["~"], counts["-"])
	return b.String()
}

type stackOpt func(*v1.RunStack)

func withSummary(adds, changes, destroys, replaces int) stackOpt {
	return func(rs *v1.RunStack) {
		rs.Summary = &v1.PlanSummary{Adds: adds, Changes: changes, Destroys: destroys, Replaces: replaces}
	}
}

func withText(text string) stackOpt { return func(rs *v1.RunStack) { rs.PlanText = text } }

func withAddrs(added, changed, destroyed, replaced []string) stackOpt {
	return func(rs *v1.RunStack) {
		if rs.Summary == nil {
			rs.Summary = &v1.PlanSummary{}
		}
		rs.Summary.Added, rs.Summary.Changed, rs.Summary.Destroyed, rs.Summary.Replaced = added, changed, destroyed, replaced
	}
}

func withStatus(s v1.StackStatus) stackOpt { return func(rs *v1.RunStack) { rs.Status = s } }

func stack(key, env string, wave, job int, opts ...stackOpt) v1.RunStack {
	path, ws := v1.SplitStackKey(key)
	rs := v1.RunStack{
		StackID:     "stk-" + strings.NewReplacer("/", "-", ":", "-").Replace(key),
		Key:         key,
		Path:        path,
		Workspace:   ws,
		Environment: env,
		Wave:        wave,
		Status:      v1.StackPlanned,
		PlanOutput:  string(v1.PlanOutputFull),
	}
	if job > 0 {
		rs.JobURL = fmt.Sprintf("%s%d", jobBase, job)
		rs.PlanArtifact = v1.PlanArtifactName(key, headSHA)
	}
	for _, o := range opts {
		o(&rs)
	}
	return rs
}

func baseRun(status v1.RunStatus, stacks ...v1.RunStack) v1.Run {
	return v1.Run{
		ID:        runID,
		Repo:      "acme/infra",
		SHA:       headSHA,
		BaseSHA:   "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567",
		PRNumber:  42,
		Trigger:   v1.TriggerPullRequest,
		Mode:      v1.ModePlan,
		Status:    status,
		CreatedAt: t0,
		Waves:     3,
		Stacks:    stacks,
	}
}

func plannedStacks() []v1.RunStack {
	return []v1.RunStack{
		stack("stacks/prod/vpc", "production", 0, 101, withSummary(2, 1, 0, 0),
			withText(tfPlan("+", "aws_vpc.main", "aws_subnet.private"))),
		stack("stacks/staging/vpc", "staging", 0, 102, withSummary(0, 0, 0, 0),
			withText("No changes. Your infrastructure matches the configuration.\n")),
		stack("stacks/prod/eks", "production", 1, 103, withSummary(0, 3, 0, 1),
			withText(tfPlan("~", "aws_eks_cluster.main", "aws_eks_node_group.default", "aws_iam_role.nodes"))),
		stack("stacks/prod/secrets", "production", 1, 104, withSummary(1, 0, 1, 0),
			withAddrs([]string{`aws_secretsmanager_secret.db["primary"]`}, nil, []string{"aws_secretsmanager_secret.legacy"}, nil),
			func(rs *v1.RunStack) { rs.PlanOutput = string(v1.PlanOutputSummary) }),
		stack("stacks/prod/apps:blue", "production", 2, 105, withSummary(0, 0, 2, 0),
			withText(tfPlan("-", "kubernetes_deployment.api", "kubernetes_service.api"))),
	}
}

func applyRun(status v1.RunStatus, stacks ...v1.RunStack) v1.Run {
	run := baseRun(status, stacks...)
	run.Mode = v1.ModeApply
	run.Trigger = v1.TriggerComment
	run.RequestedBy = "alice"
	return run
}

func hugeRun(n, textBytes int) v1.Run {
	line := "  ~ resource \"aws_instance\" \"web\" { tags = { \"owner\" = \"platform-team\" } }\n"
	text := strings.Repeat(line, textBytes/len(line)+1)
	stacks := make([]v1.RunStack, 0, n)
	for i := range n {
		stacks = append(stacks, stack(fmt.Sprintf("stacks/prod/service-%02d", i), "production", i%3, 100+i, withSummary(i, 1, 0, 0), withText(text)))
	}
	return baseRun(v1.RunPlanned, stacks...)
}

func stickyCases() map[string]struct {
	run  v1.Run
	opts Options
} {
	planning := plannedStacks()
	planning[2].Status = v1.StackPlanning
	planning[2].Summary, planning[2].PlanText = nil, ""
	planning[3].Status = v1.StackPlanning
	planning[3].Summary = nil
	planning[4].Status = v1.StackPending
	planning[4].Summary, planning[4].PlanText, planning[4].JobURL = nil, "", ""

	planFailed := plannedStacks()
	planFailed[2].Status, planFailed[2].Summary, planFailed[2].ExitCode = v1.StackFailed, nil, intp(1)
	planFailed[2].PlanText = "Error: Reference to undeclared resource\n\n  on main.tf line 12, in resource \"aws_eks_cluster\" \"main\":\n  12:   role_arn = aws_iam_role.cluster.arn\n\nA managed resource \"aws_iam_role\" \"cluster\" has not been declared in the root module.\n"

	failed := plannedStacks()
	failed[0].Status, failed[0].ExitCode = v1.StackFailed, intp(1)
	failed[0].PlanText = "Error: creating EC2 VPC: VpcLimitExceeded: The maximum number of VPCs has been reached.\n\n  with aws_vpc.main,\n  on main.tf line 1, in resource \"aws_vpc\" \"main\":\n   1: resource \"aws_vpc\" \"main\" {\n"
	failed[1].Status = v1.StackNoop
	for _, i := range []int{2, 3, 4} {
		failed[i].Status = v1.StackBlocked
		failed[i].BlockedBy = []string{"stacks/prod/vpc"}
	}
	failedRun := applyRun(v1.RunFailed, failed...)

	applied := plannedStacks()
	for i := range applied {
		applied[i].Status = v1.StackApplied
	}
	applied[1].Status = v1.StackNoop

	unconfirmed := plannedStacks()[:3]
	for i := range unconfirmed {
		unconfirmed[i].Status = v1.StackUnconfirmed
		unconfirmed[i].JobURL = ""
	}
	unconfirmedRun := baseRun(v1.RunUnconfirmed, unconfirmed...)
	unconfirmedRun.Waves = 2

	superseded := plannedStacks()
	superseded[4].Status = v1.StackPlanning

	applying := plannedStacks()
	applying[0].Status, applying[1].Status = v1.StackApplied, v1.StackNoop
	applying[2].Status, applying[3].Status = v1.StackApplying, v1.StackApplying
	applying[4].Status = v1.StackPending
	applyingRun := applyRun(v1.RunApplying, applying...)
	applyingRun.CurrentWave = 1
	applyingOpts := testOpts
	applyingOpts.PendingApprovals = []Approval{
		{Environment: "production", URL: "https://github.com/acme/infra/actions/runs/7654321"},
	}

	lockedRun := baseRun(v1.RunPlanned, plannedStacks()...)
	lockedRun.Warnings = []string{
		"stacks/legacy/dns: terraform_remote_state reads bucket acme-tfstate key legacy/dns.tfstate, which matches no stack",
		"module source git::https://github.com/acme/modules//vpc?ref=main pins a branch, not a tag; cc @acme/platform",
	}
	lockedOpts := testOpts
	lockedOpts.Locks = []v1.LockInfo{
		{StackKey: "stacks/prod/eks", RunID: "9d8c7b6a-0000-4000-8000-000000000017", PRNumber: 17, TakenAt: t0.Add(-26 * time.Hour), Reason: "apply in progress"},
		{StackKey: "stacks/prod/vpc", RunID: runID, PRNumber: 42, TakenAt: t0},
	}

	quiet := []v1.RunStack{
		stack("stacks/prod/vpc", "production", 0, 201, withSummary(0, 0, 0, 0), withText("No changes. Your infrastructure matches the configuration.\n")),
		stack("stacks/prod/dns", "production", 1, 202, withSummary(0, 0, 0, 0)),
	}
	quietRun := baseRun(v1.RunPlanned, quiet...)
	quietRun.Waves = 2
	branded := testOpts
	branded.ServerName = "Acme Infra Bot"

	return map[string]struct {
		run  v1.Run
		opts Options
	}{
		"sticky_resolve_failed": {baseRun(v1.RunFailed), testOpts},
		"sticky_no_changes":     {quietRun, branded},
		"sticky_pending":        {baseRun(v1.RunPending), testOpts},
		"sticky_planning":       {baseRun(v1.RunPlanning, planning...), testOpts},
		"sticky_planned":        {baseRun(v1.RunPlanned, plannedStacks()...), testOpts},
		"sticky_no_stacks":      {baseRun(v1.RunPlanned), Options{}},
		"sticky_plan_failed":    {baseRun(v1.RunFailed, planFailed...), testOpts},
		"sticky_failed":         {failedRun, testOpts},
		"sticky_applying":       {applyingRun, applyingOpts},
		"sticky_applied":        {applyRun(v1.RunApplied, applied...), testOpts},
		"sticky_unconfirmed":    {unconfirmedRun, testOpts},
		"sticky_superseded":     {baseRun(v1.RunSuperseded, superseded...), testOpts},
		"sticky_locks_warned":   {lockedRun, lockedOpts},
	}
}
