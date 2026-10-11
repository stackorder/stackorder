//go:build integration

package store_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func (f *fixture) stackFromEveryGetter(id uuid.UUID) map[string]store.Stack {
	f.t.Helper()
	byID, err := f.s.GetStack(f.ctx, id)
	require.NoError(f.t, err)
	byKey, err := f.s.GetStackByKey(f.ctx, byID.RepoID, byID.Key)
	require.NoError(f.t, err)
	listed, err := f.s.ListStacks(f.ctx, byID.RepoID, true)
	require.NoError(f.t, err)
	i := slices.IndexFunc(listed, func(st store.Stack) bool { return st.ID == id })
	require.NotEqual(f.t, -1, i, "ListStacks returns the stack")
	return map[string]store.Stack{"GetStack": byID, "GetStackByKey": byKey, "ListStacks": listed[i]}
}

func (f *fixture) requireStateObject(id uuid.UUID, want *v1.StateObject, wantRun *uuid.UUID) time.Time {
	f.t.Helper()
	var at time.Time
	for getter, st := range f.stackFromEveryGetter(id) {
		assert.Equal(f.t, want, st.StateObject, getter)
		assert.Equal(f.t, wantRun, st.StateObjectRunID, getter)
		require.NotNil(f.t, st.StateObjectAt, getter)
		if at.IsZero() {
			at = *st.StateObjectAt
		}
		assert.True(f.t, at.Equal(*st.StateObjectAt), getter)
	}
	return at
}

func TestSetStackStateObject(t *testing.T) {
	f := newFixture(t)
	_, ids := f.saveGraph(f.repo.ID, sampleGraph(f.repo.FullName, "sha1"))
	vpcID := ids["stacks/prod/vpc"]

	for getter, st := range f.stackFromEveryGetter(vpcID) {
		assert.Nil(t, st.StateObject, "%s: no job has reported a state object yet", getter)
		assert.Nil(t, st.StateObjectRunID, getter)
		assert.Nil(t, st.StateObjectAt, getter)
	}

	apply := f.run(store.CreateRunParams{Mode: v1.ModeApply, Trigger: v1.TriggerPush})
	f.exec(`UPDATE runs SET created_at = now() - interval '1 hour' WHERE id = $1`, apply.ID)
	first := v1.StateObject{Bucket: "acme-state", Key: "prod/vpc.tfstate", Region: "eu-west-1"}
	require.NoError(t, f.s.SetStackStateObject(f.ctx, vpcID, first, apply.ID))
	recordedAt := f.requireStateObject(vpcID, &first, &apply.ID)

	drift := f.run(store.CreateRunParams{Mode: v1.ModeDrift, Trigger: v1.TriggerSchedule})
	second := v1.StateObject{Bucket: "acme-state", Key: "env:/blue/prod/vpc.tfstate"}
	require.NoError(t, f.s.SetStackStateObject(f.ctx, vpcID, second, drift.ID))
	replacedAt := f.requireStateObject(vpcID, &second, &drift.ID)
	assert.False(t, replacedAt.Before(recordedAt), "a newer run's report replaces the older one")

	require.NoError(t, f.s.SetStackStateObject(f.ctx, vpcID, first, apply.ID))
	assert.True(t, f.requireStateObject(vpcID, &second, &drift.ID).Equal(replacedAt),
		"a late report of an older run leaves the newer run's state object alone")

	third := v1.StateObject{Bucket: "acme-state", Key: "env:/green/prod/vpc.tfstate", Region: "eu-west-1"}
	require.NoError(t, f.s.SetStackStateObject(f.ctx, vpcID, third, drift.ID))
	overwrittenAt := f.requireStateObject(vpcID, &third, &drift.ID)
	assert.False(t, overwrittenAt.Before(replacedAt), "the same run may report again")

	reupload := sampleGraph(f.repo.FullName, "sha2")
	reupload.Stacks[0].Backend = &v1.Backend{Type: "s3", Bucket: "acme-other", Key: "prod/eks.tfstate", Region: "us-east-1"}
	f.saveGraph(f.repo.ID, reupload)
	f.saveGraph(f.repo.ID, reupload)
	for getter, st := range f.stackFromEveryGetter(vpcID) {
		require.NotNil(t, st.Backend, getter)
		assert.Equal(t, "acme-other", st.Backend.Bucket, "%s: the uploaded graph moves the backend", getter)
	}
	assert.True(t, f.requireStateObject(vpcID, &third, &drift.ID).Equal(overwrittenAt),
		"a graph upload leaves the state object, its run and its time alone")

	eks, err := f.s.GetStackByKey(f.ctx, f.repo.ID, "stacks/prod/eks")
	require.NoError(t, err)
	assert.Nil(t, eks.StateObject, "only the stack reported on gets a state object")

	plan := f.run(store.CreateRunParams{Mode: v1.ModePlan})
	other := f.addRepo(1, 101, "acme", "acme/network")
	foreign := f.run(store.CreateRunParams{RepoID: other.ID, Mode: v1.ModeApply, Trigger: v1.TriggerPush})

	cases := []struct {
		name    string
		stackID uuid.UUID
		obj     v1.StateObject
		runID   uuid.UUID
		want    error
	}{
		{"unknown stack", uuid.New(), first, apply.ID, store.ErrNotFound},
		{"unknown run", vpcID, first, uuid.New(), store.ErrNotFound},
		{"plan run", vpcID, first, plan.ID, store.ErrInvalid},
		{"apply run of another repository", vpcID, first, foreign.ID, store.ErrInvalid},
		{"no bucket", vpcID, v1.StateObject{Key: "prod/vpc.tfstate"}, apply.ID, store.ErrInvalid},
		{"no key", vpcID, v1.StateObject{Bucket: "acme-state"}, apply.ID, store.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, f.s.SetStackStateObject(f.ctx, tc.stackID, tc.obj, tc.runID), tc.want)
		})
	}
	f.requireStateObject(vpcID, &third, &drift.ID)

	f.exec(`DELETE FROM runs WHERE id = $1`, drift.ID)
	assert.True(t, f.requireStateObject(vpcID, &third, nil).Equal(overwrittenAt),
		"the state object outlives the run that reported it")
}

func TestClearStackStateObject(t *testing.T) {
	f := newFixture(t)
	_, ids := f.saveGraph(f.repo.ID, sampleGraph(f.repo.FullName, "sha1"))
	vpcID := ids["stacks/prod/vpc"]
	obj := v1.StateObject{Bucket: "acme-state", Key: "prod/vpc.tfstate", Region: "eu-west-1"}

	older := f.run(store.CreateRunParams{Mode: v1.ModeApply, Trigger: v1.TriggerPush})
	f.exec(`UPDATE runs SET created_at = now() - interval '2 hours' WHERE id = $1`, older.ID)
	apply := f.run(store.CreateRunParams{Mode: v1.ModeApply, Trigger: v1.TriggerPush})
	f.exec(`UPDATE runs SET created_at = now() - interval '1 hour' WHERE id = $1`, apply.ID)
	require.NoError(t, f.s.SetStackStateObject(f.ctx, vpcID, obj, apply.ID))
	recordedAt := f.requireStateObject(vpcID, &obj, &apply.ID)

	require.NoError(t, f.s.ClearStackStateObject(f.ctx, vpcID, older.ID))
	assert.True(t, f.requireStateObject(vpcID, &obj, &apply.ID).Equal(recordedAt),
		"a late report of an older run leaves the newer run's state object alone")

	drift := f.run(store.CreateRunParams{Mode: v1.ModeDrift, Trigger: v1.TriggerSchedule})
	require.NoError(t, f.s.ClearStackStateObject(f.ctx, vpcID, drift.ID))
	clearedAt := f.requireStateObject(vpcID, nil, &drift.ID)
	assert.False(t, clearedAt.Before(recordedAt), "a newer run's report clears the state object and is recorded as the report")

	require.NoError(t, f.s.SetStackStateObject(f.ctx, vpcID, obj, apply.ID))
	assert.True(t, f.requireStateObject(vpcID, nil, &drift.ID).Equal(clearedAt),
		"a late report of an older run does not bring the cleared state object back")

	require.NoError(t, f.s.SetStackStateObject(f.ctx, vpcID, obj, drift.ID))
	assert.False(t, f.requireStateObject(vpcID, &obj, &drift.ID).Before(clearedAt), "the same run may report again")

	plan := f.run(store.CreateRunParams{Mode: v1.ModePlan})
	other := f.addRepo(1, 101, "acme", "acme/network")
	foreign := f.run(store.CreateRunParams{RepoID: other.ID, Mode: v1.ModeApply, Trigger: v1.TriggerPush})
	cases := []struct {
		name    string
		stackID uuid.UUID
		runID   uuid.UUID
		want    error
	}{
		{"unknown stack", uuid.New(), drift.ID, store.ErrNotFound},
		{"unknown run", vpcID, uuid.New(), store.ErrNotFound},
		{"plan run", vpcID, plan.ID, store.ErrInvalid},
		{"apply run of another repository", vpcID, foreign.ID, store.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, f.s.ClearStackStateObject(f.ctx, tc.stackID, tc.runID), tc.want)
		})
	}
	f.requireStateObject(vpcID, &obj, &drift.ID)
}
