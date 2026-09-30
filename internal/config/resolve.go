package config

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Effective is the merged and rendered configuration of one instance of a
// stack directory.
type Effective struct {
	// Key is v1.StackKey(Path, Instance).
	Key  string
	Path string
	// Instance is the instance name; empty for an unnamed instance.
	Instance string
	// Workspace is the Terraform workspace to select; empty means none.
	Workspace   string
	Tool        v1.Tool
	ToolVersion string
	// Environment is the GitHub environment; never empty.
	Environment string
	// EnvironmentConfigured reports that an instance override, the stack's
	// environment or an environments entry named Environment, rather than
	// the fallback to the instance name or v1.DefaultEnvironment.
	EnvironmentConfigured bool
	PlanOutput            v1.PlanOutput
	AllowedTeams          []string
	DependsOn             []string
	IgnoreInferred        []string
	// BackendConfig lists the -backend-config values for init, root first:
	// "name=value", or a file relative to the repository root.
	BackendConfig []string
	// VarFiles lists the -var-file values for plan, relative to the stack
	// directory: root, stack, the matched from_var_files file, instance.
	VarFiles []string
	// Env holds the configured environment variables per run mode; the
	// drift map already holds the plan value of a variable with no drift
	// value.
	Env map[v1.RunMode]map[string]string
}

// EnvFor returns a copy of the environment variables Resolve computed for a
// run mode, or nil when there are none.
func (e Effective) EnvFor(mode v1.RunMode) map[string]string {
	if len(e.Env[mode]) == 0 {
		return nil
	}
	return maps.Clone(e.Env[mode])
}

// InstanceOf returns the instance of a v1 object that carries an instance
// and a workspace: the instance when set, else the workspace, because an
// object whose Instance is empty but whose Workspace is set comes from a
// client older than instances. A workspace of "default" counts as none.
func InstanceOf(instance, workspace string) string {
	if instance != "" || workspace == "default" {
		return instance
	}
	return workspace
}

// VarFileInstance names the instance a from_var_files match stands for: the
// file's base name up to its first ".".
func VarFileInstance(file string) string {
	name, _, _ := strings.Cut(path.Base(strings.ReplaceAll(file, "\\", "/")), ".")
	return name
}

// MatchVarFiles globs the root's stacks.instances.from_var_files in the
// stack directory dir, a path on disk, and returns the matching files
// relative to dir, slash separated and sorted; nil when the glob is unset.
func MatchVarFiles(root *v1.RepoConfig, dir string) ([]string, error) {
	if root == nil || root.Stacks.Instances.FromVarFiles == "" {
		return nil, nil
	}
	pattern := strings.TrimPrefix(root.Stacks.Instances.FromVarFiles, "./")
	matches, err := doublestar.Glob(os.DirFS(dir), pattern, doublestar.WithFilesOnly())
	if err != nil {
		return nil, fmt.Errorf("%s: stacks.instances.from_var_files: %w", RootFile, err)
	}
	slices.Sort(matches)
	return matches, nil
}

func usableMatches(root *v1.RepoConfig, matched []string) []string {
	if root == nil || root.Stacks.Instances.FromVarFiles == "" {
		return nil
	}
	out := make([]string, 0, len(matched))
	for _, f := range matched {
		out = append(out, strings.ReplaceAll(f, "\\", "/"))
	}
	slices.Sort(out)
	return out
}

func stackFileOf(stackPath string) string {
	return path.Join(NormalizePath(stackPath), StackFile)
}

// InstanceNames returns the instance set of the stack directory stackPath,
// in order of precedence: the names in the stack's instances, sorted; else
// one name per file matched by the root's stacks.instances.from_var_files
// (matchedVarFiles, stack relative), derived by VarFileInstance and sorted;
// else the stack's workspace rendered with an empty .Instance, when that is
// neither empty nor "default" (the legacy single instance); else one
// unnamed instance "". A derived name that is not a valid instance name,
// two matches that derive the same name and a legacy workspace that is not
// a valid instance name are errors. Matches are ignored when
// from_var_files is unset.
func InstanceNames(root *v1.RepoConfig, stackPath string, stack *v1.StackConfig, matchedVarFiles []string) ([]string, error) {
	if stack == nil {
		stack = &v1.StackConfig{}
	}
	if len(stack.Instances) > 0 {
		return stack.Instances.Names(), nil
	}
	if matched := usableMatches(root, matchedVarFiles); len(matched) > 0 {
		var errs []error
		from := map[string]string{}
		for _, f := range matched {
			file := path.Join(NormalizePath(stackPath), f)
			name := VarFileInstance(f)
			if err := ValidateInstanceName(name); err != nil {
				errs = append(errs, fmt.Errorf("stacks.instances.from_var_files: %s: %w", file, err))
				continue
			}
			if other, dup := from[name]; dup {
				errs = append(errs, fmt.Errorf("stacks.instances.from_var_files: %s and %s both name instance %q", other, file, name))
				continue
			}
			from[name] = file
		}
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		return slices.Sorted(maps.Keys(from)), nil
	}
	if stack.Workspace != "" {
		ws, err := Render(stack.Workspace, TemplateDataFor(stackPath, ""))
		if err != nil {
			return nil, fmt.Errorf("%s: workspace: %w", stackFileOf(stackPath), err)
		}
		if ws != "" && ws != "default" {
			if err := ValidateInstanceName(ws); err != nil {
				return nil, fmt.Errorf("%s: workspace: %q names the stack's only instance: %w", stackFileOf(stackPath), ws, err)
			}
			return []string{ws}, nil
		}
	}
	return []string{""}, nil
}

// VarFileWarnings returns the warnings of the from_var_files matches of a
// stack directory: one for every match in the directory itself that
// Terraform auto-loads (terraform.tfvars, terraform.tfvars.json,
// *.auto.tfvars and *.auto.tfvars.json), because Terraform loads it for
// every instance; and, when the stack declares its instances, one for every
// match naming an instance it does not declare, because that file is not
// used. Each warning names the file.
func VarFileWarnings(root *v1.RepoConfig, stackPath string, stack *v1.StackConfig, matchedVarFiles []string) []string {
	var out []string
	for _, f := range usableMatches(root, matchedVarFiles) {
		file := path.Join(NormalizePath(stackPath), f)
		if autoLoaded(f) {
			out = append(out, fmt.Sprintf("%s: Terraform auto-loads it for every instance of the stack; stacks.instances.from_var_files should not match it", file))
		}
		if stack == nil || len(stack.Instances) == 0 {
			continue
		}
		name := VarFileInstance(f)
		if _, ok := stack.Instances[name]; !ok {
			out = append(out, fmt.Sprintf("%s: names instance %q, which %s does not declare; the file is not used",
				file, name, stackFileOf(stackPath)))
		}
	}
	return out
}

func autoLoaded(file string) bool {
	if strings.Contains(file, "/") {
		return false
	}
	switch {
	case file == "terraform.tfvars", file == "terraform.tfvars.json":
		return true
	case strings.HasSuffix(file, ".auto.tfvars"), strings.HasSuffix(file, ".auto.tfvars.json"):
		return true
	}
	return false
}

// Resolve merges and renders the configuration of one instance of a stack
// directory; stack may be nil. It is ResolveMatched without from_var_files
// matches, which is all the server needs for environment and policy.
func Resolve(root *v1.RepoConfig, stackPath string, stack *v1.StackConfig, instance string) (Effective, error) {
	return ResolveMatched(root, stackPath, stack, instance, nil)
}

// ResolveMatched merges the root configuration, the stack's own file and the
// instance's overrides, and renders their templates. matchedVarFiles are the
// stack relative files the root's from_var_files glob matched in the stack
// directory (MatchVarFiles); the one whose VarFileInstance equals instance
// joins VarFiles after the root and stack var_files and before the
// instance's. They are ignored when from_var_files is unset, and two that
// name the instance are an error. stackPath carries no instance suffix, and
// an instance of "default" means none.
//
// Lists concatenate root, stack, instance (backend_config, var_files) or
// stack, instance (depends_on, ignore_inferred); env merges per variable,
// instance over stack over root; workspace, plan_output, tool and
// tool_version take the most specific value set, and apply.allowed_teams
// the most specific non-empty list. The environment is the first of the
// instance override, the stack's environment and the environments match
// that renders non-empty, else the instance name, else
// v1.DefaultEnvironment. A workspace of "default" means none; the workspace
// is never derived from the instance. Rendering errors name the file and key
// that hold the template and are all reported together.
func ResolveMatched(root *v1.RepoConfig, stackPath string, stack *v1.StackConfig, instance string, matchedVarFiles []string) (Effective, error) {
	if root == nil {
		root = Default()
	}
	if stack == nil {
		stack = &v1.StackConfig{}
	}
	if instance == "default" {
		instance = ""
	}
	stackPath, suffix := v1.SplitStackKey(NormalizePath(stackPath))
	if suffix != "" {
		return Effective{}, fmt.Errorf("stack path %q carries an instance suffix", v1.StackKey(stackPath, suffix))
	}
	ov := stack.Instances[instance]
	r := &renderer{data: TemplateDataFor(stackPath, instance), stackFile: path.Join(stackPath, StackFile)}
	inst := "instances." + instance + "."

	e := Effective{
		Key:         v1.StackKey(stackPath, instance),
		Path:        stackPath,
		Instance:    instance,
		Tool:        root.Tool,
		ToolVersion: root.ToolVersion,
		PlanOutput:  root.PlanOutput,
	}
	if stack.Tool != "" {
		e.Tool = stack.Tool
	}
	if stack.ToolVersion != "" {
		e.ToolVersion = stack.ToolVersion
	}
	e.PlanOutput = firstSet(ov.PlanOutput, stack.PlanOutput, e.PlanOutput)

	e.AllowedTeams = append([]string(nil), root.Apply.AllowedTeams...)
	if stack.Apply != nil && len(stack.Apply.AllowedTeams) > 0 {
		e.AllowedTeams = append([]string(nil), stack.Apply.AllowedTeams...)
	}
	if ov.Apply != nil && len(ov.Apply.AllowedTeams) > 0 {
		e.AllowedTeams = append([]string(nil), ov.Apply.AllowedTeams...)
	}

	wsKey, wsText := "workspace", stack.Workspace
	if ov.Workspace != "" {
		wsKey, wsText = inst+"workspace", ov.Workspace
	}
	if e.Workspace = r.stack(wsKey, wsText); e.Workspace == "default" {
		e.Workspace = ""
	}
	if strings.ContainsAny(e.Workspace, "/: \t") {
		r.fail(r.stackFile, wsKey, fmt.Errorf("%q contains invalid characters", e.Workspace))
	}

	envKey, envValue := matchEnvironment(root.Environments, stackPath, instance)
	for _, c := range []struct{ file, key, text string }{
		{r.stackFile, inst + "environment", ov.Environment},
		{r.stackFile, "environment", stack.Environment},
		{RootFile, fmt.Sprintf("environments[%q]", envKey), envValue},
	} {
		if c.text == "" {
			continue
		}
		if v := r.render(c.file, c.key, c.text); v != "" {
			e.Environment, e.EnvironmentConfigured = v, true
			break
		}
	}
	if e.Environment == "" {
		e.Environment = cmp.Or(instance, v1.DefaultEnvironment)
	}

	e.DependsOn = r.dependencies("depends_on", stack.DependsOn, nil)
	e.DependsOn = r.dependencies(inst+"depends_on", ov.DependsOn, e.DependsOn)
	e.IgnoreInferred = r.stackList("ignore_inferred", stack.IgnoreInferred, nil)
	e.IgnoreInferred = r.stackList(inst+"ignore_inferred", ov.IgnoreInferred, e.IgnoreInferred)

	e.BackendConfig = r.rootList("backend_config", root.BackendConfig, nil)
	e.BackendConfig = r.stackList("backend_config", stack.BackendConfig, e.BackendConfig)
	e.BackendConfig = r.stackList(inst+"backend_config", ov.BackendConfig, e.BackendConfig)

	e.VarFiles = r.rootList("var_files", root.VarFiles, nil)
	e.VarFiles = r.stackList("var_files", stack.VarFiles, e.VarFiles)
	switch files := instanceVarFiles(usableMatches(root, matchedVarFiles), instance); len(files) {
	case 0:
	case 1:
		e.VarFiles = append(e.VarFiles, files[0])
	default:
		r.errs = append(r.errs, fmt.Errorf("%s: stacks.instances.from_var_files: %s all name instance %q",
			RootFile, strings.Join(files, ", "), instance))
	}
	e.VarFiles = r.stackList(inst+"var_files", ov.VarFiles, e.VarFiles)

	e.Env = r.env(root.Env, stack.Env, ov.Env, inst)

	if err := errors.Join(r.errs...); err != nil {
		return Effective{}, err
	}
	return e, nil
}

func instanceVarFiles(matched []string, instance string) []string {
	if instance == "" {
		return nil
	}
	var out []string
	for _, f := range matched {
		if VarFileInstance(f) == instance {
			out = append(out, f)
		}
	}
	return out
}

func firstSet[T ~string](values ...T) T {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

type renderer struct {
	data      TemplateData
	stackFile string
	errs      []error
}

func (r *renderer) fail(file, key string, err error) {
	r.errs = append(r.errs, fmt.Errorf("%s: %s: %w", file, key, err))
}

func (r *renderer) render(file, key, text string) string {
	out, err := Render(text, r.data)
	if err != nil {
		r.fail(file, key, err)
	}
	return out
}

func (r *renderer) stack(key, text string) string { return r.render(r.stackFile, key, text) }

func (r *renderer) list(file, key string, values, out []string) []string {
	for i, v := range values {
		out = append(out, r.render(file, fmt.Sprintf("%s[%d]", key, i), v))
	}
	return out
}

func (r *renderer) dependencies(key string, values, out []string) []string {
	for i, v := range values {
		k := fmt.Sprintf("%s[%d]", key, i)
		d := r.stack(k, v)
		if _, _, err := ParseDependency(d); err != nil && isTemplate(v) {
			r.fail(r.stackFile, k, err)
		}
		out = append(out, d)
	}
	return out
}

func (r *renderer) rootList(key string, values, out []string) []string {
	return r.list(RootFile, key, values, out)
}

func (r *renderer) stackList(key string, values, out []string) []string {
	return r.list(r.stackFile, key, values, out)
}

func (r *renderer) env(root, stack, instance v1.EnvConfig, inst string) map[v1.RunMode]map[string]string {
	type source struct {
		file, key string
		value     v1.EnvValue
	}
	merged := map[string]source{}
	for _, layer := range []struct {
		file, key string
		env       v1.EnvConfig
	}{{RootFile, "env", root}, {r.stackFile, "env", stack}, {r.stackFile, inst + "env", instance}} {
		for name, value := range layer.env {
			merged[name] = source{layer.file, layer.key + "." + name, value}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	out := map[v1.RunMode]map[string]string{v1.ModePlan: {}, v1.ModeApply: {}, v1.ModeDrift: {}}
	for _, name := range sortedKeys(merged) {
		src := merged[name]
		if src.value.Modes == nil {
			v := r.render(src.file, src.key, src.value.Value)
			for mode := range out {
				out[mode][name] = v
			}
			continue
		}
		for _, mv := range envModeValues(src.value.Modes) {
			out[mv.mode][name] = r.render(src.file, src.key+"."+string(mv.mode), mv.value)
		}
		if src.value.Modes.Drift == nil {
			if v, ok := out[v1.ModePlan][name]; ok {
				out[v1.ModeDrift][name] = v
			}
		}
	}
	return out
}

// EnvironmentFor picks the environments entry for one instance of a stack
// directory and returns its raw value, which may be an unrendered template,
// or "" when no key matches; Resolve renders it and is what decides the
// environment a stack runs under. Keys are "prefix", "prefix:instance" or
// ":instance"; a prefix matches whole path segments, an empty prefix (only
// valid with an instance part) matches every path, and an instance part
// must equal the instance. A key with an instance part beats one without,
// then the longest prefix wins.
func EnvironmentFor(environments map[string]string, stackPath, instance string) string {
	_, value := matchEnvironment(environments, stackPath, instance)
	return value
}

func splitEnvironmentKey(key string) (prefix, instance string, hasInstance bool) {
	if i := strings.LastIndex(key, ":"); i >= 0 {
		return key[:i], key[i+1:], true
	}
	return key, "", false
}

func normalizeEnvironmentPrefix(prefix string) string {
	p := strings.Trim(strings.TrimPrefix(strings.ReplaceAll(prefix, "\\", "/"), "./"), "/")
	if p == "." {
		return ""
	}
	return p
}

func matchEnvironment(environments map[string]string, stackPath, instance string) (key, value string) {
	stackPath, _ = v1.SplitStackKey(NormalizePath(stackPath))
	stackPath += "/"
	ok, bestLen, bestInstance := false, -1, false
	for _, k := range sortedKeys(environments) {
		prefix, inst, hasInstance := splitEnvironmentKey(k)
		if hasInstance && inst != instance {
			continue
		}
		p := normalizeEnvironmentPrefix(prefix)
		switch {
		case p != "":
			p += "/"
		case !hasInstance:
			continue
		}
		if !strings.HasPrefix(stackPath, p) {
			continue
		}
		if !ok || (hasInstance && !bestInstance) || (hasInstance == bestInstance && len(p) > bestLen) {
			key, value, ok, bestLen, bestInstance = k, environments[k], true, len(p), hasInstance
		}
	}
	return key, value
}
