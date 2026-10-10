//go:build integration

package runs_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

type logRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logRecorder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logRecorder) logger() *slog.Logger { return slog.New(slog.NewJSONHandler(l, nil)) }

func (l *logRecorder) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(l.buf.Bytes()))
	for dec.More() {
		var rec map[string]any
		require.NoError(t, dec.Decode(&rec))
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func stateAt(key string) *v1.StateObject {
	return &v1.StateObject{Bucket: "acme-state", Key: key + ".tfstate", Region: "eu-west-1"}
}

func withState(res v1.StackResult, obj *v1.StateObject) v1.StackResult {
	res.State = obj
	return res
}

func (e *env) stack(key string) store.Stack {
	e.t.Helper()
	st, err := e.st.GetStackByKey(e.ctx, repoID, key)
	require.NoError(e.t, err)
	return st
}

func (e *env) requireStateObject(key string, want *v1.StateObject, runID string) store.Stack {
	e.t.Helper()
	st := e.stack(key)
	assert.Equal(e.t, want, st.StateObject, key)
	if want == nil {
		assert.Nil(e.t, st.StateObjectRunID, key)
		assert.Nil(e.t, st.StateObjectAt, key)
		return st
	}
	require.NotNil(e.t, st.StateObjectRunID, key)
	assert.Equal(e.t, runID, st.StateObjectRunID.String(), "%s: the run that reported it", key)
	require.NotNil(e.t, st.StateObjectAt, key)
	assert.False(e.t, st.StateObjectAt.Before(e.storedRun(runID).CreatedAt), "%s: recorded once its run existed", key)
	return st
}

func (e *env) requireNoStateObjects() {
	e.t.Helper()
	stacks, err := e.st.ListStacks(e.ctx, repoID, true)
	require.NoError(e.t, err)
	require.NotEmpty(e.t, stacks)
	for _, st := range stacks {
		e.requireStateObject(st.Key, nil, "")
	}
}

func (e *env) newestDispatchOf(key string) ghfake.Dispatch {
	e.t.Helper()
	ds := e.gh.Dispatches()
	for i := len(ds) - 1; i >= 0; i-- {
		if slices.Contains(entryKeys(entries(e.t, ds[i])), key) {
			return ds[i]
		}
	}
	e.t.Fatalf("no dispatch carries %s", key)
	return ghfake.Dispatch{}
}

func TestApplyResultRecordsTheStateObject(t *testing.T) {
	tests := []struct {
		name    string
		ok      bool
		state   *v1.StateObject
		status  v1.StackStatus
		want    *v1.StateObject
		warning string
	}{
		{name: "applied", ok: true, state: stateAt(vpc), status: v1.StackApplied, want: stateAt(vpc)},
		{name: "failed after init", ok: false, state: stateAt(vpc), status: v1.StackFailed, want: stateAt(vpc)},
		{name: "no state object", ok: true, status: v1.StackApplied},
		{
			name: "no bucket", ok: true, state: &v1.StateObject{Key: vpc + ".tfstate"}, status: v1.StackApplied,
			warning: "ignore a state object without a bucket or key",
		},
		{
			name: "no key", ok: true, state: &v1.StateObject{Bucket: "acme-state"}, status: v1.StackApplied,
			warning: "ignore a state object without a bucket or key",
		},
		{
			name: "refused by the store", ok: true, state: &v1.StateObject{Bucket: "acme-state", Key: vpc + "\x00.tfstate"}, status: v1.StackApplied,
			warning: "record state object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &logRecorder{}
			e := newEnv(t, baseConfig(), withLogger(logs.logger()))
			e.planned(7, headSHA)
			e.comment(7, applier, "stackorder apply")
			apply := e.applyRun(7)
			prod := e.dispatchJob(e.newestDispatchOf(vpc).RunID, "production")

			_, err := e.svc.RecordResult(e.ctx, prod, apply.ID, staging, withState(applyResult(true), stateAt(staging)))
			require.ErrorIs(t, err, principal.ErrForbidden, "a job of another environment is refused")
			e.requireStateObject(staging, nil, "")

			row, err := e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withState(applyResult(tt.ok), tt.state))
			require.NoError(t, err, "the result is recorded whatever becomes of its state object")
			assert.Equal(t, tt.status, row.Status)
			recorded := e.requireStateObject(vpc, tt.want, apply.ID)

			_, err = e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withState(applyResult(tt.ok), stateAt(eks)))
			require.NoError(t, err, "a repeated result is accepted")
			repeated := e.requireStateObject(vpc, tt.want, apply.ID)
			if tt.want != nil {
				assert.True(t, recorded.StateObjectAt.Equal(*repeated.StateObjectAt), "a repeated result does not record its state object")
			}

			_, err = e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withState(applyResult(!tt.ok), stateAt(eks)))
			require.ErrorIs(t, err, principal.ErrConflict, "a stack's result is final")
			conflicting := e.requireStateObject(vpc, tt.want, apply.ID)
			if tt.want != nil {
				assert.True(t, recorded.StateObjectAt.Equal(*conflicting.StateObjectAt), "a refused result does not record its state object")
			}
			e.requireStateObject(eks, nil, "")

			if tt.warning == "" {
				assert.Empty(t, logs.records(t, "record state object"))
				assert.Empty(t, logs.records(t, "ignore a state object without a bucket or key"))
				return
			}
			warned := logs.records(t, tt.warning)
			require.Len(t, warned, 1)
			assert.Equal(t, apply.ID, warned[0]["run_id"])
			assert.Equal(t, vpc, warned[0]["stack"])
		})
	}
}

func TestDriftResultRecordsTheStateObject(t *testing.T) {
	tests := []struct {
		name string
		res  v1.StackResult
	}{
		{
			name: "drifted",
			res: v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, ExitCode: 2, HasChanges: true,
				Summary: &v1.PlanSummary{Changes: 1, Changed: []string{"aws_vpc.main"}}},
		},
		{name: "no drift", res: v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, Summary: &v1.PlanSummary{}}},
		{name: "failed after init", res: v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultFailure, ExitCode: 1, ErrorText: "Error: boom"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, baseConfig())
			g := testGraph(mainSHA)
			gid, _, err := e.st.SaveGraph(e.ctx, repoID, &g)
			require.NoError(t, err)
			require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, gid))
			drift := func() (string, ghfake.Dispatch) {
				require.NoError(t, e.svc.RunDriftStack(e.ctx, repoID, e.stack(vpc).ID.String()))
				d := e.newestDispatchOf(vpc)
				return d.Inputs["run_id"], d
			}

			runID, d := drift()
			_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(d.RunID, "production"), runID, vpc, withState(tt.res, stateAt(vpc)))
			require.ErrorIs(t, err, principal.ErrForbidden, "drift jobs run under the default environment")
			e.requireStateObject(vpc, nil, "")
			_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(d.RunID, v1.DefaultEnvironment), runID, vpc, withState(tt.res, stateAt(vpc)))
			require.NoError(t, err)
			e.requireStateObject(vpc, stateAt(vpc), runID)

			e.clock.Advance(time.Hour)
			later, d := drift()
			require.NotEqual(t, runID, later)
			moved := &v1.StateObject{Bucket: "acme-state", Key: "env:/blue/" + vpc + ".tfstate"}
			_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(d.RunID, v1.DefaultEnvironment), later, vpc, withState(tt.res, moved))
			require.NoError(t, err)
			e.requireStateObject(vpc, moved, later)
		})
	}
}

func TestPlanResultDoesNotRecordTheStateObject(t *testing.T) {
	tests := []struct {
		name   string
		report func(t *testing.T, e *env)
	}{
		{
			name: "pull_request plan job",
			report: func(t *testing.T, e *env) {
				e.openPull(7, headSHA)
				job, runID, resp := e.startPlan(7, headSHA)
				require.NotEmpty(t, resp.Affected)
				for _, a := range resp.Affected {
					_, err := e.svc.RecordResult(e.ctx, job.p, runID, a.Key, withState(planResult(a.Key, headSHA, 1), stateAt(a.Key)))
					require.NoError(t, err)
				}
				assert.Equal(t, v1.RunPlanned, e.run(runID).Status)
			},
		},
		{
			name: "comment-triggered plan dispatch",
			report: func(t *testing.T, e *env) {
				e.planned(7, headSHA)
				e.comment(7, applier, "stackorder plan "+eks)
				d := e.newestDispatchOf(eks)
				require.Equal(t, "plan", d.Inputs["mode"])
				runID := d.Inputs["run_id"]
				_, err := e.svc.RecordResult(e.ctx, e.dispatchJob(d.RunID, v1.DefaultEnvironment), runID, eks, withState(planResult(eks, headSHA, 5), stateAt(eks)))
				require.NoError(t, err)
				assert.Equal(t, v1.RunPlanned, e.run(runID).Status)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &logRecorder{}
			e := newEnv(t, baseConfig(), withLogger(logs.logger()))
			tt.report(t, e)
			e.requireNoStateObjects()
			assert.Empty(t, logs.records(t, "record state object"), "a plan result never reaches the store")
		})
	}
}

func TestGraphUploadDoesNotMoveTheStateObject(t *testing.T) {
	logs := &logRecorder{}
	e := newEnv(t, baseConfig(), withLogger(logs.logger()))
	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	prod := e.dispatchJob(e.newestDispatchOf(vpc).RunID, "production")
	_, err := e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withState(applyResult(true), stateAt(vpc)))
	require.NoError(t, err)
	recorded := e.requireStateObject(vpc, stateAt(vpc), apply.ID)

	g := testGraph(newHeadSHA)
	elsewhere := &v1.Backend{Type: "s3", Bucket: "acme-other", Key: eks + ".tfstate", Region: "us-east-1"}
	for i := range g.Stacks {
		if g.Stacks[i].Key == vpc {
			g.Stacks[i].Backend = elsewhere
		}
	}
	e.openPull(8, newHeadSHA)
	job, runID, resp := e.startPlanGraph(8, g)
	assert.Equal(t, elsewhere, e.stack(vpc).Backend, "the upload rewrote the stack's backend")
	require.NotEmpty(t, resp.Affected)
	for _, a := range resp.Affected {
		obj := &v1.StateObject{Bucket: elsewhere.Bucket, Key: elsewhere.Key, Region: elsewhere.Region}
		_, err := e.svc.RecordResult(e.ctx, job.p, runID, a.Key, withState(planResult(a.Key, newHeadSHA, 1), obj))
		require.NoError(t, err)
	}

	after := e.requireStateObject(vpc, stateAt(vpc), apply.ID)
	assert.True(t, recorded.StateObjectAt.Equal(*after.StateObjectAt), "the recorded location keeps its time")
	for _, key := range []string{staging, eks, apps} {
		e.requireStateObject(key, nil, "")
	}
	assert.Empty(t, logs.records(t, "record state object"))
}

func TestManualApplyRecordsTheStateObject(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.planned(7, headSHA)
	created, err := e.svc.CreateRun(e.ctx, apiKey(), v1.CreateRunRequest{
		Repo: repoName, SHA: mainSHA, Mode: v1.ModeApply, Trigger: v1.TriggerManual, Stacks: []string{vpc},
	})
	require.NoError(t, err)
	row, err := e.svc.RecordResult(e.ctx, apiKey(), created.RunID, vpc, withState(applyResult(true), stateAt(vpc)))
	require.NoError(t, err)
	assert.Equal(t, v1.StackApplied, row.Status)
	e.requireStateObject(vpc, stateAt(vpc), created.RunID)
	e.requireStateObject(staging, nil, "")
}
