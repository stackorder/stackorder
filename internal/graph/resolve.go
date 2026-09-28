package graph

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

// ErrInvalidGraph is returned, wrapped, when a graph is too malformed to be
// resolved unambiguously, such as a nil graph or duplicate node keys.
var ErrInvalidGraph = errors.New("invalid graph")

// Input is everything besides the graph that a resolution depends on.
type Input struct {
	// ChangedPaths are the repository relative file paths changed between the
	// base and the head of the change.
	ChangedPaths []string
	// Config is the root stackorder.yaml; nil means config.Default().
	Config *v1.RepoConfig
	// Requested restricts the result to these stack keys, as named by a
	// "stackorder plan" or "stackorder apply" comment. Empty means no
	// restriction.
	Requested []string
	// Locks maps stack keys to the orchestration lock held on them.
	Locks map[string]v1.LockInfo
}

type reasonSet uint8

const (
	bitChanged reasonSet = 1 << iota
	bitModule
	bitReadsState
	bitDependent
	bitRequested
)

var reasonOrder = []struct {
	bit    reasonSet
	reason v1.Reason
}{
	{bitChanged, v1.ReasonChanged},
	{bitModule, v1.ReasonModule},
	{bitReadsState, v1.ReasonReadsState},
	{bitDependent, v1.ReasonDependent},
	{bitRequested, v1.ReasonRequested},
}

func (r reasonSet) list() []v1.Reason {
	out := make([]v1.Reason, 0, len(reasonOrder))
	for _, o := range reasonOrder {
		if r&o.bit != 0 {
			out = append(out, o.reason)
		}
	}
	return out
}

type resolver struct {
	g        *v1.Graph
	ix       *index
	cfg      *v1.RepoConfig
	in       Input
	reasons  map[string]reasonSet
	via      map[string]map[string]bool
	modules  map[string]bool
	warnings []string
}

// Resolve computes the affected stacks of a change, their reasons and their
// waves, following the steps described in the package documentation. On a
// cycle it returns the response with Affected, Cycles and Warnings filled,
// every stack in wave 0, no waves and an empty matrix, together with an
// error wrapping ErrCycle.
func Resolve(g *v1.Graph, in Input) (*v1.ResolveResponse, error) {
	if g == nil {
		return nil, fmt.Errorf("resolve: %w: nil graph", ErrInvalidGraph)
	}
	ix := newIndex(g)
	if dups := append(slices.Clone(ix.dupStacks), ix.dupModules...); len(dups) > 0 {
		return nil, fmt.Errorf("resolve: %w: duplicate node keys %s", ErrInvalidGraph, strings.Join(dups, ", "))
	}
	cfg := in.Config
	if cfg == nil {
		cfg = config.Default()
	}
	r := &resolver{
		g:       g,
		ix:      ix,
		cfg:     cfg,
		in:      in,
		reasons: map[string]reasonSet{},
		via:     map[string]map[string]bool{},
		modules: map[string]bool{},
	}
	r.warnUnknownDependencies()
	changedModules := r.classifyPaths()
	r.moduleConsumers(changedModules)
	if cfg.Propagate.DependentsEnabled() {
		r.propagate()
	}
	domain, scheduled := r.request()
	return r.finish(domain, scheduled)
}

func (r *resolver) warn(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

func (r *resolver) mark(key string, bit reasonSet) {
	r.reasons[key] |= bit
}

func (r *resolver) addVia(key, through string) {
	if r.via[key] == nil {
		r.via[key] = map[string]bool{}
	}
	r.via[key][through] = true
}

func (r *resolver) warnUnknownDependencies() {
	for i := range r.g.Edges {
		e := &r.g.Edges[i]
		if e.Type != v1.EdgeDependsOn || e.To.Kind != v1.NodeStack {
			continue
		}
		if _, ok := r.ix.stacks[e.To.Key]; !ok {
			r.warn("stack %s depends_on unknown stack %s", e.From.Key, e.To.Key)
		}
	}
}

func (r *resolver) ignoreGlobs() []string {
	var globs []string
	for _, glob := range config.IgnoreGlobs(r.cfg) {
		if !doublestar.ValidatePattern(glob) {
			r.warn("ignore glob %q is invalid and was skipped", glob)
			continue
		}
		globs = append(globs, glob)
	}
	return globs
}

func ignored(p string, globs []string) bool {
	for _, glob := range globs {
		if ok, _ := doublestar.Match(glob, p); ok {
			return true
		}
	}
	return false
}

func (r *resolver) classifyPaths() []string {
	globs := r.ignoreGlobs()
	stackDirs := r.ix.stackDirs()
	moduleDirs := r.ix.moduleDirs()
	var changedModules []string
	for _, raw := range r.in.ChangedPaths {
		p := cleanPath(raw)
		if p == "" || hasSegment(p, ".terraform") || ignored(p, globs) {
			continue
		}
		if outsideRepo(p) {
			r.warn("changed path %s is inside no stack or module", p)
			continue
		}
		stacks := deepest(stackDirs, p)
		modules := deepest(moduleDirs, p)
		if len(stacks) == 0 && len(modules) == 0 {
			r.warn("changed path %s is inside no stack or module", p)
			continue
		}
		for _, key := range stacks {
			r.mark(key, bitChanged)
		}
		changedModules = append(changedModules, modules...)
	}
	return uniqueSorted(changedModules)
}

func (r *resolver) moduleConsumers(changed []string) {
	queue := slices.Clone(changed)
	for _, m := range changed {
		r.modules[m] = true
	}
	var consumers []string
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		for _, e := range r.ix.in[v1.ModuleRef(m)] {
			if !isModuleUse(e) {
				continue
			}
			switch e.From.Kind {
			case v1.NodeModule:
				if !r.modules[e.From.Key] {
					r.modules[e.From.Key] = true
					queue = append(queue, e.From.Key)
				}
			case v1.NodeStack:
				if r.ix.localStack(e.From.Key) != nil {
					r.mark(e.From.Key, bitModule)
					consumers = append(consumers, e.From.Key)
				}
			}
		}
	}
	for _, key := range uniqueSorted(consumers) {
		for _, m := range r.reachedModules(key) {
			r.addVia(key, m)
		}
	}
}

func (r *resolver) reachedModules(stack string) []string {
	seen := map[v1.NodeRef]bool{}
	todo := []v1.NodeRef{v1.StackRef(stack)}
	var found []string
	for len(todo) > 0 {
		n := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		for _, e := range r.ix.out[n] {
			if !isModuleUse(e) || !r.modules[e.To.Key] || seen[e.To] {
				continue
			}
			seen[e.To] = true
			found = append(found, e.To.Key)
			todo = append(todo, e.To)
		}
	}
	return found
}

func (r *resolver) propagate() {
	queue := keysOf(r.reasons)
	seen := make(map[string]bool, len(queue))
	for _, k := range queue {
		seen[k] = true
	}
	for len(queue) > 0 {
		y := queue[0]
		queue = queue[1:]
		for _, e := range r.ix.in[v1.StackRef(y)] {
			if !isOrdering(e) || e.From.Key == y || r.ix.localStack(e.From.Key) == nil {
				continue
			}
			x := e.From.Key
			if e.Type == v1.EdgeReadsState {
				r.mark(x, bitReadsState)
			} else {
				r.mark(x, bitDependent)
			}
			r.addVia(x, y)
			if !seen[x] {
				seen[x] = true
				queue = append(queue, x)
			}
		}
	}
}

func (r *resolver) request() (domain, scheduled []string) {
	affected := keysOf(r.reasons)
	if len(r.in.Requested) == 0 {
		return affected, affected
	}
	var picked []string
	for _, raw := range r.in.Requested {
		key := r.requestedKey(raw)
		if key == "" {
			continue
		}
		s := r.ix.stacks[key]
		switch {
		case s == nil:
			r.warn("requested stack %s is not in the graph", key)
		case s.External:
			r.warn("requested stack %s is external to this repository and cannot be scheduled", key)
		default:
			if _, ok := r.reasons[key]; !ok {
				r.mark(key, bitRequested)
			}
			picked = append(picked, key)
		}
	}
	return keysOf(r.reasons), uniqueSorted(picked)
}

func (r *resolver) requestedKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	repo, key := v1.SplitQualifiedStackKey(raw)
	if repo != "" && repo != r.g.Repo {
		return raw
	}
	return config.NormalizePath(key)
}

func (r *resolver) orderingDeps(domain []string) deps {
	member := make(map[string]bool, len(domain))
	for _, k := range domain {
		member[k] = true
	}
	d := deps{}
	for _, x := range domain {
		for _, e := range r.ix.out[v1.StackRef(x)] {
			if isOrdering(e) && member[e.To.Key] {
				d[x] = append(d[x], e.To.Key)
			}
		}
	}
	d.normalize()
	return d
}

func (r *resolver) finish(domain, scheduled []string) (*v1.ResolveResponse, error) {
	d := r.orderingDeps(domain)
	cycles := findCycles(domain, d)
	resp := &v1.ResolveResponse{
		Affected: []v1.AffectedStack{},
		Waves:    [][]string{},
		External: r.externalDependents(scheduled),
	}
	var wave map[string]int
	if len(cycles) == 0 {
		member := make(map[string]bool, len(scheduled))
		for _, k := range scheduled {
			member[k] = true
		}
		var waves [][]string
		wave, waves = layer(scheduled, closure(scheduled, member, d))
		if waves != nil {
			resp.Waves = waves
		}
	}
	for _, key := range scheduled {
		resp.Affected = append(resp.Affected, r.affectedStack(key, wave[key]))
	}
	slices.SortStableFunc(resp.Affected, func(a, b v1.AffectedStack) int { return a.Wave - b.Wave })
	resp.Warnings = uniqueSorted(r.warnings)
	if len(cycles) > 0 {
		resp.Cycles = cycles
		resp.Matrix = v1.Matrix{Include: []v1.MatrixEntry{}}
		return resp, fmt.Errorf("resolve: %w: %s", ErrCycle, formatCycles(cycles))
	}
	resp.Matrix = BuildMatrix(resp.Affected, r.g.SHA)
	return resp, nil
}

func (r *resolver) externalDependents(scheduled []string) []string {
	var out []string
	for _, y := range scheduled {
		for _, e := range r.ix.in[v1.StackRef(y)] {
			if !isOrdering(e) {
				continue
			}
			if s := r.ix.stacks[e.From.Key]; s != nil && s.External {
				out = append(out, externalID(s))
			}
		}
	}
	return uniqueSorted(out)
}

func externalID(s *v1.Stack) string {
	if s.Repo != "" && !strings.Contains(s.Key, "//") {
		return v1.QualifiedStackKey(s.Repo, s.Key)
	}
	return s.Key
}

func (r *resolver) affectedStack(key string, wave int) v1.AffectedStack {
	s := r.ix.stacks[key]
	sc := s.Config
	if sc == nil {
		sc = &v1.StackConfig{}
	}
	a := v1.AffectedStack{
		Key:         key,
		Path:        stackDir(s),
		Workspace:   s.Workspace,
		Wave:        wave,
		Reasons:     r.reasons[key].list(),
		Environment: firstNonEmpty(s.Environment, sc.Environment, config.EnvironmentFor(r.cfg.Environments, stackDir(s))),
		Tool:        v1.Tool(firstNonEmpty(string(s.Tool), string(sc.Tool), string(r.cfg.Tool))),
		ToolVersion: firstNonEmpty(s.ToolVersion, sc.ToolVersion, r.cfg.ToolVersion),
		PlanOutput:  firstNonEmpty(s.PlanOutput, string(sc.PlanOutput), string(r.cfg.PlanOutput)),
		Via:         keysOf(r.via[key]),
	}
	if a.Workspace == "" {
		_, a.Workspace = v1.SplitStackKey(key)
	}
	if a.Workspace == "default" {
		a.Workspace = ""
	}
	if len(a.Via) == 0 {
		a.Via = nil
	}
	if a.Environment == "" {
		a.Environment = v1.DefaultEnvironment
		r.warn("stack %s has no environment mapping; it runs under environment %q", key, v1.DefaultEnvironment)
	}
	if lock, ok := r.in.Locks[key]; ok {
		a.LockedBy = &lock
	}
	return a
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
