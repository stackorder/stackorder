package gh_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stackorder/stackorder/internal/gh"
)

func TestVerifySignature(t *testing.T) {
	secret := []byte("It's a Secret to Everybody")
	body := []byte("Hello, World!")
	const githubDocsSignature = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"

	tests := []struct {
		name   string
		secret []byte
		body   []byte
		header string
		want   bool
	}{
		{"github documentation example", secret, body, githubDocsSignature, true},
		{"surrounding whitespace", secret, body, " " + githubDocsSignature + " ", true},
		{"signed by SignPayload", secret, []byte(`{"a":1}`), gh.SignPayload(secret, []byte(`{"a":1}`)), true},
		{"tampered body", secret, []byte("Hello, World?"), githubDocsSignature, false},
		{"wrong secret", []byte("other"), body, githubDocsSignature, false},
		{"empty secret", nil, body, gh.SignPayload(nil, body), false},
		{"missing prefix", secret, body, githubDocsSignature[len("sha256="):], false},
		{"sha1 header", secret, body, "sha1=757107ea0eb2509fc211221cce984b8a37570b6d", false},
		{"not hex", secret, body, "sha256=zz", false},
		{"truncated", secret, body, githubDocsSignature[:40], false},
		{"empty header", secret, body, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, gh.VerifySignature(tt.secret, tt.body, tt.header))
		})
	}
	assert.Equal(t, githubDocsSignature, gh.SignPayload(secret, body))
}
