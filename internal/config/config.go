// Package config loads, defaults and validates the root stackorder.yaml and
// the per-stack .stackorder.yaml files, and merges them into the effective
// settings of one stack.
package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const (
	// RootFile is the name of the repository level configuration file.
	RootFile = "stackorder.yaml"
	// StackFile is the name of the per-stack configuration file.
	StackFile = ".stackorder.yaml"
	// LockfileName is the provider lock file ignored by ignore_lockfile.
	LockfileName = ".terraform.lock.hcl"
)

var (
	// DefaultDiscover is the default stacks.discover list.
	DefaultDiscover = []string{"stacks/**"}
	// DefaultIgnore is the default stacks.ignore list.
	DefaultIgnore = []string{"**/*.md", "**/README*"}
	// DefaultModulePaths is the default modules.paths list.
	DefaultModulePaths = []string{"modules/**"}
)

// Default returns a fully defaulted configuration equivalent to a file that
// contains only "version: 1".
func Default() *v1.RepoConfig {
	c := &v1.RepoConfig{Version: 1}
	ApplyDefaults(c)
	return c
}

// ApplyDefaults fills every zero field with its documented default.
func ApplyDefaults(c *v1.RepoConfig) {
	if c.Version == 0 {
		c.Version = 1
	}
	if len(c.Stacks.Discover) == 0 {
		c.Stacks.Discover = append([]string(nil), DefaultDiscover...)
	}
	if c.Stacks.Ignore == nil {
		c.Stacks.Ignore = append([]string(nil), DefaultIgnore...)
	}
	if len(c.Modules.Paths) == 0 {
		c.Modules.Paths = append([]string(nil), DefaultModulePaths...)
	}
	if c.Tool == "" {
		c.Tool = v1.ToolTerraform
	}
	if c.Environments == nil {
		c.Environments = map[string]string{}
	}
	if c.Apply.Mode == "" {
		c.Apply.Mode = v1.ApplyBeforeMerge
	}
	if c.Apply.MaxParallel == 0 {
		c.Apply.MaxParallel = 6
	}
	if c.Apply.FromPlan == nil {
		t := true
		c.Apply.FromPlan = &t
	}
	if c.Propagate.Dependents == nil {
		t := true
		c.Propagate.Dependents = &t
	}
	if c.Propagate.CrossRepo == "" {
		c.Propagate.CrossRepo = v1.CrossRepoOff
	}
	if c.PlanOutput == "" {
		c.PlanOutput = v1.PlanOutputFull
	}
}

// Parse decodes a root configuration from YAML, applies defaults and
// validates it.
func Parse(data []byte) (*v1.RepoConfig, error) {
	var c v1.RepoConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "EOF") {
			return nil, fmt.Errorf("%s: file is empty; the minimum is `version: 1`", RootFile)
		}
		return nil, fmt.Errorf("%s: %w", RootFile, err)
	}
	ApplyDefaults(&c)
	if err := Validate(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Load reads the root configuration from dir. A missing file yields the
// default configuration and ok == false.
func Load(dir string) (cfg *v1.RepoConfig, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, RootFile))
	if errors.Is(err, os.ErrNotExist) {
		return Default(), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	cfg, err = Parse(data)
	return cfg, err == nil, err
}

// Validate checks a defaulted configuration.
func Validate(c *v1.RepoConfig) error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.Version != 1 {
		add("version: unsupported value %d; only 1 is supported", c.Version)
	}
	if !validTool(c.Tool) {
		add("tool: %q is not one of terraform, tofu", c.Tool)
	}
	switch c.Apply.Mode {
	case v1.ApplyBeforeMerge, v1.ApplyOnMerge:
	default:
		add("apply.mode: %q is not one of before_merge, on_merge", c.Apply.Mode)
	}
	if c.Apply.RequireApprovals < 0 {
		add("apply.require_approvals: must not be negative")
	}
	if c.Apply.MaxParallel < 1 {
		add("apply.max_parallel: must be at least 1")
	}
	switch c.Propagate.CrossRepo {
	case v1.CrossRepoOff, v1.CrossRepoPlan:
	default:
		add("propagate.cross_repo: %q is not one of off, plan", c.Propagate.CrossRepo)
	}
	if !validPlanOutput(c.PlanOutput) {
		add("plan_output: %q is not one of full, summary", c.PlanOutput)
	}
	if c.Drift.Schedule != "" {
		if _, err := ParseCron(c.Drift.Schedule); err != nil {
			add("drift.schedule: %v", err)
		}
	}
	for prefix, env := range c.Environments {
		if prefix == "" {
			add("environments: empty path prefix")
		}
		if env == "" {
			add("environments[%q]: empty environment name", prefix)
		}
	}
	for i, g := range c.Stacks.Discover {
		if g == "" {
			add("stacks.discover[%d]: empty glob", i)
		}
	}
	for i, p := range c.Stacks.Include {
		if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") {
			add("stacks.include[%d]: %q must be a repository relative path", i, p)
		}
	}
	for i, t := range c.Apply.AllowedTeams {
		if t == "" {
			add("apply.allowed_teams[%d]: empty team", i)
		}
	}
	return errors.Join(errs...)
}

// ParseCron validates a five field cron expression.
func ParseCron(expr string) (cron.Schedule, error) {
	return cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(expr)
}

// ParseStack decodes a per-stack configuration and validates it.
func ParseStack(data []byte) (*v1.StackConfig, error) {
	var s v1.StackConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if strings.Contains(err.Error(), "EOF") {
			return &s, nil
		}
		return nil, fmt.Errorf("%s: %w", StackFile, err)
	}
	if err := ValidateStack(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// LoadStack reads the per-stack configuration from a stack directory. A
// missing file yields an empty configuration and ok == false.
func LoadStack(dir string) (cfg *v1.StackConfig, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, StackFile))
	if errors.Is(err, os.ErrNotExist) {
		return &v1.StackConfig{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	cfg, err = ParseStack(data)
	return cfg, err == nil, err
}

// ValidateStack checks a per-stack configuration.
func ValidateStack(s *v1.StackConfig) error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if s.Tool != "" && !validTool(s.Tool) {
		add("tool: %q is not one of terraform, tofu", s.Tool)
	}
	if s.PlanOutput != "" && !validPlanOutput(s.PlanOutput) {
		add("plan_output: %q is not one of full, summary", s.PlanOutput)
	}
	for i, d := range s.DependsOn {
		if _, _, err := ParseDependency(d); err != nil {
			add("depends_on[%d]: %v", i, err)
		}
	}
	for i, d := range s.IgnoreInferred {
		if d == "" {
			add("ignore_inferred[%d]: empty stack key", i)
		}
	}
	if strings.ContainsAny(s.Workspace, "/: \t") {
		add("workspace: %q contains invalid characters", s.Workspace)
	}
	return errors.Join(errs...)
}

// ParseDependency splits a depends_on entry into its repository and stack
// key. The repository is empty for a same repository dependency.
func ParseDependency(dep string) (repo, key string, err error) {
	dep = strings.TrimSpace(dep)
	if dep == "" {
		return "", "", errors.New("empty dependency")
	}
	repo, key = v1.SplitQualifiedStackKey(dep)
	if repo != "" && strings.Count(repo, "/") != 1 {
		return "", "", fmt.Errorf("%q: repository must be owner/repo", dep)
	}
	if strings.HasPrefix(key, "/") || strings.HasPrefix(key, "\\") {
		return "", "", fmt.Errorf("%q: stack path must be repository relative", dep)
	}
	key = NormalizePath(key)
	if key == "" || strings.HasPrefix(key, "..") || path.IsAbs(key) {
		return "", "", fmt.Errorf("%q: stack path must be repository relative", dep)
	}
	return repo, key, nil
}

// NormalizePath cleans a repository relative path: forward slashes, no
// leading "./" and no trailing "/". The workspace suffix is preserved.
func NormalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p, ws := v1.SplitStackKey(p)
	p = strings.TrimPrefix(p, "./")
	p = path.Clean(p)
	if p == "." {
		p = ""
	}
	p = strings.Trim(p, "/")
	return v1.StackKey(p, ws)
}

// Effective is the merged configuration of one stack.
type Effective struct {
	Key            string
	Path           string
	Workspace      string
	Tool           v1.Tool
	ToolVersion    string
	Environment    string
	PlanOutput     v1.PlanOutput
	AllowedTeams   []string
	DependsOn      []string
	IgnoreInferred []string
}

// Resolve merges the root configuration with a stack's own file. stack may
// be nil.
func Resolve(root *v1.RepoConfig, stackPath string, stack *v1.StackConfig) Effective {
	if stack == nil {
		stack = &v1.StackConfig{}
	}
	stackPath = NormalizePath(stackPath)
	e := Effective{
		Path:           stackPath,
		Workspace:      stack.Workspace,
		Tool:           root.Tool,
		ToolVersion:    root.ToolVersion,
		Environment:    EnvironmentFor(root.Environments, stackPath),
		PlanOutput:     root.PlanOutput,
		AllowedTeams:   append([]string(nil), root.Apply.AllowedTeams...),
		DependsOn:      append([]string(nil), stack.DependsOn...),
		IgnoreInferred: append([]string(nil), stack.IgnoreInferred...),
	}
	if e.Workspace == "default" {
		e.Workspace = ""
	}
	e.Key = v1.StackKey(stackPath, e.Workspace)
	if stack.Tool != "" {
		e.Tool = stack.Tool
	}
	if stack.ToolVersion != "" {
		e.ToolVersion = stack.ToolVersion
	}
	if stack.Environment != "" {
		e.Environment = stack.Environment
	}
	if stack.PlanOutput != "" {
		e.PlanOutput = stack.PlanOutput
	}
	if stack.Apply != nil && len(stack.Apply.AllowedTeams) > 0 {
		e.AllowedTeams = append([]string(nil), stack.Apply.AllowedTeams...)
	}
	return e
}

// EnvironmentFor picks the GitHub environment for a stack path from the
// prefix map. The longest matching prefix wins; no match yields "".
func EnvironmentFor(environments map[string]string, stackPath string) string {
	stackPath = strings.TrimSuffix(NormalizePath(stackPath), "/") + "/"
	best, bestLen := "", -1
	keys := make([]string, 0, len(environments))
	for k := range environments {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, prefix := range keys {
		p := strings.TrimSuffix(strings.TrimPrefix(prefix, "./"), "/") + "/"
		if strings.HasPrefix(stackPath, p) && len(p) > bestLen {
			best, bestLen = environments[prefix], len(p)
		}
	}
	return best
}

// IgnoreGlobs returns the effective ignore list including the lock file when
// ignore_lockfile is set.
func IgnoreGlobs(c *v1.RepoConfig) []string {
	globs := append([]string(nil), c.Stacks.Ignore...)
	if c.Stacks.IgnoreLockfile {
		globs = append(globs, "**/"+LockfileName)
	}
	return globs
}

func validTool(t v1.Tool) bool {
	return t == v1.ToolTerraform || t == v1.ToolTofu
}

func validPlanOutput(p v1.PlanOutput) bool {
	return p == v1.PlanOutputFull || p == v1.PlanOutputSummary
}
