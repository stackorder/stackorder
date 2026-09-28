package graph

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

var (
	fuzzStackKeys  = []string{"stacks/a", "stacks/a/b", "stacks/c", "stacks/a:blue", "", "stacks/c/d/e", "x", "../y", "acme/o//stacks/z", "stacks/a/b:green"}
	fuzzModuleKeys = []string{"acme/infra//modules/m", "acme/infra//modules/m/n", "acme/modules//m@v1", "registry:x/y/z@1", "", "stacks/a"}
	fuzzModulePath = []string{"modules/m", "modules/m/n", "", "stacks/a", "../out", "/abs/m", "modules\\m"}
	fuzzEdgeTypes  = []v1.EdgeType{v1.EdgeDependsOn, v1.EdgeReadsState, v1.EdgeUsesModule, "bogus"}
	fuzzKinds      = []v1.ModuleKind{v1.ModuleLocal, v1.ModuleGit, v1.ModuleRegistry, "svn"}
)

type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) next() int {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return int(b)
}

func pick[T any](r *byteReader, from []T) T {
	return from[r.next()%len(from)]
}

func fuzzGraph(spec []byte) *v1.Graph {
	r := &byteReader{data: spec}
	g := &v1.Graph{Repo: "acme/infra", SHA: "fuzz"}
	for range r.next() % 10 {
		key := pick(r, fuzzStackKeys)
		flags := r.next()
		p, ws := v1.SplitStackKey(key)
		s := v1.Stack{Key: key, Path: p, Workspace: ws}
		if flags&1 != 0 {
			s.External = true
			s.Repo = "acme/o"
		}
		if flags&2 != 0 {
			s.Path = ""
		}
		if flags&4 != 0 {
			s.Environment = "env"
		}
		if flags&8 != 0 {
			s.Config = &v1.StackConfig{Environment: "cfg", Tool: v1.ToolTofu}
		}
		if flags&16 != 0 {
			s.Path = "../elsewhere"
		}
		g.Stacks = append(g.Stacks, s)
	}
	for range r.next() % 7 {
		g.Modules = append(g.Modules, v1.Module{
			Key:  pick(r, fuzzModuleKeys),
			Kind: pick(r, fuzzKinds),
			Path: pick(r, fuzzModulePath),
			Ref:  "v1",
		})
	}
	for range r.next() % 25 {
		ref := func() v1.NodeRef {
			switch r.next() % 3 {
			case 0:
				return v1.StackRef(pick(r, fuzzStackKeys))
			case 1:
				return v1.ModuleRef(pick(r, fuzzModuleKeys))
			default:
				return v1.NodeRef{Key: pick(r, fuzzStackKeys)}
			}
		}
		e := v1.Edge{From: ref(), To: ref(), Type: pick(r, fuzzEdgeTypes), Inferred: r.next()%2 == 0}
		g.Edges = append(g.Edges, e)
	}
	return g
}

func FuzzResolve(f *testing.F) {
	f.Add([]byte{}, "", "", true)
	f.Add([]byte{3, 0, 0, 2, 0, 5, 0, 1, 0, 0, 0, 2, 0, 0, 1, 2, 0, 0, 1, 0, 0}, "stacks/a/main.tf\nmodules/m/x.tf", "", true)
	f.Add([]byte{4, 0, 0, 1, 0, 2, 0, 3, 0, 0, 4, 0, 0, 0, 2, 0, 0, 2, 0, 0, 0, 2, 0, 0, 1, 0}, "stacks/a/b/c.tf\n../x\n.terraform/y", "stacks/c,stacks/a", false)
	f.Add([]byte{9, 1, 1, 2, 2, 3, 4, 5, 8, 6, 16, 7, 3, 8, 1, 9, 0, 6, 0, 0, 0, 1, 1, 1, 2, 2, 2, 3, 3, 3, 24, 0, 0, 0, 1, 0, 0, 0, 2, 1, 1, 2, 0, 0, 0, 1, 0, 3}, "stacks/c/d/e/f.tf\nmodules/m/n/x\nREADME.md", "acme/infra//stacks/c,,acme/o//stacks/z", true)
	f.Add([]byte{2, 0, 0, 2, 0, 0, 2, 0, 0, 0, 0, 2, 0, 0, 0, 2, 0, 0, 1, 0}, "stacks/a/x.tf\nstacks/c/x.tf", "", true)
	f.Fuzz(func(t *testing.T, spec []byte, paths, requested string, dependents bool) {
		g := fuzzGraph(spec)
		cfg := config.Default()
		cfg.Propagate.Dependents = &dependents
		cfg.Environments = map[string]string{"stacks/a": "a"}
		in := Input{
			ChangedPaths: strings.Split(paths, "\n"),
			Config:       cfg,
			Locks:        map[string]v1.LockInfo{"stacks/a": {RunID: "r"}},
		}
		if requested != "" {
			in.Requested = strings.Split(requested, ",")
		}
		resp, err := Resolve(g, in)
		switch {
		case err == nil:
			checkFuzzResponse(t, g, resp)
		case errors.Is(err, ErrCycle):
			if resp == nil || len(resp.Cycles) == 0 || len(resp.Matrix.Include) != 0 {
				t.Fatalf("cycle error without cycles: %v %+v", err, resp)
			}
		case errors.Is(err, ErrInvalidGraph):
			if resp != nil {
				t.Fatalf("invalid graph with a response: %+v", resp)
			}
		default:
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := json.Marshal(resp); err != nil {
			t.Fatalf("marshal: %v", err)
		}

		keys := make([]string, 0, len(g.Stacks))
		highlight := map[string]int{}
		for i, s := range g.Stacks {
			keys = append(keys, s.Key)
			highlight[s.Key] = i - 2
		}
		_, _ = Validate(g)
		Waves(g, keys)
		ToDOT(g, highlight)
		Dependents(g, keys)
		Dependencies(g, keys, fuzzEdgeTypes...)
		for _, m := range g.Modules {
			ModuleConsumers(g, m.Key)
		}
		for _, k := range keys {
			StackModules(g, k)
		}
	})
}

func checkFuzzResponse(t *testing.T, g *v1.Graph, resp *v1.ResolveResponse) {
	t.Helper()
	if len(resp.Matrix.Include) != len(resp.Affected) {
		t.Fatalf("matrix has %d entries for %d stacks", len(resp.Matrix.Include), len(resp.Affected))
	}
	wave := map[string]int{}
	for w, keys := range resp.Waves {
		if len(keys) == 0 || !slices.IsSorted(keys) {
			t.Fatalf("wave %d is empty or unsorted: %v", w, keys)
		}
		for _, k := range keys {
			if _, dup := wave[k]; dup {
				t.Fatalf("%s is in two waves", k)
			}
			wave[k] = w
		}
	}
	if len(wave) != len(resp.Affected) {
		t.Fatalf("%d stacks in waves, %d affected", len(wave), len(resp.Affected))
	}
	for _, a := range resp.Affected {
		if w, ok := wave[a.Key]; !ok || w != a.Wave {
			t.Fatalf("%s has wave %d, waves say %d", a.Key, a.Wave, w)
		}
		if a.Environment == "" || len(a.Reasons) == 0 {
			t.Fatalf("incomplete affected stack %+v", a)
		}
	}
	for _, e := range g.Edges {
		from, okFrom := wave[e.From.Key]
		to, okTo := wave[e.To.Key]
		if isOrdering(&e) && okFrom && okTo && to >= from {
			t.Fatalf("edge %s -> %s goes from wave %d to wave %d", e.From.Key, e.To.Key, from, to)
		}
	}
}
