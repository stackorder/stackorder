package tf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const (
	awsProvider  = "registry.terraform.io/hashicorp/aws"
	helmProvider = "registry.terraform.io/hashicorp/helm"
)

func TestResources(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "state.json"))
	require.NoError(t, err)
	s, err := ParseStateJSON(data)
	require.NoError(t, err)

	got := Resources(s.Values.RootModule)
	want := []v1.StackResource{
		{Address: "aws_db_instance.main", Type: "aws_db_instance", Provider: awsProvider, Deposed: true},
		{Address: "aws_instance.web[0]", Type: "aws_instance", Provider: awsProvider, Tainted: true},
		{Address: "aws_instance.web[1]", Type: "aws_instance", Provider: awsProvider},
		{Address: `aws_s3_object.cfg["y.z/w"]`, Type: "aws_s3_object", Provider: awsProvider},
		{Address: "module.eks.aws_eks_cluster.this", Type: "aws_eks_cluster", Module: "module.eks", Provider: awsProvider},
		{Address: `module.eks.module.addons["a.b/c"].helm_release.this`, Type: "helm_release", Module: `module.eks.module.addons["a.b/c"]`, Provider: helmProvider},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Resources (-want +got):\n%s", diff)
	}

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	for _, value := range []string{"sentinel-7f3a", "hunter2hunter2", "123456789012", "acme", "depends_on", "user_data", "00000001", "data."} {
		assert.NotContains(t, string(encoded), value)
	}
}

func TestResourcesOfAnEmptyState(t *testing.T) {
	tests := []struct {
		name  string
		state string
	}{
		{name: "format version only", state: `{"format_version":"1.0"}`},
		{name: "no root module", state: `{"format_version":"1.0","values":{"outputs":{}}}`},
		{name: "no resources", state: `{"format_version":"1.0","values":{"root_module":{}}}`},
		{name: "data sources only", state: `{"format_version":"1.0","values":{"root_module":{"resources":[{"address":"data.x.y","mode":"data","type":"x"}]}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseStateJSON([]byte(tt.state))
			require.NoError(t, err)
			var got []v1.StackResource
			if s.Values != nil {
				got = Resources(s.Values.RootModule)
			} else {
				got = Resources(nil)
			}
			require.NotNil(t, got)
			assert.Empty(t, got)
		})
	}
}

func TestParseStateJSONErrors(t *testing.T) {
	const secret = "parse-sentinel-7f3a"
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "not JSON", input: secret, want: "not valid JSON"},
		{name: "partial JSON", input: `{"format_version":"1.0","values":{"root_module":{"resources":[{"values":{"password":"` + secret, want: "not valid JSON"},
		{name: "wrong type", input: `{"format_version":"1.0","values":{"root_module":{"address":7357357357}}}`, want: "unexpected value"},
		{name: "no format version", input: `{"values":{"outputs":{"x":{"value":"` + secret + `"}}}}`, want: "format version"},
		{name: "unsupported format version", input: `{"format_version":"9.0"}`, want: "format version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseStateJSON([]byte(tt.input))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), secret)
			assert.NotContains(t, err.Error(), "7357357357")
		})
	}
}

func TestRedactResources(t *testing.T) {
	r := NewRedactor([]string{"hunter2hunter2", "s3cr3t-value"})
	items := []v1.StackResource{
		{Address: `aws_ssm_parameter.p["hunter2hunter2"]`, Type: "aws_ssm_parameter", Provider: awsProvider, Tainted: true},
		{Address: `aws_ssm_parameter.p["s3cr3t-value"]`, Type: "aws_ssm_parameter_other", Provider: "registry.terraform.io/acme/other", Deposed: true},
		{Address: `module.m["s3cr3t-value"].aws_vpc.this`, Type: "aws_vpc", Module: `module.m["s3cr3t-value"]`, Provider: awsProvider},
		{Address: "aws_iam_role.app", Type: "aws_iam_role", Provider: "registry.terraform.io/hashicorp/hunter2hunter2"},
	}
	got := RedactResources(items, r)
	want := []v1.StackResource{
		{Address: "aws_iam_role.app", Type: "aws_iam_role", Provider: "registry.terraform.io/hashicorp/***"},
		{Address: `aws_ssm_parameter.p["***"]`, Type: "aws_ssm_parameter", Provider: awsProvider, Tainted: true, Deposed: true},
		{Address: `module.m["***"].aws_vpc.this`, Type: "aws_vpc", Module: `module.m["***"]`, Provider: awsProvider},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("RedactResources (-want +got):\n%s", diff)
	}
	assert.Empty(t, RedactResources(nil, r))
}

func sampleResources(n int) []v1.StackResource {
	out := make([]v1.StackResource, 0, n)
	for i := range n {
		out = append(out, v1.StackResource{Address: "aws_instance.web[" + strings.Repeat("9", i+1) + "]", Type: "aws_instance", Provider: awsProvider})
	}
	return out
}

func encodedLen(t *testing.T, v any) int {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return len(data)
}

func TestFitResources(t *testing.T) {
	items := sampleResources(5)
	full := &v1.ResourceList{Total: 5, Items: items}
	fitsThree := encodedLen(t, v1.ResourceList{Total: 5, Truncated: true, Items: items[:3]})
	tests := []struct {
		name          string
		list          *v1.ResourceList
		budget        int
		wantItems     int
		wantTruncated bool
	}{
		{name: "everything fits", list: full, budget: 1 << 20, wantItems: 5},
		{name: "exact fit", list: full, budget: encodedLen(t, full), wantItems: 5},
		{name: "cut to a prefix", list: full, budget: fitsThree, wantItems: 3, wantTruncated: true},
		{name: "one byte short of a prefix", list: full, budget: fitsThree - 1, wantItems: 2, wantTruncated: true},
		{name: "envelope only", list: full, budget: 40, wantItems: 0, wantTruncated: true},
		{name: "zero budget", list: full, budget: 0, wantItems: 0, wantTruncated: true},
		{name: "negative budget", list: full, budget: -5000, wantItems: 0, wantTruncated: true},
		{name: "empty list with no budget", list: &v1.ResourceList{Items: []v1.StackResource{}}, budget: 0},
		{name: "already truncated", list: &v1.ResourceList{Total: 9, Truncated: true, Items: items}, budget: 1 << 20, wantItems: 5, wantTruncated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FitResources(tt.list, tt.budget)
			require.NotNil(t, got)
			require.NotNil(t, got.Items)
			assert.Equal(t, tt.list.Total, got.Total)
			assert.Equal(t, tt.wantTruncated, got.Truncated)
			require.Len(t, got.Items, tt.wantItems)
			assert.Equal(t, tt.list.Items[:tt.wantItems], got.Items)
			if tt.budget > 0 && tt.wantItems > 0 {
				assert.LessOrEqual(t, encodedLen(t, got), tt.budget)
			}
		})
	}
	assert.Nil(t, FitResources(nil, 100))
	assert.Len(t, full.Items, 5, "the input list must not change")
}
