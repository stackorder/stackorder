package graph

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestBuildMatrix(t *testing.T) {
	tests := []struct {
		name     string
		affected []v1.AffectedStack
		sha      string
		want     []v1.MatrixEntry
	}{
		{
			name: "empty",
			sha:  "abc",
			want: []v1.MatrixEntry{},
		},
		{
			name: "fields copied and sha set on every entry",
			sha:  "0123abcd",
			affected: []v1.AffectedStack{
				{
					Key: "stacks/prod/vpc", Path: "stacks/prod/vpc", Wave: 0, Environment: "production",
					Tool: v1.ToolTofu, ToolVersion: "1.9.0", PlanOutput: "summary",
					Reasons: []v1.Reason{v1.ReasonChanged}, Via: []string{"x"},
				},
			},
			want: []v1.MatrixEntry{
				{
					Stack: "stacks/prod/vpc", Key: "stacks/prod/vpc", Environment: "production", Wave: 0,
					Tool: v1.ToolTofu, ToolVersion: "1.9.0", PlanOutput: "summary", SHA: "0123abcd",
				},
			},
		},
		{
			name: "sorted by wave then key",
			sha:  "s",
			affected: []v1.AffectedStack{
				{Key: "c", Path: "c", Wave: 1, Environment: "e"},
				{Key: "b", Path: "b", Wave: 0, Environment: "e"},
				{Key: "a", Path: "a", Wave: 1, Environment: "e"},
			},
			want: []v1.MatrixEntry{
				{Stack: "b", Key: "b", Environment: "e", Wave: 0, SHA: "s"},
				{Stack: "a", Key: "a", Environment: "e", Wave: 1, SHA: "s"},
				{Stack: "c", Key: "c", Environment: "e", Wave: 1, SHA: "s"},
			},
		},
		{
			name: "empty environment falls back to default",
			sha:  "s",
			affected: []v1.AffectedStack{
				{Key: "stacks/tools", Path: "stacks/tools"},
			},
			want: []v1.MatrixEntry{
				{Stack: "stacks/tools", Key: "stacks/tools", Environment: v1.DefaultEnvironment, SHA: "s"},
			},
		},
		{
			name: "workspace stacks",
			sha:  "s",
			affected: []v1.AffectedStack{
				{Key: "stacks/app:blue", Path: "stacks/app", Workspace: "blue", Environment: "e"},
				{Key: "stacks/app", Path: "stacks/app", Environment: "e"},
			},
			want: []v1.MatrixEntry{
				{Stack: "stacks/app", Key: "stacks/app", Environment: "e", SHA: "s"},
				{Stack: "stacks/app", Key: "stacks/app:blue", Workspace: "blue", Environment: "e", SHA: "s"},
			},
		},
		{
			name: "path and workspace derived from the key when missing",
			sha:  "s",
			affected: []v1.AffectedStack{
				{Key: "stacks/app:green", Environment: "e"},
				{Key: "stacks/db", Environment: "e"},
			},
			want: []v1.MatrixEntry{
				{Stack: "stacks/app", Key: "stacks/app:green", Workspace: "green", Environment: "e", SHA: "s"},
				{Stack: "stacks/db", Key: "stacks/db", Environment: "e", SHA: "s"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := append([]v1.AffectedStack(nil), tt.affected...)
			got := BuildMatrix(tt.affected, tt.sha)
			require.NotNil(t, got.Include)
			require.Empty(t, cmp.Diff(tt.want, got.Include))
			require.Empty(t, cmp.Diff(input, tt.affected), "input must not be reordered")
		})
	}
}

func TestBuildMatrixJSON(t *testing.T) {
	m := BuildMatrix([]v1.AffectedStack{
		{Key: "stacks/app:blue", Path: "stacks/app", Workspace: "blue", Wave: 1, Environment: "staging", Tool: v1.ToolTerraform, ToolVersion: "1.14.0", PlanOutput: "full"},
	}, "deadbeef")
	data, err := json.Marshal(m)
	require.NoError(t, err)
	require.JSONEq(t, `{"include":[{"stack":"stacks/app","key":"stacks/app:blue","workspace":"blue","environment":"staging","wave":1,"tool":"terraform","tool_version":"1.14.0","plan_output":"full","sha":"deadbeef"}]}`, string(data))

	empty, err := json.Marshal(BuildMatrix(nil, "x"))
	require.NoError(t, err)
	require.JSONEq(t, `{"include":[]}`, string(empty))
}
