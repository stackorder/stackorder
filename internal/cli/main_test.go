package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/client"
	"github.com/stackorder/stackorder/internal/tf"
)

const (
	fakeTFEnv        = "STACKORDER_TEST_FAKE_TF"
	fakeBuildTimeout = 5 * time.Minute
)

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
	if fakeTFBin, err = installFakeTerraform(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	clientOptions = []client.Option{client.WithBackoff(time.Millisecond), client.WithTimeout(10 * time.Second)}
	return m.Run()
}

const fakeTFScript = `#!/bin/sh
if [ -z "$STACKORDER_TEST_FAKE_TF" ] || [ ! -r "$STACKORDER_TEST_FAKE_TF" ]; then
  echo "fake terraform: STACKORDER_TEST_FAKE_TF names no configuration" >&2
  exit 97
fi
. "$STACKORDER_TEST_FAKE_TF"
if [ -n "$FAKE_LOG" ]; then
  {
    for a in "$@"; do printf '%s\037' "$a"; done
    printf '\036'
    env | grep -E '^(TF_VAR_|STACKORDER_)[A-Za-z0-9_]*=' | grep -v '^STACKORDER_TEST_FAKE_TF=' | sort | while IFS= read -r kv; do printf '%s\037' "$kv"; done
    printf '\n'
  } >> "$FAKE_LOG"
fi
fail() {
  printf '\nError: %s\n' "$FAKE_FAIL_OUTPUT" >&2
  exit "$1"
}
last=
for a in "$@"; do last=$a; done
case "$1" in
  "")
    exit 1 ;;
  version)
    if [ "$FAKE_TOFU" = true ]; then
      printf '{"terraform_version":"%s","platform":"linux_amd64","provider_selections":{}}\n' "$FAKE_VERSION"
    else
      printf '{"terraform_version":"%s","platform":"linux_amd64","provider_selections":{},"terraform_outdated":false}\n' "$FAKE_VERSION"
    fi ;;
  init)
    echo "Terraform has been successfully initialized!"
    if [ "$FAKE_INIT_EXIT" -ne 0 ]; then fail "$FAKE_INIT_EXIT"; fi
    if [ -n "$FAKE_BACKEND" ]; then
      mkdir -p "${TF_DATA_DIR:-.terraform}" && printf '%s' "$FAKE_BACKEND" > "${TF_DATA_DIR:-.terraform}/terraform.tfstate"
    fi ;;
  workspace)
    mkdir -p "${TF_DATA_DIR:-.terraform}" && printf '%s' "$last" > "${TF_DATA_DIR:-.terraform}/environment"
    printf 'Switched to workspace "%s".\n' "$last" ;;
  plan)
    printf '%s' "$FAKE_PLAN_OUTPUT"
    if [ "$FAKE_PLAN_EXIT" -eq 1 ]; then fail 1; fi
    for a in "$@"; do
      case "$a" in -out=*) printf 'fake plan' > "${a#-out=}" ;; esac
    done
    exit "$FAKE_PLAN_EXIT" ;;
  show)
    if [ ! -e "$last" ]; then
      printf 'Error: Failed to read the given file %s as a state or plan file\n' "$last" >&2
      exit 1
    fi
    if [ "$FAKE_SHOW_EXIT" -ne 0 ]; then fail "$FAKE_SHOW_EXIT"; fi
    src=$FAKE_SHOW_TEXT
    for a in "$@"; do
      if [ "$a" = -json ]; then src=$FAKE_SHOW_JSON; fi
    done
    if [ ! -r "$src" ]; then
      echo "fake terraform: cannot read $src" >&2
      exit 97
    fi
    cat "$src" ;;
  apply)
    if [ ! -e "$last" ]; then
      printf 'Error: Failed to load "%s" as a plan file\n' "$last" >&2
      exit 1
    fi
    printf '%s' "$FAKE_APPLY_OUTPUT"
    if [ "$FAKE_APPLY_EXIT" -ne 0 ]; then fail "$FAKE_APPLY_EXIT"; fi
    echo "Apply complete! Resources: 1 added, 0 changed, 0 destroyed." ;;
  output)
    echo "{}" ;;
esac
exit 0
`

func installFakeTerraform(dir string) (string, error) {
	bin := filepath.Join(dir, "terraform")
	if runtime.GOOS != "windows" {
		return bin, os.WriteFile(bin, []byte(fakeTFScript), 0o755) //nolint:gosec
	}
	bin += ".exe"
	ctx, cancel := context.WithTimeout(context.Background(), fakeBuildTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "./testdata/faketf").CombinedOutput(); err != nil {
		return "", fmt.Errorf("building the fake terraform: %w\n%s", err, out)
	}
	return bin, nil
}

func (c fakeTFConfig) shellEnv() string {
	var b strings.Builder
	for _, kv := range [][2]string{
		{"FAKE_LOG", c.Log},
		{"FAKE_TOFU", strconv.FormatBool(c.Tofu)},
		{"FAKE_VERSION", c.Version},
		{"FAKE_INIT_EXIT", strconv.Itoa(c.InitExit)},
		{"FAKE_BACKEND", string(c.Backend)},
		{"FAKE_PLAN_EXIT", strconv.Itoa(c.PlanExit)},
		{"FAKE_PLAN_OUTPUT", c.PlanOutput},
		{"FAKE_SHOW_JSON", c.ShowJSON},
		{"FAKE_SHOW_TEXT", c.ShowText},
		{"FAKE_SHOW_EXIT", strconv.Itoa(c.ShowExit)},
		{"FAKE_APPLY_EXIT", strconv.Itoa(c.ApplyExit)},
		{"FAKE_APPLY_OUTPUT", c.ApplyOutput},
		{"FAKE_FAIL_OUTPUT", c.FailOutput},
	} {
		b.WriteString(kv[0] + "='" + strings.ReplaceAll(kv[1], "'", `'\''`) + "'\n")
	}
	return b.String()
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

type harness struct {
	t       *testing.T
	root    string
	sha     string
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
	data := []byte(h.tf.shellEnv())
	if runtime.GOOS == "windows" {
		var err error
		data, err = json.Marshal(h.tf)
		require.NoError(h.t, err)
	}
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

type tfCall struct {
	args []string
	env  map[string]string
}

func (h *harness) tfLog() []tfCall {
	h.t.Helper()
	data, err := os.ReadFile(h.tf.Log)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(h.t, err)
	var out []tfCall
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		args, vars, _ := strings.Cut(line, "\x1e")
		call := tfCall{args: strings.Split(strings.TrimSuffix(args, "\x1f"), "\x1f"), env: map[string]string{}}
		for _, kv := range strings.Split(vars, "\x1f") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				call.env[k] = v
			}
		}
		out = append(out, call)
	}
	return out
}

func (h *harness) tfCalls() [][]string {
	h.t.Helper()
	calls := h.tfLog()
	out := make([][]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.args)
	}
	return out
}

func (h *harness) tfCall(command string) tfCall {
	h.t.Helper()
	for _, c := range h.tfLog() {
		if c.args[0] == command {
			return c
		}
	}
	h.t.Fatalf("terraform %s was not called; calls: %v", command, h.tfCalls())
	return tfCall{}
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
