//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/tf"
)

const (
	vpcStackFile    = "stacks/prod/vpc/.stackorder.yaml"
	actionRead      = "outputs_read"
	actionRefused   = "outputs_refused"
	sharedReadersPR = 9
)

var (
	outputsFields = []string{"lineage", "outputs", "repo", "serial", "stack", "terraform_version", "withheld"}
	outputFields  = []string{"sensitive", "type", "value"}
	vpcStateOnly  = []string{"terraform_data", "10.10.0.0/16"}
)

type toolOutput struct {
	Sensitive bool `json:"sensitive"`
	Type      any  `json:"type"`
	Value     any  `json:"value"`
}

func readersYAML(readers ...string) string {
	return "outputs:\n  readers:\n    - " + strings.Join(readers, "\n    - ") + "\n"
}

func (s *story) toolOutputs(t *testing.T, workspace, key string) map[string]toolOutput {
	t.Helper()
	bin, err := tf.Detect(s.tool)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "output", "-json", "-no-color")
	cmd.Dir = filepath.Join(workspace, filepath.FromSlash(key))
	cmd.Env = jobEnviron()
	for k, v := range s.rn.baseEnv() {
		if strings.HasPrefix(k, "AWS_") {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "%s output -json in %s: %s", s.tool, key, stderr.String())
	out := map[string]toolOutput{}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &out), "decode %s output -json of %s", s.tool, key)
	return out
}

func withholdSensitive(all map[string]toolOutput) (map[string]toolOutput, []string) {
	shared := map[string]toolOutput{}
	var withheld []string
	for _, name := range slices.Sorted(maps.Keys(all)) {
		if all[name].Sensitive {
			withheld = append(withheld, name)
			continue
		}
		shared[name] = all[name]
	}
	return shared, withheld
}

func (s *story) fetchOutputs(t *testing.T, key, token string, query url.Values) response {
	t.Helper()
	path := "/v1/repos/" + repoName + "/stacks/" + url.PathEscape(key) + "/outputs"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return s.cp.send(t, http.MethodGet, path, nil, token)
}

func servedOutputs(t *testing.T, res response) (v1.StackOutputs, map[string]toolOutput) {
	t.Helper()
	require.Equal(t, http.StatusOK, res.status, "GET outputs: %s", res.body)
	assert.Equal(t, "no-store", res.header.Get("Cache-Control"), "nothing caches the outputs")
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(res.body, &fields))
	assert.Subset(t, outputsFields, slices.Collect(maps.Keys(fields)), "the body holds the outputs and the identity of the state, nothing else")
	var wire map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fields["outputs"], &wire))
	for name, o := range wire {
		assert.Subset(t, outputFields, slices.Collect(maps.Keys(o)), "output %s holds its value, type and sensitive flag only", name)
	}
	var out v1.StackOutputs
	require.NoError(t, json.Unmarshal(res.body, &out))
	values := make(map[string]toolOutput, len(out.Outputs))
	for name, o := range out.Outputs {
		v := toolOutput{Sensitive: o.Sensitive}
		require.NoError(t, json.Unmarshal(o.Value, &v.Value), "value of %s", name)
		require.NoError(t, json.Unmarshal(o.Type, &v.Type), "type of %s", name)
		values[name] = v
	}
	return out, values
}

func refusal(t *testing.T, res response, status int) v1.Error {
	t.Helper()
	require.Equal(t, status, res.status, "GET outputs: %s", res.body)
	var e v1.Error
	require.NoError(t, json.Unmarshal(res.body, &e))
	return e
}

func (s *story) sharedOutputs(t *testing.T) {
	for _, key := range bootstrapOrder {
		stack, err := s.cp.store.GetStackByKey(t.Context(), repoID, key)
		require.NoError(t, err)
		assert.Equal(t, &v1.StateObject{Bucket: stateBucket, Key: stateKeys[key], Region: awsRegion}, stack.StateObject,
			"the apply --local result of %s told the server where its state is", key)
	}
	want, withheld := withholdSensitive(s.vpcOutputs)
	require.Contains(t, want, "vpc_id", "%s output -json shows the VPC id", s.tool)

	state := s.ls.requireState(t, stateKeys[prodVPC])
	raw, _ := s.ls.get(t, stateBucket, stateKeys[prodVPC])
	res := s.fetchOutputs(t, prodVPC, s.cp.apiKey, nil)
	served, values := servedOutputs(t, res)
	for _, canary := range vpcStateOnly {
		require.Contains(t, string(raw), canary, "the state of %s holds %s outside its outputs", prodVPC, canary)
		assert.NotContains(t, string(res.body), canary, "the server serves the outputs of the state, not its resources")
	}
	assert.Equal(t, repoName, served.Repo)
	assert.Equal(t, prodVPC, served.Stack)
	assert.Positive(t, served.Serial)
	assert.EqualValues(t, s.serials[prodVPC], served.Serial, "the server read the state the bootstrap wrote")
	assert.Equal(t, state.Lineage, served.Lineage)
	assert.Equal(t, state.TerraformVersion, served.TerraformVersion)
	assert.Equal(t, want, values, "the server serves the names, values, types and sensitive flags %s output -json shows", s.tool)
	assert.Equal(t, withheld, served.Withheld, "sensitive outputs are withheld by name")

	unknown := refusal(t, s.fetchOutputs(t, kycProduction, s.cp.apiKey, nil), http.StatusConflict)
	assert.Equal(t, v1.ErrCodeStateLocationUnknown, unknown.Code, "%s has had no apply or drift job yet", kycProduction)

	pr := s.runnerReads(t, want)
	s.outputsAudit(t, pr, want, withheld)
}

func (s *story) runnerReads(t *testing.T, want map[string]toolOutput) *pullRequest {
	t.Helper()
	policy := s.repo.commit(t, "main", s.main, "feat(vpc): share the VPC outputs with the EKS stack",
		writeFile(vpcStackFile, readersYAML(prodEKS)))
	s.pushMain(t, policy, []string{vpcStackFile})

	head := s.repo.commit(t, "vpc-readers", s.main, "feat(vpc): share the VPC outputs with the apps stack too",
		writeFile(vpcStackFile, readersYAML(prodEKS, prodApps)))
	pr := s.rn.openPull(t, sharedReadersPR, "vpc-readers", s.main, head, "feat(vpc): share the VPC outputs with the apps stack too")
	s.pulls[pr.number] = pr
	s.rn.resolve(t, pr)
	require.ElementsMatch(t, []string{prodVPC, prodEKS, prodApps}, pr.affected,
		"a change to the VPC stack's configuration affects it and the stacks that read it")

	read := func(reader string) response {
		token := s.cp.runnerToken(s.rn.planClaims(pr))
		return s.fetchOutputs(t, prodVPC, token, url.Values{"run": {pr.runID}, "reader": {reader}})
	}
	served, values := servedOutputs(t, read(prodEKS))
	assert.Equal(t, prodVPC, served.Stack)
	assert.Equal(t, want, values, "the plan job of %s reads what the default branch shares with it", prodEKS)
	notShared := refusal(t, read(prodApps), http.StatusForbidden)
	assert.Equal(t, v1.ErrCodeOutputsNotShared, notShared.Code,
		"the pull request lists %s, but the readers come from the default branch", prodApps)

	s.rn.plan(t, pr)
	assert.Equal(t, http.StatusConflict, read(prodEKS).status, "a token of a finished plan run reads no outputs")
	s.closePull(t, pr)
	return pr
}

func (s *story) outputsAudit(t *testing.T, pr *pullRequest, want map[string]toolOutput, withheld []string) {
	t.Helper()
	vpcID, ok := want["vpc_id"].Value.(string)
	require.True(t, ok, "vpc_id is a string")
	canaries := []string{vpcID}
	if subnets, ok := want["subnet_ids"].Value.([]any); ok {
		for _, id := range subnets {
			if id, ok := id.(string); ok {
				canaries = append(canaries, id)
			}
		}
	}

	type entry struct {
		actor, action, stack, reader, runID, reason string
	}
	var audit v1.Page[v1.AuditEntry]
	s.cp.getJSON(t, "/v1/audit?limit=100", &audit)
	var got []entry
	for _, e := range audit.Items {
		if e.Action != actionRead && e.Action != actionRefused {
			continue
		}
		detail := func(name string) string {
			v, _ := e.Details[name].(string)
			return v
		}
		got = append(got, entry{e.Actor, e.Action, detail("stack"), detail("reader"), detail("run_id"), detail("reason")})
		assert.Equal(t, repoName, e.Details["repo"], "%s %s names the producer's repository", e.Action, detail("stack"))
		assert.Equal(t, v1.QualifiedStackKey(repoName, detail("stack")), e.Target)
		if e.Action == actionRead {
			assert.InDelta(t, len(want), e.Details["outputs"], 0, "the read is audited with the number of outputs served")
			assert.InDelta(t, 0, e.Details["sensitive"], 0)
			assert.InDelta(t, len(withheld), e.Details["withheld"], 0)
		}
		details, err := json.Marshal(e.Details)
		require.NoError(t, err)
		for _, canary := range canaries {
			assert.NotContains(t, string(details), canary, "the audit holds counts, never output values")
		}
	}
	assert.ElementsMatch(t, []entry{
		{"apikey:e2e", actionRead, prodVPC, "", "", ""},
		{"apikey:e2e", actionRefused, kycProduction, "", "", v1.ErrCodeStateLocationUnknown},
		{author, actionRead, prodVPC, prodEKS, pr.runID, ""},
		{author, actionRefused, prodVPC, prodApps, pr.runID, v1.ErrCodeOutputsNotShared},
		{author, actionRefused, prodVPC, prodEKS, pr.runID, "conflict"},
	}, got, "every read and refusal of the story is audited")
}
