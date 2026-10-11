package stateread_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/stateread"
)

func FuzzDecode(f *testing.F) {
	f.Add(fixture(f, "terraform.tfstate"))
	f.Add(fixture(f, "tofu.tfstate"))
	f.Add([]byte(`{"serial": 7, "resources": [{"a": [1.5e-3, -0, "\u00e9\n", true, null]}], "outputs": {"b": {"value": {"c": [false]}, "type": ["object", {"c": ["list", "bool"]}], "sensitive": null}}, "version": 4}`))
	f.Add([]byte(`{"version": 4, "lineage": "acme", "outputs": {"a": {"Value": " x ", "TYPE": "string", "sensitive": true}}}`))
	f.Fuzz(func(t *testing.T, state []byte) {
		for _, limits := range [][2]int64{{stateread.MaxObjectBytes, stateread.MaxOutputsBytes}, {512, 128}} {
			whole, wholeErr := stateread.DecodeWithLimits(bytes.NewReader(state), limits[0], limits[1])
			bytewise, bytewiseErr := stateread.DecodeWithLimits(iotest.OneByteReader(bytes.NewReader(state)), limits[0], limits[1])
			if wholeErr != nil || bytewiseErr != nil {
				require.Error(t, wholeErr)
				require.Error(t, bytewiseErr)
				require.Equal(t, wholeErr.Error(), bytewiseErr.Error(), "the same error whatever the reads return")
				continue
			}
			require.Empty(t, cmp.Diff(whole, bytewise), "the same state whatever the reads return")
			for _, o := range whole.Outputs {
				requireCompact(t, o.Value)
				requireCompact(t, o.Type)
			}
		}

		err := stateread.SkipValue(bytes.NewReader(state), 1<<30)
		require.Equal(t, json.Valid(state), err == nil, "a skipped value is accepted as json.Valid accepts it: %v", err)
	})
}

func requireCompact(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, raw))
	require.Equal(t, compact.String(), string(raw))
}
