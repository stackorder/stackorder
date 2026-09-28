package graph

import (
	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

const testRepo = "acme/infra"

type builder struct {
	g v1.Graph
}

func newGraph() *builder {
	return &builder{g: v1.Graph{Repo: testRepo, SHA: "abc123"}}
}

func (b *builder) stacks(keys ...string) *builder {
	for _, k := range keys {
		p, ws := v1.SplitStackKey(k)
		b.g.Stacks = append(b.g.Stacks, v1.Stack{Key: k, Path: p, Workspace: ws})
	}
	return b
}

func (b *builder) stack(s v1.Stack) *builder {
	b.g.Stacks = append(b.g.Stacks, s)
	return b
}

func (b *builder) external(key, repo string) *builder {
	_, p := v1.SplitQualifiedStackKey(key)
	b.g.Stacks = append(b.g.Stacks, v1.Stack{Key: key, Path: p, Repo: repo, External: true})
	return b
}

func (b *builder) local(dir string) *builder {
	b.g.Modules = append(b.g.Modules, v1.Module{
		Key:    localKey(dir),
		Kind:   v1.ModuleLocal,
		Path:   dir,
		Source: "../../" + dir,
	})
	return b
}

func (b *builder) git(key, ref string) *builder {
	b.g.Modules = append(b.g.Modules, v1.Module{
		Key:    key,
		Kind:   v1.ModuleGit,
		Source: "git::https://github.com/" + key + "?ref=" + ref,
		Ref:    ref,
	})
	return b
}

func (b *builder) module(m v1.Module) *builder {
	b.g.Modules = append(b.g.Modules, m)
	return b
}

func (b *builder) dep(from, to string) *builder {
	b.g.Edges = append(b.g.Edges, v1.Edge{From: v1.StackRef(from), To: v1.StackRef(to), Type: v1.EdgeDependsOn})
	return b
}

func (b *builder) reads(from, to string) *builder {
	b.g.Edges = append(b.g.Edges, v1.Edge{
		From:     v1.StackRef(from),
		To:       v1.StackRef(to),
		Type:     v1.EdgeReadsState,
		Inferred: true,
		Meta:     map[string]string{"bucket": "acme-state", "key": to + "/terraform.tfstate"},
	})
	return b
}

func (b *builder) uses(from, to string) *builder {
	fromRef := v1.StackRef(from)
	for _, m := range b.g.Modules {
		if m.Key == from {
			fromRef = v1.ModuleRef(from)
		}
	}
	b.g.Edges = append(b.g.Edges, v1.Edge{From: fromRef, To: v1.ModuleRef(to), Type: v1.EdgeUsesModule})
	return b
}

func (b *builder) edge(e v1.Edge) *builder {
	b.g.Edges = append(b.g.Edges, e)
	return b
}

func (b *builder) build() *v1.Graph {
	g := b.g
	return &g
}

func localKey(dir string) string {
	return testRepo + "//" + dir
}

func boolPtr(v bool) *bool {
	return &v
}

const (
	prodVPC     = "stacks/prod/vpc"
	stagingVPC  = "stacks/staging/vpc"
	prodEKS     = "stacks/prod/eks"
	prodApps    = "stacks/prod/apps"
	stagingApps = "stacks/staging/apps"
)

var (
	vpcModule = localKey("modules/vpc")
	eksModule = localKey("modules/eks")
)

func exampleGraph() *builder {
	return newGraph().
		stacks(prodVPC, stagingVPC, prodEKS, prodApps, stagingApps).
		local("modules/vpc").
		local("modules/eks").
		uses(prodVPC, vpcModule).
		uses(stagingVPC, vpcModule).
		uses(prodEKS, eksModule).
		dep(prodEKS, prodVPC).
		dep(stagingApps, stagingVPC).
		reads(prodApps, prodEKS)
}

func exampleConfig() *v1.RepoConfig {
	c := &v1.RepoConfig{
		Version:     1,
		Tool:        v1.ToolTofu,
		ToolVersion: "1.9.0",
		Environments: map[string]string{
			"stacks/prod/":    "production",
			"stacks/staging/": "staging",
		},
	}
	config.ApplyDefaults(c)
	return c
}

func affectedKeys(resp *v1.ResolveResponse) []string {
	out := make([]string, 0, len(resp.Affected))
	for _, a := range resp.Affected {
		out = append(out, a.Key)
	}
	return out
}

func reasonsByKey(resp *v1.ResolveResponse) map[string][]v1.Reason {
	out := make(map[string][]v1.Reason, len(resp.Affected))
	for _, a := range resp.Affected {
		out[a.Key] = a.Reasons
	}
	return out
}

func viaByKey(resp *v1.ResolveResponse) map[string][]string {
	out := make(map[string][]string, len(resp.Affected))
	for _, a := range resp.Affected {
		out[a.Key] = a.Via
	}
	return out
}

func reasons(rs ...v1.Reason) []v1.Reason {
	return rs
}
