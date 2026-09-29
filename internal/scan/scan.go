package scan

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/terraform-config-inspect/tfconfig"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

// ErrNotDirectory is returned when the scan root is not a directory.
var ErrNotDirectory = errors.New("not a directory")

var skippedDirs = map[string]bool{".git": true, ".terraform": true, "node_modules": true}

// Options configures one Scan.
type Options struct {
	// Repo is the "owner/repo" of the checkout. It prefixes local module keys
	// and tells same-repository depends_on entries from cross-repository ones,
	// comparing owner and name case-insensitively as GitHub does.
	Repo string
	// SHA is the commit of the checkout, copied into the graph.
	SHA string
	// Config is the root configuration. Nil reads stackorder.yaml from the
	// scan root and defaults it as config.Load does.
	Config *v1.RepoConfig
	// GitHubURL is the web root of the GitHub instance hosting Repo, such as
	// GITHUB_SERVER_URL. Git modules on its host are keyed without the host,
	// as "owner/repo//path@ref", and those on any other host, github.com
	// included, keep it. Empty means https://github.com.
	GitHubURL string
	// Logger receives debug output. Nil discards it.
	Logger *slog.Logger
}

type stackInfo struct {
	stack        v1.Stack
	dependsOn    []string
	ignored      []string
	remoteStates []remoteState
	inputs       []string
}

type edgeID struct {
	from, to v1.NodeRef
	typ      v1.EdgeType
}

type caller struct {
	ref v1.NodeRef
	dir string
}

type scanner struct {
	root string
	fsys *os.Root
	opts Options
	cfg  *v1.RepoConfig
	log  *slog.Logger
	hcl  *hclReader

	ghHost string

	dirs     map[string][]string
	dirOrder []string

	stacks   map[string]*stackInfo
	external map[string]v1.Stack
	modules  map[string]*v1.Module
	edges    map[edgeID]*v1.Edge
	warnings map[string]bool
	loaded   map[string]*tfconfig.Module
	hashes   map[string]string
}

// Scan walks the checkout at root and builds its dependency graph.
//
// Stacks are the directories matching stacks.discover, outside
// modules.paths, whose .tf or .tf.json files declare an s3 backend, plus every
// stacks.include directory, less the directories matching stacks.exclude.
// Each stack directory yields one stack per instance. Module nodes are the directories under
// modules.paths that hold configuration and every module source reachable
// from a stack, following local sources transitively; local modules are keyed
// "owner/repo//path". A cross-repository depends_on target becomes an
// External stack keyed by its qualified "owner/repo//key", the same key its
// edges point at. Inferred reads_state edges carry the matched bucket and the
// full state object key, workspace prefix included, in Meta.
//
// Each instance's Backend is the literal backend "s3" block overlaid, in
// order, with its rendered backend_config entries: a file, repository
// relative, parsed as HCL attributes, or a name=value pair; only bucket,
// key, region, dynamodb_table, use_lockfile and workspace_key_prefix are
// read. A backend_config file that cannot be read is an error. WatchPaths
// holds the backend_config files and var files that the stack's own
// directory does not own, repository relative, sorted and unique: a file
// is owned by the deepest stack directory enclosing it, the rule the graph
// uses for changed paths. A var file that does not exist is a warning.
//
// A same-repository depends_on or ignore_inferred entry without an instance
// suffix that names a directory with instances resolves to the instance of
// the dependent's own name when the target has one, else to the target's
// only stack.
//
// TreeHash is the hex SHA-256 over the sorted lines
// "path\x00hex(sha256(content))\n" of every *.tf, *.tf.json, *.tfvars,
// *.tfvars.json, *.tfbackend, stackorder.yaml and .stackorder.yaml file
// outside .git, .terraform and node_modules, and of every other file the
// scan read to build the graph: backend_config files, var files and
// from_var_files matches of any name. A file that matched from_var_files
// but could not be read contributes its path with an empty digest.
//
// Problems that leave the graph usable, such as a non-literal backend
// attribute or an unsupported module source, are reported in Graph.Warnings;
// an unreadable tree or an invalid configuration file is an error.
func Scan(ctx context.Context, root string, opts Options) (*v1.Graph, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan %s: %w", root, ErrNotDirectory)
	}
	fsys, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	defer func() { _ = fsys.Close() }()
	cfg, err := resolveConfig(fsys, opts.Config)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	ghHost, err := gitHubHost(opts.GitHubURL)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	s := &scanner{
		ghHost:   ghHost,
		root:     abs,
		fsys:     fsys,
		opts:     opts,
		cfg:      cfg,
		log:      opts.Logger,
		dirs:     map[string][]string{},
		stacks:   map[string]*stackInfo{},
		external: map[string]v1.Stack{},
		modules:  map[string]*v1.Module{},
		edges:    map[edgeID]*v1.Edge{},
		warnings: map[string]bool{},
		loaded:   map[string]*tfconfig.Module{},
		hashes:   map[string]string{},
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.hcl = &hclReader{fsys: fsys, parser: hclparse.NewParser(), warn: s.warn}

	if err := s.walk(ctx); err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	if err := s.discoverStacks(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	s.setWatchPaths()
	if err := s.scanModules(ctx); err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	explicit := s.addDependsOn()
	s.inferRemoteState(explicit)

	g := s.graph()
	g.TreeHash = s.treeHash()
	return g, nil
}

func resolveConfig(fsys *os.Root, cfg *v1.RepoConfig) (*v1.RepoConfig, error) {
	if cfg == nil {
		data, err := fsys.ReadFile(config.RootFile)
		if errors.Is(err, fs.ErrNotExist) {
			return config.Default(), nil
		}
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		loaded, err := config.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		return loaded, nil
	}
	c := *cfg
	config.ApplyDefaults(&c)
	return &c, nil
}

func (s *scanner) loadStackConfig(dir string) (*v1.StackConfig, bool, error) {
	data, err := s.fsys.ReadFile(filepath.FromSlash(path.Join(dir, config.StackFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return &v1.StackConfig{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	cfg, err := config.ParseStack(data)
	if err != nil {
		return nil, false, err
	}
	return cfg, true, nil
}

func (s *scanner) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !s.warnings[msg] {
		s.log.Debug("scan warning", "warning", msg)
	}
	s.warnings[msg] = true
}

func (s *scanner) walk(ctx context.Context) error {
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}
		if d.IsDir() {
			if rel != "" && skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			s.dirs[rel] = nil
			s.dirOrder = append(s.dirOrder, rel)
			return nil
		}
		name := d.Name()
		tf := isTerraformFile(name)
		if !tf && name != config.StackFile && name != config.RootFile && !isInputFile(name) {
			return nil
		}
		content, err := s.fsys.ReadFile(filepath.FromSlash(rel))
		if err != nil {
			s.warn("%s: %v", rel, pathError(err))
			return nil
		}
		s.recordInput(rel, content)
		if tf {
			dir := path.Dir(rel)
			if dir == "." {
				dir = ""
			}
			s.dirs[dir] = append(s.dirs[dir], name)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}
	return nil
}

func (s *scanner) recordInput(rel string, content []byte) {
	sum := sha256.Sum256(content)
	s.hashes[rel] = hex.EncodeToString(sum[:])
}

func (s *scanner) hashInput(rel string) error {
	if _, ok := s.hashes[rel]; ok {
		return nil
	}
	content, err := s.fsys.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		return err
	}
	s.recordInput(rel, content)
	return nil
}

func (s *scanner) treeHash() string {
	h := sha256.New()
	for _, rel := range slices.Sorted(maps.Keys(s.hashes)) {
		h.Write([]byte(rel + "\x00" + s.hashes[rel] + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isInputFile(name string) bool {
	return strings.HasSuffix(name, ".tfvars") || strings.HasSuffix(name, ".tfvars.json") || strings.HasSuffix(name, ".tfbackend")
}

func isTerraformFile(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") {
		return false
	}
	return strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json")
}

func (s *scanner) underModulePaths(dir string) bool {
	for d := dir; ; d = path.Dir(d) {
		if d == "." {
			d = ""
		}
		if matchAny(s.cfg.Modules.Paths, d) {
			return true
		}
		if d == "" {
			return false
		}
	}
}

func (s *scanner) discoverStacks() error {
	excludes := make([]string, 0, len(s.cfg.Stacks.Exclude))
	for _, g := range s.cfg.Stacks.Exclude {
		excludes = append(excludes, strings.TrimSuffix(strings.TrimPrefix(g, "./"), "/"))
	}
	included := map[string]bool{}
	for _, inc := range s.cfg.Stacks.Include {
		dir := config.NormalizePath(inc)
		if _, ok := s.dirs[dir]; !ok {
			s.warn("stacks.include: %s: directory not found", inc)
			continue
		}
		if matchAny(excludes, dir) {
			s.warn("stacks.include: %s: excluded by stacks.exclude", inc)
			continue
		}
		included[dir] = true
	}
	for _, dir := range s.dirOrder {
		if matchAny(excludes, dir) {
			continue
		}
		if !included[dir] && (len(s.dirs[dir]) == 0 || s.underModulePaths(dir) || !matchAny(s.cfg.Stacks.Discover, dir)) {
			continue
		}
		dc := s.hcl.readDir(dir, s.dirs[dir])
		if dir == "" {
			if included[dir] || dc.backendType == backendS3 {
				s.warn(".: the repository root cannot be a stack; move its configuration into a directory")
			}
			continue
		}
		switch {
		case dc.backendType == backendS3:
		case included[dir]:
			s.warn("%s: listed in stacks.include but has no s3 backend", displayDir(dir))
		case dc.backendType != "":
			s.warn("%s: backend %q is not s3; not a stack", displayDir(dir), dc.backendType)
			continue
		default:
			continue
		}
		if err := s.addStack(dir, dc); err != nil {
			return err
		}
	}
	return nil
}

func osPath(rel string) string {
	if rel == "" {
		return "."
	}
	return filepath.FromSlash(rel)
}

func displayDir(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

func (s *scanner) addStack(dir string, dc dirConfig) error {
	stackCfg, ok, err := s.loadStackConfig(dir)
	if err != nil {
		return fmt.Errorf("%s: %w", path.Join(dir, config.StackFile), err)
	}
	matched, err := config.MatchVarFiles(s.cfg, filepath.Join(s.root, osPath(dir)))
	if err != nil {
		return fmt.Errorf("%s: %w", displayDir(dir), err)
	}
	for _, m := range matched {
		if rel := path.Join(dir, m); s.hashInput(rel) != nil {
			s.hashes[rel] = ""
		}
	}
	instances, err := config.InstanceNames(s.cfg, dir, stackCfg, matched)
	if err != nil {
		return fmt.Errorf("%s: %w", displayDir(dir), err)
	}
	for _, w := range config.VarFileWarnings(s.cfg, dir, stackCfg, matched) {
		s.warn("%s", w)
	}
	for _, instance := range instances {
		eff, err := config.ResolveMatched(s.cfg, dir, stackCfg, instance, matched)
		if err != nil {
			return err
		}
		backend, backendFiles, err := s.effectiveBackend(eff.Key, dc.backend, eff.BackendConfig)
		if err != nil {
			return err
		}
		st := v1.Stack{
			Key:         eff.Key,
			Path:        eff.Path,
			Instance:    eff.Instance,
			Workspace:   eff.Workspace,
			Backend:     backend,
			Environment: eff.Environment,
			Tool:        eff.Tool,
			ToolVersion: eff.ToolVersion,
			PlanOutput:  string(eff.PlanOutput),
		}
		if ok {
			st.Config = stackCfg
		}
		ignored := make([]string, 0, len(eff.IgnoreInferred))
		for _, k := range eff.IgnoreInferred {
			ignored = append(ignored, config.NormalizePath(k))
		}
		s.stacks[st.Key] = &stackInfo{
			stack:        st,
			dependsOn:    eff.DependsOn,
			ignored:      ignored,
			remoteStates: dc.remoteStates,
			inputs:       s.inputFiles(eff, backendFiles),
		}
		s.log.Debug("stack discovered", "key", st.Key, "backend", dc.backendType)
	}
	return nil
}

func (s *scanner) sortedStackKeys() []string {
	keys := make([]string, 0, len(s.stacks))
	for k := range s.stacks {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (s *scanner) localModuleKey(dir string) string {
	return s.opts.Repo + "//" + dir
}

func (s *scanner) scanModules(ctx context.Context) error {
	var queue []caller
	for _, key := range s.sortedStackKeys() {
		queue = append(queue, caller{ref: v1.StackRef(key), dir: s.stacks[key].stack.Path})
	}
	visited := map[string]bool{}
	for _, dir := range s.dirOrder {
		if len(s.dirs[dir]) == 0 || !s.underModulePaths(dir) {
			continue
		}
		visited[dir] = true
		key := s.localModuleKey(dir)
		s.modules[key] = &v1.Module{Key: key, Kind: v1.ModuleLocal, Path: dir}
		queue = append(queue, caller{ref: v1.ModuleRef(key), dir: dir})
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		c := queue[0]
		queue = queue[1:]
		for _, call := range s.moduleCalls(c.dir) {
			if next, ok := s.addModuleCall(c, call); ok && !visited[next.dir] {
				visited[next.dir] = true
				queue = append(queue, next)
			}
		}
	}
	return nil
}

func (s *scanner) moduleCalls(dir string) []*tfconfig.ModuleCall {
	mod, ok := s.loaded[dir]
	if !ok {
		var diags tfconfig.Diagnostics
		mod, diags = tfconfig.LoadModuleFromFilesystem(rootFS{s.fsys}, osPath(dir))
		for _, d := range diags {
			if d.Severity != tfconfig.DiagError {
				continue
			}
			if d.Pos != nil {
				s.warn("%s:%d: %s", filepath.ToSlash(d.Pos.Filename), d.Pos.Line, d.Summary)
			} else {
				s.warn("%s: %s", displayDir(dir), d.Summary)
			}
		}
		s.loaded[dir] = mod
	}
	calls := make([]*tfconfig.ModuleCall, 0, len(mod.ModuleCalls))
	for _, c := range mod.ModuleCalls {
		calls = append(calls, c)
	}
	slices.SortFunc(calls, func(a, b *tfconfig.ModuleCall) int { return cmp.Compare(a.Name, b.Name) })
	return calls
}

type rootFS struct{ root *os.Root }

func (r rootFS) Open(name string) (tfconfig.File, error) {
	f, err := r.root.Open(name)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (r rootFS) ReadFile(name string) ([]byte, error) { return r.root.ReadFile(name) }

func (r rootFS) ReadDir(name string) ([]os.FileInfo, error) {
	f, err := r.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	infos, err := f.Readdir(-1)
	slices.SortFunc(infos, func(a, b os.FileInfo) int { return cmp.Compare(a.Name(), b.Name()) })
	return infos, err
}

func (s *scanner) addModuleCall(c caller, call *tfconfig.ModuleCall) (caller, bool) {
	loc := fmt.Sprintf("%s:%d", filepath.ToSlash(call.Pos.Filename), call.Pos.Line)
	if strings.TrimSpace(call.Source) == "" {
		s.warn("%s: module %q has no source; skipped", loc, call.Name)
		return caller{}, false
	}
	m, ok := parseModuleSource(call.Source, call.Version, s.ghHost)
	if !ok {
		s.warn("%s: module %q: unsupported source %q; skipped", loc, call.Name, call.Source)
		return caller{}, false
	}
	var next caller
	if m.Kind == v1.ModuleLocal {
		dir := path.Join(c.dir, m.Path)
		if dir == ".." || strings.HasPrefix(dir, "../") {
			s.warn("%s: module %q: source %q is outside the repository; skipped", loc, call.Name, call.Source)
			return caller{}, false
		}
		if dir == "." {
			dir = ""
		}
		if dir == c.dir {
			s.warn("%s: module %q: source %q is the calling directory itself; skipped", loc, call.Name, call.Source)
			return caller{}, false
		}
		if info, err := s.fsys.Stat(osPath(dir)); err != nil || !info.IsDir() {
			s.warn("%s: module %q: source %q is not a directory; skipped", loc, call.Name, call.Source)
			return caller{}, false
		}
		m.Path, m.Key = dir, s.localModuleKey(dir)
		next = caller{ref: v1.ModuleRef(m.Key), dir: dir}
	}
	old, ok := s.modules[m.Key]
	switch {
	case !ok:
		s.modules[m.Key] = &m
	case old.Kind != m.Kind:
		s.warn("%s: module %q: source %q resolves to %s, which is both a local and a git module; treated as local", loc, call.Name, call.Source, m.Key)
		if m.Kind == v1.ModuleLocal {
			*old = m
		}
	case old.Source == "" || m.Source < old.Source:
		old.Source = m.Source
	}
	s.addEdge(v1.Edge{
		From: c.ref,
		To:   v1.ModuleRef(m.Key),
		Type: v1.EdgeUsesModule,
		Meta: map[string]string{"ref": m.Ref, "source": call.Source},
	})
	return next, m.Kind == v1.ModuleLocal
}

func (s *scanner) addEdge(e v1.Edge) {
	id := edgeID{from: e.From, to: e.To, typ: e.Type}
	if old, ok := s.edges[id]; ok {
		if e.Meta["source"] < old.Meta["source"] {
			old.Meta = e.Meta
		}
		return
	}
	s.edges[id] = &e
}

func (s *scanner) stackKeysByDir() map[string][]string {
	dirs := map[string][]string{}
	for _, key := range s.sortedStackKeys() {
		p := s.stacks[key].stack.Path
		dirs[p] = append(dirs[p], key)
	}
	return dirs
}

func (s *scanner) addDependsOn() map[string]map[string]bool {
	explicit := map[string]map[string]bool{}
	dirs := s.stackKeysByDir()
	for _, key := range s.sortedStackKeys() {
		info := s.stacks[key]
		explicit[key] = map[string]bool{}
		for _, dep := range info.dependsOn {
			repo, target, err := config.ParseDependency(dep)
			if err != nil {
				s.warn("%s: depends_on: %v", key, err)
				continue
			}
			if repo == "" || strings.EqualFold(repo, s.opts.Repo) {
				target = s.dependencyTarget(dirs, info.stack.Instance, target)
				if target == key {
					s.warn("%s: depends_on names the stack itself; ignored", key)
					continue
				}
				if _, ok := s.stacks[target]; !ok {
					s.warn("%s: depends_on %s: no such stack in this repository", key, target)
				}
				explicit[key][target] = true
				s.addEdge(v1.Edge{From: v1.StackRef(key), To: v1.StackRef(target), Type: v1.EdgeDependsOn})
				continue
			}
			qualified := v1.QualifiedStackKey(repo, target)
			p, instance := v1.SplitStackKey(target)
			s.external[qualified] = v1.Stack{Key: qualified, Path: p, Instance: instance, Repo: repo, External: true}
			s.addEdge(v1.Edge{From: v1.StackRef(key), To: v1.StackRef(qualified), Type: v1.EdgeDependsOn})
		}
	}
	return explicit
}

func (s *scanner) dependencyTarget(dirs map[string][]string, instance, target string) string {
	if _, ok := s.stacks[target]; ok {
		return target
	}
	p, suffix := v1.SplitStackKey(target)
	if suffix != "" {
		return target
	}
	if instance != "" {
		if k := v1.StackKey(p, instance); s.stacks[k] != nil {
			return k
		}
	}
	if keys := dirs[p]; len(keys) == 1 {
		return keys[0]
	}
	return target
}

type stateLocation struct{ bucket, key string }

func (s *scanner) inferRemoteState(explicit map[string]map[string]bool) {
	writers := map[stateLocation][]string{}
	for _, key := range s.sortedStackKeys() {
		st := s.stacks[key].stack
		if st.Backend == nil || st.Backend.Bucket == "" || st.Backend.Key == "" {
			continue
		}
		loc := stateLocation{st.Backend.Bucket, stateObjectKey(st.Backend.WorkspaceKeyPrefix, st.Workspace, st.Backend.Key)}
		writers[loc] = append(writers[loc], key)
	}
	for loc, keys := range writers {
		if len(keys) > 1 {
			s.warn("stacks %s share the state object s3://%s/%s", strings.Join(keys, ", "), loc.bucket, loc.key)
		}
	}
	dirs := s.stackKeysByDir()
	for _, key := range s.sortedStackKeys() {
		info := s.stacks[key]
		ignored := make([]string, 0, len(info.ignored))
		for _, entry := range info.ignored {
			ignored = append(ignored, s.dependencyTarget(dirs, info.stack.Instance, entry))
		}
		for _, rs := range info.remoteStates {
			if rs.problem != "" {
				s.warn("%s: data.%s.%s: cannot infer reads_state edge: %s", rs.loc, remoteStateType, rs.name, rs.problem)
				continue
			}
			if rs.backend != backendS3 {
				continue
			}
			objectKey := rs.objectKey()
			for _, target := range writers[stateLocation{rs.bucket, objectKey}] {
				switch {
				case target == key, explicit[key][target]:
				case slices.Contains(ignored, target):
					s.warn("%s: inferred reads_state edge to %s suppressed by ignore_inferred", key, target)
				default:
					s.addEdge(v1.Edge{
						From:     v1.StackRef(key),
						To:       v1.StackRef(target),
						Type:     v1.EdgeReadsState,
						Inferred: true,
						Meta:     map[string]string{"bucket": rs.bucket, "key": objectKey},
					})
				}
			}
		}
	}
}

func (s *scanner) graph() *v1.Graph {
	g := &v1.Graph{
		Repo:    s.opts.Repo,
		SHA:     s.opts.SHA,
		Stacks:  make([]v1.Stack, 0, len(s.stacks)+len(s.external)),
		Modules: make([]v1.Module, 0, len(s.modules)),
		Edges:   make([]v1.Edge, 0, len(s.edges)),
	}
	for _, info := range s.stacks {
		g.Stacks = append(g.Stacks, info.stack)
	}
	for _, st := range s.external {
		g.Stacks = append(g.Stacks, st)
	}
	slices.SortFunc(g.Stacks, func(a, b v1.Stack) int { return cmp.Compare(a.Key, b.Key) })
	for _, m := range s.modules {
		if m.Source == "" {
			m.Source = "./" + m.Path
		}
		g.Modules = append(g.Modules, *m)
	}
	slices.SortFunc(g.Modules, func(a, b v1.Module) int { return cmp.Compare(a.Key, b.Key) })
	for _, e := range s.edges {
		g.Edges = append(g.Edges, *e)
	}
	slices.SortFunc(g.Edges, compareEdges)
	for w := range s.warnings {
		g.Warnings = append(g.Warnings, w)
	}
	slices.Sort(g.Warnings)
	return g
}

func compareEdges(a, b v1.Edge) int {
	return cmp.Or(
		cmp.Compare(a.From.Kind, b.From.Kind),
		cmp.Compare(a.From.Key, b.From.Key),
		cmp.Compare(a.Type, b.Type),
		cmp.Compare(a.To.Kind, b.To.Kind),
		cmp.Compare(a.To.Key, b.To.Key),
	)
}

// StackDirs returns the sorted, de-duplicated directories of the graph's own
// stacks, leaving out external stacks from other repositories.
func StackDirs(g *v1.Graph) []string {
	if g == nil {
		return nil
	}
	dirs := make([]string, 0, len(g.Stacks))
	for _, st := range g.Stacks {
		if !st.External {
			dirs = append(dirs, st.Path)
		}
	}
	slices.Sort(dirs)
	return slices.Compact(dirs)
}

// FindStack returns the stack with the given key, or nil. The key is
// normalised first, so "./stacks/prod/vpc/" finds "stacks/prod/vpc"; a key
// qualified with the graph's own repository, in any letter case, finds the
// local stack, and one qualified with another repository finds the external
// stack.
func FindStack(g *v1.Graph, key string) *v1.Stack {
	if g == nil {
		return nil
	}
	repo, k := v1.SplitQualifiedStackKey(key)
	k = config.NormalizePath(k)
	if repo != "" && !strings.EqualFold(repo, g.Repo) {
		k = v1.QualifiedStackKey(repo, k)
	}
	for i := range g.Stacks {
		if g.Stacks[i].Key == k {
			return &g.Stacks[i]
		}
	}
	return nil
}
