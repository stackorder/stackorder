package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOIDCAudience(t *testing.T) {
	clearEnv(t)
	tests := []struct {
		name   string
		server string
		envAud string
		want   string
	}{
		{name: "server url untouched", server: "https://stackorder.example.com", want: "https://stackorder.example.com"},
		{name: "trailing slash trimmed", server: "https://stackorder.example.com/", want: "https://stackorder.example.com"},
		{name: "multiple trailing slashes trimmed", server: "https://stackorder.example.com///", want: "https://stackorder.example.com"},
		{name: "server url with path keeps path", server: "https://example.com/stackorder/", want: "https://example.com/stackorder"},
		{name: "explicit audience passed through", server: "https://stackorder.example.com/", envAud: "https://aud.example.com", want: "https://aud.example.com"},
		{name: "explicit audience trailing slash kept", server: "https://stackorder.example.com", envAud: "https://aud.example.com/", want: "https://aud.example.com/"},
		{name: "explicit audience whitespace trimmed", server: "https://stackorder.example.com", envAud: "  https://aud.example.com/  ", want: "https://aud.example.com/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envAud != "" {
				t.Setenv(EnvOIDCAudience, tt.envAud)
			} else {
				t.Setenv(EnvOIDCAudience, "")
			}
			a := &app{server: tt.server}
			require.Equal(t, tt.want, a.oidcAudience())
		})
	}
}
