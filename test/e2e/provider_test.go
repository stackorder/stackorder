//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const (
	providerSigningKey = "AD4D71AAF02E647A"
	vpcOutputsData     = "data.stackorder_outputs.vpc"
)

var providerRegistry = map[v1.Tool]string{
	v1.ToolTerraform: "registry.terraform.io",
	v1.ToolTofu:      "registry.opentofu.org",
}

var providerSignature = map[v1.Tool]string{
	v1.ToolTerraform: "self-signed, key ID " + providerSigningKey,
	v1.ToolTofu:      "signed, key ID " + providerSigningKey,
}

var (
	installedProvider = regexp.MustCompile(`(?m)^- Installed (\S+) v(\S+) \(([^)]*)\)$`)
	lockedProvider    = regexp.MustCompile(`(?m)^provider "([^"]+)" \{\s+version\s+= "([^"]+)"`)
	providerVersion   = regexp.MustCompile(`^0\.1\.\d+$`)
)

type planJSON struct {
	ResourceChanges []struct {
		Address string `json:"address"`
		Change  struct {
			Actions []string       `json:"actions"`
			After   map[string]any `json:"after"`
		} `json:"change"`
	} `json:"resource_changes"`
	PriorState struct {
		Values struct {
			RootModule struct {
				Resources []struct {
					Address string         `json:"address"`
					Values  map[string]any `json:"values"`
				} `json:"resources"`
			} `json:"root_module"`
		} `json:"values"`
	} `json:"prior_state"`
	Configuration struct {
		RootModule struct {
			ModuleCalls map[string]struct {
				Expressions map[string]struct {
					References []string `json:"references"`
				} `json:"expressions"`
			} `json:"module_calls"`
		} `json:"root_module"`
	} `json:"configuration"`
}

func (p planJSON) read(address string) (map[string]any, bool) {
	for _, r := range p.PriorState.Values.RootModule.Resources {
		if r.Address == address {
			return r.Values, true
		}
	}
	return nil, false
}

func (p planJSON) planned(address string) ([]string, map[string]any, bool) {
	for _, r := range p.ResourceChanges {
		if r.Address == address {
			input, _ := r.Change.After["input"].(map[string]any)
			return r.Change.Actions, input, true
		}
	}
	return nil, nil, false
}

func (p planJSON) references(module, argument string) []string {
	return p.Configuration.RootModule.ModuleCalls[module].Expressions[argument].References
}

func (s *story) eksPlannedFromOutputs(t *testing.T, pr *pullRequest) {
	ws, ok := pr.workspaces[prodEKS]
	require.True(t, ok, "#%d planned %s", pr.number, prodEKS)
	job := pr.plans[prodEKS]

	installs := installedProvider.FindAllStringSubmatch(job.stdout, -1)
	if assert.Len(t, installs, 1, "the plan job of %s installed one provider: %q", prodEKS, installs) {
		assert.Equal(t, "stackorder/stackorder", installs[0][1], "the provider comes from the tool's default registry")
		assert.Regexp(t, providerVersion, installs[0][2])
		assert.Equal(t, providerSignature[s.tool], installs[0][3], "%s checked the provider's signature", s.tool)
	}
	lock, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(prodEKS), ".terraform.lock.hcl"))
	require.NoError(t, err, "init of %s wrote a dependency lock file", prodEKS)
	locked := lockedProvider.FindAllStringSubmatch(string(lock), -1)
	if assert.Len(t, locked, 1, "the lock file of %s records one provider: %q", prodEKS, locked) {
		assert.Equal(t, providerRegistry[s.tool]+"/stackorder/stackorder", locked[0][1], "%s installed stackorder/stackorder from %s", s.tool, providerRegistry[s.tool])
		assert.Regexp(t, providerVersion, locked[0][2])
	}

	shared, _ := withholdSensitive(s.vpcOutputs)
	values := map[string]any{}
	for name, o := range shared {
		values[name] = o.Value
	}
	require.Contains(t, values, "vpc_id")
	require.Contains(t, values, "subnet_ids")

	var plan planJSON
	s.toolJSON(t, ws, prodEKS, &plan, "show", "-json", "-no-color", job.outputs["plan-file"])
	read, ok := plan.read(vpcOutputsData)
	require.True(t, ok, "the plan of %s read %s", prodEKS, vpcOutputsData)
	assert.Equal(t, prodVPC, read["stack"])
	assert.Equal(t, v1.QualifiedStackKey(repoName, prodVPC), read["id"])
	assert.Equal(t, values, read["nonsensitive_values"], "%s holds the outputs %s output -json shows for %s", vpcOutputsData, s.tool, prodVPC)

	actions, cluster, ok := plan.planned("module.eks.terraform_data.cluster")
	require.True(t, ok, "the plan of %s covers the cluster", prodEKS)
	assert.Equal(t, []string{"no-op"}, actions, "the cluster the bootstrap applied matches the outputs read again")
	assert.Equal(t, values["vpc_id"], cluster["vpc_id"])
	assert.Equal(t, values["subnet_ids"], cluster["subnet_ids"])
	_, nodes, ok := plan.planned("module.eks.terraform_data.node_group")
	require.True(t, ok, "the plan of %s covers the node group", prodEKS)
	assert.Equal(t, values["subnet_ids"], nodes["subnet_ids"])
	assert.Contains(t, plan.references("eks", "vpc_id"), vpcOutputsData+".nonsensitive_values.vpc_id",
		"the cluster's vpc_id comes from %s, not from a literal", vpcOutputsData)
	assert.Contains(t, plan.references("eks", "subnet_ids"), vpcOutputsData+".nonsensitive_values.subnet_ids",
		"the cluster's subnet_ids come from %s, not from a literal", vpcOutputsData)

	assert.Equal(t, []outputsRow{{s.rn.planClaims(pr).Actor, actionRead, prodVPC, prodEKS, pr.runID, ""}}, s.outputsRows(t, pr.runID),
		"the plan job of %s read the outputs of %s once, with its own OIDC token rather than the API key, for the plan run of #%d", prodEKS, prodVPC, pr.number)

	var view v1.GraphView
	s.cp.getJSON(t, "/v1/repos/"+repoName+"/graph?run="+pr.runID, &view)
	var edges []v1.Edge
	for _, e := range view.Graph.Edges {
		if e.From == v1.StackRef(prodEKS) && e.To == v1.StackRef(prodVPC) {
			edges = append(edges, e)
		}
	}
	assert.Equal(t, []v1.Edge{{
		From: v1.StackRef(prodEKS), To: v1.StackRef(prodVPC), Type: v1.EdgeReadsState, Inferred: true,
		Meta: map[string]string{"source": "stackorder_outputs"},
	}}, edges, "the scanner inferred the edge from %s to %s from the stackorder_outputs block", prodEKS, prodVPC)
}
