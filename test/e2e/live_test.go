//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/test/e2e/live"
)

const liveBranch = "e2e-vpc-route-table"

func liveConfig(t *testing.T) (live.Config, bool) {
	t.Helper()
	suffix := time.Now().UTC().Format("20060102-150405") + "-" + strings.ToLower(rand.Text()[:6])
	cfg, ok, err := live.FromEnv(os.Getenv, suffix)
	require.NoError(t, err)
	return cfg, ok
}

func TestLiveGitHub(t *testing.T) {
	cfg, ok := liveConfig(t)
	if !ok {
		t.Skipf("%s, %s and %s are unset; the live GitHub variant needs an organisation with the App installed and a server GitHub can reach",
			live.EnvToken, live.EnvOrg, live.EnvServerURL)
	}
	repo, base := newExampleRepo(t, exampleSource(t), v1.ToolTerraform)
	head := repo.commit(t, liveBranch, base, "feat(vpc): add a route table",
		appendFile("modules/vpc/main.tf", routeTableHCL),
		appendFile("modules/vpc/outputs.tf", routeTableOutputHCL))

	r := &live.Runner{Config: cfg, Dir: repo.dir, Logf: t.Logf}
	if cfg.KeepRepo {
		t.Logf("e2e: %s is kept: %s/%s", live.EnvKeepRepo, cfg.WebURL, cfg.FullName())
	} else {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := r.Run(ctx, []live.Step{live.Cleanup(cfg)}); err != nil {
				t.Errorf("e2e: %v", err)
			}
		})
	}

	checks := []string{
		report.CheckResolve,
		report.CheckPlan,
		report.StackCheckName(report.CheckPlan, prodVPC),
		report.StackCheckName(report.CheckPlan, stagingVPC),
	}
	res, err := r.Run(t.Context(), live.Plan(cfg, live.Change{
		BaseSHA: base, HeadSHA: head, Branch: liveBranch, Title: "feat(vpc): add a route table", Checks: checks,
	}))
	require.NoError(t, err)
	assert.Positive(t, res.PullNumber, "the pull request was opened")
	resolve := res.Checks[report.CheckResolve]
	assert.Equal(t, gh.ConclusionSuccess, resolve.Conclusion, "the server resolved the change: %s", resolve.HTMLURL)
	if cfg.PlanRoleARN == "" {
		t.Logf("e2e: %s is unset, so the plan jobs cannot read state and only the resolve check must succeed", live.EnvPlanRoleARN)
		return
	}
	for _, name := range checks {
		c := res.Checks[name]
		assert.Equal(t, gh.ConclusionSuccess, c.Conclusion, "%s: %s", name, c.HTMLURL)
	}
}
