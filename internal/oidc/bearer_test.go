package oidc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBearerToken(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		want    string
		wantOK  bool
	}{
		{name: "bearer", headers: []string{"Bearer eyJ.a.b"}, want: "eyJ.a.b", wantOK: true},
		{name: "lower case scheme", headers: []string{"bearer eyJ.a.b"}, want: "eyJ.a.b", wantOK: true},
		{name: "upper case scheme", headers: []string{"BEARER sk_123"}, want: "sk_123", wantOK: true},
		{name: "extra blanks", headers: []string{"  Bearer    eyJ.a.b  "}, want: "eyJ.a.b", wantOK: true},
		{name: "missing"},
		{name: "empty", headers: []string{""}},
		{name: "scheme only", headers: []string{"Bearer"}},
		{name: "scheme and blank", headers: []string{"Bearer   "}},
		{name: "basic", headers: []string{"Basic dXNlcjpwYXNz"}},
		{name: "no scheme", headers: []string{"eyJ.a.b"}},
		{name: "token with blank", headers: []string{"Bearer eyJ.a.b extra"}},
		{name: "token with tab", headers: []string{"Bearer eyJ.a.b\textra"}},
		{name: "repeated header", headers: []string{"Bearer one", "Bearer two"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/runs", http.NoBody)
			for _, h := range tt.headers {
				r.Header.Add("Authorization", h)
			}
			got, ok := BearerToken(r)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
