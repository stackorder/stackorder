//go:build integration

package integration

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/testutil/faketf"
)

const (
	kycDir         = "infra/kyc"
	registryDir    = "infra/registry"
	kycProduction  = "infra/kyc:production"
	kycStaging     = "infra/kyc:staging"
	registryShared = "infra/registry:shared"
	stateBackend   = "infra/state.s3.tfbackend"
	sandboxBlue    = "stacks/sandbox/blue:blue"
)

var infraKeys = []string{kycProduction, kycStaging, registryShared}

func withInstances(t *testing.T, kycPrefix string) fixtureOption {
	return withEdit(func(root string) {
		for _, path := range []string{stateBackend, "infra/kyc/.stackorder.yaml", "infra/kyc/workspaces/production.tfvars.json", "infra/kyc/workspaces/staging.tfvars.json", "infra/registry/.stackorder.yaml"} {
			require.FileExists(t, filepath.Join(root, filepath.FromSlash(path)), "the vendored example holds the infra/ instances")
		}
		if kycPrefix != "" {
			editFile(t, root, "infra/kyc/.stackorder.yaml", func(s string) string { return kycPrefix + s })
		}
	})
}

func (f *fixture) instancePlans() {
	f.setStack(kycDir, func(b *faketf.Behavior) { b.PlanExit, b.ShowJSON = 2, fixturePath(f.t, "kyc.json") })
	f.setStack(kycStaging, func(b *faketf.Behavior) { b.PlanExit, b.ShowJSON = 2, fixturePath(f.t, "kyc-staging.json") })
	f.setStack(registryDir, func(b *faketf.Behavior) { b.PlanExit, b.ShowJSON = 2, fixturePath(f.t, "registry.json") })
}

func (f *fixture) branchEditing(branch, path, msg string, change func(string) string) string {
	f.t.Helper()
	f.co.branch(branch, f.co.base)
	return f.co.edit(path, msg, change)
}

func (f *fixture) kycBranch() string {
	return f.branchEditing("feature/kyc", "infra/kyc/main.tf", "feat(kyc): describe the instance size", describeInstanceSize(f.t))
}

func describeInstanceSize(t *testing.T) func(string) string {
	return func(s string) string {
		const old = "variable \"instance_size\" {\n  type = string\n}"
		require.Contains(t, s, old, "infra/kyc/main.tf declares instance_size")
		return strings.Replace(s, old, "variable \"instance_size\" {\n  type        = string\n  description = \"Instance size of the environment.\"\n}", 1)
	}
}

func matrixByKey(m v1.Matrix) map[string]v1.MatrixEntry {
	out := make(map[string]v1.MatrixEntry, len(m.Include))
	for _, e := range m.Include {
		out[e.Key] = e
	}
	return out
}

func TestStackInstancesPlanAndApply(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "instances-apply", withInstances(t, ""))
	f.instancePlans()
	head := f.kycBranch()
	const pr = 50
	ev := f.openPR(pr, head, "feature/kyc")
	p := f.plan(ev)

	entries := matrixByKey(p.matrix)
	require.ElementsMatch(t, infraKeys, keysOf(entries), "a change under infra/kyc affects both of its instances and the dependent registry")
	for key, want := range map[string]struct {
		stack, instance string
		wave            int
	}{
		kycProduction:  {kycDir, "production", 0},
		kycStaging:     {kycDir, "staging", 0},
		registryShared: {registryDir, "shared", 1},
	} {
		entry := entries[key]
		assert.Equal(t, want.stack, entry.Stack, key)
		assert.Equal(t, want.instance, entry.Instance, key)
		assert.Empty(t, entry.Workspace, "an instance is not a Terraform workspace: %s", key)
		assert.Equal(t, want.wave, entry.Wave, key)
	}

	run := e.run(p.runID)
	stacks := runStacks(run)
	assert.Equal(t, []v1.Reason{v1.ReasonChanged}, stacks[kycProduction].Reasons)
	assert.Equal(t, []v1.Reason{v1.ReasonChanged}, stacks[kycStaging].Reasons)
	assert.Contains(t, stacks[registryShared].Reasons, v1.ReasonDependent)
	for _, key := range infraKeys {
		assert.Equal(t, v1.StackPlanned, stacks[key].Status, key)
		assert.Equal(t, strings.TrimSuffix(key, ":"+stacks[key].Instance), stacks[key].Path, key)
	}
	assert.Equal(t, "production", stacks[kycProduction].Instance)
	assert.Equal(t, "shared", stacks[registryShared].Instance)

	var planChecks []string
	for name := range f.checks(head) {
		if rest, ok := strings.CutPrefix(name, report.StackCheckName(report.CheckPlan, "")); ok && rest != "" {
			planChecks = append(planChecks, rest)
		}
	}
	slices.Sort(planChecks)
	assert.Equal(t, infraKeys, planChecks, "one plan check per instance key and none for the bare directory")
	for _, key := range infraKeys {
		assert.Equal(t, report.ConclusionSuccess, f.check(head, report.StackCheckName(report.CheckPlan, key)).Conclusion, key)
	}

	f.approve(pr, reviewer, head)
	cmd := f.comment(pr, applier, applyCommand)
	require.Equal(t, []string{gh.ReactionEyes, gh.ReactionRocket}, e.GH.Reactions(cmd.ID), "the apply is accepted")
	all := f.dispatches("")
	require.NotEmpty(t, all)
	runID := all[0].Inputs["run_id"]
	wave0 := f.waitDispatches(runID, 0, 2)
	for i, want := range []struct{ key, instance, env string }{
		{kycProduction, "production", "production"},
		{kycStaging, "staging", "staging"},
	} {
		d := wave0[i]
		assert.Equal(t, string(v1.ModeApply), d.Inputs["mode"])
		got := dispatchEntries(t, d)
		require.Len(t, got, 1, "wave 0 is dispatched once per environment")
		assert.Equal(t, want.key, got[0].Key)
		assert.Equal(t, kycDir, got[0].Stack)
		assert.Equal(t, want.instance, got[0].Instance)
		assert.Equal(t, want.env, got[0].Environment, "an unmapped instance runs under the GitHub environment of its name")
		assert.Empty(t, got[0].Workspace)
		assert.Equal(t, v1.PlanArtifactName(want.key, head), got[0].Artifact)
	}

	locks := f.locks()
	assert.Equal(t, infraKeys, keysOf(locks), "every instance is locked on its own")
	ids := map[string]bool{}
	for key, l := range locks {
		assert.Equal(t, runID, l.RunID.String(), key)
		ids[l.StackID.String()] = true
	}
	assert.Len(t, ids, 3, "the instances of one directory are separate stacks")

	applied := e.run(runID)
	for key, rs := range runStacks(applied) {
		assert.Equal(t, strings.TrimPrefix(key, rs.Path+":"), rs.Instance, key)
	}
	assert.Equal(t, "staging", runStacks(applied)[kycStaging].Environment)

	for _, d := range wave0 {
		for key, res := range f.apply(d, nil) {
			requireExit(t, 0, res)
			assert.NotEmpty(t, res.outputs["summary"], key)
		}
		f.complete(d, "success")
	}
	wave1 := f.waitDispatches(runID, 1, 1)
	got := dispatchEntries(t, wave1[0])
	require.Len(t, got, 1)
	assert.Equal(t, registryShared, got[0].Key)
	assert.Equal(t, registryDir, got[0].Stack)
	assert.Equal(t, "shared", got[0].Instance)
	assert.Equal(t, "shared", got[0].Environment)
	requireExit(t, 0, f.apply(wave1[0], nil)[registryShared])
	f.complete(wave1[0], "success")

	done := e.waitRun(runID, v1.RunApplied)
	for _, key := range infraKeys {
		assert.Equal(t, v1.StackApplied, runStacks(done)[key].Status, key)
	}
	assert.Len(t, f.dispatches(runID), 3, "production and staging in wave 0, shared in wave 1")

	detail := f.stackDetail(kycProduction)
	assert.Equal(t, "production", detail.Instance)
	assert.Equal(t, kycDir, detail.Path)
	assert.Empty(t, detail.Workspace)
	assert.Equal(t, "production", detail.Environment)
	if assert.NotNil(t, detail.Backend) {
		assert.Equal(t, "kyc/production.tfstate", detail.Backend.Key, "the rendered backend_config names the state object")
		assert.Equal(t, "stackorder-example-state", detail.Backend.Bucket)
	}
	var list v1.Page[v1.StackDetail]
	e.getJSON("/v1/repos/"+f.name+"/stacks", &list)
	instances := map[string]string{}
	for _, st := range list.Items {
		if strings.HasPrefix(st.Key, "infra/") {
			instances[st.Key] = st.Instance
		}
	}
	assert.Equal(t, map[string]string{kycProduction: "production", kycStaging: "staging", registryShared: "shared"}, instances)

	t.Run("the CLI runs each instance with its own var file, env and backend", func(t *testing.T) {
		assertInstanceCalls(t, f, p)
	})
}

func callsFor(t *testing.T, f *fixture, key, command string) []faketf.Call {
	t.Helper()
	var out []faketf.Call
	for _, c := range faketf.Calls(t, f.tf.Log) {
		if len(c.Args) > 0 && c.Args[0] == command && c.Stack() == key {
			out = append(out, c)
		}
	}
	return out
}

func assertInstanceCalls(t *testing.T, f *fixture, p *planned) {
	root := f.co.dir
	for _, want := range []struct {
		key, dir, instance, varFile string
	}{
		{kycProduction, kycDir, "production", "workspaces/production.tfvars.json"},
		{kycStaging, kycDir, "staging", "workspaces/staging.tfvars.json"},
		{registryShared, registryDir, "shared", ""},
	} {
		inits := callsFor(t, f, want.key, "init")
		if assert.NotEmpty(t, inits, "init calls of %s carry STACKORDER_STACK", want.key) {
			for _, c := range inits {
				assert.Contains(t, c.Args, "-reconfigure", want.key)
				assert.Contains(t, c.Args, "-backend-config="+filepath.Join(root, filepath.FromSlash(stateBackend)), want.key)
				assert.Contains(t, c.Args, "-backend-config=key="+strings.TrimPrefix(want.dir, "infra/")+"/"+want.instance+".tfstate", want.key)
			}
		}
		plans := callsFor(t, f, want.key, "plan")
		if assert.NotEmpty(t, plans, "plan calls of %s", want.key) {
			c := plans[0]
			assert.Equal(t, want.instance, c.Env["TF_VAR_environment"], want.key)
			assert.Equal(t, "plan", c.Env["TF_VAR_role"], want.key)
			assert.Equal(t, want.instance, c.Env["STACKORDER_INSTANCE"], want.key)
			assert.Equal(t, want.dir, c.Env["STACKORDER_STACK_PATH"], want.key)
			if want.varFile != "" {
				assert.Contains(t, c.Args, "-var-file="+want.varFile, want.key)
			} else {
				assert.False(t, slices.ContainsFunc(c.Args, func(a string) bool { return strings.HasPrefix(a, "-var-file=") }), want.key)
			}
		}
		applies := callsFor(t, f, want.key, "apply")
		if assert.Len(t, applies, 1, "apply calls of %s", want.key) {
			assert.Equal(t, "deploy", applies[0].Env["TF_VAR_role"], "the apply mode's value of env: %s", want.key)
			assert.Equal(t, want.instance, applies[0].Env["TF_VAR_environment"], want.key)
		}
	}
	assert.Contains(t, p.plans[kycStaging].outputs["summary"], `"changes":1`,
		"infra/kyc:staging answers with the behaviour keyed by its stack key")
	assert.Contains(t, p.plans[kycProduction].outputs["summary"], `"adds":1`,
		"infra/kyc:production falls back to the behaviour of its directory")
}

func TestStackInstanceAllowedTeams(t *testing.T) {
	e := shared(t)
	kycConfig := "instances:\n" +
		"  production:\n" +
		"    apply:\n" +
		"      allowed_teams: [kyc-prod]\n" +
		"  staging: {}\n"
	f := newFixture(t, e, "instances-teams", withInstances(t, kycConfig))
	e.GH.SetTeamMembership(acmeOrg, "kyc-prod", reviewer, gh.MembershipActive)
	f.instancePlans()
	head := f.kycBranch()
	const pr = 51
	ev := f.openPR(pr, head, "feature/kyc")
	p := f.plan(ev)
	require.ElementsMatch(t, infraKeys, keysOf(matrixByKey(p.matrix)))
	f.approve(pr, reviewer, head)

	cmd := f.comment(pr, applier, applyCommand)
	replies := f.botComments(pr, cmd.ID)
	require.Len(t, replies, 1, "one reply to the command")
	body := replies[0].Body
	assert.True(t, strings.HasPrefix(body, "**`stackorder apply` was refused.** Nothing was dispatched."), body)
	assert.Contains(t, body, "**Layer 1, authorization** (`infra/kyc:production`): carol is not an active member of `acme/kyc-prod`, which apply.allowed_teams requires")
	for line := range strings.Lines(body) {
		if strings.Contains(line, "Layer 1") {
			assert.NotContains(t, line, kycStaging, "the override of one instance does not bind its sibling")
			assert.NotContains(t, line, registryShared)
		}
	}
	assert.Equal(t, []string{gh.ReactionEyes}, e.GH.Reactions(cmd.ID))
	assert.Empty(t, f.dispatches(""))
	assert.Empty(t, f.locks())

	cmd = f.comment(pr, reviewer, applyCommand)
	require.Equal(t, []string{gh.ReactionEyes, gh.ReactionRocket}, e.GH.Reactions(cmd.ID), "a member of the instance's team may apply it")
	all := f.dispatches("")
	require.NotEmpty(t, all)
	wave0 := f.waitDispatches(all[0].Inputs["run_id"], 0, 2)
	assert.Equal(t, kycProduction, dispatchEntries(t, wave0[0])[0].Key)
	assert.Equal(t, infraKeys, keysOf(f.locks()))
}

func TestStackInstancesWatchPath(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "instances-watch", withInstances(t, ""))
	f.instancePlans()
	head := f.branchEditing("feature/state-bucket", stateBackend, "chore(infra): name the state region", func(s string) string {
		return strings.Replace(s, "region       = \"us-east-1\"\n", "region       = \"us-east-2\"\n", 1)
	})
	ev := f.openPR(52, head, "feature/state-bucket")
	p := f.plan(ev)

	entries := matrixByKey(p.matrix)
	require.ElementsMatch(t, infraKeys, keysOf(entries), "the shared backend config file affects every instance that reads it")
	assert.Equal(t, 1, entries[registryShared].Wave, "the registry still applies after infra/kyc:production")
	stacks := runStacks(e.run(p.runID))
	for _, key := range infraKeys {
		assert.Contains(t, stacks[key].Reasons, v1.ReasonWatchPath, key)
		assert.NotContains(t, stacks[key].Reasons, v1.ReasonChanged, key)
	}

	var view v1.GraphView
	e.getJSON("/v1/repos/"+f.name+"/graph?run="+p.runID, &view)
	regions := map[string]string{}
	for _, st := range view.Graph.Stacks {
		if slices.Contains(infraKeys, st.Key) {
			assert.Contains(t, st.WatchPaths, stateBackend, st.Key)
			if assert.NotNil(t, st.Backend, st.Key) {
				regions[st.Key] = st.Backend.Region
			}
		}
	}
	assert.Equal(t, map[string]string{kycProduction: "us-east-2", kycStaging: "us-east-2", registryShared: "us-east-2"}, regions,
		"the graph of the pull request carries the changed backend config file, not a stored graph of the base")
}

func TestLegacyWorkspaceStackEnvironment(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "instances-legacy", withInstances(t, ""), withConfig(func(c string) string {
		return strings.Replace(c, "  \"stacks/staging/\": staging\n", "  \"stacks/staging/\": staging\n  \":staging\": kyc-staging\n", 1)
	}))
	f.instancePlans()
	f.setStack(sandboxBlue, func(b *faketf.Behavior) { b.PlanExit, b.ShowJSON = 2, fixturePath(t, "kyc.json") })
	f.co.branch("feature/sandbox", f.co.base)
	for path, content := range map[string]string{
		"stacks/sandbox/blue/.stackorder.yaml": "workspace: blue\n",
		"stacks/sandbox/blue/backend.tf":       "terraform {\n  backend \"s3\" {\n    bucket = \"stackorder-example-state\"\n    key    = \"sandbox/blue.tfstate\"\n    region = \"us-east-1\"\n  }\n}\n",
		"stacks/sandbox/blue/main.tf":          "resource \"terraform_data\" \"marker\" {}\n",
	} {
		f.co.edit(path, "feat(sandbox): add "+path, func(string) string { return content })
	}
	head := f.co.edit("infra/kyc/main.tf", "feat(kyc): describe the instance size", describeInstanceSize(t))
	const pr = 53
	ev := f.openPR(pr, head, "feature/sandbox")
	p := f.plan(ev)

	entries := matrixByKey(p.matrix)
	require.ElementsMatch(t, append([]string{sandboxBlue}, infraKeys...), keysOf(entries))
	blue := entries[sandboxBlue]
	assert.Equal(t, "blue", blue.Instance, "a legacy workspace stack is the one instance named after its workspace")
	assert.Equal(t, "blue", blue.Workspace)
	assert.Equal(t, "blue", blue.Environment, "without an environment key a legacy workspace stack is protected by the environment of its instance")
	assert.Equal(t, "kyc-staging", entries[kycStaging].Environment, "an :instance key of environments maps that instance in any directory")
	assert.Equal(t, "production", entries[kycProduction].Environment, "the :staging key does not bind the sibling instance")

	f.approve(pr, reviewer, head)
	cmd := f.comment(pr, applier, applyCommand)
	require.Equal(t, []string{gh.ReactionEyes, gh.ReactionRocket}, e.GH.Reactions(cmd.ID), "the apply is accepted")
	all := f.dispatches("")
	require.NotEmpty(t, all)
	wave0 := f.waitDispatches(all[0].Inputs["run_id"], 0, 3)
	groups := make([]string, 0, len(wave0))
	for _, d := range wave0 {
		got := dispatchEntries(t, d)
		require.Len(t, got, 1, "wave 0 is dispatched once per environment")
		groups = append(groups, got[0].Environment+"="+got[0].Key)
	}
	assert.Equal(t, []string{"blue=" + sandboxBlue, "kyc-staging=" + kycStaging, "production=" + kycProduction}, groups)

	res := f.apply(wave0[0], nil)[sandboxBlue]
	requireExit(t, 0, res)
	applies := callsFor(t, f, sandboxBlue, "apply")
	require.Len(t, applies, 1, "the job bound to the blue environment applied the stack")
}
