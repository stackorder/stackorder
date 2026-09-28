package graph

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestValidate(t *testing.T) {
	m := localKey("modules/m")
	gitVPC := "acme/modules//vpc@v1.2.0"
	tests := []struct {
		name         string
		graph        *v1.Graph
		wantWarnings []string
		wantErrs     []string
		wantCycle    bool
	}{
		{
			name:  "design example is valid",
			graph: exampleGraph().build(),
		},
		{
			name:  "empty graph is valid",
			graph: &v1.Graph{},
		},
		{
			name: "externals, workspaces, git and registry modules are valid",
			graph: newGraph().stacks("s/a", "s/a:blue").external("acme/net//s/tgw", "acme/net").
				git(gitVPC, "v1.2.0").
				module(v1.Module{Key: "registry:ns/name/aws@1.0", Kind: v1.ModuleRegistry}).
				uses("s/a", gitVPC).uses("s/a:blue", "registry:ns/name/aws@1.0").
				dep("s/a", "acme/net//s/tgw").dep("acme/net//s/tgw", "s/a:blue").build(),
		},
		{
			name:     "nil graph",
			wantErrs: []string{"nil graph"},
		},
		{
			name:     "empty stack key",
			graph:    newGraph().stacks("s/a").stack(v1.Stack{Path: "s/b"}).build(),
			wantErrs: []string{"stacks[1]: empty key"},
		},
		{
			name:     "duplicate stack key",
			graph:    newGraph().stacks("s/a", "s/b", "s/a", "s/a").build(),
			wantErrs: []string{"stack s/a: duplicate key"},
		},
		{
			name:     "empty module key",
			graph:    newGraph().module(v1.Module{Kind: v1.ModuleGit}).build(),
			wantErrs: []string{"modules[0]: empty key"},
		},
		{
			name:     "duplicate module key",
			graph:    newGraph().local("modules/m").local("modules/m").build(),
			wantErrs: []string{"module acme/infra//modules/m: duplicate key"},
		},
		{
			name:     "unknown module kind",
			graph:    newGraph().module(v1.Module{Key: "x", Kind: "svn"}).build(),
			wantErrs: []string{`module x: unknown kind "svn"`},
		},
		{
			name:     "unknown edge type",
			graph:    newGraph().stacks("s/a", "s/b").edge(v1.Edge{From: v1.StackRef("s/b"), To: v1.StackRef("s/a"), Type: "bogus"}).build(),
			wantErrs: []string{"edge stack s/b -> stack s/a (bogus): unknown edge type"},
		},
		{
			name: "depends_on and reads_state must connect stacks",
			graph: newGraph().stacks("s/a").local("modules/m").
				edge(v1.Edge{From: v1.ModuleRef(m), To: v1.StackRef("s/a"), Type: v1.EdgeDependsOn}).
				edge(v1.Edge{From: v1.StackRef("s/a"), To: v1.ModuleRef(m), Type: v1.EdgeReadsState}).
				build(),
			wantErrs: []string{
				"edge module acme/infra//modules/m -> stack s/a (depends_on): must connect two stacks",
				"edge stack s/a -> module acme/infra//modules/m (reads_state): must connect two stacks",
			},
		},
		{
			name: "uses_module must point at a module from a stack or module",
			graph: newGraph().stacks("s/a", "s/b").local("modules/m").
				edge(v1.Edge{From: v1.StackRef("s/a"), To: v1.StackRef("s/b"), Type: v1.EdgeUsesModule}).
				edge(v1.Edge{From: v1.NodeRef{Key: "s/a"}, To: v1.ModuleRef(m), Type: v1.EdgeUsesModule}).
				build(),
			wantErrs: []string{
				"edge stack s/a -> stack s/b (uses_module): must go from a stack or module to a module",
				"edge node s/a -> module acme/infra//modules/m (uses_module): must go from a stack or module to a module",
			},
		},
		{
			name: "self edges",
			graph: newGraph().stacks("s/a").local("modules/m").
				dep("s/a", "s/a").reads("s/a", "s/a").uses(m, m).build(),
			wantErrs: []string{
				"edge stack s/a -> stack s/a (depends_on): self edge",
				"edge stack s/a -> stack s/a (reads_state): self edge",
				"edge module acme/infra//modules/m -> module acme/infra//modules/m (uses_module): self edge",
			},
		},
		{
			name: "dangling endpoints",
			graph: newGraph().stacks("s/a").local("modules/m").
				dep("ghost", "s/a").
				reads("s/a", "phantom").
				edge(v1.Edge{From: v1.StackRef("s/a"), To: v1.ModuleRef("acme/infra//modules/gone"), Type: v1.EdgeUsesModule}).
				edge(v1.Edge{From: v1.ModuleRef("acme/infra//modules/gone"), To: v1.ModuleRef(m), Type: v1.EdgeUsesModule}).
				build(),
			wantErrs: []string{
				"edge stack ghost -> stack s/a (depends_on): unknown stack ghost",
				"edge stack s/a -> stack phantom (reads_state): unknown stack phantom",
				"edge stack s/a -> module acme/infra//modules/gone (uses_module): unknown module acme/infra//modules/gone",
				"edge module acme/infra//modules/gone -> module acme/infra//modules/m (uses_module): unknown module acme/infra//modules/gone",
			},
		},
		{
			name:         "depends_on an unknown stack is only a warning",
			graph:        newGraph().stacks("s/a", "s/b").dep("s/a", "s/typo").dep("s/b", "s/typo").dep("s/b", "s/a").build(),
			wantWarnings: []string{"stack s/a depends_on unknown stack s/typo", "stack s/b depends_on unknown stack s/typo"},
		},
		{
			name: "dangling source of a depends_on to an unknown stack is an error and a warning",
			graph: newGraph().stacks("s/a").
				dep("ghost", "phantom").build(),
			wantWarnings: []string{"stack ghost depends_on unknown stack phantom"},
			wantErrs:     []string{"edge stack ghost -> stack phantom (depends_on): unknown stack ghost"},
		},
		{
			name: "key that does not match path and workspace",
			graph: newGraph().
				stack(v1.Stack{Key: "s/x", Path: "s/y"}).
				stack(v1.Stack{Key: "s/z", Path: "s/z", Workspace: "blue"}).
				stack(v1.Stack{Key: "s/w:blue", Path: "s/w", Workspace: "blue"}).
				stack(v1.Stack{Key: "s/v", Path: "s/v", Workspace: "default"}).
				stack(v1.Stack{Key: "keyonly"}).
				build(),
			wantWarnings: []string{
				`stack s/x: key does not match path "s/y" and workspace ""`,
				`stack s/z: key does not match path "s/z" and workspace "blue"`,
			},
		},
		{
			name: "local stack outside the repository",
			graph: newGraph().
				stack(v1.Stack{Key: "../../etc"}).
				stack(v1.Stack{Key: "s/escape", Path: "../s/escape"}).
				stack(v1.Stack{Key: "./s/dot"}).
				external("acme/net//s/tgw", "acme/net").
				build(),
			wantWarnings: []string{
				`stack ../../etc: no canonical directory inside the repository, so it is never scheduled`,
				`stack ./s/dot: no canonical directory inside the repository, so it is never scheduled`,
				`stack s/escape: key does not match path "../s/escape" and workspace ""`,
				`stack s/escape: no canonical directory inside the repository, so it is never scheduled`,
			},
		},
		{
			name: "local module without a path",
			graph: newGraph().
				module(v1.Module{Key: "acme/infra//modules/np", Kind: v1.ModuleLocal}).
				module(v1.Module{Key: "acme/infra//modules/up", Kind: v1.ModuleLocal, Path: "../up"}).
				build(),
			wantWarnings: []string{
				"module acme/infra//modules/np: local module has no path, so changes to it are never detected",
				"module acme/infra//modules/up: local module has no path, so changes to it are never detected",
			},
		},
		{
			name: "module cycle is a warning",
			graph: newGraph().stacks("s/a").local("m/a").local("m/b").
				uses("s/a", localKey("m/a")).uses(localKey("m/a"), localKey("m/b")).uses(localKey("m/b"), localKey("m/a")).build(),
			wantWarnings: []string{"module cycle: acme/infra//m/a -> acme/infra//m/b -> acme/infra//m/a"},
		},
		{
			name:      "two-cycle",
			graph:     newGraph().stacks("s/a", "s/b").dep("s/a", "s/b").dep("s/b", "s/a").build(),
			wantErrs:  []string{"dependency cycle: s/a -> s/b -> s/a"},
			wantCycle: true,
		},
		{
			name:      "three-cycle over both ordering edge types",
			graph:     newGraph().stacks("s/a", "s/b", "s/c", "s/d").dep("s/a", "s/c").reads("s/c", "s/b").dep("s/b", "s/a").dep("s/d", "s/a").build(),
			wantErrs:  []string{"dependency cycle: s/a -> s/c -> s/b -> s/a"},
			wantCycle: true,
		},
		{
			name: "cycle through external stacks",
			graph: newGraph().stacks("s/a").external("acme/net//s/tgw", "acme/net").
				dep("s/a", "acme/net//s/tgw").dep("acme/net//s/tgw", "s/a").build(),
			wantErrs:  []string{"dependency cycle: acme/net//s/tgw -> s/a -> acme/net//s/tgw"},
			wantCycle: true,
		},
		{
			name:      "several cycles in one error",
			graph:     newGraph().stacks("s/a", "s/b", "t/x", "t/y").dep("s/a", "s/b").dep("s/b", "s/a").dep("t/x", "t/y").reads("t/y", "t/x").build(),
			wantErrs:  []string{"dependency cycle: s/a -> s/b -> s/a; t/x -> t/y -> t/x"},
			wantCycle: true,
		},
		{
			name: "several problems are all reported",
			graph: newGraph().stacks("s/a", "s/a", "s/b").module(v1.Module{Key: "x", Kind: "svn"}).
				dep("s/a", "s/b").dep("s/b", "s/a").dep("s/b", "s/b").dep("s/b", "s/typo").build(),
			wantWarnings: []string{"stack s/b depends_on unknown stack s/typo"},
			wantErrs: []string{
				"stack s/a: duplicate key",
				`module x: unknown kind "svn"`,
				"edge stack s/b -> stack s/b (depends_on): self edge",
				"dependency cycle: s/a -> s/b -> s/a",
			},
			wantCycle: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings, err := Validate(tt.graph)
			require.Empty(t, cmp.Diff(tt.wantWarnings, warnings), "warnings")
			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidGraph)
			require.Equal(t, tt.wantCycle, errors.Is(err, ErrCycle))
			require.Equal(t, "invalid graph: "+strings.Join(tt.wantErrs, "\n"), err.Error())
		})
	}
}
