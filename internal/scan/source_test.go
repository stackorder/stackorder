package scan

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestParseModuleSource(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		version string
		want    v1.Module
		ok      bool
	}{
		{
			name:   "local sibling",
			source: "../vpc",
			want:   v1.Module{Kind: v1.ModuleLocal, Path: "../vpc", Source: "../vpc"},
			ok:     true,
		},
		{
			name:   "local child with trailing slash",
			source: "./modules/net/",
			want:   v1.Module{Kind: v1.ModuleLocal, Path: "modules/net", Source: "./modules/net/"},
			ok:     true,
		},
		{
			name:   "local windows separators",
			source: `..\..\modules\vpc`,
			want:   v1.Module{Kind: v1.ModuleLocal, Path: "../../modules/vpc", Source: `..\..\modules\vpc`},
			ok:     true,
		},
		{
			name:   "git https with subdir and ref",
			source: "git::https://github.com/o/r.git//sub?ref=v1",
			want:   v1.Module{Key: "o/r//sub@v1", Kind: v1.ModuleGit, Source: "git::https://github.com/o/r.git//sub?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "github shorthand with subdir and ref",
			source: "github.com/o/r//sub?ref=v1",
			want:   v1.Module{Key: "o/r//sub@v1", Kind: v1.ModuleGit, Source: "github.com/o/r//sub?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "scp-like ssh with subdir and ref",
			source: "git@github.com:o/r.git//sub?ref=v1",
			want:   v1.Module{Key: "o/r//sub@v1", Kind: v1.ModuleGit, Source: "git@github.com:o/r.git//sub?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "git ssh url with subdir and ref",
			source: "git::ssh://git@github.com/o/r.git//sub?ref=v1",
			want:   v1.Module{Key: "o/r//sub@v1", Kind: v1.ModuleGit, Source: "git::ssh://git@github.com/o/r.git//sub?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "forced scp-like",
			source: "git::git@github.com:o/r.git?ref=v2",
			want:   v1.Module{Key: "o/r@v2", Kind: v1.ModuleGit, Source: "git::git@github.com:o/r.git?ref=v2", Ref: "v2"},
			ok:     true,
		},
		{
			name:   "github without subdir",
			source: "git::https://github.com/o/r.git?ref=v1",
			want:   v1.Module{Key: "o/r@v1", Kind: v1.ModuleGit, Source: "git::https://github.com/o/r.git?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "github without ref",
			source: "github.com/o/r//sub",
			want:   v1.Module{Key: "o/r//sub", Kind: v1.ModuleGit, Source: "github.com/o/r//sub"},
			ok:     true,
		},
		{
			name:   "github shorthand without subdir or ref",
			source: "github.com/o/r",
			want:   v1.Module{Key: "o/r", Kind: v1.ModuleGit, Source: "github.com/o/r"},
			ok:     true,
		},
		{
			name:   "github shorthand with path segments beyond the repository",
			source: "github.com/o/r/modules/vpc?ref=v3",
			want:   v1.Module{Key: "o/r//modules/vpc@v3", Kind: v1.ModuleGit, Source: "github.com/o/r/modules/vpc?ref=v3", Ref: "v3"},
			ok:     true,
		},
		{
			name:   "nested subdir and extra query parameters",
			source: "git::https://github.com/o/r.git//a/b/?depth=1&ref=release/1.x",
			want:   v1.Module{Key: "o/r//a/b@release/1.x", Kind: v1.ModuleGit, Source: "git::https://github.com/o/r.git//a/b/?depth=1&ref=release/1.x", Ref: "release/1.x"},
			ok:     true,
		},
		{
			name:    "version is ignored for git sources",
			source:  "github.com/o/r?ref=v1",
			version: "2.0.0",
			want:    v1.Module{Key: "o/r@v1", Kind: v1.ModuleGit, Source: "github.com/o/r?ref=v1", Ref: "v1"},
			ok:      true,
		},
		{
			name:   "uppercase host",
			source: "git::https://GitHub.com/o/r.git?ref=v1",
			want:   v1.Module{Key: "o/r@v1", Kind: v1.ModuleGit, Source: "git::https://GitHub.com/o/r.git?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "gitlab https keeps host",
			source: "git::https://gitlab.com/o/r.git//sub?ref=v1",
			want:   v1.Module{Key: "gitlab.com/o/r//sub@v1", Kind: v1.ModuleGit, Source: "git::https://gitlab.com/o/r.git//sub?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "gitlab ssh url keeps host",
			source: "git::ssh://git@gitlab.com/o/r.git//sub?ref=v1",
			want:   v1.Module{Key: "gitlab.com/o/r//sub@v1", Kind: v1.ModuleGit, Source: "git::ssh://git@gitlab.com/o/r.git//sub?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "gitlab scp-like keeps host",
			source: "git@gitlab.com:o/r.git//sub?ref=v1",
			want:   v1.Module{Key: "gitlab.com/o/r//sub@v1", Kind: v1.ModuleGit, Source: "git@gitlab.com:o/r.git//sub?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "gitlab subgroups",
			source: "git::https://gitlab.com/group/sub/r.git//mod?ref=v1",
			want:   v1.Module{Key: "gitlab.com/group/sub/r//mod@v1", Kind: v1.ModuleGit, Source: "git::https://gitlab.com/group/sub/r.git//mod?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "generic host without .git suffix",
			source: "git::https://git.example.com/infra/modules?ref=abc123",
			want:   v1.Module{Key: "git.example.com/infra/modules@abc123", Kind: v1.ModuleGit, Source: "git::https://git.example.com/infra/modules?ref=abc123", Ref: "abc123"},
			ok:     true,
		},
		{
			name:   "generic host with path after .git",
			source: "git::https://git.example.com/infra.git/net",
			want:   v1.Module{Key: "git.example.com/infra//net", Kind: v1.ModuleGit, Source: "git::https://git.example.com/infra.git/net"},
			ok:     true,
		},
		{
			name:   "bitbucket shorthand keeps host",
			source: "bitbucket.org/o/r//sub?ref=v1",
			want:   v1.Module{Key: "bitbucket.org/o/r//sub@v1", Kind: v1.ModuleGit, Source: "bitbucket.org/o/r//sub?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:   "git protocol url",
			source: "git://example.com/o/r.git?ref=v1",
			want:   v1.Module{Key: "example.com/o/r@v1", Kind: v1.ModuleGit, Source: "git://example.com/o/r.git?ref=v1", Ref: "v1"},
			ok:     true,
		},
		{
			name:    "public registry with version",
			source:  "terraform-aws-modules/vpc/aws",
			version: "5.1.0",
			want:    v1.Module{Key: "registry:terraform-aws-modules/vpc/aws@5.1.0", Kind: v1.ModuleRegistry, Source: "terraform-aws-modules/vpc/aws", Ref: "5.1.0"},
			ok:      true,
		},
		{
			name:   "public registry without version",
			source: "hashicorp/consul/aws",
			want:   v1.Module{Key: "registry:hashicorp/consul/aws", Kind: v1.ModuleRegistry, Source: "hashicorp/consul/aws"},
			ok:     true,
		},
		{
			name:    "public registry with subdir and constraint",
			source:  "hashicorp/consul/aws//modules/consul-cluster",
			version: " ~> 0.1 ",
			want:    v1.Module{Key: "registry:hashicorp/consul/aws//modules/consul-cluster@~> 0.1", Kind: v1.ModuleRegistry, Source: "hashicorp/consul/aws//modules/consul-cluster", Ref: "~> 0.1"},
			ok:      true,
		},
		{
			name:    "default registry host is dropped",
			source:  "registry.terraform.io/terraform-aws-modules/vpc/aws",
			version: "5.1.0",
			want:    v1.Module{Key: "registry:terraform-aws-modules/vpc/aws@5.1.0", Kind: v1.ModuleRegistry, Source: "registry.terraform.io/terraform-aws-modules/vpc/aws", Ref: "5.1.0"},
			ok:      true,
		},
		{
			name:    "opentofu registry host is dropped",
			source:  "registry.opentofu.org/terraform-aws-modules/vpc/aws",
			version: "5.1.0",
			want:    v1.Module{Key: "registry:terraform-aws-modules/vpc/aws@5.1.0", Kind: v1.ModuleRegistry, Source: "registry.opentofu.org/terraform-aws-modules/vpc/aws", Ref: "5.1.0"},
			ok:      true,
		},
		{
			name:    "private registry keeps host",
			source:  "app.terraform.io/acme/vpc/aws",
			version: "1.0.0",
			want:    v1.Module{Key: "registry:app.terraform.io/acme/vpc/aws@1.0.0", Kind: v1.ModuleRegistry, Source: "app.terraform.io/acme/vpc/aws", Ref: "1.0.0"},
			ok:      true,
		},
		{
			name:    "private registry with port",
			source:  "localhost:8443/acme/vpc/aws",
			version: "1.0.0",
			want:    v1.Module{Key: "registry:localhost:8443/acme/vpc/aws@1.0.0", Kind: v1.ModuleRegistry, Source: "localhost:8443/acme/vpc/aws", Ref: "1.0.0"},
			ok:      true,
		},
		{name: "empty", source: "  "},
		{name: "s3 bucket", source: "s3::https://s3-eu-west-1.amazonaws.com/bucket/vpc.zip"},
		{name: "gcs bucket", source: "gcs::https://www.googleapis.com/storage/v1/modules/vpc.zip"},
		{name: "mercurial", source: "hg::http://example.com/vpc.hg"},
		{name: "http archive", source: "https://example.com/vpc-module.zip"},
		{name: "plain http archive", source: "http://example.com/vpc-module?archive=zip"},
		{name: "forced git with unknown scheme", source: "git::file:///tmp/repo"},
		{name: "forced git with unsupported scheme", source: "git::ftp://example.com/r.git"},
		{name: "unforced scp-like with another user", source: "deploy@github.com:o/r.git"},
		{name: "unforced shorthand on unknown host", source: "gitlab.com/o/r//sub?ref=v1"},
		{name: "github shorthand without repository", source: "github.com/o"},
		{name: "github url without repository", source: "git::https://github.com/o"},
		{name: "git url without host", source: "git::https:///o/r.git"},
		{name: "forced git with bare word", source: "git::repo"},
		{name: "absolute path", source: "/opt/modules/vpc"},
		{name: "registry with bad provider", source: "acme/vpc/AWS"},
		{
			name:   "registry shape on github host is a git shorthand",
			source: "github.com/acme/vpc/aws",
			want:   v1.Module{Key: "acme/vpc//aws", Kind: v1.ModuleGit, Source: "github.com/acme/vpc/aws"},
			ok:     true,
		},
		{name: "two segments", source: "acme/vpc"},
		{name: "unparsable url", source: "git::https://exa mple.com/%zz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseModuleSource(tt.source, tt.version)
			if ok != tt.ok {
				t.Fatalf("ParseModuleSource(%q, %q) ok = %v, want %v (got %+v)", tt.source, tt.version, ok, tt.ok, got)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ParseModuleSource(%q, %q) mismatch (-want +got):\n%s", tt.source, tt.version, diff)
			}
		})
	}
}

func TestParseModuleSourceOnGitHubEnterpriseServer(t *testing.T) {
	const host = "ghe.acme.com"
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "https on the instance", source: "git::https://ghe.acme.com/acme/modules.git//vpc?ref=v1.2.0", want: "acme/modules//vpc@v1.2.0"},
		{name: "ssh url on the instance", source: "git::ssh://git@ghe.acme.com/acme/modules.git//vpc?ref=v1.2.0", want: "acme/modules//vpc@v1.2.0"},
		{name: "scp-like on the instance", source: "git@GHE.acme.com:acme/modules.git//vpc?ref=v1.2.0", want: "acme/modules//vpc@v1.2.0"},
		{name: "github.com keeps its host", source: "git::https://github.com/acme/modules.git//dns?ref=v1.0.0", want: "github.com/acme/modules//dns@v1.0.0"},
		{name: "github.com shorthand keeps its host", source: "github.com/acme/modules//dns?ref=v1.0.0", want: "github.com/acme/modules//dns@v1.0.0"},
		{name: "another host keeps its host", source: "git::https://gitlab.com/o/r.git//sub?ref=v1", want: "gitlab.com/o/r//sub@v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseModuleSource(tt.source, "", host)
			if !ok {
				t.Fatalf("parseModuleSource(%q) ok = false", tt.source)
			}
			if got.Key != tt.want {
				t.Errorf("parseModuleSource(%q).Key = %q, want %q", tt.source, got.Key, tt.want)
			}
		})
	}
}
