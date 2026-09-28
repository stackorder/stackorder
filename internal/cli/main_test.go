package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/client"
	"github.com/stackorder/stackorder/internal/tf"
)

const fakeTFEnv = "STACKORDER_TEST_FAKE_TF"

var fakeTFBin string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "stackorder-cli-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	fakeTFBin = filepath.Join(dir, "faketf")
	if runtime.GOOS == "windows" {
		fakeTFBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeTFBin, "./testdata/faketf")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the fake terraform: %v\n%s", err, out)
		return 1
	}
	clientOptions = []client.Option{client.WithBackoff(time.Millisecond), client.WithTimeout(10 * time.Second)}
	return m.Run()
}

type fakeTFConfig struct {
	Log         string          `json:"log"`
	Tofu        bool            `json:"tofu"`
	Version     string          `json:"version"`
	InitExit    int             `json:"init_exit"`
	Backend     json.RawMessage `json:"backend,omitempty"`
	PlanExit    int             `json:"plan_exit"`
	PlanOutput  string          `json:"plan_output"`
	ShowJSON    string          `json:"show_json"`
	ShowText    string          `json:"show_text"`
	ShowExit    int             `json:"show_exit"`
	ApplyExit   int             `json:"apply_exit"`
	ApplyOutput string          `json:"apply_output"`
	FailOutput  string          `json:"fail_output"`
}

type tfCall struct {
	Dir  string   `json:"dir"`
	Args []string `json:"args"`
}

type harness struct {
	t       *testing.T
	root    string
	tf      fakeTFConfig
	tfCfg   string
	output  string
	summary string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clearEnv(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "stackorder.yaml"), "version: 1\n")
	writeFile(t, filepath.Join(root, "stacks", "app", "main.tf"), "terraform {\n  backend \"s3\" {}\n}\n")
	testdata, err := filepath.Abs("testdata")
	require.NoError(t, err)
	tmp := t.TempDir()
	h := &harness{
		t:       t,
		root:    root,
		tfCfg:   filepath.Join(tmp, "faketf.json"),
		output:  filepath.Join(tmp, "github_output"),
		summary: filepath.Join(tmp, "step_summary"),
		tf: fakeTFConfig{
			Log:        filepath.Join(tmp, "tf.log"),
			Version:    "1.14.4",
			PlanExit:   2,
			PlanOutput: "Plan: 1 to add, 1 to change, 0 to destroy.\n",
			ShowJSON:   filepath.Join(testdata, "plan_changes.json"),
			ShowText:   writeFile(t, filepath.Join(tmp, "show.txt"), "  # aws_s3_bucket.logs will be created\n  + password = \"hunter2hunter2\"\n"),
			FailOutput: "Invalid provider configuration",
		},
	}
	t.Setenv(tf.EnvTerraformBin, fakeTFBin)
	t.Setenv(tf.EnvTofuBin, fakeTFBin)
	t.Setenv(fakeTFEnv, h.tfCfg)
	return h
}

func (h *harness) run(args ...string) result {
	h.t.Helper()
	data, err := json.Marshal(h.tf)
	require.NoError(h.t, err)
	require.NoError(h.t, os.WriteFile(h.tfCfg, data, 0o600))
	if !slices.Contains(args, "--repo-root") && os.Getenv("GITHUB_WORKSPACE") == "" {
		args = append([]string{"--repo-root", h.root}, args...)
	}
	return runCLI(h.t, args...)
}

func (h *harness) ci(fs *fakeServer, event string, payload map[string]any) {
	h.t.Helper()
	eventPath := filepath.Join(h.t.TempDir(), "event.json")
	data, err := json.Marshal(payload)
	require.NoError(h.t, err)
	require.NoError(h.t, os.WriteFile(eventPath, data, 0o600))
	for k, v := range map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"GITHUB_REPOSITORY":              "acme/infra",
		"GITHUB_SHA":                     mergeSHA,
		"GITHUB_REF":                     "refs/pull/7/merge",
		"GITHUB_EVENT_NAME":              event,
		"GITHUB_EVENT_PATH":              eventPath,
		"GITHUB_OUTPUT":                  h.output,
		"GITHUB_STEP_SUMMARY":            h.summary,
		"GITHUB_RUN_ID":                  "4242",
		"GITHUB_RUN_ATTEMPT":             "2",
		"GITHUB_SERVER_URL":              "https://github.example",
		"GITHUB_WORKSPACE":               h.root,
		"GITHUB_TOKEN":                   "ghs_" + strings.Repeat("t", 36),
		"GITHUB_ACTOR":                   "octocat",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
	} {
		h.t.Setenv(k, v)
	}
	if fs != nil {
		h.t.Setenv("GITHUB_API_URL", fs.url()+"/github")
		h.t.Setenv(EnvServerURL, fs.url())
		h.t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", fs.url()+"/oidc/token?api-version=2.0")
	}
}

func (h *harness) outputs() map[string]string {
	h.t.Helper()
	return parseOutputs(h.t, h.output)
}

func (h *harness) stepSummary() string {
	h.t.Helper()
	data, err := os.ReadFile(h.summary)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(h.t, err)
	return string(data)
}

func (h *harness) tfCalls() [][]string {
	h.t.Helper()
	data, err := os.ReadFile(h.tf.Log)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(h.t, err)
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var c tfCall
		require.NoError(h.t, json.Unmarshal([]byte(line), &c))
		out = append(out, c.Args)
	}
	return out
}

func (h *harness) tfCommands() []string {
	h.t.Helper()
	calls := h.tfCalls()
	out := make([]string, 0, len(calls))
	for _, args := range calls {
		out = append(out, args[0])
	}
	return out
}
