package graph

import (
	"cmp"
	"slices"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// BuildMatrix turns affected stacks into the GitHub Actions matrix that plans
// them at sha, one entry per stack ordered by wave and then key. An empty
// environment becomes v1.DefaultEnvironment and an empty path is taken from
// the key. PlanRunID and Artifact are left for apply dispatches to set.
func BuildMatrix(affected []v1.AffectedStack, sha string) v1.Matrix {
	sorted := slices.Clone(affected)
	slices.SortStableFunc(sorted, func(a, b v1.AffectedStack) int {
		return cmp.Or(cmp.Compare(a.Wave, b.Wave), cmp.Compare(a.Key, b.Key))
	})
	m := v1.Matrix{Include: make([]v1.MatrixEntry, 0, len(sorted))}
	for _, a := range sorted {
		dir, workspace := a.Path, a.Workspace
		if dir == "" {
			dir, workspace = v1.SplitStackKey(a.Key)
			workspace = cmp.Or(a.Workspace, workspace)
		}
		env := a.Environment
		if env == "" {
			env = v1.DefaultEnvironment
		}
		m.Include = append(m.Include, v1.MatrixEntry{
			Stack:       dir,
			Key:         a.Key,
			Workspace:   workspace,
			Environment: env,
			Wave:        a.Wave,
			Tool:        a.Tool,
			ToolVersion: a.ToolVersion,
			PlanOutput:  a.PlanOutput,
			SHA:         sha,
		})
	}
	return m
}
