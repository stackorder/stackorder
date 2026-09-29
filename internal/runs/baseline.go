package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/graph"
	"github.com/stackorder/stackorder/internal/store"
)

var structuredConfigKeys = map[string]bool{"stacks": true, "modules": true, "apply": true, "propagate": true, "drift": true}

func configDiff(base, pr *v1.RepoConfig) []string {
	a, b := configFields(base), configFields(pr)
	var out []string
	for _, k := range unionKeys(a, b) {
		sa, aok := a[k].(map[string]any)
		sb, bok := b[k].(map[string]any)
		if !structuredConfigKeys[k] || !aok || !bok {
			if !reflect.DeepEqual(a[k], b[k]) {
				out = append(out, k)
			}
			continue
		}
		for _, sub := range unionKeys(sa, sb) {
			if !reflect.DeepEqual(sa[sub], sb[sub]) {
				out = append(out, k+"."+sub)
			}
		}
	}
	return out
}

func configFields(c *v1.RepoConfig) map[string]any {
	out := map[string]any{}
	b, err := json.Marshal(c)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func unionKeys(a, b map[string]any) []string {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

func (s *Service) baselineGraph(ctx context.Context, repo store.Repo) *v1.Graph {
	g, _, err := s.st.GetDefaultGraph(ctx, repo.ID)
	if errors.Is(err, store.ErrNotFound) {
		g, _, err = s.st.LatestGraph(ctx, repo.ID)
	}
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.WarnContext(ctx, "load the default-branch graph", "repo", repo.FullName, "error", err)
		}
		return nil
	}
	return g
}

func withDefaultBranchStacks(g, baseline *v1.Graph, in graph.Input, pr, base *v1.RepoConfig) *v1.Graph {
	if baseline == nil {
		return nil
	}
	have := make(map[string]bool, len(g.Stacks))
	for _, st := range g.Stacks {
		have[st.Key] = true
	}
	var candidates []string
	for _, st := range baseline.Stacks {
		if have[st.Key] || !localTo(baseline, st) {
			continue
		}
		dir := stackDir(st)
		if discovers(base, dir) && !discovers(pr, dir) {
			candidates = append(candidates, st.Key)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	in.Config = base
	resp, _ := graph.Resolve(withStacks(g, baseline, candidates), in)
	if resp == nil {
		return nil
	}
	affected := make(map[string]bool, len(resp.Affected))
	for _, a := range resp.Affected {
		affected[a.Key] = true
	}
	extras := slices.DeleteFunc(candidates, func(k string) bool { return !affected[k] })
	if len(extras) == 0 {
		return nil
	}
	return withStacks(g, baseline, extras)
}

func localTo(g *v1.Graph, st v1.Stack) bool {
	return !st.External && (st.Repo == "" || strings.EqualFold(st.Repo, g.Repo))
}

func stackDir(st v1.Stack) string {
	if st.Path != "" {
		return st.Path
	}
	p, _ := v1.SplitStackKey(st.Key)
	return p
}

func discovers(cfg *v1.RepoConfig, dir string) bool {
	for _, inc := range cfg.Stacks.Include {
		if config.NormalizePath(inc) == dir {
			return true
		}
	}
	return matchesAny(cfg.Stacks.Discover, dir) && !underAny(cfg.Modules.Paths, dir)
}

func underAny(globs []string, dir string) bool {
	for d := dir; ; d = path.Dir(d) {
		if d == "." {
			d = ""
		}
		if matchesAny(globs, d) {
			return true
		}
		if d == "" {
			return false
		}
	}
}

func matchesAny(globs []string, p string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(g, p); err == nil && ok {
			return true
		}
	}
	return false
}

type edgeID struct {
	from, to v1.NodeRef
	typ      v1.EdgeType
}

func withStacks(g, baseline *v1.Graph, keys []string) *v1.Graph {
	out := *g
	out.Stacks = slices.Clone(g.Stacks)
	out.Modules = slices.Clone(g.Modules)
	out.Edges = slices.Clone(g.Edges)
	stacks := map[string]bool{}
	for _, st := range out.Stacks {
		stacks[st.Key] = true
	}
	modules := map[string]bool{}
	for _, m := range out.Modules {
		modules[m.Key] = true
	}
	added := map[string]bool{}
	for _, k := range keys {
		added[k] = true
	}
	for _, st := range baseline.Stacks {
		if added[st.Key] && !stacks[st.Key] {
			out.Stacks = append(out.Stacks, st)
			stacks[st.Key] = true
		}
	}
	reached := map[string]bool{}
	var queue []string
	for _, e := range baseline.Edges {
		if e.Type == v1.EdgeUsesModule && e.From.Kind == v1.NodeStack && added[e.From.Key] && !reached[e.To.Key] {
			reached[e.To.Key] = true
			queue = append(queue, e.To.Key)
		}
	}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		for _, e := range baseline.Edges {
			if e.Type == v1.EdgeUsesModule && e.From.Kind == v1.NodeModule && e.From.Key == m && !reached[e.To.Key] {
				reached[e.To.Key] = true
				queue = append(queue, e.To.Key)
			}
		}
	}
	fromBaseline := map[string]bool{}
	for _, m := range baseline.Modules {
		if reached[m.Key] && !modules[m.Key] {
			out.Modules = append(out.Modules, m)
			modules[m.Key] = true
			fromBaseline[m.Key] = true
		}
	}
	present := func(n v1.NodeRef) bool {
		if n.Kind == v1.NodeModule {
			return modules[n.Key]
		}
		return stacks[n.Key]
	}
	seen := map[edgeID]bool{}
	for _, e := range out.Edges {
		seen[edgeID{e.From, e.To, e.Type}] = true
	}
	for _, e := range baseline.Edges {
		id := edgeID{e.From, e.To, e.Type}
		touches := e.From.Kind == v1.NodeStack && added[e.From.Key] ||
			e.To.Kind == v1.NodeStack && added[e.To.Key] ||
			e.From.Kind == v1.NodeModule && fromBaseline[e.From.Key]
		if !touches || seen[id] || !present(e.From) || !present(e.To) {
			continue
		}
		seen[id] = true
		out.Edges = append(out.Edges, e)
	}
	return &out
}

func unionResolution(g *v1.Graph, pr, base *v1.ResolveResponse, baseErr error, differs []string) (*v1.ResolveResponse, error) {
	if baseErr != nil {
		return base, baseErr
	}
	known := make(map[string]bool, len(pr.Affected))
	for _, a := range pr.Affected {
		known[a.Key] = true
	}
	var added []v1.AffectedStack
	for _, a := range base.Affected {
		if !known[a.Key] {
			added = append(added, a)
		}
	}
	if len(added) == 0 {
		return pr, nil
	}
	out := *pr
	out.Affected = append(slices.Clone(pr.Affected), added...)
	keys := make([]string, len(out.Affected))
	for i, a := range out.Affected {
		keys[i] = a.Key
	}
	addedKeys := keys[len(pr.Affected):]
	out.Warnings = append(slices.Clone(pr.Warnings), fmt.Sprintf(
		"the stackorder.yaml of this pull request differs from the default branch's in %s; under the default branch's configuration %s %s affected as well and planned too",
		strings.Join(differs, ", "), strings.Join(slices.Sorted(slices.Values(addedKeys)), ", "), isAre(len(addedKeys))))
	slices.Sort(out.Warnings)
	out.Warnings = slices.Compact(out.Warnings)
	out.External = slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(pr.External), base.External...))))
	waves, cycles := graph.Waves(g, keys)
	if len(cycles) > 0 {
		for i := range out.Affected {
			out.Affected[i].Wave = 0
		}
		out.Waves, out.Cycles = [][]string{}, cycles
		out.Matrix = v1.Matrix{Include: []v1.MatrixEntry{}}
		return &out, fmt.Errorf("resolve: %w: with the stacks the default branch's configuration adds", graph.ErrCycle)
	}
	wave := map[string]int{}
	for i, w := range waves {
		for _, k := range w {
			wave[k] = i
		}
	}
	for i := range out.Affected {
		out.Affected[i].Wave = wave[out.Affected[i].Key]
	}
	slices.SortStableFunc(out.Affected, func(a, b v1.AffectedStack) int { return a.Wave - b.Wave })
	out.Waves = waves
	out.Matrix = graph.BuildMatrix(out.Affected, g.SHA)
	return &out, nil
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
