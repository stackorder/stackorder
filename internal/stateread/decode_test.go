package stateread_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/stateread"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func out(value, typ string, sensitive bool) stateread.Output {
	return stateread.Output{Value: json.RawMessage(value), Type: json.RawMessage(typ), Sensitive: sensitive}
}

func fixtureOutputs() map[string]stateread.Output {
	return map[string]stateread.Output{
		"db_password":  out(`"acme-canary-password"`, `"string"`, true),
		"name":         out(`"acme-network"`, `"string"`, false),
		"nat_enabled":  out(`true`, `"bool"`, false),
		"subnet_count": out(`3`, `"number"`, false),
		"zones":        out(`["eu-west-1a","eu-west-1b"]`, `["list","string"]`, false),
		"tags":         out(`{"env":"prod","team":"platform"}`, `["map","string"]`, false),
		"network": out(`{"cidr":"10.0.0.0/16","private":["10.0.1.0/24","10.0.2.0/24"]}`,
			`["object",{"cidr":"string","private":["tuple",["string","string"]]}]`, false),
	}
}

func decodeString(s string) (*stateread.State, error) {
	return stateread.Decode(strings.NewReader(s))
}

func requireFixture(t *testing.T, name, terraformVersion, lineage string) {
	t.Helper()
	st, err := stateread.Decode(bytes.NewReader(fixture(t, name)))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), st.Serial)
	assert.Equal(t, lineage, st.Lineage)
	assert.Equal(t, terraformVersion, st.TerraformVersion)
	require.Empty(t, cmp.Diff(fixtureOutputs(), st.Outputs), "every output, with its type and sensitivity; the null output is not in state")
}

func TestDecodeTerraformState(t *testing.T) {
	requireFixture(t, "terraform.tfstate", "1.14.4", "eb002722-7a8b-24b3-41a7-d4da1f3593be")
}

func TestDecodeOpenTofuState(t *testing.T) {
	requireFixture(t, "tofu.tfstate", "1.12.6", "7ed41fe9-fdd7-7e04-d401-7983789b8b28")
}

var errReadPastOutputs = errors.New("read past the outputs")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errReadPastOutputs }

func TestDecodeStopsAfterOutputs(t *testing.T) {
	tests := []struct {
		name  string
		state []byte
		stop  string
	}{
		{"terraform", fixture(t, "terraform.tfstate"), `"resources"`},
		{"opentofu", fixture(t, "tofu.tfstate"), `"resources"`},
		{"version after outputs", []byte(`{"outputs": {"a": {"value": 1, "type": "number"}}, "version": 4, "resources": [`), `"resources"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stop := bytes.Index(tt.state, []byte(tt.stop))
			require.Positive(t, stop)
			_, err := stateread.Decode(io.MultiReader(bytes.NewReader(tt.state[:stop]), failingReader{}))
			require.NoError(t, err, "nothing at or after the resources key is read")

			cut := bytes.LastIndexByte(tt.state[:stop], '}')
			_, err = stateread.Decode(io.MultiReader(bytes.NewReader(tt.state[:cut]), failingReader{}))
			require.ErrorIs(t, err, errReadPastOutputs, "the reader fails when the decoder needs more")
		})
	}
}

func stateWithResources(resource string, size int) []byte {
	return slices.Concat(
		[]byte(`{"version": 4, "terraform_version": "1.14.4", "serial": 7, "lineage": "acme", "resources": [`),
		bytes.Repeat([]byte(resource+",\n"), size/(len(resource)+2)),
		[]byte(resource+`], "outputs": {"vpc_id": {"value": "vpc-0acme", "type": "string"}}}`))
}

func stateWithMembers(member string, size int) []byte {
	return slices.Concat(
		[]byte(`{"version": 4, "terraform_version": "1.14.4", "serial": 7, "lineage": "acme", `),
		bytes.Repeat([]byte(member+",\n"), size/(len(member)+2)),
		[]byte(`"outputs": {"vpc_id": {"value": "vpc-0acme", "type": "string"}}}`))
}

func TestDecodeSkipsResourcesQuickly(t *testing.T) {
	tests := []struct {
		name  string
		state func(string, int) []byte
		piece string
	}{
		{"resources", stateWithResources, `{"mode": "managed", "type": "aws_iam_policy", "name": "acme", "provider": "provider[\"registry.terraform.io/hashicorp/aws\"]",
  "instances": [{"schema_version": 0, "attributes": {"arn": "arn:aws:iam::123456789012:policy/acme", "id": "acme",
    "policy": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:GetObject\",\"Resource\":\"*\"}]}",
    "tags": {"env": "prod"}, "count": 3, "enabled": true, "weights": [1.5, -2, 3e2, null]}, "sensitive_attributes": [], "dependencies": ["aws_vpc.main"]}]}`},
		{"small objects", stateWithResources, `{"a":0}`},
		{"numbers", stateWithResources, `0`},
		{"strings", stateWithResources, `"\u0061"`},
		{"keys", stateWithMembers, `"ab": 0`},
		{"escaped keys", stateWithMembers, `"\u0061b": 0`},
		{"long keys", stateWithMembers, `"` + strings.Repeat("k", 200) + `": 0`},
	}
	size, timed := 32<<20, !raceDetector && testing.CoverMode() == ""
	if !timed {
		size /= 16
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := tt.state(tt.piece, size)
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			st, err := stateread.Decode(bytes.NewReader(state))
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(map[string]stateread.Output{"vpc_id": out(`"vpc-0acme"`, `"string"`, false)}, st.Outputs))
			assert.Less(t, after.Mallocs-before.Mallocs, uint64(64), "skipping allocates nothing per token")
			if timed {
				assert.Less(t, elapsed, 2*time.Second, "%d MiB before the outputs", size>>20)
			}
		})
	}
}

func TestDecodeSkipsInConstantMemory(t *testing.T) {
	long := `"` + strings.Repeat("r", stateread.MaxOutputsBytes-2) + `"`
	state := stateWithResources(long, 8*stateread.MaxOutputsBytes)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	st, err := stateread.Decode(bytes.NewReader(state))
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	assert.Len(t, st.Outputs, 1)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(1<<20),
		"strings as long as MaxOutputsBytes before the outputs are skipped without being held")

	half := `"` + strings.Repeat("f", stateread.MaxOutputsBytes/2) + `"`
	state = slices.Concat([]byte(`{"version": 4, `), []byte(long), []byte(`: 1, "outputs": {"vpc_id": {`),
		[]byte(half), []byte(`: 1, "value": 1, "type": "number"}}}`))
	runtime.ReadMemStats(&before)
	st, err = stateread.Decode(bytes.NewReader(state))
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	assert.Len(t, st.Outputs, 1)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(1<<20),
		"long unknown keys, at the top level and in an output, are skipped without being held")

	_, err = stateread.Decode(bytes.NewReader(stateWithResources(long[:len(long)-1]+`r"`, 0)))
	require.ErrorIs(t, err, stateread.ErrTooLarge, "one byte longer is too large")
}

func TestDecodeFieldNames(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  map[string]stateread.Output
		err   error
	}{
		{"escaped keys", `{"\u0076ersion": 4, "\u006Fu\u0074puts": {"a": {"value": 1, "type": "number"}}}`,
			map[string]stateread.Output{"a": out(`1`, `"number"`, false)}, nil},
		{"output fields in any case", `{"version": 4, "outputs": {"a": {"VALUE": 1, "\u0054ype": "number", "ſensitive": true}}}`,
			map[string]stateread.Output{"a": out(`1`, `"number"`, true)}, nil},
		{"escaped folded field", `{"version": 4, "outputs": {"a": {"Value": 1, "type": "number", "\u017Fensitive": true}}}`,
			map[string]stateread.Output{"a": out(`1`, `"number"`, true)}, nil},
		{"surrogate pairs", `{"version": 4, "\ud83d\ude00": 1, "\ud800outputs": 1, "\ud800\u0041": 1, "outputs": {}}`,
			map[string]stateread.Output{}, nil},
		{"long unknown keys", `{"version": 4, "` + strings.Repeat("version", 40) + `": 1, "outputs": {"a": {"` +
			strings.Repeat(" ", 200) + `": 1, "value": 1, "type": "number"}}}`,
			map[string]stateread.Output{"a": out(`1`, `"number"`, false)}, nil},
		{"escaped encryption key", `{"encrypted\u005fdata": "c2VjcmV0"}`, nil, stateread.ErrEncrypted},
		{"top-level keys match case", `{"Version": 4, "outputs": {}}`, nil, stateread.ErrUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := decodeString(tt.state)
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(tt.want, st.Outputs))
		})
	}
}

func TestDecodeOutputsAfterResources(t *testing.T) {
	const resources = `"resources": [
    {"mode": "managed", "type": "aws_vpc", "name": "main", "provider": "provider[\"registry.terraform.io/hashicorp/aws\"]",
     "instances": [{"schema_version": 1, "attributes": {"id": "vpc-0acme", "cidr_block": "10.0.0.0/16", "enable_dns": true,
       "tags": {"Name": "acme"}, "ipv6": null, "weights": [1.5, -2, 3e2], "outputs": {"value": "not an output"}}}]}
  ],
  "check_results": [{"object_kind": "resource", "config_addr": "aws_vpc.main", "status": "pass", "objects": null}]`
	tests := []struct {
		name  string
		state string
	}{
		{"terraform order with outputs last", `{"version": 4, "terraform_version": "1.14.4", "serial": 7, "lineage": "acme",
  ` + resources + `,
  "outputs": {"vpc_id": {"value": "vpc-0acme", "type": "string"}}}`},
		{"version last", `{"serial": 7, "lineage": "acme", "terraform_version": "1.14.4", ` + resources + `,
  "outputs": {"vpc_id": {"value": "vpc-0acme", "type": "string"}}, "unknown": {"nested": [[{}], []]}, "version": 4}`},
		{"unknown keys first", `{"future": [{"a": [1, {"b": null}]}], "flag": false, "version": 4, "serial": 7, "lineage": "acme",
  "terraform_version": "1.14.4", ` + resources + `, "outputs": {"vpc_id": {"value": "vpc-0acme", "type": "string"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := decodeString(tt.state)
			require.NoError(t, err)
			want := &stateread.State{Serial: 7, Lineage: "acme", TerraformVersion: "1.14.4",
				Outputs: map[string]stateread.Output{"vpc_id": out(`"vpc-0acme"`, `"string"`, false)}}
			require.Empty(t, cmp.Diff(want, st))
		})
	}
}

func TestDecodeRepeatedOutputs(t *testing.T) {
	st, err := decodeString(`{"outputs": {"a": {"value": 1, "type": "number"}}, "outputs": {"b": {"value": 2, "type": "number"}}, "version": 4}`)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(map[string]stateread.Output{"b": out(`2`, `"number"`, false)}, st.Outputs), "the last outputs key wins")
}

func TestDecodeIdentityLimit(t *testing.T) {
	tests := []struct {
		field string
		size  int
		ok    bool
	}{
		{"lineage", 256, true},
		{"lineage", 257, false},
		{"terraform_version", 256, true},
		{"terraform_version", 257, false},
		{"lineage", stateread.MaxOutputsBytes, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s of %d bytes", tt.field, tt.size), func(t *testing.T) {
			value := strings.Repeat("a", tt.size)
			prefix := `{"version": 4, "` + tt.field + `": `
			st, err := decodeString(prefix + `"` + value + `", "outputs": {}}`)
			if tt.ok {
				require.NoError(t, err)
				assert.Equal(t, value, st.Lineage+st.TerraformVersion)
				return
			}
			require.ErrorIs(t, err, stateread.ErrMalformed)
			assert.EqualError(t, err, fmt.Sprintf("stateread: malformed state: %s longer than 256 bytes at byte %d", tt.field, len(prefix)))
		})
	}
}

func TestDecodeEmptyOutputs(t *testing.T) {
	tests := []struct {
		name  string
		state string
	}{
		{"empty object", `{"version": 4, "serial": 3, "lineage": "acme", "outputs": {}, "resources": []}`},
		{"null", `{"version": 4, "serial": 3, "lineage": "acme", "outputs": null, "resources": []}`},
		{"absent", `{"version": 4, "serial": 3, "lineage": "acme", "resources": [{"mode": "data"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := decodeString(tt.state)
			require.NoError(t, err)
			assert.Equal(t, uint64(3), st.Serial)
			assert.NotNil(t, st.Outputs)
			assert.Empty(t, st.Outputs)
		})
	}
}

func TestDecodeRejectsEncryptedState(t *testing.T) {
	tests := []struct {
		name  string
		state string
	}{
		{"opentofu fixture", string(fixture(t, "tofu-encrypted.tfstate"))},
		{"encryption_version only", `{"serial": 1, "encryption_version": "v0"}`},
		{"encrypted_data only", `{"serial": 1, "lineage": "acme", "encrypted_data": "c2VjcmV0"}`},
		{"before outputs of a version 4 state", `{"version": 4, "encrypted_data": "c2VjcmV0", "outputs": {}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeString(tt.state)
			require.ErrorIs(t, err, stateread.ErrEncrypted)
		})
	}
}

func TestDecodeRejectsVersion3(t *testing.T) {
	tests := []struct {
		name  string
		state string
	}{
		{"version 3", `{"version": 3, "terraform_version": "0.11.14", "serial": 2, "lineage": "acme",
  "modules": [{"path": ["root"], "outputs": {"vpc_id": {"sensitive": false, "type": "string", "value": "vpc-0acme"}},
    "resources": {}, "depends_on": []}]}`},
		{"version 5", `{"version": 5, "outputs": {}}`},
		{"no version", `{"serial": 2, "lineage": "acme", "outputs": {}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeString(tt.state)
			require.ErrorIs(t, err, stateread.ErrUnsupported)
		})
	}
}

func outputsOf(n, size int) string {
	var b strings.Builder
	b.WriteString(`{`)
	for i := range n {
		if i > 0 {
			b.WriteString(`,`)
		}
		fmt.Fprintf(&b, `"o%d": {"value": "%s", "type": "string"}`, i, strings.Repeat("v", size))
	}
	b.WriteString(`}`)
	return b.String()
}

func numberOutputs(n int) string {
	var b strings.Builder
	b.WriteString(`{`)
	for i := range n {
		if i > 0 {
			b.WriteString(`,`)
		}
		fmt.Fprintf(&b, `"o%d": {"value": 1, "type": "number"}`, i)
	}
	b.WriteString(`}`)
	return b.String()
}

func numbers(n int) string {
	return strings.TrimSuffix(strings.Repeat("1,", n), ",")
}

func TestDecodeLimits(t *testing.T) {
	versionLast := `{"outputs": {}, "version": 4}`
	tests := []struct {
		name       string
		state      string
		maxObject  int64
		maxOutputs int64
		want       string
	}{
		{"object within the cap", versionLast, int64(len(versionLast)), 64, ""},
		{"object one byte over the cap", versionLast, int64(len(versionLast)) - 1, 64, fmt.Sprintf("no outputs in the first %d bytes", len(versionLast)-1)},
		{"long scan before the outputs", `{"version": 4, "resources": [` + numbers(600) + `], "outputs": {}}`, 1024, 256, "no outputs in the first 1024 bytes"},
		{"long resources after the outputs", `{"version": 4, "outputs": {}, "resources": [` + numbers(600) + `]}`, 1024, 256, ""},
		{"outputs within the cap", `{"version": 4, "outputs": ` + outputsOf(4, 40) + `}`, 1 << 20, 2048, ""},
		{"outputs over the cap", `{"version": 4, "outputs": ` + outputsOf(40, 40) + `}`, 1 << 20, 2048, "outputs exceed 2048 bytes"},
		{"one output over the cap", `{"version": 4, "outputs": ` + outputsOf(1, 2100) + `}`, 1 << 20, 2048, "exceed"},
		{"small outputs within the cap", `{"version": 4, "outputs": ` + numberOutputs(4) + `}`, 1 << 20, 2048, ""},
		{"many small outputs over the cap", `{"version": 4, "outputs": ` + numberOutputs(20) + `}`, 1 << 20, 2048, "outputs exceed 2048 bytes"},
		{"skipped value over the cap", `{"version": 4, "resources": ["` + strings.Repeat("r", 600) + `"], "outputs": {}}`, 1 << 20, 512, `a value at byte \d+ exceeds 512 bytes`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := stateread.DecodeWithLimits(strings.NewReader(tt.state), tt.maxObject, tt.maxOutputs)
			if tt.want == "" {
				require.NoError(t, err)
				assert.NotNil(t, st.Outputs)
				return
			}
			require.ErrorIs(t, err, stateread.ErrTooLarge)
			assert.Regexp(t, tt.want, err.Error())
		})
	}

	t.Run("exported limits", func(t *testing.T) {
		_, err := decodeString(`{"version": 4, "outputs": ` + outputsOf(stateread.MaxOutputsBytes/(64<<10)+1, 64<<10) + `}`)
		require.ErrorIs(t, err, stateread.ErrTooLarge, "more than MaxOutputsBytes of outputs")
		_, err = decodeString(`{"version": 4, "outputs": ` + outputsOf(16, 64<<10) + `}`)
		require.NoError(t, err, "less than MaxOutputsBytes of outputs")

		small := numberOutputs(stateread.MaxOutputsBytes / 100)
		require.Less(t, len(small), stateread.MaxOutputsBytes)
		_, err = decodeString(`{"version": 4, "outputs": ` + small + `}`)
		require.ErrorIs(t, err, stateread.ErrTooLarge, "less than MaxOutputsBytes of JSON, but more once each output counts its map entry")
	})
}

func TestDecodeMissingType(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  string
	}{
		{"missing type", `{"version": 4, "outputs": {"vpc_id": {"value": "vpc-0acme"}}}`, "output without a type"},
		{"null type", `{"version": 4, "outputs": {"vpc_id": {"value": "vpc-0acme", "type": null}}}`, "output without a type"},
		{"missing value", `{"version": 4, "outputs": {"vpc_id": {"type": "string"}}}`, "output without a value"},
		{"null output", `{"version": 4, "outputs": {"vpc_id": null}}`, "output without a value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeString(tt.state)
			require.ErrorIs(t, err, stateread.ErrMalformed)
			at := strings.Index(tt.state, `"vpc_id"`) + len(`"vpc_id"`)
			assert.Contains(t, err.Error(), fmt.Sprintf("%s at byte %d", tt.want, at))
		})
	}

	st, err := decodeString(`{"version": 4, "outputs": {"vpc_id": {"value": null, "type": "string"}}}`)
	require.NoError(t, err, "a null value with a type is an output")
	assert.Equal(t, out(`null`, `"string"`, false), st.Outputs["vpc_id"])
}

func TestDecodeMalformed(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  error
	}{
		{"empty", ``, stateread.ErrMalformed},
		{"not an object", `["version", 4]`, stateread.ErrMalformed},
		{"truncated", `{"version": 4, "outputs": {"a": {"value": "x", "ty`, stateread.ErrMalformed},
		{"string version", `{"version": "4", "outputs": {}}`, stateread.ErrMalformed},
		{"fractional version", `{"version": 4.0, "outputs": {}}`, stateread.ErrMalformed},
		{"negative serial", `{"version": 4, "serial": -1, "outputs": {}}`, stateread.ErrMalformed},
		{"numeric lineage", `{"version": 4, "lineage": 7, "outputs": {}}`, stateread.ErrMalformed},
		{"outputs array", `{"version": 4, "outputs": []}`, stateread.ErrMalformed},
		{"output string", `{"version": 4, "outputs": {"a": "x"}}`, stateread.ErrMalformed},
		{"sensitive string", `{"version": 4, "outputs": {"a": {"value": 1, "type": "number", "sensitive": "yes"}}}`, stateread.ErrMalformed},
		{"unterminated resources", `{"version": 4, "resources": [{"a": [1, 2}], "outputs": {}}`, stateread.ErrMalformed},
		{"too deep", `{"version": 4, "resources": ` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `, "outputs": {}}`, stateread.ErrMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeString(tt.state)
			require.ErrorIs(t, err, tt.want)
			assert.Regexp(t, `at byte \d+$`, err.Error())
		})
	}
}

var errNetwork = errors.New("connection reset by peer")

type brokenReader struct {
	r     io.Reader
	after int
}

func (b *brokenReader) Read(p []byte) (int, error) {
	if b.after <= 0 {
		return 0, errNetwork
	}
	p = p[:min(len(p), b.after)]
	n, err := b.r.Read(p)
	b.after -= n
	return n, err
}

func TestDecodeReturnsReadErrors(t *testing.T) {
	state := fixture(t, "terraform.tfstate")
	_, err := stateread.Decode(&brokenReader{r: bytes.NewReader(state), after: 100})
	require.ErrorIs(t, err, errNetwork)
	for _, sentinel := range []error{stateread.ErrMalformed, stateread.ErrTooLarge, stateread.ErrUnsupported} {
		assert.NotErrorIs(t, err, sentinel, "a failed read is not a property of the state")
	}
}

func TestDecodeErrorsCarryNoStateBytes(t *testing.T) {
	const canary = "acme-canary-7f3a9c"
	secrets := []string{canary, "7313", "acme-canary-password", "acme-network", "10.0.0.0", "platform", "eu-west-1a", "eb002722", "1.14.4"}
	tests := []struct {
		name  string
		state string
	}{
		{"truncated value", `{"version": 4, "outputs": {"k": {"value": "` + canary},
		{"invalid character after a value", `{"version": 4, "outputs": {"k": {"value": "` + canary + `" "type": "string"}}}`},
		{"bare word type", `{"version": 4, "outputs": {"k": {"value": "x", "type": ` + canary + `}}}`},
		{"sensitive as a string", `{"version": 4, "outputs": {"k": {"value": 1, "type": "number", "sensitive": "` + canary + `"}}}`},
		{"sensitive as a number", `{"version": 4, "outputs": {"k": {"value": 1, "type": "number", "sensitive": 7313}}}`},
		{"canary output name", `{"version": 4, "outputs": {"` + canary + `": {"value": 1}}}`},
		{"canary in resources", `{"version": 4, "resources": [{"attributes": {"password": "` + canary + `"}},]`},
		{"canary key", `{"` + canary + `": [1, 2,`},
		{"canary lineage", `{"version": 4, "lineage": {"` + canary + `": 1}}`},
		{"canary version", `{"version": 7313, "outputs": {}}`},
		{"bare word", canary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeString(tt.state)
			require.Error(t, err)
			for _, s := range secrets {
				assert.NotContains(t, err.Error(), s)
			}
		})
	}

	t.Run("every truncation of a fixture", func(t *testing.T) {
		state := fixture(t, "terraform.tfstate")
		end := bytes.Index(state, []byte(`"resources"`))
		for i := range end {
			_, err := stateread.Decode(bytes.NewReader(state[:i]))
			if err == nil {
				continue
			}
			require.ErrorIs(t, err, stateread.ErrMalformed, "truncated at %d", i)
			for _, s := range secrets {
				require.NotContains(t, err.Error(), s, "truncated at %d", i)
			}
		}
	})
}
