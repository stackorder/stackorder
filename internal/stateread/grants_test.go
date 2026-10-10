package stateread_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/stateread"
)

func TestParseGrants(t *testing.T) {
	tests := []struct {
		in      string
		want    []stateread.Grant
		wantErr string
	}{
		{in: "", want: nil},
		{in: "   ", want: nil},
		{in: "acme-state", want: []stateread.Grant{{Bucket: "acme-state"}}},
		{in: "acme-state/", want: []stateread.Grant{{Bucket: "acme-state"}}},
		{in: "acme-state/stacks/prod/", want: []stateread.Grant{{Bucket: "acme-state", Prefix: "stacks/prod/"}}},
		{in: " acme-state/stacks/prod , acme.tfstate.eu/env:/blue/ ,acme-other", want: []stateread.Grant{
			{Bucket: "acme-state", Prefix: "stacks/prod"},
			{Bucket: "acme.tfstate.eu", Prefix: "env:/blue/"},
			{Bucket: "acme-other"},
		}},
		{in: "acme-state/a, acme-state/b", want: []stateread.Grant{{Bucket: "acme-state", Prefix: "a"}, {Bucket: "acme-state", Prefix: "b"}}},
		{in: "/stacks/prod", wantErr: "has no bucket"},
		{in: "acme-state,,acme-other", wantErr: "has no bucket"},
		{in: "acme-state,", wantErr: "has no bucket"},
		{in: "Acme-State", wantErr: "not a valid S3 bucket name"},
		{in: "ac", wantErr: "not a valid S3 bucket name"},
		{in: strings.Repeat("a", 64), wantErr: "not a valid S3 bucket name"},
		{in: "acme..state", wantErr: "not a valid S3 bucket name"},
		{in: "-acme-state", wantErr: "not a valid S3 bucket name"},
		{in: "acme-state-", wantErr: "not a valid S3 bucket name"},
		{in: "acme_state", wantErr: "not a valid S3 bucket name"},
		{in: "acme state", wantErr: "not a valid S3 bucket name"},
		{in: "192.168.5.4", wantErr: "not a valid S3 bucket name"},
		{in: "xn--acme", wantErr: "not a valid S3 bucket name"},
		{in: "acme--x-s3", wantErr: "not a valid S3 bucket name"},
		{in: "acme-s3alias/prefix", wantErr: "not a valid S3 bucket name"},
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
	_, err := stateread.ParseGrants(strings.Repeat("a", 63))
	require.NoError(t, err, "63 characters is the longest bucket name")
}

func TestGrantAllows(t *testing.T) {
	const key = "stacks/prod/vpc/terraform.tfstate"
	tests := []struct {
		name  string
		grant stateread.Grant
		loc   stateread.Location
		want  bool
	}{
		{"whole bucket", stateread.Grant{Bucket: "acme-state"}, stateread.Location{Bucket: "acme-state", Key: key}, true},
		{"region is not part of the grant", stateread.Grant{Bucket: "acme-state"}, stateread.Location{Bucket: "acme-state", Key: key, Region: "ap-southeast-2"}, true},
		{"another bucket", stateread.Grant{Bucket: "acme-state"}, stateread.Location{Bucket: "acme-other", Key: key}, false},
		{"bucket names are exact", stateread.Grant{Bucket: "acme-state"}, stateread.Location{Bucket: "acme-state-2", Key: key}, false},
		{"under the prefix", stateread.Grant{Bucket: "acme-state", Prefix: "stacks/prod/"}, stateread.Location{Bucket: "acme-state", Key: key}, true},
		{"outside the prefix", stateread.Grant{Bucket: "acme-state", Prefix: "stacks/prod/"}, stateread.Location{Bucket: "acme-state", Key: "stacks/production/vpc/terraform.tfstate"}, false},
		{"plain string prefix", stateread.Grant{Bucket: "acme-state", Prefix: "stacks/prod"}, stateread.Location{Bucket: "acme-state", Key: "stacks/production/vpc/terraform.tfstate"}, true},
		{"prefix is case sensitive", stateread.Grant{Bucket: "acme-state", Prefix: "stacks/prod/"}, stateread.Location{Bucket: "acme-state", Key: "Stacks/prod/vpc/terraform.tfstate"}, false},
		{"key equal to the prefix", stateread.Grant{Bucket: "acme-state", Prefix: "terraform.tfstate"}, stateread.Location{Bucket: "acme-state", Key: "terraform.tfstate"}, true},
		{"workspace key prefix", stateread.Grant{Bucket: "acme-state", Prefix: "env:/blue/"}, stateread.Location{Bucket: "acme-state", Key: "env:/blue/stacks/prod/apps"}, true},
		{"parent segment", stateread.Grant{Bucket: "acme-state", Prefix: "stacks/prod/"}, stateread.Location{Bucket: "acme-state", Key: "stacks/prod/../dev/terraform.tfstate"}, false},
		{"current segment", stateread.Grant{Bucket: "acme-state"}, stateread.Location{Bucket: "acme-state", Key: "./terraform.tfstate"}, false},
		{"trailing parent segment", stateread.Grant{Bucket: "acme-state"}, stateread.Location{Bucket: "acme-state", Key: "stacks/.."}, false},
		{"dots inside a name", stateread.Grant{Bucket: "acme-state"}, stateread.Location{Bucket: "acme-state", Key: "stacks/..prod/.terraform.tfstate"}, true},
		{"empty key", stateread.Grant{Bucket: "acme-state"}, stateread.Location{Bucket: "acme-state"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.grant.Allows(tt.loc))
		})
	}
}
