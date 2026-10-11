//go:build integration

package runs_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/stateread"
)

const refuseUngranted = "refuse a state object no grant of its repository covers"

var canaryStateJSON = fmt.Sprintf(`{"version":4,"terraform_version":"1.9.8","serial":42,"lineage":%q,`+
	`"outputs":{"vpc_id":{"value":%q,"type":"string"},"db_password":{"value":%q,"type":"string","sensitive":true}},"resources":[]}`,
	canaryLineage, canaryValue, canarySecret)

type fakeS3 struct {
	mu   sync.Mutex
	gets []string
}

func (f *fakeS3) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gets...)
}

func newGrantedReader(t *testing.T, grants string) (*stateread.Reader, *fakeS3) {
	t.Helper()
	parsed, err := stateread.ParseGrants(grants)
	require.NoError(t, err)
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_PROFILE", "")
	f := &fakeS3{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.gets = append(f.gets, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, canaryStateJSON)
	}))
	t.Cleanup(srv.Close)
	r, err := stateread.New(t.Context(), parsed, stateread.WithEndpoint(srv.URL), stateread.WithRegion("us-east-1"),
		stateread.WithStaticCredentials("AKIDACME", "secret"))
	require.NoError(t, err)
	return r, f
}

func objectAt(bucket, key string) *v1.StateObject {
	return &v1.StateObject{Bucket: bucket, Key: key, Region: "eu-west-1"}
}

func TestOutputsLocationMustBeGrantedToTheProducerRepository(t *testing.T) {
	const grants = "acme/infra=acme-state/stacks/prod, acme/network=acme-state/acme/network/"
	tests := []struct {
		name string
		obj  *v1.StateObject
		ok   bool
	}{
		{name: "under the repository's own prefix", obj: objectAt("acme-state", "stacks/prod/vpc.tfstate"), ok: true},
		{name: "the repository's prefix itself", obj: objectAt("acme-state", "stacks/prod"), ok: true},
		{name: "under another repository's prefix", obj: objectAt("acme-state", "acme/network/stacks/shared/vpc.tfstate")},
		{name: "a sibling of the repository's prefix", obj: objectAt("acme-state", "stacks/production/vpc.tfstate")},
		{name: "a dot segment out of the repository's prefix", obj: objectAt("acme-state", "stacks/prod/../../acme/network/stacks/shared/vpc.tfstate")},
		{name: "another bucket", obj: objectAt("acme-other", "stacks/prod/vpc.tfstate")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &logRecorder{}
			reader, s3 := newGrantedReader(t, grants)
			e := newEnv(t, sharedConfig("stacks/**"), withStateReader(reader), withLogger(logs.logger()))
			e.setStackDir(repoName, mainSHA, vpc)
			job, runID := e.readingPlan(7)
			e.setStateObject(repoID, vpc, tt.obj)

			got, err := e.outputs(job.p, vpc, runID, eks)
			if tt.ok {
				require.NoError(t, err)
				assert.Equal(t, map[string]v1.StackOutput{"vpc_id": served(output(canaryValue, false))}, got.Outputs)
				assert.Equal(t, []string{"GET /" + tt.obj.Bucket + "/" + tt.obj.Key}, s3.requests())
				assert.Len(t, e.audit("outputs_read"), 1)
				assert.Empty(t, e.audit("outputs_refused"))
				assert.Empty(t, logs.records(t, refuseUngranted))
				return
			}
			message := requireCoded(t, err, principal.ErrForbidden, v1.ErrCodeStateBucketNotAllowed)
			assert.Equal(t, "the state object of "+vpc+" lies outside the buckets and prefixes this server may read for "+repoName, message)
			assert.Empty(t, s3.requests(), "no request reached S3")

			rows := e.audit("outputs_refused")
			require.Len(t, rows, 1)
			assert.Equal(t, job.p.Actor(), rows[0].Actor)
			assert.Equal(t, repoName+"//"+vpc, rows[0].Target)
			assert.Equal(t, map[string]any{
				"repo": repoName, "stack": vpc, "reader_repo": repoName, "reader": eks, "run_id": runID, "reason": v1.ErrCodeStateBucketNotAllowed,
			}, rows[0].Details)
			assert.Empty(t, e.audit("outputs_read"))

			warned := logs.records(t, refuseUngranted)
			require.Len(t, warned, 1)
			assert.Equal(t, "WARN", warned[0]["level"])
			assert.Equal(t, repoName, warned[0]["repo"])
			assert.Equal(t, vpc, warned[0]["stack"])
			assert.Equal(t, tt.obj.Bucket, warned[0]["bucket"])
			assert.Equal(t, tt.obj.Key, warned[0]["key"])
			assert.NotContains(t, logs.String(), "canary")
		})
	}
}

func TestOutputsAPIKeyReadsOnlyGrantedLocations(t *testing.T) {
	reader, s3 := newGrantedReader(t, "acme/network=acme-state/stacks/")
	e := newEnv(t, baseConfig(), withStateReader(reader))
	e.setStackDir(repoName, mainSHA, vpc)
	e.readingPlan(7)
	e.setStateObject(repoID, vpc, objectAt("acme-state", "stacks/prod/vpc.tfstate"))

	_, err := e.outputs(apiKey(), vpc, "", "")
	requireCoded(t, err, principal.ErrForbidden, v1.ErrCodeStateBucketNotAllowed)
	assert.Empty(t, s3.requests(), "an admin key does not widen what the repository was granted")
	rows := e.audit("outputs_refused")
	require.Len(t, rows, 1)
	assert.Equal(t, "apikey:ci", rows[0].Actor)
	assert.Equal(t, v1.ErrCodeStateBucketNotAllowed, rows[0].Details["reason"])
}

func TestOutputsWildcardGrantAllowsAnyRepository(t *testing.T) {
	reader, s3 := newGrantedReader(t, "*=acme-state/shared/, acme/infra=acme-state/stacks/")
	e := newEnv(t, baseConfig(), withStateReader(reader))
	e.gh.SetRepo(networkRepo, gh.Repository{ID: networkID, DefaultBranch: "main", Private: true})
	e.gh.AddInstallation(instID, "acme", networkRepo)
	e.addRepo(networkID, instID, networkRepo, &v1.RepoConfig{Version: 1, Outputs: &v1.OutputsConfig{Readers: []string{"acme/infra//stacks/prod/*"}}},
		sharedVPC, sharedDNS)
	e.setStackFile(repoName, mainSHA, vpc, "outputs:\n  readers: [stacks/prod/eks]\n")
	job, runID := e.readingPlan(7)

	tests := []struct {
		name string
		repo string
		key  string
		obj  *v1.StateObject
		ok   bool
	}{
		{name: "the wildcard covers the producer's own repository", repo: repoName, key: vpc, obj: objectAt("acme-state", "shared/infra/vpc.tfstate"), ok: true},
		{name: "the wildcard covers another repository", repo: networkRepo, key: sharedVPC, obj: objectAt("acme-state", "shared/network/vpc.tfstate"), ok: true},
		{name: "a grant of the producer's own beside the wildcard", repo: repoName, key: vpc, obj: objectAt("acme-state", "stacks/prod/vpc.tfstate"), ok: true},
		{name: "outside the wildcard's prefix", repo: networkRepo, key: sharedDNS, obj: objectAt("acme-state", "acme/network/dns.tfstate")},
		{name: "another repository's own grant", repo: networkRepo, key: sharedDNS, obj: objectAt("acme-state", "stacks/prod/vpc.tfstate")},
		{name: "a sibling of the wildcard's prefix", repo: networkRepo, key: sharedDNS, obj: objectAt("acme-state", "shared-legacy/dns.tfstate")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := repoID
			if tt.repo == networkRepo {
				id = networkID
			}
			e.setStateObject(id, tt.key, tt.obj)
			before := len(s3.requests())

			got, err := e.svc.StackOutputs(e.ctx, job.p, tt.repo, tt.key, runID, eks)
			if !tt.ok {
				requireCoded(t, err, principal.ErrForbidden, v1.ErrCodeStateBucketNotAllowed)
				assert.Len(t, s3.requests(), before, "no request reached S3")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.repo, got.Repo)
			assert.Equal(t, []string{"GET /" + tt.obj.Bucket + "/" + tt.obj.Key}, s3.requests()[before:])
		})
	}
}

func (e *env) requireClearedStateObject(key, runID string) {
	e.t.Helper()
	st := e.stack(key)
	assert.Nil(e.t, st.StateObject, key)
	require.NotNil(e.t, st.StateObjectRunID, key)
	assert.Equal(e.t, runID, st.StateObjectRunID.String(), "%s: the run whose report cleared it", key)
	require.NotNil(e.t, st.StateObjectAt, key)
	assert.False(e.t, st.StateObjectAt.Before(e.storedRun(runID).CreatedAt), "%s: cleared once its run existed", key)
}

const clearUngranted = "clear the state location for a state object no grant of its repository covers"

func TestResultDoesNotRecordAnUngrantedStateObject(t *testing.T) {
	const grants = "acme/infra=acme-state/stacks/prod, acme/network=acme-state/acme/network/"
	granted := objectAt("acme-state", "stacks/prod/vpc.tfstate")
	elsewhere := objectAt("acme-state", "acme/network/stacks/shared/vpc.tfstate")
	reports := []struct {
		name   string
		status v1.StackStatus
		report func(t *testing.T, e *env, obj *v1.StateObject) (string, v1.StackStatus)
	}{
		{
			name: "apply",
			report: func(t *testing.T, e *env, obj *v1.StateObject) (string, v1.StackStatus) {
				e.planned(7, headSHA)
				e.comment(7, applier, "stackorder apply")
				apply := e.applyRun(7)
				prod := e.dispatchJob(e.newestDispatchOf(vpc).RunID, "production")
				row, err := e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withState(applyResult(false), obj))
				require.NoError(t, err, "the result is accepted whatever becomes of its state object")
				return apply.ID, row.Status
			},
			status: v1.StackFailed,
		},
		{
			name: "drift",
			report: func(t *testing.T, e *env, obj *v1.StateObject) (string, v1.StackStatus) {
				g := testGraph(mainSHA)
				gid, _, err := e.st.SaveGraph(e.ctx, repoID, &g)
				require.NoError(t, err)
				require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, gid))
				require.NoError(t, e.svc.RunDriftStack(e.ctx, repoID, e.stack(vpc).ID.String()))
				d := e.newestDispatchOf(vpc)
				drift := v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, Summary: &v1.PlanSummary{}}
				row, err := e.svc.RecordResult(e.ctx, e.dispatchJob(d.RunID, v1.DefaultEnvironment), d.Inputs["run_id"], vpc, withState(drift, obj))
				require.NoError(t, err, "the result is accepted whatever becomes of its state object")
				return d.Inputs["run_id"], row.Status
			},
			status: v1.StackPlanned,
		},
	}
	tests := []struct {
		name   string
		obj    *v1.StateObject
		record bool
	}{
		{name: "under the repository's own prefix", obj: granted, record: true},
		{name: "under another repository's prefix", obj: elsewhere},
		{name: "a sibling of the repository's prefix", obj: objectAt("acme-state", "stacks/production/vpc.tfstate")},
		{name: "another bucket", obj: objectAt("acme-other", "stacks/prod/vpc.tfstate")},
	}
	for _, rp := range reports {
		for _, tt := range tests {
			t.Run(rp.name+"/"+tt.name, func(t *testing.T) {
				logs := &logRecorder{}
				reader, s3 := newGrantedReader(t, grants)
				e := newEnv(t, baseConfig(), withStateReader(reader), withLogger(logs.logger()))

				runID, status := rp.report(t, e, tt.obj)
				assert.Equal(t, rp.status, status)
				assert.Empty(t, s3.requests(), "recording a location reads nothing")
				warned := logs.records(t, clearUngranted)
				if tt.record {
					e.requireStateObject(vpc, tt.obj, runID)
					assert.Empty(t, warned)
					return
				}
				e.requireClearedStateObject(vpc, runID)
				require.Len(t, warned, 1)
				assert.Equal(t, "WARN", warned[0]["level"])
				assert.Equal(t, runID, warned[0]["run_id"])
				assert.Equal(t, repoName, warned[0]["repo"])
				assert.Equal(t, vpc, warned[0]["stack"])
				assert.Equal(t, tt.obj.Bucket, warned[0]["bucket"])
				assert.Equal(t, tt.obj.Key, warned[0]["key"])
			})
		}
	}

	t.Run("an ungranted report clears the recorded location", func(t *testing.T) {
		reader, s3 := newGrantedReader(t, grants)
		e := newEnv(t, baseConfig(), withStateReader(reader))
		e.setStackDir(repoName, mainSHA, vpc)
		applyID, _ := reports[0].report(t, e, granted)
		e.requireStateObject(vpc, granted, applyID)
		e.clock.Advance(time.Hour)
		driftID, _ := reports[1].report(t, e, elsewhere)
		e.requireClearedStateObject(vpc, driftID)

		_, err := e.outputs(apiKey(), vpc, "", "")
		message := requireCoded(t, err, principal.ErrConflict, v1.ErrCodeStateLocationUnknown)
		assert.Equal(t, "the server does not know where the state of "+vpc+" is: its last apply or drift job reported a state object "+
			"outside the buckets and prefixes this server may read for "+repoName, message)
		assert.Empty(t, s3.requests(), "outputs no longer come from the object the stack left")
		rows := e.audit("outputs_refused")
		require.Len(t, rows, 1)
		assert.Equal(t, v1.ErrCodeStateLocationUnknown, rows[0].Details["reason"])
	})

	t.Run("without a state reader every location is recorded", func(t *testing.T) {
		e := newEnv(t, baseConfig())
		runID, _ := reports[0].report(t, e, elsewhere)
		e.requireStateObject(vpc, elsewhere, runID)
	})
}
