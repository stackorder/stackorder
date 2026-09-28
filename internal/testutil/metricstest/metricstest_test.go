package metricstest_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/testutil/metricstest"
)

func TestScrapeAndValue(t *testing.T) {
	r := metrics.New()
	r.RunStatusChanged(v1.RunPlanned, v1.TriggerPush, v1.ModePlan)
	r.StackFinished(v1.ModeApply, v1.StackApplied, 3*time.Second)
	r.SetLocksHeld(4)

	samples := metricstest.Scrape(t, r)
	assert.InDelta(t, 1, samples[`stackorder_runs_total{mode="plan",status="planned",trigger="push"}`], 0)
	assert.InDelta(t, 1, samples[`stackorder_stack_duration_seconds_count{mode="apply"}`], 0)
	assert.InDelta(t, 3, samples[`stackorder_stack_duration_seconds_sum{mode="apply"}`], 1e-9)
	assert.InDelta(t, 1, samples[`stackorder_stack_duration_seconds_bucket{mode="apply",le="5"}`], 0)
	assert.InDelta(t, 4, metricstest.Value(t, r, "stackorder_locks_held"), 0)
	assert.Zero(t, metricstest.Value(t, r, `stackorder_webhook_received_total{event="push"}`))
}
