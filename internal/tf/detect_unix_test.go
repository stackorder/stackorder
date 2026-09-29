//go:build unix

package tf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestDetect(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"terraform", "tofu", "terraform-1.9"} {
		require.NoError(t, os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"), 0o755))
	}
	overrideDir := t.TempDir()
	override := filepath.Join(overrideDir, "my-tofu")
	require.NoError(t, os.WriteFile(override, []byte("#!/bin/sh\n"), 0o755))

	tests := []struct {
		name    string
		tool    v1.Tool
		env     map[string]string
		want    string
		wantErr error
	}{
		{name: "terraform", tool: v1.ToolTerraform, want: filepath.Join(binDir, "terraform")},
		{name: "default is terraform", tool: "", want: filepath.Join(binDir, "terraform")},
		{name: "tofu", tool: v1.ToolTofu, want: filepath.Join(binDir, "tofu")},
		{name: "terraform override by name", tool: v1.ToolTerraform, env: map[string]string{EnvTerraformBin: "terraform-1.9"}, want: filepath.Join(binDir, "terraform-1.9")},
		{name: "tofu override by path", tool: v1.ToolTofu, env: map[string]string{EnvTofuBin: override}, want: override},
		{name: "override of the other tool is ignored", tool: v1.ToolTofu, env: map[string]string{EnvTerraformBin: "terraform-1.9"}, want: filepath.Join(binDir, "tofu")},
		{name: "missing override", tool: v1.ToolTerraform, env: map[string]string{EnvTerraformBin: "terraform-0.12"}, wantErr: ErrToolNotFound},
		{name: "unknown tool", tool: "pulumi", wantErr: ErrUnknownTool},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", binDir)
			t.Setenv(EnvTerraformBin, "")
			t.Setenv(EnvTofuBin, "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got, err := Detect(tt.tool)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetectNotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv(EnvTofuBin, "")
	_, err := Detect(v1.ToolTofu)
	require.ErrorIs(t, err, ErrToolNotFound)
}

func TestVersion(t *testing.T) {
	tests := []struct {
		name     string
		stdout   string
		exit     string
		want     string
		wantTofu bool
		wantErr  string
	}{
		{
			name:   "terraform",
			stdout: `{"terraform_version":"1.14.4","platform":"linux_amd64","provider_selections":{},"terraform_outdated":false}`,
			want:   "1.14.4",
		},
		{
			name:     "tofu",
			stdout:   `{"terraform_version":"1.12.6","platform":"linux_amd64","provider_selections":{}}`,
			want:     "1.12.6",
			wantTofu: true,
		},
		{name: "not json", stdout: "Terraform v0.12.31", wantErr: "decoding"},
		{name: "no version", stdout: `{"platform":"linux_amd64"}`, wantErr: "no terraform_version"},
		{name: "failure", stdout: "", exit: "1", wantErr: "version -json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeTF(t, "terraform")
			t.Setenv("FAKE_TF_LOG", f.log)
			t.Setenv("FAKE_TF_EXIT", tt.exit)
			t.Setenv("FAKE_TF_STDOUT", "")
			if tt.stdout != "" {
				_, v, _ := strings.Cut(f.stdoutFile(tt.stdout), "=")
				t.Setenv("FAKE_TF_STDOUT", v)
			}
			got, isTofu, err := Version(f.ctx(), f.bin)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantTofu, isTofu)
			assert.Equal(t, []string{"version", "-json"}, f.lastCall().Args)
		})
	}
}
