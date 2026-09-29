package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// MaxInstanceNameLength is the longest instance name accepted.
const MaxInstanceNameLength = 64

var (
	instanceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	envNamePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ReservedEnvPrefixes are the prefixes an env variable configured in
// stackorder.yaml or .stackorder.yaml may not start with.
var ReservedEnvPrefixes = []string{"STACKORDER_", "GITHUB_", "ACTIONS_", "RUNNER_"}

// ReservedEnvNames are the env variable names that may not be configured.
var ReservedEnvNames = []string{"PATH", "HOME"}

// ValidateInstanceName checks an instance name: [A-Za-z0-9][A-Za-z0-9._-]*,
// at most MaxInstanceNameLength characters and never "default" in any
// letter case.
func ValidateInstanceName(name string) error {
	switch {
	case name == "":
		return errors.New("empty instance name")
	case len(name) > MaxInstanceNameLength:
		return fmt.Errorf("instance name %q is longer than %d characters", name, MaxInstanceNameLength)
	case strings.EqualFold(name, v1.DefaultEnvironment):
		return fmt.Errorf("instance name %q is reserved", name)
	case !instanceNamePattern.MatchString(name):
		return fmt.Errorf("instance name %q must match [A-Za-z0-9][A-Za-z0-9._-]*", name)
	}
	return nil
}

// ValidateEnvName checks the name of a configured env variable:
// [A-Za-z_][A-Za-z0-9_]*, not starting with a ReservedEnvPrefixes entry and
// not one of ReservedEnvNames, both compared without regard to case.
func ValidateEnvName(name string) error {
	if !envNamePattern.MatchString(name) {
		return fmt.Errorf("variable name %q must match [A-Za-z_][A-Za-z0-9_]*", name)
	}
	upper := strings.ToUpper(name)
	for _, prefix := range ReservedEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return fmt.Errorf("variable name %q uses the reserved prefix %s", name, prefix)
		}
	}
	if slices.Contains(ReservedEnvNames, upper) {
		return fmt.Errorf("variable name %q is reserved", name)
	}
	return nil
}

type addFunc func(format string, args ...any)

func isAbsPath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") || filepath.IsAbs(p)
}

func validateTemplateField(add addFunc, key, value string) {
	if err := checkTemplate(value); err != nil {
		add("%s: %v", key, err)
	}
}

func validateWorkspace(add addFunc, key, ws string) {
	if isTemplate(ws) {
		validateTemplateField(add, key, ws)
		return
	}
	if strings.ContainsAny(ws, "/: \t") {
		add("%s: %q contains invalid characters", key, ws)
	}
}

func validateDependsOn(add addFunc, key string, deps []string) {
	for i, d := range deps {
		rendered, err := Render(d, sampleTemplateData)
		if err != nil {
			add("%s[%d]: %v", key, i, err)
			continue
		}
		if _, _, err := ParseDependency(rendered); err != nil {
			add("%s[%d]: %v", key, i, err)
		}
	}
}

func validateIgnoreInferred(add addFunc, key string, keys []string) {
	for i, k := range keys {
		if k == "" {
			add("%s[%d]: empty stack key", key, i)
			continue
		}
		validateTemplateField(add, fmt.Sprintf("%s[%d]", key, i), k)
	}
}

func validateTeams(add addFunc, key string, teams []string) {
	for i, t := range teams {
		if t == "" {
			add("%s[%d]: empty team", key, i)
		}
	}
}

func validateBackendConfig(add addFunc, key string, values []string) {
	for i, v := range values {
		k := fmt.Sprintf("%s[%d]", key, i)
		switch name, _, isPair := strings.Cut(v, "="); {
		case strings.TrimSpace(v) == "":
			add("%s: empty entry", k)
		case isTemplate(v):
			validateTemplateField(add, k, v)
		case isPair && strings.TrimSpace(name) == "":
			add("%s: %q has no attribute name before =", k, v)
		case !isPair && isAbsPath(v):
			add("%s: %q must be a repository relative file", k, v)
		}
	}
}

func validateVarFiles(add addFunc, key string, values []string) {
	for i, v := range values {
		k := fmt.Sprintf("%s[%d]", key, i)
		switch {
		case strings.TrimSpace(v) == "":
			add("%s: empty path", k)
		case isTemplate(v):
			validateTemplateField(add, k, v)
		case isAbsPath(v):
			add("%s: %q must be a stack relative file", k, v)
		}
	}
}

func validateEnv(add addFunc, key string, env v1.EnvConfig) {
	for _, name := range sortedKeys(env) {
		k := key + "." + name
		if err := ValidateEnvName(name); err != nil {
			add("%s: %v", k, err)
		}
		value := env[name]
		if value.Modes == nil {
			validateTemplateField(add, k, value.Value)
			continue
		}
		if value.Modes.Plan == nil && value.Modes.Apply == nil && value.Modes.Drift == nil {
			add("%s: set at least one of plan, apply and drift", k)
		}
		for _, mv := range envModeValues(value.Modes) {
			validateTemplateField(add, k+"."+string(mv.mode), mv.value)
		}
	}
}

func validateInstance(add addFunc, prefix string, ic v1.InstanceConfig) {
	validateTemplateField(add, prefix+"environment", ic.Environment)
	validateWorkspace(add, prefix+"workspace", ic.Workspace)
	validateBackendConfig(add, prefix+"backend_config", ic.BackendConfig)
	validateVarFiles(add, prefix+"var_files", ic.VarFiles)
	validateEnv(add, prefix+"env", ic.Env)
	if ic.PlanOutput != "" && !validPlanOutput(ic.PlanOutput) {
		add("%splan_output: %q is not one of full, summary", prefix, ic.PlanOutput)
	}
	if ic.Apply != nil {
		validateTeams(add, prefix+"apply.allowed_teams", ic.Apply.AllowedTeams)
	}
	validateDependsOn(add, prefix+"depends_on", ic.DependsOn)
	validateIgnoreInferred(add, prefix+"ignore_inferred", ic.IgnoreInferred)
}

type modeValue struct {
	mode  v1.RunMode
	value string
}

func envModeValues(m *v1.EnvModes) []modeValue {
	var out []modeValue
	for _, mv := range []struct {
		mode  v1.RunMode
		value *string
	}{{v1.ModePlan, m.Plan}, {v1.ModeApply, m.Apply}, {v1.ModeDrift, m.Drift}} {
		if mv.value != nil {
			out = append(out, modeValue{mv.mode, *mv.value})
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
