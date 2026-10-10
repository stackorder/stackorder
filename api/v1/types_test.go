package v1_test

import (
	"bytes"
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
