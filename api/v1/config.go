package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// ApplyMode says when applies happen relative to the merge.
type ApplyMode string

const (
	ApplyBeforeMerge ApplyMode = "before_merge"
	ApplyOnMerge     ApplyMode = "on_merge"
)

// CrossRepoMode says what happens to external dependents after an apply.
type CrossRepoMode string

const (
	CrossRepoOff  CrossRepoMode = "off"
	CrossRepoPlan CrossRepoMode = "plan"
)

// PlanOutput says how much of a plan reaches the server and the PR comment.
type PlanOutput string

const (
	PlanOutputFull    PlanOutput = "full"
	PlanOutputSummary PlanOutput = "summary"
)

// RepoConfig is the root stackorder.yaml. Field defaults are applied by
// internal/config; the zero value is not valid on its own.
type RepoConfig struct {
	Version     int           `json:"version" yaml:"version"`
	Stacks      StacksConfig  `json:"stacks" yaml:"stacks"`
	Modules     ModulesConfig `json:"modules" yaml:"modules"`
	Tool        Tool          `json:"tool" yaml:"tool"`
	ToolVersion string        `json:"tool_version,omitempty" yaml:"tool_version"`
	// Environments maps path prefixes to GitHub environments. A key is
	// "prefix", "prefix:instance" or ":instance"; a value is a template.
	Environments map[string]string `json:"environments,omitempty" yaml:"environments"`
	Apply        ApplyConfig       `json:"apply" yaml:"apply"`
	Propagate    PropagateConfig   `json:"propagate" yaml:"propagate"`
	Drift        DriftConfig       `json:"drift" yaml:"drift"`
	PlanOutput   PlanOutput        `json:"plan_output" yaml:"plan_output"`
	// BackendConfig lists -backend-config values for init, before those of
	// the stack and the instance: "name=value", or a repository relative
	// file.
	BackendConfig []string `json:"backend_config,omitempty" yaml:"backend_config"`
	// VarFiles lists stack relative -var-file values for plan, before those
	// of the stack and the instance.
	VarFiles []string `json:"var_files,omitempty" yaml:"var_files"`
	// Env sets environment variables for the tool and the hooks.
	Env EnvConfig `json:"env,omitempty" yaml:"env"`
}

// StacksConfig controls stack discovery.
type StacksConfig struct {
	// Discover lists directory globs; a match is a stack when it contains a
	// terraform block with an s3 backend.
	Discover []string `json:"discover" yaml:"discover"`
	// Include lists directories that are stacks regardless of discovery.
	Include []string `json:"include,omitempty" yaml:"include"`
	// Ignore lists file globs whose changes never affect a stack.
	Ignore []string `json:"ignore" yaml:"ignore"`
	// IgnoreLockfile adds .terraform.lock.hcl to Ignore.
	IgnoreLockfile bool `json:"ignore_lockfile,omitempty" yaml:"ignore_lockfile"`
	// Exclude lists directory globs that are never stacks; it beats
	// Discover and Include.
	Exclude []string `json:"exclude,omitempty" yaml:"exclude"`
	// Instances derives the instances of stacks that declare none.
	Instances InstancesConfig `json:"instances,omitzero" yaml:"instances"`
}

// InstancesConfig derives the instance set of stacks that declare none.
type InstancesConfig struct {
	// FromVarFiles is a stack relative glob; every matching file is an
	// instance named by the file's base name up to its first ".".
	FromVarFiles string `json:"from_var_files,omitempty" yaml:"from_var_files"`
}

// ModulesConfig controls local module discovery.
type ModulesConfig struct {
	Paths []string `json:"paths" yaml:"paths"`
}

// ApplyConfig is the apply gate policy.
type ApplyConfig struct {
	Mode                   ApplyMode `json:"mode" yaml:"mode"`
	RequireApprovals       int       `json:"require_approvals" yaml:"require_approvals"`
	RequireCodeownerReview bool      `json:"require_codeowner_review,omitempty" yaml:"require_codeowner_review"`
	AllowedTeams           []string  `json:"allowed_teams,omitempty" yaml:"allowed_teams"`
	FourEyes               bool      `json:"four_eyes,omitempty" yaml:"four_eyes"`
	FromPlan               *bool     `json:"from_plan,omitempty" yaml:"from_plan"`
	MaxParallel            int       `json:"max_parallel" yaml:"max_parallel"`
}

// FromPlanEnabled returns FromPlan with its default of true.
func (a ApplyConfig) FromPlanEnabled() bool { return a.FromPlan == nil || *a.FromPlan }

// PropagateConfig controls how changes ripple through the graph.
type PropagateConfig struct {
	Dependents *bool         `json:"dependents,omitempty" yaml:"dependents"`
	CrossRepo  CrossRepoMode `json:"cross_repo" yaml:"cross_repo"`
}

// DependentsEnabled returns Dependents with its default of true.
func (p PropagateConfig) DependentsEnabled() bool { return p.Dependents == nil || *p.Dependents }

// DriftConfig controls scheduled drift detection.
type DriftConfig struct {
	// Schedule is a five field cron expression; empty disables drift runs.
	Schedule  string `json:"schedule,omitempty" yaml:"schedule"`
	OpenIssue bool   `json:"open_issue,omitempty" yaml:"open_issue"`
}

// StackConfig is a per-stack .stackorder.yaml.
type StackConfig struct {
	// DependsOn lists stack keys, optionally qualified with "owner/repo//".
	DependsOn      []string          `json:"depends_on,omitempty" yaml:"depends_on"`
	Workspace      string            `json:"workspace,omitempty" yaml:"workspace"`
	Tool           Tool              `json:"tool,omitempty" yaml:"tool"`
	ToolVersion    string            `json:"tool_version,omitempty" yaml:"tool_version"`
	Environment    string            `json:"environment,omitempty" yaml:"environment"`
	Apply          *StackApplyConfig `json:"apply,omitempty" yaml:"apply"`
	PlanOutput     PlanOutput        `json:"plan_output,omitempty" yaml:"plan_output"`
	IgnoreInferred []string          `json:"ignore_inferred,omitempty" yaml:"ignore_inferred"`
	// Instances declares the instances of the directory, as a list of
	// names or a map from name to overrides.
	Instances     Instances `json:"instances,omitempty" yaml:"instances"`
	BackendConfig []string  `json:"backend_config,omitempty" yaml:"backend_config"`
	VarFiles      []string  `json:"var_files,omitempty" yaml:"var_files"`
	Env           EnvConfig `json:"env,omitempty" yaml:"env"`
}

// StackApplyConfig narrows the apply policy for one stack.
type StackApplyConfig struct {
	AllowedTeams []string `json:"allowed_teams,omitempty" yaml:"allowed_teams"`
}

// InstanceConfig overrides the stack settings for one instance.
type InstanceConfig struct {
	Environment    string            `json:"environment,omitempty" yaml:"environment"`
	Workspace      string            `json:"workspace,omitempty" yaml:"workspace"`
	BackendConfig  []string          `json:"backend_config,omitempty" yaml:"backend_config"`
	VarFiles       []string          `json:"var_files,omitempty" yaml:"var_files"`
	Env            EnvConfig         `json:"env,omitempty" yaml:"env"`
	PlanOutput     PlanOutput        `json:"plan_output,omitempty" yaml:"plan_output"`
	Apply          *StackApplyConfig `json:"apply,omitempty" yaml:"apply"`
	DependsOn      []string          `json:"depends_on,omitempty" yaml:"depends_on"`
	IgnoreInferred []string          `json:"ignore_inferred,omitempty" yaml:"ignore_inferred"`
}

// Instances maps instance names to their overrides. YAML and JSON accept
// a list of names or a map; JSON and YAML always encode the map form.
type Instances map[string]InstanceConfig

// Names returns the instance names in sorted order.
func (in Instances) Names() []string {
	names := make([]string, 0, len(in))
	for name := range in {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// UnmarshalYAML decodes a sequence of names or a mapping of overrides. It
// implements yaml.v3's function-based interface so that a strict decoder
// stays strict for the overrides.
func (in *Instances) UnmarshalYAML(unmarshal func(any) error) error {
	var raw any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	switch list := raw.(type) {
	case []any:
		for i, item := range list {
			if item == nil {
				return fmt.Errorf("instances[%d]: empty instance name", i)
			}
		}
		var names []string
		if err := unmarshal(&names); err != nil {
			return err
		}
		return in.fromNames(names)
	case map[string]any, map[any]any:
		m := map[string]InstanceConfig{}
		if err := unmarshal(&m); err != nil {
			return err
		}
		*in = m
		return nil
	default:
		return errors.New("instances must be a list of names or a map from name to overrides")
	}
}

// UnmarshalJSON decodes an array of names or an object of overrides.
func (in *Instances) UnmarshalJSON(data []byte) error {
	switch trimmed := bytes.TrimSpace(data); {
	case bytes.Equal(trimmed, []byte("null")):
		*in = nil
		return nil
	case len(trimmed) > 0 && trimmed[0] == '[':
		var names []string
		if err := json.Unmarshal(trimmed, &names); err != nil {
			return err
		}
		return in.fromNames(names)
	default:
		var m map[string]InstanceConfig
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return err
		}
		*in = m
		return nil
	}
}

func (in *Instances) fromNames(names []string) error {
	m := make(Instances, len(names))
	for _, name := range names {
		if _, dup := m[name]; dup {
			return fmt.Errorf("instances: %q is listed twice", name)
		}
		m[name] = InstanceConfig{}
	}
	*in = m
	return nil
}

// EnvConfig maps environment variable names to their values.
type EnvConfig map[string]EnvValue

// UnmarshalYAML decodes a mapping of names to values and refuses a null
// value, which is spelled "" for an empty string.
func (c *EnvConfig) UnmarshalYAML(unmarshal func(any) error) error {
	var raw map[string]any
	if err := unmarshal(&raw); err == nil {
		for _, name := range slices.Sorted(maps.Keys(raw)) {
			if raw[name] == nil {
				return fmt.Errorf("env.%s: no value; write \"\" for an empty string", name)
			}
		}
	}
	var m map[string]EnvValue
	if err := unmarshal(&m); err != nil {
		return err
	}
	*c = m
	return nil
}

// EnvValue is the value of one environment variable: a string for every
// mode, or per-mode values where a mode with no value leaves the variable
// unset.
type EnvValue struct {
	// Value is the string form, used when Modes is nil.
	Value string
	// Modes is the object form.
	Modes *EnvModes
}

// EnvModes holds the per-mode values of an environment variable; a drift
// run falls back to Plan.
type EnvModes struct {
	Plan  *string `json:"plan,omitempty" yaml:"plan"`
	Apply *string `json:"apply,omitempty" yaml:"apply"`
	Drift *string `json:"drift,omitempty" yaml:"drift"`
}

// EnvString returns the string form of an environment variable value.
func EnvString(s string) EnvValue { return EnvValue{Value: s} }

// MarshalYAML encodes the string or the object form, whichever was set.
func (v EnvValue) MarshalYAML() (any, error) {
	if v.Modes != nil {
		return v.Modes, nil
	}
	return v.Value, nil
}

// UnmarshalYAML decodes a scalar or a mapping with plan, apply and drift
// keys, strictly when the decoder is strict.
func (v *EnvValue) UnmarshalYAML(unmarshal func(any) error) error {
	var raw any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	switch raw.(type) {
	case []any:
		return errors.New("an env value must be a string or a map with plan, apply and drift")
	case map[string]any, map[any]any:
		modes := &EnvModes{}
		if err := unmarshal(modes); err != nil {
			return err
		}
		*v = EnvValue{Modes: modes}
		return nil
	default:
		var s string
		if err := unmarshal(&s); err != nil {
			return err
		}
		*v = EnvValue{Value: s}
		return nil
	}
}

// MarshalJSON encodes the string or the object form, whichever was set.
func (v EnvValue) MarshalJSON() ([]byte, error) {
	if v.Modes != nil {
		return json.Marshal(v.Modes)
	}
	return json.Marshal(v.Value)
}

// UnmarshalJSON decodes a string or an object with plan, apply and drift
// keys.
func (v *EnvValue) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		modes := &EnvModes{}
		if err := json.Unmarshal(trimmed, modes); err != nil {
			return err
		}
		*v = EnvValue{Modes: modes}
		return nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return fmt.Errorf("an env value must be a string or an object with plan, apply and drift: %w", err)
	}
	*v = EnvValue{Value: s}
	return nil
}
