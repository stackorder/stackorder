package v1_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestPlanArtifactNameIsDistinctPerStack(t *testing.T) {
	const sha = "4f1c0de2b9a8e7d6c5b4a3928170f6e5d4c3b2a1"
	keys := []string{"stacks/a-b", "stacks/a/b", "stacks/a:b", "stacks-a/b", "stacks/a-b:c", "stacks/a/b:c", "stacks/a/b/c"}
	seen := map[string]string{}
	for _, key := range keys {
		name := v1.PlanArtifactName(key, sha)
		if other, dup := seen[name]; dup {
			t.Errorf("%s and %s share the artifact name %s", other, key, name)
		}
		seen[name] = key
		assert.True(t, strings.HasPrefix(name, "stackorder-plan-"), name)
		assert.True(t, strings.HasSuffix(name, "-"+sha), name)
		assert.NotContains(t, name, "/")
		assert.NotContains(t, name, ":")
		assert.Equal(t, name, v1.PlanArtifactName(key, sha), "stable")
	}
}

var compactJSON = cmp.Transformer("compactJSON", func(m json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, m); err != nil {
		return string(m)
	}
	return b.String()
})

func TestStackOutputsWireFormat(t *testing.T) {
	tests := []struct {
		name    string
		golden  string
		outputs v1.StackOutputs
	}{
		{
			name:   "fully populated",
			golden: "stack_outputs.json",
			outputs: v1.StackOutputs{
				Repo:             "acme/infra",
				Stack:            "stacks/prod/vpc",
				Serial:           42,
				Lineage:          "8d3f6c1e-2b7a-4e9d-9c51-0f4a6b2d7e13",
				TerraformVersion: "1.9.8",
				Outputs: map[string]v1.StackOutput{
					"vpc_id": {
						Value: json.RawMessage(`"vpc-0a1b2c3d4e5f67890"`),
						Type:  json.RawMessage(`"string"`),
					},
					"nat_gateway_count": {
						Value: json.RawMessage(`3`),
						Type:  json.RawMessage(`"number"`),
					},
					"private_subnet_ids": {
						Value: json.RawMessage(`["subnet-0a1b2c3d4e5f6a7b8","subnet-0b2c3d4e5f6a7b8c9"]`),
						Type:  json.RawMessage(`["list","string"]`),
					},
					"tags": {
						Value: json.RawMessage(`{"environment":"prod","team":"platform"}`),
						Type:  json.RawMessage(`["map","string"]`),
					},
					"endpoints": {
						Value: json.RawMessage(`{"dynamodb":"vpce-0d4e5f6a7b8c9d0e1","s3":"vpce-0a1b2c3d4e5f6a7b8"}`),
						Type:  json.RawMessage(`["object",{"dynamodb":"string","s3":"string"}]`),
					},
				},
				Withheld: []string{"vpn_preshared_key"},
			},
		},
		{
			name:   "no outputs",
			golden: "stack_outputs_empty.json",
			outputs: v1.StackOutputs{
				Repo:             "acme/infra",
				Stack:            "stacks/prod/dns",
				Serial:           3,
				Lineage:          "5b2e9a74-1c3d-4f60-8a7e-2d9c0b1f6e48",
				TerraformVersion: "1.9.8",
				Outputs:          map[string]v1.StackOutput{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			golden, err := os.ReadFile(filepath.Join("testdata", tt.golden))
			require.NoError(t, err)

			got, err := json.MarshalIndent(tt.outputs, "", "  ")
			require.NoError(t, err)
			if diff := cmp.Diff(string(golden), string(got)+"\n"); diff != "" {
				t.Errorf("wire format differs from testdata/%s (-want +got):\n%s", tt.golden, diff)
			}

			var decoded v1.StackOutputs
			require.NoError(t, json.Unmarshal(golden, &decoded))
			if diff := cmp.Diff(tt.outputs, decoded, compactJSON); diff != "" {
				t.Errorf("round trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStackResultStateIsOptional(t *testing.T) {
	tests := []struct {
		name   string
		result v1.StackResult
		want   string
	}{
		{
			name:   "without state",
			result: v1.StackResult{Mode: v1.ModePlan, Status: v1.ResultSuccess, HasChanges: true},
			want:   `{"mode":"plan","status":"success","exit_code":0,"has_changes":true}`,
		},
		{
			name: "with state",
			result: v1.StackResult{
				Mode:   v1.ModeApply,
				Status: v1.ResultSuccess,
				State: &v1.StateObject{
					Bucket: "acme-terraform-state",
					Key:    "env:/blue/stacks/prod/vpc/terraform.tfstate",
					Region: "eu-west-1",
				},
			},
			want: `{"mode":"apply","status":"success","exit_code":0,"has_changes":false,` +
				`"state":{"bucket":"acme-terraform-state","key":"env:/blue/stacks/prod/vpc/terraform.tfstate","region":"eu-west-1"}}`,
		},
		{
			name: "with an empty resource list",
			result: v1.StackResult{
				Mode:      v1.ModeDrift,
				Status:    v1.ResultSuccess,
				Resources: &v1.ResourceList{Items: []v1.StackResource{}},
			},
			want: `{"mode":"drift","status":"success","exit_code":0,"has_changes":false,` +
				`"resources":{"total":0,"items":[]}}`,
		},
		{
			name: "with a truncated resource list",
			result: v1.StackResult{
				Mode:   v1.ModeApply,
				Status: v1.ResultFailure,
				Resources: &v1.ResourceList{
					Total:     2,
					Truncated: true,
					Items: []v1.StackResource{{
						Address:  `module.vpc.aws_subnet.private["eu-west-1a"]`,
						Type:     "aws_subnet",
						Module:   "module.vpc",
						Provider: "registry.terraform.io/hashicorp/aws",
						Tainted:  true,
					}},
				},
			},
			want: `{"mode":"apply","status":"failure","exit_code":0,"has_changes":false,` +
				`"resources":{"total":2,"truncated":true,"items":[{"address":"module.vpc.aws_subnet.private[\"eu-west-1a\"]",` +
				`"type":"aws_subnet","module":"module.vpc","provider":"registry.terraform.io/hashicorp/aws","tainted":true}]}}`,
		},
		{
			name: "with state in the environment's region",
			result: v1.StackResult{
				Mode:   v1.ModeDrift,
				Status: v1.ResultSuccess,
				State:  &v1.StateObject{Bucket: "acme-terraform-state", Key: "stacks/prod/vpc/terraform.tfstate"},
			},
			want: `{"mode":"drift","status":"success","exit_code":0,"has_changes":false,` +
				`"state":{"bucket":"acme-terraform-state","key":"stacks/prod/vpc/terraform.tfstate"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.result)
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))

			var decoded v1.StackResult
			require.NoError(t, json.Unmarshal(got, &decoded))
			if diff := cmp.Diff(tt.result, decoded); diff != "" {
				t.Errorf("round trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStackResourcesWireFormat(t *testing.T) {
	reported := time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC)
	changed := time.Date(2026, 10, 28, 9, 41, 0, 0, time.UTC)
	finished := time.Date(2026, 10, 28, 9, 40, 12, 0, time.UTC)
	tests := []struct {
		name      string
		golden    string
		resources v1.StackResources
	}{
		{
			name:   "reported by an apply",
			golden: "stack_resources.json",
			resources: v1.StackResources{
				RunID:      "9e1a3c5e-4b6d-4f2a-8c7e-1d2f3a4b5c6d",
				Mode:       v1.ModeApply,
				SHA:        "4f9c2b7e8a1d3c5b6e7f8a9b0c1d2e3f4a5b6c7d",
				ReportedAt: &reported,
				ChangedAt:  &changed,
				Total:      3,
				Items: []v1.StackResource{
					{
						Address:  "aws_iam_role.nodes",
						Type:     "aws_iam_role",
						Provider: "registry.terraform.io/hashicorp/aws",
					},
					{
						Address:  `module.eks.aws_eks_addon.this["coredns"]`,
						Type:     "aws_eks_addon",
						Module:   "module.eks",
						Provider: "registry.terraform.io/hashicorp/aws",
						LastChange: &v1.ResourceChange{
							Action:     v1.ResourceCreate,
							RunID:      "9e1a3c5e-4b6d-4f2a-8c7e-1d2f3a4b5c6d",
							SHA:        "4f9c2b7e8a1d3c5b6e7f8a9b0c1d2e3f4a5b6c7d",
							PRNumber:   42,
							FinishedAt: &finished,
						},
					},
					{
						Address:  "module.eks.aws_launch_template.nodes",
						Type:     "aws_launch_template",
						Module:   "module.eks",
						Provider: "registry.terraform.io/hashicorp/aws",
						Tainted:  true,
						Deposed:  true,
					},
				},
			},
		},
		{
			name:      "never reported",
			golden:    "stack_resources_empty.json",
			resources: v1.StackResources{Items: []v1.StackResource{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			golden, err := os.ReadFile(filepath.Join("testdata", tt.golden))
			require.NoError(t, err)

			got, err := json.MarshalIndent(tt.resources, "", "  ")
			require.NoError(t, err)
			if diff := cmp.Diff(string(golden), string(got)+"\n"); diff != "" {
				t.Errorf("wire format differs from testdata/%s (-want +got):\n%s", tt.golden, diff)
			}

			var decoded v1.StackResources
			require.NoError(t, json.Unmarshal(golden, &decoded))
			if diff := cmp.Diff(tt.resources, decoded); diff != "" {
				t.Errorf("round trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStackResourceCarriesNoValues(t *testing.T) {
	var fields []string
	typ := reflect.TypeFor[v1.StackResource]()
	for i := range typ.NumField() {
		fields = append(fields, typ.Field(i).Name)
	}
	assert.Equal(t, []string{"Address", "Type", "Module", "Provider", "Tainted", "Deposed", "LastChange"}, fields,
		"a resource is metadata only; a new field must not carry anything read from an attribute or output")
}
