package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatOutputs(t *testing.T) {
	tests := []struct {
		name  string
		outs  []output
		check func(t *testing.T, text string)
	}{
		{
			name: "single line values",
			outs: []output{{"count", "2"}, {"matrix", `{"include":[]}`}, {"empty", ""}},
			check: func(t *testing.T, text string) {
				assert.Equal(t, "count=2\nmatrix={\"include\":[]}\nempty=\n", text)
			},
		},
		{
			name: "multi line values use a heredoc",
			outs: []output{{"text", "a\nb\r\nc"}},
			check: func(t *testing.T, text string) {
				lines := strings.Split(text, "\n")
				require.Len(t, lines, 6)
				key, delim, ok := strings.Cut(lines[0], "<<")
				require.True(t, ok)
				assert.Equal(t, "text", key)
				assert.True(t, strings.HasPrefix(delim, "ghadelimiter_"))
				assert.Equal(t, []string{"a", "b\r", "c", delim, ""}, lines[1:])
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, err := formatOutputs(tt.outs)
			require.NoError(t, err)
			tt.check(t, text)
		})
	}
}

func TestSetOutputsAppends(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "out")
	a := &app{}
	require.NoError(t, a.setOutputs(output{"a", "1"}))
	assert.NoFileExists(t, path)

	t.Setenv(EnvGitHubOutput, path)
	require.NoError(t, a.setOutputs(output{"a", "1"}))
	require.NoError(t, a.setOutputs(output{"b", "two\nlines"}))
	assert.Equal(t, map[string]string{"a": "1", "b": "two\nlines"}, parseOutputs(t, path))

	t.Setenv(EnvGitHubOutput, filepath.Join(t.TempDir(), "missing", "out"))
	require.ErrorContains(t, a.setOutputs(output{"a", "1"}), "writing step outputs")
}

func TestStepSummary(t *testing.T) {
	clearEnv(t)
	var logs strings.Builder
	a := newTestApp(&logs)
	path := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv(EnvGitHubStepSummary, path)
	a.stepSummary("# one\n")
	a.stepSummary("")
	a.stepSummary("# two\n")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# one\n# two\n", string(data))

	t.Setenv(EnvGitHubStepSummary, filepath.Join(t.TempDir(), "missing", "summary.md"))
	a.stepSummary("# three\n")
	assert.Contains(t, logs.String(), "writing the step summary failed")
}

func TestJSONLine(t *testing.T) {
	assert.Equal(t, `{"a":[1,2]}`, jsonLine(map[string][]int{"a": {1, 2}}))
	assert.Equal(t, "null", jsonLine(func() {}))
}
