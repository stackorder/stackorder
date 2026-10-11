package stateread_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/stateread"
)

const forms = "write owner/repo=bucket or owner/repo=bucket/prefix, or *=bucket[/prefix] for every repository"

func TestParseGrants(t *testing.T) {
	tests := []struct {
		in      string
		want    []stateread.Grant
		wantErr string
	}{
		{in: "", want: nil},
		{in: "   ", want: nil},
		{in: "acme/infra=acme-state", want: []stateread.Grant{{Repository: "acme/infra", Bucket: "acme-state"}}},
		{in: "acme/infra=acme-state/", want: []stateread.Grant{{Repository: "acme/infra", Bucket: "acme-state"}}},
		{in: "acme/infra=acme-state/stacks/prod/", want: []stateread.Grant{{Repository: "acme/infra", Bucket: "acme-state", Prefix: "stacks/prod/"}}},
		{in: "*=acme-state/shared/", want: []stateread.Grant{{Repository: "*", Bucket: "acme-state", Prefix: "shared/"}}},
		{in: "Acme/Infra.tf=acme-state", want: []stateread.Grant{{Repository: "Acme/Infra.tf", Bucket: "acme-state"}}},
		{in: " acme/infra = acme-state/stacks/prod , acme/network=acme.tfstate.eu/env:/blue/ ,*= acme-other", want: []stateread.Grant{
			{Repository: "acme/infra", Bucket: "acme-state", Prefix: "stacks/prod"},
			{Repository: "acme/network", Bucket: "acme.tfstate.eu", Prefix: "env:/blue/"},
			{Repository: "*", Bucket: "acme-other"},
		}},
		{in: "acme/a=acme-state/a, acme/b=acme-state/b", want: []stateread.Grant{
			{Repository: "acme/a", Bucket: "acme-state", Prefix: "a"},
			{Repository: "acme/b", Bucket: "acme-state", Prefix: "b"},
		}},
		{in: "acme/infra=acme-state/k=v/", want: []stateread.Grant{{Repository: "acme/infra", Bucket: "acme-state", Prefix: "k=v/"}}},
		{in: "acme/infra=acme-state/stacks/.terraform/", want: []stateread.Grant{{Repository: "acme/infra", Bucket: "acme-state", Prefix: "stacks/.terraform/"}}},
		{in: "acme-state", wantErr: `grant "acme-state" names no repository; ` + forms},
		{in: "acme-state/stacks/prod/", wantErr: `grant "acme-state/stacks/prod/" names no repository; ` + forms},
		{in: "acme/infra=acme-state,acme-shared", wantErr: `grant "acme-shared" names no repository`},
		{in: "=acme-state", wantErr: `repository "" is neither owner/repo nor *; ` + forms},
		{in: "acme=acme-state", wantErr: `repository "acme" is neither owner/repo nor *`},
		{in: "acme/*=acme-state", wantErr: `repository "acme/*" is neither owner/repo nor *`},
		{in: "*/*=acme-state", wantErr: `repository "*/*" is neither owner/repo nor *`},
		{in: "**=acme-state", wantErr: `repository "**" is neither owner/repo nor *`},
		{in: "acme/infra/x=acme-state", wantErr: `repository "acme/infra/x" is neither owner/repo nor *`},
		{in: "https://github.com/acme/infra=acme-state", wantErr: "is neither owner/repo nor *"},
		{in: "acme/infra=", wantErr: `grant "acme/infra=" has no bucket; ` + forms},
		{in: "acme/infra=/stacks/prod", wantErr: "has no bucket"},
		{in: "acme/infra=acme-state,,acme/network=acme-other", wantErr: `grant "" names no repository`},
		{in: "acme/infra=acme-state,", wantErr: `grant "" names no repository`},
		{in: "acme/infra=Acme-State", wantErr: `"Acme-State" is not a valid S3 bucket name`},
		{in: "acme/infra=ac", wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=" + strings.Repeat("a", 64), wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=acme..state", wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=-acme-state", wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=acme-state-", wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=acme_state", wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=acme state", wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=192.168.5.4", wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=xn--acme", wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=acme--x-s3", wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=acme-s3alias/prefix", wantErr: "not a valid S3 bucket name"},
		{in: "acme/infra=acme-state/stacks/../", wantErr: "a prefix with a . or .. segment covers no key the server reads"},
		{in: "acme/infra=acme-state/./", wantErr: "a prefix with a . or .. segment"},
		{in: "acme/infra=acme-state/stacks/.", wantErr: "a prefix with a . or .. segment"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := stateread.ParseGrants(tt.in)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(tt.want, got))
		})
	}
	_, err := stateread.ParseGrants("acme/infra=" + strings.Repeat("a", 63))
	require.NoError(t, err, "63 characters is the longest bucket name")
}

func TestGrantString(t *testing.T) {
	for _, in := range []string{"acme/infra=acme-state", "acme/infra=acme-state/stacks/prod/", "*=acme-state/env:/blue/"} {
		grants, err := stateread.ParseGrants(in)
		require.NoError(t, err)
		require.Len(t, grants, 1)
		assert.Equal(t, in, grants[0].String(), "String is what ParseGrants reads")
	}
}

func TestGrantAllows(t *testing.T) {
	const (
		repo = "acme/infra"
		key  = "stacks/prod/vpc/terraform.tfstate"
	)
	bucket := stateread.Grant{Repository: repo, Bucket: "acme-state"}
	prod := stateread.Grant{Repository: repo, Bucket: "acme-state", Prefix: "stacks/prod/"}
	prodNoSlash := stateread.Grant{Repository: repo, Bucket: "acme-state", Prefix: "stacks/prod"}
	at := func(key string) stateread.Location { return stateread.Location{Bucket: "acme-state", Key: key} }
	tests := []struct {
		name  string
		grant stateread.Grant
		repo  string
		loc   stateread.Location
		want  bool
	}{
		{"whole bucket", bucket, repo, at(key), true},
		{"region is not part of the grant", bucket, repo, stateread.Location{Bucket: "acme-state", Key: key, Region: "ap-southeast-2"}, true},
		{"another bucket", bucket, repo, stateread.Location{Bucket: "acme-other", Key: key}, false},
		{"bucket names are exact", bucket, repo, stateread.Location{Bucket: "acme-state-2", Key: key}, false},
		{"another repository", bucket, "acme/network", at(key), false},
		{"a repository whose name starts alike", bucket, "acme/infra-legacy", at(key), false},
		{"repository names ignore case", bucket, "ACME/Infra", at(key), true},
		{"no repository", bucket, "", at(key), false},
		{"a grant without a repository", stateread.Grant{Bucket: "acme-state"}, repo, at(key), false},
		{"the wildcard covers every repository", stateread.Grant{Repository: "*", Bucket: "acme-state"}, "globex/infra", at(key), true},
		{"the wildcard needs a repository", stateread.Grant{Repository: "*", Bucket: "acme-state"}, "", at(key), false},
		{"the wildcard keeps the bucket", stateread.Grant{Repository: "*", Bucket: "acme-state"}, repo, stateread.Location{Bucket: "acme-other", Key: key}, false},
		{"the wildcard keeps the prefix", stateread.Grant{Repository: "*", Bucket: "acme-state", Prefix: "stacks/shared/"}, repo, at(key), false},
		{"a wildcard in a repository is literal", stateread.Grant{Repository: "acme/*", Bucket: "acme-state"}, repo, at(key), false},
		{"under the prefix", prod, repo, at(key), true},
		{"outside the prefix", prod, repo, at("stacks/production/vpc/terraform.tfstate"), false},
		{"under a prefix without a trailing slash", prodNoSlash, repo, at(key), true},
		{"a sibling of a prefix without a trailing slash", prodNoSlash, repo, at("stacks/production/vpc/terraform.tfstate"), false},
		{"a sibling with a dash", prodNoSlash, repo, at("stacks/prod-legacy/vpc/terraform.tfstate"), false},
		{"the prefix itself", prodNoSlash, repo, at("stacks/prod"), true},
		{"a prefix ending in the middle of a name", stateread.Grant{Repository: repo, Bucket: "acme-state", Prefix: "stacks/pr"}, repo, at(key), false},
		{"prefix is case sensitive", prod, repo, at("Stacks/prod/vpc/terraform.tfstate"), false},
		{"key equal to the prefix", stateread.Grant{Repository: repo, Bucket: "acme-state", Prefix: "terraform.tfstate"}, repo, at("terraform.tfstate"), true},
		{"a longer name after a file prefix", stateread.Grant{Repository: repo, Bucket: "acme-state", Prefix: "terraform.tfstate"}, repo, at("terraform.tfstate.backup"), false},
		{"workspace key prefix", stateread.Grant{Repository: repo, Bucket: "acme-state", Prefix: "env:/blue/"}, repo, at("env:/blue/stacks/prod/apps"), true},
		{"workspace key prefix without a trailing slash", stateread.Grant{Repository: repo, Bucket: "acme-state", Prefix: "env:/blue"}, repo, at("env:/blue-green/stacks/prod/apps"), false},
		{"parent segment", prod, repo, at("stacks/prod/../dev/terraform.tfstate"), false},
		{"current segment", bucket, repo, at("./terraform.tfstate"), false},
		{"trailing parent segment", bucket, repo, at("stacks/.."), false},
		{"dots inside a name", bucket, repo, at("stacks/..prod/.terraform.tfstate"), true},
		{"empty key", bucket, repo, at(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.grant.Allows(tt.repo, tt.loc))
			assert.Equal(t, tt.want, stateread.Allowed([]stateread.Grant{tt.grant}, tt.repo, tt.loc))
		})
	}
}

func TestGrantOverlaps(t *testing.T) {
	at := func(repo, prefix string) stateread.Grant {
		return stateread.Grant{Repository: repo, Bucket: "acme-state", Prefix: prefix}
	}
	tests := []struct {
		name string
		a, b stateread.Grant
		want bool
	}{
		{"a prefix under another", at("acme/a", "stacks/"), at("acme/b", "stacks/b/"), true},
		{"a whole bucket covers every prefix", at("acme/a", ""), at("acme/b", "stacks/b/"), true},
		{"two whole buckets", at("acme/a", ""), at("acme/b", ""), true},
		{"the same prefix", at("acme/a", "stacks/"), at("acme/b", "stacks/"), true},
		{"a prefix and the same prefix with a slash", at("acme/a", "stacks/prod"), at("acme/b", "stacks/prod/"), true},
		{"a prefix without a slash covers its subtree", at("acme/a", "stacks/prod"), at("acme/b", "stacks/prod/vpc"), true},
		{"the wildcard over a repository's prefix", at("*", "stacks/"), at("acme/b", "stacks/b/"), true},
		{"the repository does not matter", at("acme/a", "stacks/"), at("acme/a", "stacks/a/"), true},
		{"sibling prefixes", at("acme/a", "stacks/a/"), at("acme/b", "stacks/b/"), false},
		{"a sibling that starts alike", at("acme/a", "stacks/prod"), at("acme/b", "stacks/production/"), false},
		{"a sibling with a trailing slash that starts alike", at("acme/a", "stacks/prod/"), at("acme/b", "stacks/production"), false},
		{"a prefix ending in the middle of a name", at("acme/a", "stacks/pr"), at("acme/b", "stacks/prod/"), false},
		{"another bucket", at("acme/a", ""), stateread.Grant{Repository: "acme/b", Bucket: "acme-other"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.a.Overlaps(tt.b))
			assert.Equal(t, tt.want, tt.b.Overlaps(tt.a), "symmetric")
		})
	}
}

func TestAllowed(t *testing.T) {
	grants, err := stateread.ParseGrants("acme/infra=acme-state/acme/infra/, acme/network=acme-state/acme/network/, *=acme-shared/outputs/")
	require.NoError(t, err)
	tests := []struct {
		name string
		repo string
		loc  stateread.Location
		want bool
	}{
		{"its own prefix", "acme/infra", stateread.Location{Bucket: "acme-state", Key: "acme/infra/vpc.tfstate"}, true},
		{"another repository's prefix", "acme/infra", stateread.Location{Bucket: "acme-state", Key: "acme/network/vpc.tfstate"}, false},
		{"the other repository's own prefix", "acme/network", stateread.Location{Bucket: "acme-state", Key: "acme/network/vpc.tfstate"}, true},
		{"a repository without a grant of its own", "globex/infra", stateread.Location{Bucket: "acme-state", Key: "acme/infra/vpc.tfstate"}, false},
		{"a wildcard grant", "globex/infra", stateread.Location{Bucket: "acme-shared", Key: "outputs/vpc.tfstate"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stateread.Allowed(grants, tt.repo, tt.loc))
		})
	}
	assert.False(t, stateread.Allowed(nil, "acme/infra", stateread.Location{Bucket: "acme-state", Key: "acme/infra/vpc.tfstate"}), "no grants allow nothing")
}
