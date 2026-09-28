//go:build integration

package store_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func TestCreateAndFindRuns(t *testing.T) {
	f := newFixture(t)
	r := f.run(store.CreateRunParams{SHA: "s1", BaseSHA: "b1", PRNumber: 7, RequestedBy: "octocat", WorkflowRunID: 55, WorkflowRunAttempt: 2})
	assert.Equal(t, v1.RunPending, r.Status)
	assert.Equal(t, "acme/infra", r.Repo)
	assert.Equal(t, int64(55), r.WorkflowRunID)
	assert.Nil(t, r.StartedAt)
	assert.Nil(t, r.GraphID)

	got, err := f.s.GetRun(f.ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, r, got)

	open, err := f.s.FindOpenRun(f.ctx, f.repo.ID, "s1", 7, v1.ModePlan)
	require.NoError(t, err)
	assert.Equal(t, r.ID, open.ID)
	_, err = f.s.FindOpenRun(f.ctx, f.repo.ID, "s1", 7, v1.ModeApply)
	require.ErrorIs(t, err, store.ErrNotFound)

	_, err = f.s.UpdateRunStatus(f.ctx, r.ID, v1.RunFailed)
	require.NoError(t, err)
	_, err = f.s.FindOpenRun(f.ctx, f.repo.ID, "s1", 7, v1.ModePlan)
	require.ErrorIs(t, err, store.ErrNotFound, "terminal runs are not open")

	started := f.run(store.CreateRunParams{SHA: "s2", Status: v1.RunUnconfirmed})
	assert.NotNil(t, started.FinishedAt, "a terminal initial status stamps finished_at")

	_, err = f.s.CreateRun(f.ctx, store.CreateRunParams{RepoID: f.repo.ID})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = f.s.CreateRun(f.ctx, store.CreateRunParams{RepoID: f.repo.ID, SHA: "x", Trigger: "cron", Mode: v1.ModePlan})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = f.s.GetRun(f.ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestFindOrCreateRunConcurrent(t *testing.T) {
	f := newFixture(t)
	p := store.CreateRunParams{RepoID: f.repo.ID, SHA: "s1", PRNumber: 3, Trigger: v1.TriggerPullRequest, Mode: v1.ModePlan}
	const callers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ids     = map[uuid.UUID]int{}
		created int
	)
	for range callers {
		wg.Go(func() {
			r, c, err := f.s.FindOrCreateRun(f.ctx, p)
			assert.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			ids[r.ID]++
			if c {
				created++
			}
		})
	}
	wg.Wait()
	assert.Len(t, ids, 1)
	assert.Equal(t, 1, created)
}

func TestListRuns(t *testing.T) {
	f := newFixture(t)
	other := f.addRepo(1, 101, "acme", "acme/apps")
	all := make([]store.Run, 0, 7)
	for i := range 7 {
		p := store.CreateRunParams{SHA: "s", PRNumber: 1 + i%2}
		if i == 6 {
			p.RepoID, p.Mode, p.Trigger = other.ID, v1.ModeDrift, v1.TriggerSchedule
		}
		all = append(all, f.run(p))
	}
	_, err := f.s.UpdateRunStatus(f.ctx, all[0].ID, v1.RunPlanning)
	require.NoError(t, err)

	cases := []struct {
		name   string
		filter store.RunFilter
		want   int
	}{
		{"all", store.RunFilter{}, 7},
		{"repo", store.RunFilter{RepoID: f.repo.ID}, 6},
		{"pr", store.RunFilter{RepoID: f.repo.ID, PRNumber: 1}, 3},
		{"status", store.RunFilter{Status: v1.RunPlanning}, 1},
		{"mode", store.RunFilter{Mode: v1.ModeDrift}, 1},
		{"sha", store.RunFilter{SHA: "nope"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runs, next, err := f.s.ListRuns(f.ctx, tc.filter)
			require.NoError(t, err)
			assert.Len(t, runs, tc.want)
			assert.Empty(t, next)
		})
	}

	var paged []uuid.UUID
	cursor := ""
	for page := 0; ; page++ {
		runs, next, err := f.s.ListRuns(f.ctx, store.RunFilter{Limit: 3, Cursor: cursor})
		require.NoError(t, err)
		for _, r := range runs {
			paged = append(paged, r.ID)
		}
		if next == "" {
			break
		}
		require.Less(t, page, 5)
		cursor = next
	}
	require.Len(t, paged, 7)
	for i := range all {
		assert.Equal(t, all[len(all)-1-i].ID, paged[i], "newest first")
	}

	_, _, err = f.s.ListRuns(f.ctx, store.RunFilter{Cursor: "!!"})
	require.ErrorIs(t, err, store.ErrInvalid)
}

func TestUpdateRunStatus(t *testing.T) {
	f := newFixture(t)
	r := f.run(store.CreateRunParams{})

	steps := []struct {
		status       v1.RunStatus
		from         []v1.RunStatus
		wantErr      error
		wantStarted  bool
		wantFinished bool
	}{
		{status: v1.RunPlanning, wantStarted: true},
		{status: v1.RunPlanned, from: []v1.RunStatus{v1.RunPlanning}, wantStarted: true, wantFinished: true},
		{status: v1.RunApplying, from: []v1.RunStatus{v1.RunPending}, wantErr: store.ErrConflict},
		{status: v1.RunApplying, from: []v1.RunStatus{v1.RunPlanned}, wantStarted: true},
		{status: v1.RunApplied, wantStarted: true, wantFinished: true},
	}
	var firstStart *string
	for _, st := range steps {
		got, err := f.s.UpdateRunStatus(f.ctx, r.ID, st.status, st.from...)
		if st.wantErr != nil {
			require.ErrorIs(t, err, st.wantErr, st.status)
			continue
		}
		require.NoError(t, err, st.status)
		assert.Equal(t, st.status, got.Status)
		assert.Equal(t, st.wantStarted, got.StartedAt != nil, st.status)
		assert.Equal(t, st.wantFinished, got.FinishedAt != nil, st.status)
		if got.StartedAt != nil {
			s := got.StartedAt.String()
			if firstStart == nil {
				firstStart = &s
			}
			assert.Equal(t, *firstStart, s, "started_at keeps the first start")
		}
	}

	_, err := f.s.UpdateRunStatus(f.ctx, uuid.New(), v1.RunPlanning)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.UpdateRunStatus(f.ctx, uuid.New(), v1.RunPlanning, v1.RunPending)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.UpdateRunStatus(f.ctx, r.ID, "bogus")
	require.ErrorIs(t, err, store.ErrInvalid)
}

func TestRunGraphWaveAndWorkflow(t *testing.T) {
	f := newFixture(t)
	r := f.run(store.CreateRunParams{})
	graphID, _ := f.saveGraph(f.repo.ID, sampleGraph(f.repo.FullName, "abc123"))

	require.NoError(t, f.s.SetRunGraph(f.ctx, r.ID, graphID, 3, []string{"w1"}))
	require.NoError(t, f.s.SetRunWave(f.ctx, r.ID, 2))
	require.NoError(t, f.s.SetRunWorkflowRun(f.ctx, r.ID, 99, 3))
	got, err := f.s.GetRun(f.ctx, r.ID)
	require.NoError(t, err)
	require.NotNil(t, got.GraphID)
	assert.Equal(t, graphID, *got.GraphID)
	assert.Equal(t, 3, got.Waves)
	assert.Equal(t, 2, got.CurrentWave)
	assert.Equal(t, []string{"w1"}, got.Warnings)
	assert.Equal(t, int64(99), got.WorkflowRunID)
	assert.Equal(t, 3, got.WorkflowRunAttempt)
	assert.Equal(t, []string{"w1"}, got.ToV1().Warnings)

	require.ErrorIs(t, f.s.SetRunWave(f.ctx, uuid.New(), 1), store.ErrNotFound)
	require.ErrorIs(t, f.s.SetRunGraph(f.ctx, r.ID, uuid.New(), 1, nil), store.ErrNotFound)
}

func TestSupersedeRuns(t *testing.T) {
	f := newFixture(t)
	oldPlanning := f.run(store.CreateRunParams{SHA: "old", PRNumber: 5, Status: v1.RunPlanning})
	oldPlanned := f.run(store.CreateRunParams{SHA: "old", PRNumber: 5, Status: v1.RunPlanned})
	oldFailed := f.run(store.CreateRunParams{SHA: "older", PRNumber: 5, Status: v1.RunFailed})
	head := f.run(store.CreateRunParams{SHA: "head", PRNumber: 5})
	otherPR := f.run(store.CreateRunParams{SHA: "old", PRNumber: 6})
	push := f.run(store.CreateRunParams{SHA: "main1", Trigger: v1.TriggerPush})

	n, err := f.s.SupersedeRuns(f.ctx, f.repo.ID, 5, "head")
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	want := map[uuid.UUID]v1.RunStatus{
		oldPlanning.ID: v1.RunSuperseded,
		oldPlanned.ID:  v1.RunSuperseded,
		oldFailed.ID:   v1.RunFailed,
		head.ID:        v1.RunPending,
		otherPR.ID:     v1.RunPending,
		push.ID:        v1.RunPending,
	}
	for id, status := range want {
		got, err := f.s.GetRun(f.ctx, id)
		require.NoError(t, err)
		assert.Equal(t, status, got.Status, id)
		if status == v1.RunSuperseded {
			assert.NotNil(t, got.FinishedAt)
		}
	}

	n, err = f.s.SupersedeRuns(f.ctx, f.repo.ID, 0, "x")
	require.NoError(t, err)
	assert.Zero(t, n, "runs without a pull request are never superseded")
}

func TestRunStacks(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b", "stacks/c")
	r := f.run(store.CreateRunParams{Mode: v1.ModeApply})

	rows := []store.RunStack{
		{StackID: ids["stacks/b"], Wave: 1, Reasons: []v1.Reason{v1.ReasonDependent}, Environment: "production"},
		{StackID: ids["stacks/a"], Wave: 0, Reasons: []v1.Reason{v1.ReasonChanged, v1.ReasonModule}, Environment: "production"},
		{StackID: ids["stacks/c"], Wave: 0, Status: v1.StackSkipped, Mode: v1.ModePlan},
	}
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, r.ID, rows))
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, r.ID, nil))

	got, err := f.s.GetRunStacks(f.ctx, r.ID)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"stacks/a", "stacks/c", "stacks/b"}, []string{got[0].Key, got[1].Key, got[2].Key}, "ordered by wave then key")
	assert.Equal(t, v1.StackPending, got[0].Status)
	assert.Equal(t, v1.ModeApply, got[0].Mode, "mode defaults to the run's")
	assert.Equal(t, []v1.Reason{v1.ReasonChanged, v1.ReasonModule}, got[0].Reasons)
	assert.Equal(t, v1.StackSkipped, got[1].Status)
	assert.Equal(t, v1.ModePlan, got[1].Mode)
	assert.Empty(t, got[1].Reasons)

	a := ids["stacks/a"]
	planned := v1.StackPlanned
	summary := &v1.PlanSummary{Adds: 2, Changes: 1, Replaces: 1, Added: []string{"aws_vpc.main"}}
	text := strings.Repeat("é", store.MaxPlanTextBytes)
	updated, err := f.s.UpdateRunStack(f.ctx, r.ID, a, store.RunStackPatch{
		Status:       &planned,
		Summary:      summary,
		HasChanges:   ptr(true),
		ExitCode:     ptr(2),
		JobURL:       ptr("https://github.com/acme/infra/actions/runs/1/job/2"),
		PlanArtifact: ptr(v1.PlanArtifactName("stacks/a", "abc123")),
		PlanRunID:    ptr(int64(1)),
		PlanText:     &text,
	})
	require.NoError(t, err)
	assert.Equal(t, v1.StackPlanned, updated.Status)
	assert.Equal(t, 2, updated.Adds)
	assert.Equal(t, 1, updated.Replaces)
	assert.True(t, updated.HasChanges)
	require.NotNil(t, updated.ExitCode)
	assert.Equal(t, 2, *updated.ExitCode)
	assert.Equal(t, summary, updated.Summary)
	assert.True(t, updated.PlanTextTruncated)
	assert.LessOrEqual(t, len(updated.PlanText), store.MaxPlanTextBytes)
	assert.True(t, strings.HasPrefix(text, updated.PlanText))
	assert.Equal(t, "é", updated.PlanText[len(updated.PlanText)-2:], "cut on a rune boundary")
	assert.NotNil(t, updated.FinishedAt)
	assert.Nil(t, updated.StartedAt)

	listed, err := f.s.GetRunStacks(f.ctx, r.ID)
	require.NoError(t, err)
	assert.Empty(t, listed[0].PlanText, "lists omit plan text")
	assert.True(t, listed[0].PlanTextTruncated)
	one, err := f.s.GetRunStack(f.ctx, r.ID, a)
	require.NoError(t, err)
	assert.Equal(t, updated.PlanText, one.PlanText)

	require.NoError(t, f.s.UpsertRunStacks(f.ctx, r.ID, []store.RunStack{{StackID: a, Wave: 4, Status: v1.StackPending}}))
	one, err = f.s.GetRunStack(f.ctx, r.ID, a)
	require.NoError(t, err)
	assert.Equal(t, 4, one.Wave)
	assert.Equal(t, v1.StackPlanned, one.Status, "re-upserting keeps reported results")

	applying := v1.StackApplying
	mode := v1.ModeApply
	one, err = f.s.UpdateRunStack(f.ctx, r.ID, a, store.RunStackPatch{Status: &applying, Mode: &mode, IfStatus: []v1.StackStatus{v1.StackPlanned}})
	require.NoError(t, err)
	assert.NotNil(t, one.StartedAt)
	assert.Nil(t, one.FinishedAt, "starting a phase clears finished_at")
	assert.Equal(t, updated.PlanText, one.PlanText, "nil fields are kept")

	_, err = f.s.UpdateRunStack(f.ctx, r.ID, a, store.RunStackPatch{Status: &planned, IfStatus: []v1.StackStatus{v1.StackPending}})
	require.ErrorIs(t, err, store.ErrConflict)
	_, err = f.s.UpdateRunStack(f.ctx, r.ID, uuid.New(), store.RunStackPatch{Status: &planned, IfStatus: []v1.StackStatus{v1.StackPending}})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.UpdateRunStack(f.ctx, r.ID, uuid.New(), store.RunStackPatch{Status: &planned})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.GetRunStack(f.ctx, r.ID, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	err = f.s.UpsertRunStacks(f.ctx, r.ID, []store.RunStack{{StackID: uuid.New()}})
	require.ErrorIs(t, err, store.ErrNotFound)
	err = f.s.UpsertRunStacks(f.ctx, uuid.New(), []store.RunStack{{StackID: a}})
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestRunDetailChecksAndHistory(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b")
	a, b := ids["stacks/a"], ids["stacks/b"]

	runs := make([]store.Run, 0, 3)
	for i := range 3 {
		r := f.run(store.CreateRunParams{SHA: "s" + string(rune('0'+i)), PRNumber: 9})
		require.NoError(t, f.s.UpsertRunStacks(f.ctx, r.ID, []store.RunStack{{StackID: a}, {StackID: b, Wave: 1}}))
		runs = append(runs, r)
	}
	last := runs[2]
	applied, mode := v1.StackApplied, v1.ModeApply
	_, err := f.s.UpdateRunStack(f.ctx, runs[1].ID, a, store.RunStackPatch{Status: &applied, Mode: &mode})
	require.NoError(t, err)

	c, err := f.s.UpsertCheck(f.ctx, store.Check{RunID: last.ID, StackID: a, Name: "policy", Status: v1.CheckFail, Summary: "2 violations"})
	require.NoError(t, err)
	assert.Equal(t, "stacks/a", c.StackKey)
	c, err = f.s.UpsertCheck(f.ctx, store.Check{RunID: last.ID, StackID: a, Name: "policy", Status: v1.CheckPass, DetailsURL: "https://x"})
	require.NoError(t, err)
	assert.Equal(t, v1.CheckPass, c.Status)
	_, err = f.s.UpsertCheck(f.ctx, store.Check{RunID: last.ID, StackID: b, Name: "cost", Status: v1.CheckWarn})
	require.NoError(t, err)

	checkErrs := []struct {
		name  string
		check store.Check
		want  error
	}{
		{"stack not in run", store.Check{RunID: last.ID, StackID: uuid.New(), Name: "policy", Status: v1.CheckPass}, store.ErrNotFound},
		{"missing name", store.Check{RunID: last.ID, StackID: a, Status: v1.CheckPass}, store.ErrInvalid},
		{"bad status", store.Check{RunID: last.ID, StackID: a, Name: "x", Status: "maybe"}, store.ErrInvalid},
	}
	for _, tc := range checkErrs {
		_, err := f.s.UpsertCheck(f.ctx, tc.check)
		require.ErrorIs(t, err, tc.want, tc.name)
	}

	checks, err := f.s.ListChecks(f.ctx, last.ID)
	require.NoError(t, err)
	require.Len(t, checks, 2)
	assert.Equal(t, "policy", checks[0].Name)

	detail, err := f.s.RunDetail(f.ctx, last.ID)
	require.NoError(t, err)
	assert.Equal(t, last.ID.String(), detail.ID)
	require.Len(t, detail.Stacks, 2)
	assert.Equal(t, "stacks/a", detail.Stacks[0].Key)
	require.Len(t, detail.Stacks[0].Checks, 1)
	assert.Equal(t, v1.CheckPass, detail.Stacks[0].Checks[0].Status)
	assert.Equal(t, "cost", detail.Stacks[1].Checks[0].Name)
	_, err = f.s.RunDetail(f.ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	latestPlan, err := f.s.LatestRunStackForStack(f.ctx, a, v1.ModePlan)
	require.NoError(t, err)
	assert.Equal(t, last.ID, latestPlan.RunID)
	assert.Equal(t, 9, latestPlan.PRNumber)
	latestApply, err := f.s.LatestRunStackForStack(f.ctx, a, v1.ModeApply, v1.StackApplied)
	require.NoError(t, err)
	assert.Equal(t, runs[1].ID, latestApply.RunID)
	ref := latestApply.ToV1()
	assert.Equal(t, v1.StackApplied, ref.Status)
	assert.Equal(t, runs[1].SHA, ref.SHA)
	_, err = f.s.LatestRunStackForStack(f.ctx, b, v1.ModeApply)
	require.ErrorIs(t, err, store.ErrNotFound)

	var history []uuid.UUID
	cursor := ""
	for {
		page, next, err := f.s.StackHistory(f.ctx, a, 2, cursor)
		require.NoError(t, err)
		for _, h := range page {
			history = append(history, h.RunID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	assert.Equal(t, []uuid.UUID{runs[2].ID, runs[1].ID, runs[0].ID}, history)
	_, _, err = f.s.StackHistory(f.ctx, a, 2, "bad cursor")
	require.ErrorIs(t, err, store.ErrInvalid)
}

func TestDispatches(t *testing.T) {
	f := newFixture(t)
	r := f.run(store.CreateRunParams{Mode: v1.ModeApply})

	d, created, err := f.s.CreateDispatch(f.ctx, r.ID, 0, "production", v1.ModeApply)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, f.repo.ID, d.RepoID)
	assert.Nil(t, d.WorkflowRunID)

	again, created, err := f.s.CreateDispatch(f.ctx, r.ID, 0, "production", v1.ModeApply)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, d.ID, again.ID)

	staging, _, err := f.s.CreateDispatch(f.ctx, r.ID, 0, "staging", v1.ModeApply)
	require.NoError(t, err)
	wave1, _, err := f.s.CreateDispatch(f.ctx, r.ID, 1, "production", v1.ModeApply)
	require.NoError(t, err)

	require.NoError(t, f.s.SetDispatchWorkflowRun(f.ctx, d.ID, 4242))
	found, err := f.s.FindDispatchByWorkflowRun(f.ctx, 4242)
	require.NoError(t, err)
	assert.Equal(t, d.ID, found.ID)
	_, err = f.s.FindDispatchByWorkflowRun(f.ctx, 1)
	require.ErrorIs(t, err, store.ErrNotFound)

	done, err := f.s.CompleteDispatch(f.ctx, d.ID, "success")
	require.NoError(t, err)
	require.NotNil(t, done.CompletedAt)
	redone, err := f.s.CompleteDispatch(f.ctx, d.ID, "failure")
	require.NoError(t, err)
	assert.Equal(t, *done.CompletedAt, *redone.CompletedAt, "first completion time is kept")
	assert.Equal(t, "failure", redone.Conclusion)

	open, err := f.s.OpenDispatches(f.ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{staging.ID, wave1.ID}, []uuid.UUID{open[0].ID, open[1].ID})

	list, err := f.s.ListDispatches(f.ctx, r.ID)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, []string{"production", "staging", "production"}, []string{list[0].Environment, list[1].Environment, list[2].Environment})

	_, err = f.s.CompleteDispatch(f.ctx, uuid.New(), "x")
	require.ErrorIs(t, err, store.ErrNotFound)
	require.ErrorIs(t, f.s.SetDispatchWorkflowRun(f.ctx, uuid.New(), 1), store.ErrNotFound)
	_, _, err = f.s.CreateDispatch(f.ctx, uuid.New(), 0, "x", v1.ModeApply)
	require.ErrorIs(t, err, store.ErrNotFound)
}
