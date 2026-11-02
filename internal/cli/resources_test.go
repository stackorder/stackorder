package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const awsProvider = "registry.terraform.io/hashicorp/aws"

var stateShow = []string{"show", "-json", "-no-color"}

func stateFixture(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "state.json"))
	require.NoError(t, err)
	return p
}

func fixtureResources() *v1.ResourceList {
	return &v1.ResourceList{Total: 4, Items: []v1.StackResource{
		{Address: "aws_iam_role.app", Type: "aws_iam_role", Provider: awsProvider, Deposed: true},
		{Address: "aws_instance.web", Type: "aws_instance", Provider: awsProvider, Tainted: true},
		{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Provider: awsProvider},
		{Address: `module.vpc.aws_subnet.private["eu-west-1a/10.0.1.0"]`, Type: "aws_subnet", Module: "module.vpc", Provider: awsProvider},
	}}
}

func (h *harness) stateShows() int {
	h.t.Helper()
	n := 0
	for _, args := range h.tfCalls() {
		if slices.Equal(args, stateShow) {
			n++
		}
	}
	return n
}

func diffResources(t *testing.T, want, got *v1.ResourceList) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("posted resources (-want +got):\n%s", diff)
	}
}

func TestApplyReportsTheResourceList(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, h *harness, fs *fakeServer)
		wantCode   int
		wantStatus v1.ResultStatus
		want       *v1.ResourceList
		wantShows  int
		wantWarn   string
	}{
		{
			name:       "successful apply",
			setup:      func(_ *testing.T, h *harness, _ *fakeServer) { h.savedPlan(h.sha) },
			wantStatus: v1.ResultSuccess,
			want:       fixtureResources(),
			wantShows:  1,
		},
		{
			name: "failed apply still reads the state",
			setup: func(_ *testing.T, h *harness, _ *fakeServer) {
				h.savedPlan(h.sha)
				h.tf.ApplyExit = 1
			},
			wantCode:   ExitFailure,
			wantStatus: v1.ResultFailure,
			want:       fixtureResources(),
			wantShows:  1,
		},
		{
			name: "empty state",
			setup: func(_ *testing.T, h *harness, _ *fakeServer) {
				h.savedPlan(h.sha)
				h.tf.StateJSON = ""
			},
			wantStatus: v1.ResultSuccess,
			want:       &v1.ResourceList{Items: []v1.StackResource{}},
			wantShows:  1,
		},
		{
			name: "re-plan mismatch refuses before the apply",
			setup: func(_ *testing.T, h *harness, _ *fakeServer) {
				h.tf.ShowJSON = filepath.Join(filepath.Dir(h.tf.ShowJSON), "plan_other.json")
			},
			wantCode:   ExitRefused,
			wantStatus: v1.ResultFailure,
		},
		{
			name: "missing recorded summary refuses before the apply",
			setup: func(_ *testing.T, h *harness, fs *fakeServer) {
				row := plannedRow()
				row.Summary = nil
				fs.setRun(applyRun(h.sha, v1.RunApplying, row))
			},
			wantCode:   ExitRefused,
			wantStatus: v1.ResultFailure,
		},
		{
			name: "init fails",
			setup: func(_ *testing.T, h *harness, _ *fakeServer) {
				h.savedPlan(h.sha)
				h.tf.InitExit = 1
			},
			wantCode:   ExitFailure,
			wantStatus: v1.ResultFailure,
		},
		{
			name: "state read fails",
			setup: func(_ *testing.T, h *harness, _ *fakeServer) {
				h.savedPlan(h.sha)
				h.tf.StateExit = 1
				h.tf.FailOutput = "No state file was found!"
			},
			wantStatus: v1.ResultSuccess,
			wantShows:  1,
			wantWarn:   "::warning::reading the resource list of stacks/app from its state failed, so the result carries none: terraform show exited with code 1: Error: No state file was found!",
		},
		{
			name: "state is not JSON",
			setup: func(t *testing.T, h *harness, _ *fakeServer) {
				h.savedPlan(h.sha)
				h.tf.StateJSON = writeFile(t, filepath.Join(t.TempDir(), "state.json"), "<html>bad gateway</html>")
			},
			wantStatus: v1.ResultSuccess,
			wantShows:  1,
			wantWarn:   "::warning::reading the resource list of stacks/app from its state failed, so the result carries none: tf: decoding state JSON: not valid JSON at byte 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fs := newApplyHarness(t)
			h.tf.StateJSON = stateFixture(t)
			tt.setup(t, h, fs)

			r := h.run("apply", "--stack", "stacks/app")
			require.Equal(t, tt.wantCode, r.code, r.stderr)
			got := fs.lastResult().Result
			assert.Equal(t, tt.wantStatus, got.Status)
			diffResources(t, tt.want, got.Resources)
			assert.Equal(t, tt.wantShows, h.stateShows())
			if tt.wantShows > 0 {
				calls := h.tfCalls()
				assert.Equal(t, stateShow, calls[len(calls)-1], "the state is read last")
			}
			if tt.wantWarn != "" {
				assert.Contains(t, r.stderr, tt.wantWarn)
			}
		})
	}
}

func TestDriftReportsTheResourceList(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, h *harness)
		wantCode   int
		wantStatus v1.ResultStatus
		want       *v1.ResourceList
		wantShows  int
	}{
		{
			name:       "drifted",
			setup:      func(*testing.T, *harness) {},
			wantCode:   ExitChanges,
			wantStatus: v1.ResultSuccess,
			want:       fixtureResources(),
			wantShows:  1,
		},
		{
			name: "plan text rendering fails",
			setup: func(t *testing.T, h *harness) {
				h.tf.ShowText = filepath.Join(t.TempDir(), "missing.txt")
			},
			wantCode:   ExitFailure,
			wantStatus: v1.ResultFailure,
			want:       fixtureResources(),
			wantShows:  1,
		},
		{
			name:       "plan fails after init",
			setup:      func(_ *testing.T, h *harness) { h.tf.PlanExit = 1 },
			wantCode:   ExitFailure,
			wantStatus: v1.ResultFailure,
			want:       fixtureResources(),
			wantShows:  1,
		},
		{
			name:       "init fails",
			setup:      func(_ *testing.T, h *harness) { h.tf.InitExit = 1 },
			wantCode:   ExitFailure,
			wantStatus: v1.ResultFailure,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "workflow_dispatch", dispatchPayload("run-drift", headSHA))
			h.tf.StateJSON = stateFixture(t)
			tt.setup(t, h)

			r := h.run("drift", "--stack", "stacks/app")
			require.Equal(t, tt.wantCode, r.code, r.stderr)
			got := fs.lastResult().Result
			assert.Equal(t, tt.wantStatus, got.Status)
			diffResources(t, tt.want, got.Resources)
			assert.Equal(t, tt.wantShows, h.stateShows())
			if tt.wantShows > 0 {
				calls := h.tfCalls()
				assert.Equal(t, stateShow, calls[len(calls)-1], "the state is read last")
			}
		})
	}
}

func TestPlanReportsNoResourceList(t *testing.T) {
	h := newHarness(t)
	fs := newFakeServer(t)
	h.ci(fs, "pull_request", prPayload())
	h.tf.StateJSON = stateFixture(t)
	r := h.run("plan", "--stack", "stacks/app", "--run-id", "run-1")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Nil(t, fs.lastResult().Result.Resources)
	assert.Zero(t, h.stateShows())
}

func TestDriftPrintsTheResourceListAsJSON(t *testing.T) {
	h := newHarness(t)
	h.tf.StateJSON = stateFixture(t)
	r := h.run("--format", "json", "drift", "--stack", "stacks/app")
	require.Equal(t, ExitChanges, r.code, r.stderr)
	var out stackOutput
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &out))
	diffResources(t, fixtureResources(), out.Result.Resources)
}

func TestResourceListRedactsSecrets(t *testing.T) {
	const secret = "hunter2hunter2"
	h, fs := newApplyHarness(t)
	h.savedPlan(h.sha)
	t.Setenv("TF_VAR_db_password", secret)
	h.tf.StateJSON = writeFile(t, filepath.Join(t.TempDir(), "state.json"), `{"format_version":"1.0","values":{"root_module":{
		"resources":[
			{"address":"aws_ssm_parameter.p[\"`+secret+`\"]","mode":"managed","type":"aws_ssm_parameter","provider_name":"`+awsProvider+`","index":"`+secret+`"},
			{"address":"aws_ssm_parameter.p[\"plain\"]","mode":"managed","type":"aws_ssm_parameter","provider_name":"`+awsProvider+`","index":"plain"}],
		"child_modules":[{"address":"module.db[\"`+secret+`\"]","resources":[
			{"address":"module.db[\"`+secret+`\"].aws_db_instance.main","mode":"managed","type":"aws_db_instance","provider_name":"`+awsProvider+`","tainted":true}]}]}}}`)

	r := h.run("apply", "--stack", "stacks/app")
	require.Equal(t, 0, r.code, r.stderr)
	want := &v1.ResourceList{Total: 3, Items: []v1.StackResource{
		{Address: `aws_ssm_parameter.p["***"]`, Type: "aws_ssm_parameter", Provider: awsProvider},
		{Address: `aws_ssm_parameter.p["plain"]`, Type: "aws_ssm_parameter", Provider: awsProvider},
		{Address: `module.db["***"].aws_db_instance.main`, Type: "aws_db_instance", Module: `module.db["***"]`, Provider: awsProvider, Tainted: true},
	}}
	diffResources(t, want, fs.lastResult().Result.Resources)
	for _, body := range fs.requestBodies("result") {
		assert.NotContains(t, string(body), secret)
	}
}

func TestResourceListFitsTheResultBudget(t *testing.T) {
	const n = 8000
	h, fs := newApplyHarness(t)
	h.savedPlan(h.sha)
	var b strings.Builder
	b.WriteString(`{"format_version":"1.0","values":{"root_module":{"resources":[`)
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"address":"aws_route53_record.acme_example_records[\"service-%05d.internal.acme.example\"]","mode":"managed","type":"aws_route53_record","provider_name":"%s"}`, i, awsProvider)
	}
	b.WriteString(`]}}}`)
	h.tf.StateJSON = writeFile(t, filepath.Join(t.TempDir(), "state.json"), b.String())

	r := h.run("apply", "--stack", "stacks/app")
	require.Equal(t, 0, r.code, r.stderr)
	got := fs.lastResult().Result.Resources
	require.NotNil(t, got)
	assert.Equal(t, n, got.Total)
	assert.True(t, got.Truncated)
	assert.NotEmpty(t, got.Items)
	assert.Less(t, len(got.Items), n)
	assert.Equal(t, `aws_route53_record.acme_example_records["service-00000.internal.acme.example"]`, got.Items[0].Address)
	bodies := fs.requestBodies("result")
	require.Len(t, bodies, 1)
	assert.LessOrEqual(t, len(bodies[0]), v1.MaxResultBytes-resultMargin)
	assert.Greater(t, len(bodies[0]), v1.MaxResultBytes-2*resultMargin)
}

func TestResultIsPostedAgainWithoutTheResourceList(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      v1.Error
		wantCode  int
		wantPosts int
	}{
		{
			name:      "body too large",
			status:    http.StatusRequestEntityTooLarge,
			body:      v1.Error{Code: "invalid", Message: "the request body exceeds 1048576 bytes"},
			wantPosts: 2,
		},
		{
			name:      "proxy limit without a v1 body",
			status:    http.StatusRequestEntityTooLarge,
			wantPosts: 2,
		},
		{
			name:      "invalid resources field",
			status:    http.StatusBadRequest,
			body:      v1.Error{Code: "invalid", Message: "invalid resources", Details: map[string]any{"field": "resources.items", "reason": "too many"}},
			wantPosts: 2,
		},
		{
			name:      "invalid other field",
			status:    http.StatusBadRequest,
			body:      v1.Error{Code: "invalid", Message: "invalid summary", Details: map[string]any{"field": "summary.added", "reason": "bad"}},
			wantCode:  ExitFailure,
			wantPosts: 1,
		},
		{
			name:      "invalid without details",
			status:    http.StatusBadRequest,
			body:      v1.Error{Code: "invalid", Message: "the request body is not valid JSON"},
			wantCode:  ExitFailure,
			wantPosts: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fs := newApplyHarness(t)
			h.savedPlan(h.sha)
			h.tf.StateJSON = stateFixture(t)
			fs.rejectNext("result", tt.status, tt.body)

			r := h.run("apply", "--stack", "stacks/app")
			require.Equal(t, tt.wantCode, r.code, r.stderr)
			bodies := fs.requestBodies("result")
			require.Len(t, bodies, tt.wantPosts)
			assert.Contains(t, string(bodies[0]), `"resources":`)
			if tt.wantPosts == 1 {
				assert.Contains(t, r.stderr, "applied stacks/app, but reporting the result failed")
				return
			}
			assert.NotContains(t, string(bodies[1]), `"resources"`)
			var first, second v1.StackResult
			require.NoError(t, json.Unmarshal(bodies[0], &first))
			require.NoError(t, json.Unmarshal(bodies[1], &second))
			first.Resources = nil
			assert.Equal(t, first, second)
			assert.Contains(t, r.stderr, fmt.Sprintf("::warning::the server refused the apply result of stacks/app with its resource list (%d ", tt.status))
		})
	}
}

func TestDriftResultIsPostedAgainWithoutTheResourceList(t *testing.T) {
	h := newHarness(t)
	fs := newFakeServer(t)
	h.ci(fs, "workflow_dispatch", dispatchPayload("run-drift", headSHA))
	h.tf.StateJSON = stateFixture(t)
	fs.rejectNext("result", http.StatusRequestEntityTooLarge, v1.Error{Code: "invalid"})
	r := h.run("drift", "--stack", "stacks/app")
	require.Equal(t, ExitChanges, r.code, r.stderr)
	bodies := fs.requestBodies("result")
	require.Len(t, bodies, 2)
	assert.NotContains(t, string(bodies[1]), `"resources"`)
	assert.Nil(t, fs.lastResult().Result.Resources)
}

func TestStateValuesNeverLeaveTheRunner(t *testing.T) {
	const sentinel = "leak-0b5e"
	full, err := os.ReadFile(stateFixture(t))
	require.NoError(t, err)
	require.Contains(t, string(full), "values-"+sentinel)
	require.Contains(t, string(full), "sensitive-"+sentinel)
	require.Contains(t, string(full), "identity-"+sentinel)
	require.Contains(t, string(full), "outputs-"+sentinel)
	tests := []struct {
		name      string
		state     string
		stateExit int
		wantList  bool
	}{
		{name: "valid state", state: string(full), wantList: true},
		{name: "partial JSON and a failed show", state: string(full[:len(full)/2]), stateExit: 1},
		{name: "partial JSON", state: string(full[:len(full)/2])},
		{name: "unparseable", state: "values-" + sentinel + ` {"format_version":"1.0"}`},
		{name: "wrong format version", state: `{"format_version":"values-` + sentinel + `"}`},
	}
	for _, tt := range tests {
		for _, command := range []string{"apply", "drift"} {
			t.Run(tt.name+"/"+command, func(t *testing.T) {
				var h *harness
				var fs *fakeServer
				if command == "apply" {
					h, fs = newApplyHarness(t)
					h.savedPlan(h.sha)
				} else {
					h, fs = newHarness(t), newFakeServer(t)
					h.ci(fs, "workflow_dispatch", dispatchPayload("run-drift", headSHA))
				}
				h.tf.StateJSON = writeFile(t, filepath.Join(t.TempDir(), "state.json"), tt.state)
				h.tf.StateExit = tt.stateExit
				tmp := t.TempDir()
				t.Setenv("TMPDIR", tmp)

				r := h.run("--verbose", command, "--stack", "stacks/app")
				require.Contains(t, []int{0, ExitChanges}, r.code, r.stderr)
				got := fs.lastResult().Result
				assert.Equal(t, tt.wantList, got.Resources != nil)
				if !tt.wantList {
					assert.Contains(t, r.stderr, "::warning::reading the resource list of stacks/app")
				}

				places := map[string]string{
					"stdout":              r.stdout,
					"stderr":              r.stderr,
					"GITHUB_OUTPUT":       readIfExists(t, h.output),
					"GITHUB_STEP_SUMMARY": readIfExists(t, h.summary),
				}
				for i, body := range fs.requestBodies("result") {
					places[fmt.Sprintf("result body %d", i)] = string(body)
				}
				for _, dir := range []string{h.root, tmp} {
					for path, content := range filesUnder(t, dir) {
						places[path] = content
					}
				}
				for place, content := range places {
					assert.NotContains(t, content, sentinel, place)
					assert.NotContains(t, content, "123456789012", place)
				}
			})
		}
	}
}

func readIfExists(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

func filesUnder(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(data)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}
