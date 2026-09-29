//go:build integration

package integration

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func (e *Env) metrics() map[string]float64 {
	e.t.Helper()
	status, body := e.Get("/metrics")
	require.Equal(e.t, http.StatusOK, status)
	out := map[string]float64{}
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		v, err := strconv.ParseFloat(line[i+1:], 64)
		require.NoError(e.t, err, line)
		out[line[:i]] = v
	}
	return out
}

func TestMetricsAfterTheFlows(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "metrics")
	a := f.startApply(110, applyCommand)
	for _, d := range a.wave0 {
		for _, res := range f.apply(d, nil) {
			requireExit(t, 0, res)
		}
		f.complete(d, "success")
	}
	e.waitRun(a.runID, v1.RunApplied)
	f.merge(a.ev, f.co.merge(f.co.head, "Merge pull request #110"), applier)
	other := f.openPR(111, secondBranch(f), "feature/vpc-outputs")
	f.plan(other)
	f.approve(111, reviewer, other.PullRequest.HeadSHA)
	f.comment(111, applier, applyCommand)
	require.Len(t, f.locks(), 5)

	m := e.metrics()
	atLeast := map[string]float64{
		`stackorder_runs_total{mode="plan",status="pending",trigger="pull_request"}`:  2,
		`stackorder_runs_total{mode="plan",status="planning",trigger="pull_request"}`: 2,
		`stackorder_runs_total{mode="plan",status="planned",trigger="pull_request"}`:  2,
		`stackorder_runs_total{mode="apply",status="applying",trigger="comment"}`:     2,
		`stackorder_runs_total{mode="apply",status="applied",trigger="comment"}`:      1,
		`stackorder_stack_finished_total{mode="plan",status="planned"}`:               10,
		`stackorder_stack_finished_total{mode="apply",status="applied"}`:              2,
		`stackorder_dispatches_total{mode="apply",result="ok"}`:                       4,
		`stackorder_commands_total{accepted="true",verb="apply"}`:                     2,
		`stackorder_webhook_received_total{event="pull_request"}`:                     3,
		`stackorder_webhook_received_total{event="issue_comment"}`:                    2,
		`stackorder_webhook_received_total{event="workflow_run"}`:                     2,
	}
	for name, min := range atLeast {
		v, ok := m[name]
		if assert.True(t, ok, "%s is exported", name) {
			assert.GreaterOrEqual(t, v, min, name)
		}
	}
	locks, err := e.Store.ListLocks(t.Context(), 0)
	require.NoError(t, err)
	require.Contains(t, m, "stackorder_locks_held")
	assert.Equal(t, float64(len(locks)), m["stackorder_locks_held"], "the gauge counts every lock held on the server")
	assert.GreaterOrEqual(t, m["stackorder_locks_held"], 5.0)
	assert.Contains(t, m, "stackorder_drifted_stacks")
	assert.Contains(t, m, `stackorder_http_requests_total{method="POST",route="POST /v1/runs/{id}/stacks/{key}/result",status="200"}`)
}
