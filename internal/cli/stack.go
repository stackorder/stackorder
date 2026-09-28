package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tfjson "github.com/hashicorp/terraform-json"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/tf"
)

const (
	maxErrorText   = 64 * 1024
	errorTailLines = 40
	reportTimeout  = 30 * time.Second
)

var plainVersion = regexp.MustCompile(`^\d+(\.\d+){0,2}$`)

type stack struct {
	key       string
	path      string
	workspace string
	dir       string
	cfg       *v1.RepoConfig
	eff       config.Effective
}

func loadStack(root, key string) (*stack, error) {
	key = config.NormalizePath(key)
	p, ws := v1.SplitStackKey(key)
	if p == "" || p == ".." || strings.HasPrefix(p, "../") {
		return nil, fmt.Errorf("stack %q: not a repository relative directory", key)
	}
	cfg, _, err := config.Load(root)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", config.RootFile, err)
	}
	dir := filepath.Join(root, filepath.FromSlash(p))
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stack %s: %w", key, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("stack %s: %s is not a directory", key, dir)
	}
	sc, _, err := config.LoadStack(dir)
	if err != nil {
		return nil, fmt.Errorf("stack %s: loading %s: %w", key, config.StackFile, err)
	}
	eff := config.Resolve(cfg, p, sc)
	if ws == "" {
		ws = eff.Workspace
	}
	eff.Workspace = ws
	eff.Key = v1.StackKey(p, ws)
	return &stack{key: eff.Key, path: p, workspace: ws, dir: dir, cfg: cfg, eff: eff}, nil
}

func backendConfig() []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(EnvBackendConfig), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func readBackend(dir string) *v1.Backend {
	dataDir := os.Getenv("TF_DATA_DIR")
	switch {
	case dataDir == "":
		dataDir = filepath.Join(dir, ".terraform")
	case !filepath.IsAbs(dataDir):
		dataDir = filepath.Join(dir, dataDir)
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "terraform.tfstate")) //nolint:gosec
	if err != nil {
		return nil
	}
	var state struct {
		Backend *struct {
			Type   string         `json:"type"`
			Config map[string]any `json:"config"`
		} `json:"backend"`
	}
	if err := json.Unmarshal(data, &state); err != nil || state.Backend == nil || state.Backend.Type == "" {
		return nil
	}
	str := func(k string) string {
		s, _ := state.Backend.Config[k].(string)
		return s
	}
	lockfile, _ := state.Backend.Config["use_lockfile"].(bool)
	return &v1.Backend{
		Type:               state.Backend.Type,
		Bucket:             str("bucket"),
		Key:                str("key"),
		Region:             str("region"),
		DynamoDBTable:      str("dynamodb_table"),
		UseLockfile:        lockfile,
		WorkspaceKeyPrefix: str("workspace_key_prefix"),
	}
}

func versionMatches(want, got string) bool {
	want = strings.TrimPrefix(strings.TrimSpace(want), "v")
	if !plainVersion.MatchString(want) {
		return true
	}
	return got == want || strings.HasPrefix(got, want+".")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func errorExcerpt(output string) string {
	lines := strings.SplitAfter(output, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "│╷╵")), "Error:") {
			return strings.TrimSpace(strings.Join(lines[i:], ""))
		}
	}
	if len(lines) > errorTailLines {
		lines = lines[len(lines)-errorTailLines:]
	}
	return strings.TrimSpace(strings.Join(lines, ""))
}

type session struct {
	a        *app
	gh       *Context
	root     string
	key      string
	runID    string
	st       *stack
	secrets  []string
	redactor *tf.Redactor
	out      io.Writer
	errOut   io.Writer
	flushers []func() error
	start    time.Time

	tool        v1.Tool
	bin         string
	toolVersion string
	backend     *v1.Backend
}

func (a *app) newSession(ctx context.Context, key, runID string) (*session, error) {
	gh, err := a.github(ctx)
	if err != nil {
		return nil, err
	}
	key = config.NormalizePath(strings.TrimSpace(key))
	if key == "" {
		return nil, failed("--stack is required")
	}
	secrets := envSecrets()
	s := &session{
		a:        a,
		gh:       gh,
		root:     a.root,
		key:      key,
		runID:    firstNonEmpty(runID, os.Getenv(EnvRunID), gh.DispatchRunID),
		secrets:  secrets,
		redactor: tf.NewRedactor(secrets),
		out:      a.toolOut(),
		errOut:   a.stderr,
		start:    time.Now(),
	}
	if gh.CI {
		for _, cmd := range tf.MaskCommands(secrets) {
			_, _ = fmt.Fprintln(a.toolOut(), cmd)
		}
		out := newMaskWriter(a.toolOut(), s.redactor, secrets)
		errOut := newMaskWriter(a.stderr, s.redactor, secrets)
		s.out, s.errOut = out, errOut
		s.flushers = []func() error{out.Flush, errOut.Flush}
	}
	return s, nil
}

func (s *session) flush() {
	for _, f := range s.flushers {
		_ = f()
	}
}

func (s *session) loadStack() error {
	st, err := loadStack(s.root, s.key)
	if err != nil {
		return err
	}
	s.st, s.key = st, st.key
	return nil
}

func (s *session) detectTool(ctx context.Context) error {
	tool := s.st.eff.Tool
	if v := strings.TrimSpace(os.Getenv(EnvTool)); v != "" {
		tool = v1.Tool(strings.ToLower(v))
	}
	bin, err := tf.Detect(tool)
	if err != nil {
		return err
	}
	ver, isTofu, err := tf.Version(ctx, bin)
	if err != nil {
		return err
	}
	actual := v1.ToolTerraform
	if isTofu {
		actual = v1.ToolTofu
	}
	if actual != cmp.Or(tool, v1.ToolTerraform) {
		s.a.warn(fmt.Sprintf("stack %s is configured for %s but %s reports %s %s", s.key, tool, bin, actual, ver))
	}
	if want := firstNonEmpty(os.Getenv(EnvToolVersion), s.st.eff.ToolVersion); want != "" && !versionMatches(want, ver) {
		s.a.warn(fmt.Sprintf("stack %s wants %s %s but %s is %s", s.key, actual, want, bin, ver))
	}
	s.tool, s.bin, s.toolVersion = actual, bin, ver
	s.a.log.Debug("detected tool", "tool", actual, "version", ver, "bin", bin)
	return nil
}

func (s *session) runner() *tf.Runner {
	return &tf.Runner{Bin: s.bin, Dir: s.st.dir, Stdout: s.out, Stderr: s.errOut, Workspace: s.st.workspace}
}

func (s *session) init(ctx context.Context, r *tf.Runner) error {
	if err := r.Init(ctx, backendConfig(), tf.InitOptions{}); err != nil {
		return err
	}
	s.backend = readBackend(s.st.dir)
	if s.st.workspace != "" {
		return r.SelectWorkspace(ctx)
	}
	return nil
}

func (s *session) runHook(ctx context.Context, name, planFile, planJSON string) error {
	env := map[string]string{
		tf.HookEnvStack:    s.key,
		tf.HookEnvRunID:    s.runID,
		tf.HookEnvPlanFile: planFile,
		tf.HookEnvPlanJSON: planJSON,
	}
	ran, err := tf.RunHook(ctx, s.root, name, env, s.out, s.errOut)
	if ran {
		s.a.log.Info("ran hook", "hook", name, "stack", s.key)
	}
	if err != nil {
		return fmt.Errorf("%s hook: %w", name, err)
	}
	return nil
}

func (s *session) planDir() (string, error) {
	dir := firstNonEmpty(os.Getenv(EnvPlanDir), DefaultPlanDir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.root, filepath.FromSlash(dir))
	}
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec
		return "", fmt.Errorf("creating the plan directory: %w", err)
	}
	return dir, nil
}

func (s *session) artifact(sha string) string {
	return v1.PlanArtifactName(s.key, cmp.Or(sha, "local"))
}

func planJSONPath(planFile string) string {
	return strings.TrimSuffix(planFile, filepath.Ext(planFile)) + ".json"
}

func (s *session) writePlanJSON(ctx context.Context, r *tf.Runner, planFile, jsonFile string) (*tfjson.Plan, error) {
	raw, err := r.ShowJSONRaw(ctx, planFile)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(jsonFile, raw, 0o600); err != nil {
		return nil, fmt.Errorf("writing the plan JSON: %w", err)
	}
	return tf.ParsePlanJSON(raw)
}

func (s *session) planText(ctx context.Context, r *tf.Runner, planFile string) (string, bool, error) {
	if s.st.eff.PlanOutput == v1.PlanOutputSummary {
		return "", false, nil
	}
	text, err := r.ShowText(ctx, planFile)
	if err != nil {
		return "", false, err
	}
	out, cut := tf.Truncate(s.redactor.Redact(text), tf.MaxPlanText)
	return out, cut, nil
}

func (s *session) fail(res *v1.StackResult, err error) {
	res.Status = v1.ResultError
	text := err.Error()
	var exitErr *tf.ExitError
	var refusal *ExitError
	switch {
	case errors.As(err, &exitErr):
		res.Status = v1.ResultFailure
		res.ExitCode = exitErr.ExitCode
		if exitErr.Output != "" {
			text += "\n\n" + errorExcerpt(exitErr.Output)
		}
	case errors.As(err, &refusal) && refusal.Code == ExitRefused:
		res.Status = v1.ResultFailure
		res.ExitCode = ExitRefused
	default:
		res.ExitCode = ExitFailure
	}
	res.ErrorText, _ = tf.Truncate(s.redactor.Redact(text), maxErrorText)
}

func (s *session) finalize(res *v1.StackResult) {
	res.JobURL = s.gh.JobURL()
	res.Tool = s.tool
	res.ToolVersion = s.toolVersion
	res.Backend = s.backend
	res.DurationMS = time.Since(s.start).Milliseconds()
}

func (s *session) post(ctx context.Context, res v1.StackResult) error {
	cl, err := s.a.newClient(s.gh, !s.gh.CI)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), reportTimeout)
		defer cancel()
	}
	if _, err := cl.PostResult(ctx, s.runID, s.key, res); err != nil {
		return fmt.Errorf("reporting the %s result of %s to run %s: %w", res.Mode, s.key, s.runID, err)
	}
	return nil
}

func (s *session) report(ctx context.Context, res v1.StackResult) (unconfirmed bool, reason string, err error) {
	switch {
	case s.a.server == "":
		return true, "no server is configured", nil
	case s.runID == "":
		return true, "no run id was given, so the result was not reported", nil
	case !s.gh.CI && strings.TrimSpace(os.Getenv(EnvAPIKey)) == "":
		return true, "outside GitHub Actions the server needs " + EnvAPIKey, nil
	}
	err = s.post(ctx, res)
	switch {
	case err == nil:
		return false, "", nil
	case isUnreachable(err):
		return true, "the server is unreachable: " + err.Error(), nil
	}
	return true, "", err
}

type stackOutput struct {
	Stack       string         `json:"stack"`
	RunID       string         `json:"run_id,omitempty"`
	PlanFile    string         `json:"plan_file,omitempty"`
	Unconfirmed bool           `json:"unconfirmed"`
	Result      v1.StackResult `json:"result"`
}

func (s *session) print(res v1.StackResult, planFile string, unconfirmed bool, line string) error {
	if s.a.format == formatJSON {
		return s.a.writeJSON(stackOutput{Stack: s.key, RunID: s.runID, PlanFile: planFile, Unconfirmed: unconfirmed, Result: res})
	}
	_, err := fmt.Fprintf(s.a.stdout, "%s: %s\n", s.key, line)
	return err
}
