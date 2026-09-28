package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestReadDir(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		names    []string
		want     dirConfig
		warnings []string
	}{
		{
			name: "s3 backend with literal attributes",
			files: map[string]string{"main.tf": `terraform {
  required_version = ">= 1.10"
  backend "s3" {
    bucket               = "acme-state"
    key                  = "prod/vpc.tfstate"
    region               = "eu-west-1"
    dynamodb_table       = "locks"
    use_lockfile         = true
    workspace_key_prefix = "ws"
    encrypt              = true
    assume_role {
      role_arn = "arn:aws:iam::123456789012:role/state"
    }
  }
}
`},
			want: dirConfig{
				backendType: "s3",
				backendLoc:  "stack/main.tf:3",
				backend: &v1.Backend{
					Type: "s3", Bucket: "acme-state", Key: "prod/vpc.tfstate", Region: "eu-west-1",
					DynamoDBTable: "locks", UseLockfile: true, WorkspaceKeyPrefix: "ws",
				},
			},
		},
		{
			name: "non-literal attributes are left empty",
			files: map[string]string{"backend.tf": `terraform {
  backend "s3" {
    bucket         = "acme-${"state"}"
    key            = "${var.env}/vpc.tfstate"
    region         = var.region
    dynamodb_table = lower("LOCKS")
    use_lockfile   = var.lock
  }
}
`},
			want: dirConfig{backendType: "s3", backendLoc: "stack/backend.tf:2", backend: &v1.Backend{Type: "s3"}},
			warnings: []string{
				`stack/backend.tf:3: backend "s3" attribute "bucket" is not a literal value; left empty`,
				`stack/backend.tf:4: backend "s3" attribute "key" is not a literal value; left empty`,
				`stack/backend.tf:5: backend "s3" attribute "region" is not a literal value; left empty`,
				`stack/backend.tf:6: backend "s3" attribute "dynamodb_table" is not a literal value; left empty`,
				`stack/backend.tf:7: backend "s3" attribute "use_lockfile" is not a literal value; left empty`,
			},
		},
		{
			name: "json backend",
			files: map[string]string{"main.tf.json": `{
  "terraform": {
    "backend": {
      "s3": {
        "bucket": "acme-state",
        "key": "json.tfstate",
        "region": "${var.region}",
        "use_lockfile": true
      }
    }
  }
}
`},
			want: dirConfig{
				backendType: "s3",
				backendLoc:  "stack/main.tf.json:4",
				backend:     &v1.Backend{Type: "s3", Bucket: "acme-state", Key: "json.tfstate", UseLockfile: true},
			},
			warnings: []string{`stack/main.tf.json:7: backend "s3" attribute "region" is not a literal value; left empty`},
		},
		{
			name:  "other backend types carry no s3 settings",
			files: map[string]string{"main.tf": "terraform {\n  backend \"gcs\" {\n    bucket = \"x\"\n  }\n}\n"},
			want:  dirConfig{backendType: "gcs", backendLoc: "stack/main.tf:2"},
		},
		{
			name: "duplicate backend keeps the first",
			files: map[string]string{
				"a.tf": "terraform {\n  backend \"s3\" {\n    bucket = \"a\"\n  }\n}\n",
				"b.tf": "terraform {\n  backend \"s3\" {\n    bucket = \"b\"\n  }\n}\n",
			},
			want:     dirConfig{backendType: "s3", backendLoc: "stack/a.tf:2", backend: &v1.Backend{Type: "s3", Bucket: "a"}},
			warnings: []string{"stack/b.tf:2: duplicate backend block; using the one at stack/a.tf:2"},
		},
		{
			name: "override file replaces the backend",
			files: map[string]string{
				"main.tf":          "terraform {\n  backend \"s3\" {\n    bucket = \"a\"\n  }\n}\n",
				"main_override.tf": "terraform {\n  backend \"s3\" {\n    bucket = \"b\"\n  }\n}\n",
				"override.tf.json": `{"locals": {"x": 1}}`,
			},
			names: []string{"main.tf", "main_override.tf", "override.tf.json"},
			want:  dirConfig{backendType: "s3", backendLoc: "stack/main_override.tf:2", backend: &v1.Backend{Type: "s3", Bucket: "b"}},
		},
		{
			name: "remote state with literal config",
			files: map[string]string{"data.tf": `data "terraform_remote_state" "vpc" {
  backend = "s3"
  config = {
    bucket = "acme-state"
    key    = "prod/vpc.tfstate"
    region = var.region
  }
}

data "aws_caller_identity" "current" {}
`},
			want: dirConfig{remoteStates: []remoteState{
				{name: "vpc", loc: "stack/data.tf:1", backend: "s3", bucket: "acme-state", key: "prod/vpc.tfstate"},
			}},
		},
		{
			name: "remote state with quoted keys, prefix and workspace",
			files: map[string]string{"data.tf": `data "terraform_remote_state" "blue" {
  backend   = "s3"
  workspace = "blue"
  config = {
    "bucket"             = "acme-state"
    "key"                = "vpc.tfstate"
    workspace_key_prefix = "ws"
  }
}
`},
			want: dirConfig{remoteStates: []remoteState{
				{name: "blue", loc: "stack/data.tf:1", backend: "s3", bucket: "acme-state", key: "vpc.tfstate", prefix: "ws", workspace: "blue"},
			}},
		},
		{
			name: "remote state in json",
			files: map[string]string{"data.tf.json": `{
  "data": {
    "terraform_remote_state": {
      "vpc": {
        "backend": "s3",
        "config": {"bucket": "acme-state", "key": "prod/vpc.tfstate"}
      }
    }
  }
}
`},
			want: dirConfig{remoteStates: []remoteState{
				{name: "vpc", loc: "stack/data.tf.json:4", backend: "s3", bucket: "acme-state", key: "prod/vpc.tfstate"},
			}},
		},
		{
			name: "remote state problems",
			files: map[string]string{"data.tf": `data "terraform_remote_state" "local" {
  backend = "local"
  config  = { path = "x.tfstate" }
}
data "terraform_remote_state" "no_backend" {
  config = {}
}
data "terraform_remote_state" "var_backend" {
  backend = var.backend
}
data "terraform_remote_state" "var_workspace" {
  backend   = "s3"
  workspace = terraform.workspace
  config    = { bucket = "b", key = "k" }
}
data "terraform_remote_state" "no_config" {
  backend = "s3"
}
data "terraform_remote_state" "var_config" {
  backend = "s3"
  config  = var.state
}
data "terraform_remote_state" "var_key" {
  backend = "s3"
  config = {
    (var.name) = "b"
  }
}
data "terraform_remote_state" "var_bucket" {
  backend = "s3"
  config = {
    bucket = var.bucket
    key    = "k"
  }
}
data "terraform_remote_state" "no_bucket" {
  backend = "s3"
  config  = { key = "k" }
}
data "terraform_remote_state" "no_key" {
  backend = "s3"
  config  = { bucket = "b" }
}
`},
			want: dirConfig{remoteStates: []remoteState{
				{name: "local", loc: "stack/data.tf:1", backend: "local"},
				{name: "no_backend", loc: "stack/data.tf:5", problem: "backend is not set"},
				{name: "var_backend", loc: "stack/data.tf:9", problem: "backend is not a literal value"},
				{name: "var_workspace", loc: "stack/data.tf:13", backend: "s3", problem: "workspace is not a literal value"},
				{name: "no_config", loc: "stack/data.tf:16", backend: "s3", problem: "config is not set"},
				{name: "var_config", loc: "stack/data.tf:21", backend: "s3", problem: "config is not an object literal"},
				{name: "var_key", loc: "stack/data.tf:26", backend: "s3", problem: "config has a key that is not a literal value"},
				{name: "var_bucket", loc: "stack/data.tf:32", backend: "s3", problem: "config.bucket is not a literal value"},
				{name: "no_bucket", loc: "stack/data.tf:38", backend: "s3", key: "k", problem: "config.bucket is not set"},
				{name: "no_key", loc: "stack/data.tf:42", backend: "s3", bucket: "b", problem: "config.key is not set"},
			}},
		},
		{
			name:     "syntax errors become warnings",
			files:    map[string]string{"broken.tf": "terraform {\n  backend \"s3\" {\n"},
			want:     dirConfig{backendType: "s3", backendLoc: "stack/broken.tf:2", backend: &v1.Backend{Type: "s3"}},
			warnings: []string{"stack/broken.tf:2: Unclosed configuration block"},
		},
		{
			name:  "invalid json becomes a warning",
			files: map[string]string{"broken.tf.json": "{\"terraform\": "},
			warnings: []string{
				"stack/broken.tf.json:1: Missing value",
				"stack/broken.tf.json:1: Unclosed object",
				"stack/broken.tf.json:1: Root value must be object",
			},
		},
		{
			name:     "blocks with the wrong labels become warnings",
			files:    map[string]string{"main.tf": "terraform {\n  backend {}\n}\ndata \"terraform_remote_state\" {}\n"},
			warnings: []string{"stack/main.tf:4: Missing name for data", "stack/main.tf:2: Missing type for backend"},
		},
		{
			name:     "unreadable files become warnings",
			names:    []string{"missing.tf"},
			warnings: []string{"stack/missing.tf: no such file or directory"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			names := tt.names
			for name, content := range tt.files {
				writeFile(t, root, "stack/"+name, content)
				if tt.names == nil {
					names = append(names, name)
				}
			}
			slices.Sort(names)
			var warnings []string
			fsys, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fsys.Close(); err != nil {
					t.Error(err)
				}
			})
			r := &hclReader{fsys: fsys, parser: hclparse.NewParser(), warn: func(format string, args ...any) {
				warnings = append(warnings, fmt.Sprintf(format, args...))
			}}
			got := r.readDir("stack", names)
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(dirConfig{}, remoteState{})); diff != "" {
				t.Errorf("readDir mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.warnings, warnings); diff != "" {
				t.Errorf("warnings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStateObjectKey(t *testing.T) {
	tests := []struct {
		prefix, workspace, key string
		want                   string
	}{
		{key: "vpc.tfstate", want: "vpc.tfstate"},
		{workspace: "default", key: "vpc.tfstate", want: "vpc.tfstate"},
		{workspace: "blue", key: "vpc.tfstate", want: "env:/blue/vpc.tfstate"},
		{prefix: "ws", workspace: "blue", key: "vpc.tfstate", want: "ws/blue/vpc.tfstate"},
		{prefix: "ws", key: "vpc.tfstate", want: "vpc.tfstate"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := stateObjectKey(tt.prefix, tt.workspace, tt.key); got != tt.want {
				t.Errorf("stateObjectKey(%q, %q, %q) = %q, want %q", tt.prefix, tt.workspace, tt.key, got, tt.want)
			}
		})
	}
}

func TestDiagnosticsAndPathErrors(t *testing.T) {
	var warnings []string
	r := &hclReader{warn: func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }}
	r.diagnostics(hcl.Diagnostics{
		{Severity: hcl.DiagWarning, Summary: "ignored warning"},
		{Severity: hcl.DiagError, Summary: "no subject"},
		{Severity: hcl.DiagError, Summary: "with subject", Subject: &hcl.Range{Filename: "a.tf", Start: hcl.Pos{Line: 7}}},
	})
	if diff := cmp.Diff([]string{"no subject", "a.tf:7: with subject"}, warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}
	plain := errors.New("plain")
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "path error is unwrapped", err: &fs.PathError{Op: "open", Path: "/abs/x.tf", Err: fs.ErrPermission}, want: fs.ErrPermission},
		{name: "other errors pass through", err: plain, want: plain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathError(tt.err); !errors.Is(got, tt.want) || got.Error() != tt.want.Error() {
				t.Errorf("pathError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
