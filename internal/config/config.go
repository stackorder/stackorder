// Package config loads, defaults and validates the root stackorder.yaml and
// the per-stack .stackorder.yaml files, and merges them into the effective
// settings of one stack.
package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
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
	seenEnvironments := map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(c.Environments)) {
		env := c.Environments[key]
		prefix, instance, hasInstance := splitEnvironmentKey(key)
		normalized := normalizeEnvironmentPrefix(prefix)
		switch {
		case strings.Contains(prefix, ":"):
			add("environments[%q]: path prefix %q contains \":\"", key, prefix)
		case normalized == "" && !hasInstance:
			add("environments[%q]: empty path prefix; only a key with an instance part (\":instance\") may leave it empty", key)
		case hasInstance:
			if err := ValidateInstanceName(instance); err != nil {
				add("environments[%q]: %v", key, err)
			}
		}
		id := normalized + ":" + instance
		if !hasInstance {
			id = normalized
		}
		if other, dup := seenEnvironments[id]; dup {
			add("environments: keys %q and %q name the same path prefix and instance", other, key)
		} else {
			seenEnvironments[id] = key
		}
		if env == "" {
			add("environments[%q]: empty environment name", key)
		} else if err := checkTemplate(env); err != nil {
			add("environments[%q]: %v", key, err)
		}
	}
	for i, g := range c.Stacks.Discover {
		if g == "" {
			add("stacks.discover[%d]: empty glob", i)
		}
	}
	for i, g := range c.Stacks.Exclude {
		switch {
		case g == "":
			add("stacks.exclude[%d]: empty glob", i)
		case strings.HasPrefix(g, "/") || !doublestar.ValidatePattern(g):
			add("stacks.exclude[%d]: %q is not a repository relative glob", i, g)
		}
	}
	if g := c.Stacks.Instances.FromVarFiles; g != "" {
		if strings.HasPrefix(g, "/") || filepath.IsAbs(g) || !doublestar.ValidatePattern(g) {
			add("stacks.instances.from_var_files: %q is not a stack relative glob", g)
		}
	}
	validateBackendConfig(add, "backend_config", c.BackendConfig)
	validateVarFiles(add, "var_files", c.VarFiles)
	validateEnv(add, "env", c.Env)
	for i, p := range c.Stacks.Include {
		if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") {
			add("stacks.include[%d]: %q must be a repository relative path", i, p)
		}
	}
	validateTeams(add, "apply.allowed_teams", c.Apply.AllowedTeams)
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
	validateDependsOn(add, "depends_on", s.DependsOn)
	validateIgnoreInferred(add, "ignore_inferred", s.IgnoreInferred)
	validateWorkspace(add, "workspace", s.Workspace)
	if s.Workspace != "" && len(s.Instances) == 0 {
		if ws, err := Render(s.Workspace, TemplateDataFor(sampleTemplateData.Path, "")); err == nil && ws != "" && ws != "default" {
			if err := ValidateInstanceName(ws); err != nil {
				add("workspace: %q names the stack's only instance: %v", ws, err)
			}
		}
	}
	validateTemplateField(add, "environment", s.Environment)
	if s.Apply != nil {
		validateTeams(add, "apply.allowed_teams", s.Apply.AllowedTeams)
	}
	validateBackendConfig(add, "backend_config", s.BackendConfig)
	validateVarFiles(add, "var_files", s.VarFiles)
	validateEnv(add, "env", s.Env)
	for _, name := range s.Instances.Names() {
		if err := ValidateInstanceName(name); err != nil {
			add("instances: %v", err)
			continue
		}
		validateInstance(add, "instances."+name+".", s.Instances[name])
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
// leading "./" and no trailing "/". An instance suffix (":instance") is
// kept intact.
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
