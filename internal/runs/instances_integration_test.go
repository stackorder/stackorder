//go:build integration

package runs_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
)

func instancesGraph(sha string, names ...string) v1.Graph {
	g := testGraph(sha)
	var stacks []v1.Stack
	for _, st := range g.Stacks {
		if st.Key != vpc {
			stacks = append(stacks, st)
			continue
		}
		for _, name := range names {
			key := v1.StackKey(vpc, name)
			stacks = append(stacks, v1.Stack{
				Key: key, Path: vpc, Instance: name, Environment: "dev",
				Backend: &v1.Backend{Type: "s3", Bucket: "state", Key: key + ".tfstate"},
				Config:  &v1.StackConfig{Instances: v1.Instances{name: {}}},
			})
		}
	}
	var edges []v1.Edge
	for _, ed := range g.Edges {
		switch {
		case ed.From.Key == vpc:
			for _, name := range names {
				e := ed
				e.From = v1.StackRef(v1.StackKey(vpc, name))
				edges = append(edges, e)
			}
		case ed.To.Key == vpc:
			ed.To = v1.StackRef(v1.StackKey(vpc, names[0]))
			edges = append(edges, ed)
		default:
			edges = append(edges, ed)
		}
	}
	g.Stacks, g.Edges = stacks, edges
	return g
}

func TestDefaultBranchPolicyFollowsTheDirectory(t *testing.T) {
	cfg := baseConfig()
	cfg.Apply.AllowedTeams = []string{"platform"}
	e := newEnv(t, cfg)
	e.gh.SetTeamMembership("acme", "platform", applier, gh.MembershipActive)
	onMain := testGraph(mainSHA)
	for i := range onMain.Stacks {
		if onMain.Stacks[i].Key == vpc {
			onMain.Stacks[i].Config = &v1.StackConfig{
				Environment: "vpc-protected",
				PlanOutput:  v1.PlanOutputSummary,
				Apply:       &v1.StackApplyConfig{AllowedTeams: []string{"acme/platform-prod"}},
			}
		}
	}
	id, _, err := e.st.SaveGraph(e.ctx, repoID, &onMain)
	require.NoError(t, err)
	require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, id))

	a, b := v1.StackKey(vpc, "a"), v1.StackKey(vpc, "b")
	pr := instancesGraph(headSHA, "a", "b")
	e.openPull(7, headSHA)
	job, runID, resp := e.startPlanGraph(7, pr)
	outputs := map[string]string{}
	for _, m := range resp.Matrix.Include {
		outputs[m.Key] = m.PlanOutput
	}
	assert.Equal(t, "summary", outputs[a], "an instance the default branch does not know keeps its directory's plan_output")
	assert.Equal(t, "summary", outputs[b])
	assert.Equal(t, "full", outputs[staging])
	for _, st := range resp.Affected {
		_, err := e.svc.RecordResult(e.ctx, job.p, runID, st.Key, planResult(st.Key, headSHA, 1))
		require.NoError(t, err)
	}

	e.comment(7, applier, "stackorder apply")
	body := e.lastComment(7)
	assert.Contains(t, body, "**`stackorder apply` was refused.** Nothing was dispatched.")
	assert.Contains(t, body, "**Layer 1, authorization** (`"+a+"`, `"+b+"`)")
	assert.Contains(t, body, "`acme/platform-prod`")
	assert.Empty(t, e.gh.Dispatches())

	e.gh.SetTeamMembership("acme", "platform-prod", applier, gh.MembershipActive)
	e.clock.Advance(2 * time.Minute)
	e.comment(7, applier, "stackorder apply")
	envs := map[string]string{}
	for _, d := range e.gh.Dispatches() {
		for _, en := range entries(t, d) {
			envs[en.Key] = en.Environment
		}
	}
	assert.Equal(t, "vpc-protected", envs[a], "the pull request's recorded environment never replaces the directory's default-branch one")
	assert.Equal(t, "vpc-protected", envs[b])
}
