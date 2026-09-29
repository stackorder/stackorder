package config

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemplateDataFor(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		instance string
		want     TemplateData
	}{
		{"unnamed instance", "./infra/apps/", "", TemplateData{Path: "infra/apps", Name: "apps", Key: "infra/apps"}},
		{"named instance", "infra/apps", "prod", TemplateData{Path: "infra/apps", Name: "apps", Instance: "prod", Key: "infra/apps:prod"}},
		{"repository root", ".", "prod", TemplateData{Instance: "prod", Key: ":prod"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, TemplateDataFor(tt.path, tt.instance))
		})
	}
}

func TestRender(t *testing.T) {
	data := TemplateDataFor("infra/network/vpc", "eu-West-1")
	tests := []struct {
		name    string
		text    string
		want    string
		wantErr string
	}{
		{name: "plain text", text: "infra-prod", want: "infra-prod"},
		{name: "text that only looks broken", text: "a }} b {", want: "a }} b {"},
		{name: "empty", text: "", want: ""},
		{name: "path", text: "{{ .Path }}", want: "infra/network/vpc"},
		{name: "name", text: "{{ .Name }}", want: "vpc"},
		{name: "instance", text: "{{ .Instance }}", want: "eu-West-1"},
		{name: "key", text: "{{ .Key }}", want: "infra/network/vpc:eu-West-1"},
		{name: "trimPrefix", text: `key={{ trimPrefix "infra/" .Path }}/{{ .Instance }}.tfstate`, want: "key=network/vpc/eu-West-1.tfstate"},
		{name: "trimPrefix in a pipeline", text: `{{ .Path | trimPrefix "infra/" }}`, want: "network/vpc"},
		{name: "trimSuffix", text: `{{ trimSuffix "/vpc" .Path }}`, want: "infra/network"},
		{name: "base", text: "{{ base .Path }}", want: "vpc"},
		{name: "dir", text: "{{ dir .Path }}", want: "infra/network"},
		{name: "replace", text: `{{ replace "/" "-" .Path }}`, want: "infra-network-vpc"},
		{name: "lower", text: "{{ lower .Instance }}", want: "eu-west-1"},
		{name: "upper", text: "{{ upper .Instance }}", want: "EU-WEST-1"},
		{name: "missing key", text: "{{ .Environment }}", wantErr: `map has no entry for key "Environment"`},
		{name: "unknown function", text: "{{ title .Name }}", wantErr: `function "title" not defined`},
		{name: "syntax error", text: "{{ .Path ", wantErr: "unclosed action"},
		{name: "wrong arity", text: `{{ replace "/" .Path }}`, wantErr: "wrong number of args"},
		{name: "if and comparisons", text: `{{ if eq .Instance "eu-West-1" }}eu{{ else }}other{{ end }}`, want: "eu"},
		{name: "with and a variable", text: `{{ with $n := .Name }}{{ $n }}{{ end }}`, want: "vpc"},
		{name: "range", text: "{{ range 20000000 }}xxxxxxxxxx{{ end }}", wantErr: "range is not supported"},
		{name: "define", text: `{{ define "x" }}a{{ end }}b`, wantErr: "define and block are not supported"},
		{name: "template", text: `{{ template "x" }}`, wantErr: "template is not supported"},
		{name: "block", text: `{{ block "x" . }}a{{ end }}`, wantErr: "not supported"},
		{name: "printf", text: `{{ printf "%099999999d" 1 }}`, wantErr: `function "printf" is not supported`},
		{name: "call", text: `{{ call .Name }}`, wantErr: `function "call" is not supported`},
		{name: "oversized output", text: strings.Repeat("{{ .Path }}", MaxRenderedLength/len("infra/network/vpc")+1), wantErr: "output is longer than 4096 bytes"},
		{
			name:    "nested replace growth",
			text:    `{{ replace "" "aaaaaaaaaaaaaaaa" (replace "" "aaaaaaaaaaaaaaaa" (replace "" "aaaaaaaaaaaaaaaa" .Path)) }}`,
			wantErr: "output is longer than 4096 bytes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.text, data)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Contains(t, err.Error(), strconv.Quote(tt.text))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
