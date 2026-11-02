//go:build integration

package store_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func resourceList(addresses ...string) v1.ResourceList {
	list := v1.ResourceList{Total: len(addresses), Items: []v1.StackResource{}}
	for _, a := range addresses {
		list.Items = append(list.Items, v1.StackResource{Address: a, Type: "aws_vpc", Provider: "registry.terraform.io/hashicorp/aws"})
	}
	return list
}

func (f *fixture) requireResources(stackID uuid.UUID, wantRun *uuid.UUID, want v1.ResourceList) store.StackResources {
	f.t.Helper()
	got, err := f.s.GetStackResources(f.ctx, stackID)
	require.NoError(f.t, err)
	assert.Equal(f.t, wantRun, got.RunID)
	assert.Equal(f.t, want.Total, got.Total)
	assert.Equal(f.t, want.Truncated, got.Truncated)
	if diff := cmp.Diff(want.Items, got.Resources); diff != "" {
		f.t.Errorf("recorded resources (-want +got):\n%s", diff)
	}
	return got
}

func (f *fixture) backdate(runID uuid.UUID, ago time.Duration) {
	f.t.Helper()
	f.exec(`UPDATE runs SET created_at = now() - make_interval(secs => $2) WHERE id = $1`, runID, ago.Seconds())
}

func TestSetStackResources(t *testing.T) {
	f := newFixture(t)
	_, ids := f.saveGraph(f.repo.ID, sampleGraph(f.repo.FullName, "sha1"))
	vpcID := ids["stacks/prod/vpc"]

	_, err := f.s.GetStackResources(f.ctx, vpcID)
	require.ErrorIs(t, err, store.ErrNotFound, "no job has reported a list yet")

	apply := f.run(store.CreateRunParams{Mode: v1.ModeApply, Trigger: v1.TriggerComment, SHA: "sha-apply"})
	f.backdate(apply.ID, 2*time.Hour)
	waitDrift := f.run(store.CreateRunParams{Mode: v1.ModeDrift, Trigger: v1.TriggerSchedule, SHA: "sha-drift"})
	f.backdate(waitDrift.ID, time.Hour)

	before := resourceList("aws_vpc.main")
	require.NoError(t, f.s.SetStackResources(f.ctx, vpcID, waitDrift.ID, before, false))
	first := f.requireResources(vpcID, &waitDrift.ID, before)
	assert.Equal(t, v1.ModeDrift, first.RunMode)
	assert.Equal(t, "sha-drift", first.SHA)

	after := resourceList("aws_subnet.a", "aws_vpc.main")
	require.NoError(t, f.s.SetStackResources(f.ctx, vpcID, apply.ID, after, false))
	applied := f.requireResources(vpcID, &apply.ID, after)
	assert.Equal(t, v1.ModeApply, applied.RunMode)
	assert.Equal(t, "sha-apply", applied.SHA)
	assert.True(t, applied.ObservedAt.After(first.ObservedAt),
		"an apply whose run is older than a drift run created during its approval wait still replaces that drift's list")

	lateDrift := f.run(store.CreateRunParams{Mode: v1.ModeDrift, Trigger: v1.TriggerSchedule})
	f.backdate(lateDrift.ID, 10*time.Minute)
	require.NoError(t, f.s.SetStackResources(f.ctx, vpcID, lateDrift.ID, before, false))
	assert.Equal(t, applied, f.requireResources(vpcID, &apply.ID, after),
		"a drift run created before the apply's list was recorded cannot replace it, even posting after it")

	require.NoError(t, f.s.SetStackResources(f.ctx, vpcID, apply.ID, before, true))
	assert.Equal(t, applied, f.requireResources(vpcID, &apply.ID, after),
		"an unconfirmed apply result counts as read when its run was created")

	again := v1.ResourceList{Total: 3, Truncated: true, Items: []v1.StackResource{
		{Address: "module.b.aws_vpc.x", Type: "aws_vpc", Module: "module.b", Tainted: true},
		{Address: "aws_vpc.main", Type: "aws_vpc", Deposed: true, LastChange: &v1.ResourceChange{Action: v1.ResourceCreate, RunID: "forged"}},
	}}
	require.NoError(t, f.s.SetStackResources(f.ctx, vpcID, apply.ID, again, false))
	reported := f.requireResources(vpcID, &apply.ID, v1.ResourceList{Total: 3, Truncated: true, Items: []v1.StackResource{
		{Address: "aws_vpc.main", Type: "aws_vpc", Deposed: true},
		{Address: "module.b.aws_vpc.x", Type: "aws_vpc", Module: "module.b", Tainted: true},
	}})
	assert.True(t, reported.ChangedAt.After(applied.ChangedAt), "the same run may report again; a different list moves changed_at")

	require.NoError(t, f.s.SetStackResources(f.ctx, vpcID, apply.ID, again, false))
	unchanged := f.requireResources(vpcID, &apply.ID, v1.ResourceList{Total: 3, Truncated: true, Items: reported.Resources})
	assert.True(t, unchanged.ChangedAt.Equal(reported.ChangedAt), "an unchanged list keeps changed_at")
	assert.True(t, unchanged.ReportedAt.After(reported.ReportedAt), "an unchanged list still moves reported_at")

	nextDrift := f.run(store.CreateRunParams{Mode: v1.ModeDrift, Trigger: v1.TriggerSchedule, SHA: "sha-next"})
	f.exec(`UPDATE runs SET created_at = now() + interval '1 minute' WHERE id = $1`, nextDrift.ID)
	empty := v1.ResourceList{}
	require.NoError(t, f.s.SetStackResources(f.ctx, vpcID, nextDrift.ID, empty, false))
	emptied := f.requireResources(vpcID, &nextDrift.ID, v1.ResourceList{Items: []v1.StackResource{}})
	assert.NotNil(t, emptied.Resources, "an empty list is recorded, unlike no list")
	assert.Equal(t, []v1.StackResource{}, emptied.ToV1().Items)

	_, err = f.s.GetStackResources(f.ctx, ids["stacks/prod/eks"])
	require.ErrorIs(t, err, store.ErrNotFound, "only the stack reported on gets a list")

	plan := f.run(store.CreateRunParams{Mode: v1.ModePlan})
	other := f.addRepo(1, 101, "acme", "acme/network")
	foreign := f.run(store.CreateRunParams{RepoID: other.ID, Mode: v1.ModeApply, Trigger: v1.TriggerPush})
	cases := []struct {
		name    string
		stackID uuid.UUID
		runID   uuid.UUID
		want    error
	}{
		{"unknown stack", uuid.New(), apply.ID, store.ErrNotFound},
		{"unknown run", vpcID, uuid.New(), store.ErrNotFound},
		{"plan run", vpcID, plan.ID, store.ErrInvalid},
		{"apply run of another repository", vpcID, foreign.ID, store.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, f.s.SetStackResources(f.ctx, tc.stackID, tc.runID, after, false), tc.want)
		})
	}
	f.requireResources(vpcID, &nextDrift.ID, v1.ResourceList{Items: []v1.StackResource{}})

	f.exec(`DELETE FROM runs WHERE id = $1`, nextDrift.ID)
	kept := f.requireResources(vpcID, nil, v1.ResourceList{Items: []v1.StackResource{}})
	assert.Equal(t, v1.ModeDrift, kept.RunMode, "the list outlives the run that reported it")
	assert.Empty(t, kept.ToV1().RunID)
	assert.Equal(t, "sha-next", kept.ToV1().SHA)
}

func TestStackResourceChanges(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/prod/vpc", "stacks/prod/eks")
	vpcID, eksID := ids["stacks/prod/vpc"], ids["stacks/prod/eks"]

	type row struct {
		stack   uuid.UUID
		status  v1.StackStatus
		summary v1.PlanSummary
	}
	runAt := func(mode v1.RunMode, pr int, ago time.Duration, rows ...row) store.Run {
		t.Helper()
		r := f.run(store.CreateRunParams{Mode: mode, Trigger: v1.TriggerComment, PRNumber: pr, SHA: "sha-" + uuid.NewString()[:8]})
		f.backdate(r.ID, ago)
		for _, rw := range rows {
			require.NoError(t, f.s.UpsertRunStacks(f.ctx, r.ID, []store.RunStack{{StackID: rw.stack}}))
			summary := rw.summary
			_, err := f.s.UpdateRunStack(f.ctx, r.ID, rw.stack, store.RunStackPatch{Status: &rw.status, Summary: &summary})
			require.NoError(t, err)
		}
		return r
	}

	first := runAt(v1.ModeApply, 1, 6*time.Hour, row{vpcID, v1.StackApplied, v1.PlanSummary{
		Added: []string{"aws_vpc.main", "aws_subnet.a", `aws_route.r["10.0.0.0/16"]`}, Destroyed: []string{"aws_eip.old"},
	}})
	second := runAt(v1.ModeApply, 2, 5*time.Hour, row{vpcID, v1.StackApplied, v1.PlanSummary{
		Changed: []string{"aws_vpc.main"}, Destroyed: []string{"aws_subnet.a"},
	}})
	third := runAt(v1.ModeApply, 0, 4*time.Hour, row{vpcID, v1.StackApplied, v1.PlanSummary{
		Added: []string{"aws_subnet.a"}, Replaced: []string{"aws_instance.web"}, Destroyed: []string{"aws_instance.web"},
	}})
	runAt(v1.ModeApply, 4, 3*time.Hour, row{vpcID, v1.StackSkipped, v1.PlanSummary{Changed: []string{"aws_vpc.main"}}})
	runAt(v1.ModeApply, 5, 2*time.Hour, row{vpcID, v1.StackNoop, v1.PlanSummary{Changed: []string{"aws_vpc.main"}}})
	runAt(v1.ModeApply, 6, time.Hour, row{vpcID, v1.StackFailed, v1.PlanSummary{Replaced: []string{"aws_vpc.main", "aws_subnet.a"}}})
	runAt(v1.ModeApply, 7, 50*time.Minute, row{vpcID, v1.StackUnconfirmed, v1.PlanSummary{Changed: []string{"aws_subnet.a"}}})
	runAt(v1.ModePlan, 8, 40*time.Minute, row{vpcID, v1.StackPlanned, v1.PlanSummary{Changed: []string{"aws_vpc.main"}}})
	runAt(v1.ModeDrift, 0, 30*time.Minute, row{vpcID, v1.StackPlanned, v1.PlanSummary{Changed: []string{"aws_vpc.main"}}})
	runAt(v1.ModeApply, 9, 20*time.Minute, row{eksID, v1.StackApplied, v1.PlanSummary{Changed: []string{"aws_vpc.main"}}})

	asked := []string{"aws_vpc.main", "aws_subnet.a", "aws_eip.old", "aws_instance.web", `aws_route.r["10.0.0.0/16"]`, "aws_never.changed"}
	got, err := f.s.StackResourceChanges(f.ctx, vpcID, asked)
	require.NoError(t, err)
	for _, c := range got {
		require.NotNil(t, c.FinishedAt, c.Address)
	}
	type change struct {
		Address  string
		Action   v1.ResourceAction
		RunID    uuid.UUID
		SHA      string
		PRNumber int
	}
	var compact []change
	for _, c := range got {
		compact = append(compact, change{c.Address, c.Action, c.RunID, c.SHA, c.PRNumber})
	}
	want := []change{
		{"aws_instance.web", v1.ResourceReplace, third.ID, third.SHA, 0},
		{`aws_route.r["10.0.0.0/16"]`, v1.ResourceCreate, first.ID, first.SHA, 1},
		{"aws_subnet.a", v1.ResourceCreate, third.ID, third.SHA, 0},
		{"aws_vpc.main", v1.ResourceUpdate, second.ID, second.SHA, 2},
	}
	if diff := cmp.Diff(want, compact); diff != "" {
		t.Errorf("last changes (-want +got):\n%s", diff)
	}

	only, err := f.s.StackResourceChanges(f.ctx, vpcID, []string{"aws_vpc.main"})
	require.NoError(t, err)
	require.Len(t, only, 1, "only the asked addresses are returned")
	assert.Equal(t, v1.ResourceChange{Action: v1.ResourceUpdate, RunID: second.ID.String(), SHA: second.SHA, PRNumber: 2, FinishedAt: only[0].FinishedAt}, only[0].ToV1())

	gone := runAt(v1.ModeApply, 10, 10*time.Minute, row{vpcID, v1.StackApplied, v1.PlanSummary{Destroyed: []string{"aws_vpc.main"}}})
	require.NotEqual(t, uuid.Nil, gone.ID)
	destroyed, err := f.s.StackResourceChanges(f.ctx, vpcID, []string{"aws_vpc.main"})
	require.NoError(t, err)
	assert.Empty(t, destroyed, "an address whose newest applied change destroyed it has no last change")

	none, err := f.s.StackResourceChanges(f.ctx, vpcID, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}
