package scan

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3EndpointOf(t *testing.T) {
	tests := []struct {
		src      string
		endpoint string
		found    bool
		ok       bool
	}{
		{src: `{ s3 = "https://0123abcd.r2.cloudflarestorage.com" }`, endpoint: "https://0123abcd.r2.cloudflarestorage.com", found: true, ok: true},
		{src: `{ s3 = "" }`, found: true, ok: true},
		{src: `{ s3 = " " }`, endpoint: " ", found: true, ok: true},
		{src: `{ s3 = null }`, found: true, ok: true},
		{src: `{ sts = "https://sts.example.com" }`, ok: true},
		{src: `{}`, ok: true},
		{src: `{ s3 = var.endpoint }`, found: true},
		{src: `var.endpoints`},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			expr, diags := hclsyntax.ParseExpression([]byte(tt.src), "test.tf", hcl.InitialPos)
			require.False(t, diags.HasErrors(), diags.Error())
			endpoint, found, ok := s3EndpointOf(expr)
			assert.Equal(t, tt.endpoint, endpoint)
			assert.Equal(t, tt.found, found, "found")
			assert.Equal(t, tt.ok, ok, "ok")
		})
	}
}
