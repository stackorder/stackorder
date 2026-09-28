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
	// Logger receives debug output. Nil discards it.
	Logger *slog.Logger
}

type stackInfo struct {
	stack        v1.Stack
	dependsOn    []string
	ignored      []string
	remoteStates []remoteState
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

	dirs     map[string][]string
	dirOrder []string

	stacks   map[string]*stackInfo
	external map[string]v1.Stack
	modules  map[string]*v1.Module
	edges    map[edgeID]*v1.Edge
	warnings map[string]bool
	loaded   map[string]*tfconfig.Module
}

// Scan walks the checkout at root and builds its dependency graph.
//
// Stacks are the directories matching stacks.discover, outside
// modules.paths, whose .tf or .tf.json files declare an s3 backend, plus every
// stacks.include directory. Module nodes are the directories under
// modules.paths that hold configuration and every module source reachable
// from a stack, following local sources transitively; local modules are keyed
// "owner/repo//path". A cross-repository depends_on target becomes an
// External stack keyed by its qualified "owner/repo//key", the same key its
// edges point at. Inferred reads_state edges carry the matched bucket and the
// full state object key, workspace prefix included, in Meta.
//
// TreeHash is the hex SHA-256 over the sorted lines
// "path\x00hex(sha256(content))\n" of every *.tf, *.tf.json, stackorder.yaml
// and .stackorder.yaml file outside .git, .terraform and node_modules.
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
	s := &scanner{
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
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.hcl = &hclReader{fsys: fsys, parser: hclparse.NewParser(), warn: s.warn}

	treeHash, err := s.walk(ctx)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	if err := s.discoverStacks(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	if err := s.scanModules(ctx); err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	explicit := s.addDependsOn()
	s.inferRemoteState(explicit)

	g := s.graph()
	g.TreeHash = treeHash
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

func (s *scanner) walk(ctx context.Context) (string, error) {
	var entries []string
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
		if !tf && name != config.StackFile && name != config.RootFile {
			return nil
		}
		content, err := s.fsys.ReadFile(filepath.FromSlash(rel))
		if err != nil {
			s.warn("%s: %v", rel, pathError(err))
			return nil
		}
		sum := sha256.Sum256(content)
		entries = append(entries, rel+"\x00"+hex.EncodeToString(sum[:])+"\n")
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
		return "", fmt.Errorf("walk: %w", err)
	}
	slices.Sort(entries)
	h := sha256.New()
	for _, e := range entries {
		h.Write([]byte(e))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
	included := map[string]bool{}
	for _, inc := range s.cfg.Stacks.Include {
		dir := config.NormalizePath(inc)
		if _, ok := s.dirs[dir]; !ok {
			s.warn("stacks.include: %s: directory not found", inc)
			continue
		}
		included[dir] = true
	}
	for _, dir := range s.dirOrder {
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
	eff := config.Resolve(s.cfg, dir, stackCfg)
	st := v1.Stack{
		Key:         eff.Key,
		Path:        eff.Path,
		Workspace:   eff.Workspace,
		Backend:     dc.backend,
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
	}
	s.log.Debug("stack discovered", "key", st.Key, "backend", dc.backendType)
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
	m, ok := ParseModuleSource(call.Source, call.Version)
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
	if old, ok := s.modules[m.Key]; !ok {
		s.modules[m.Key] = &m
	} else if old.Source == "" || m.Source < old.Source {
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

func (s *scanner) addDependsOn() map[string]map[string]bool {
	explicit := map[string]map[string]bool{}
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
			p, ws := v1.SplitStackKey(target)
			s.external[qualified] = v1.Stack{Key: qualified, Path: p, Workspace: ws, Repo: repo, External: true}
			s.addEdge(v1.Edge{From: v1.StackRef(key), To: v1.StackRef(qualified), Type: v1.EdgeDependsOn})
		}
	}
	return explicit
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
	for _, key := range s.sortedStackKeys() {
		info := s.stacks[key]
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
				case slices.Contains(info.ignored, target):
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
