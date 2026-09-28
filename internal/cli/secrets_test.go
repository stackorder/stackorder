package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/tf"
)

func TestEnvSecrets(t *testing.T) {
	clearEnv(t)
	for k, v := range map[string]string{
		"TF_VAR_db_password":        "correct-horse",
		"TF_VAR_enable_secret":      "false",
		"TF_VAR_token_ttl":          "3600",
		"AWS_SECRET_ACCESS_KEY":     "wJalrXUtnFEMI/K7MDENG",
		"AWS_SESSION_TOKEN":         "FwoGZXIvYXdzE",
		"STACKORDER_API_KEY":        "sk_live_abc",
		"ARM_CLIENT_SECRET":         "arm-secret-1",
		"GITHUB_APP_PRIVATE_KEY":    "-----BEGIN RSA PRIVATE KEY-----",
		"GOOGLE_CREDENTIALS":        "{\"type\":\"service_account\"}",
		"SHORT_TOKEN":               "abc",
		"TOKENIZER_MODE":            "strict-mode",
		"SECRETARY":                 "alice-smith",
		"DUPLICATE_SECRET":          "correct-horse",
		"TF_TOKEN_app_terraform_io": "tfc-token-1",
	} {
		t.Setenv(k, v)
	}
	got := envSecrets()
	assert.Subset(t, got, []string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"FwoGZXIvYXdzE",
		"arm-secret-1",
		"correct-horse",
		"sk_live_abc",
		"tfc-token-1",
		"wJalrXUtnFEMI/K7MDENG",
		"{\"type\":\"service_account\"}",
	})
	for _, v := range []string{"false", "3600", "abc", "strict-mode", "alice-smith"} {
		assert.NotContains(t, got, v)
	}
	assert.True(t, slices.IsSorted(got))
	assert.Len(t, slices.Compact(slices.Clone(got)), len(got))
}

func TestRedactedValues(t *testing.T) {
	r := tf.NewRedactor([]string{"correct-horse"})
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "nothing to redact", in: "plain text", want: nil},
		{name: "quoted assignment", in: `  + password = "hunter2hunter2"`, want: []string{"hunter2hunter2"}},
		{name: "change from one secret to another", in: `  ~ token = "old-token-1" -> "new-token-2"`, want: []string{"old-token-1", "new-token-2"}},
		{name: "literal at the end", in: "value correct-horse", want: []string{"correct-horse"}},
		{name: "literal at the start", in: "correct-horse is set", want: []string{"correct-horse"}},
		{name: "adjacent masks merge", in: "correct-horsecorrect-horse!", want: []string{"correct-horsecorrect-horse"}},
		{name: "access key", in: "key AKIAABCDEFGHIJKLMNOP used", want: []string{"AKIAABCDEFGHIJKLMNOP"}},
		{name: "already masked text", in: "x *** y", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, redactedValues(tt.in, r.Redact(tt.in)))
		})
	}
	assert.Nil(t, redactedValues("abc", "zz***"))
	assert.Nil(t, redactedValues("abc def", "abc ***x"))
	assert.Nil(t, redactedValues("abc def ghi", "abc ***Q*** ghi"))
}

func TestMaskWriter(t *testing.T) {
	tests := []struct {
		name   string
		known  []string
		writes []string
		want   string
	}{
		{
			name:   "masks before the line and redacts it",
			writes: []string{"  + password = \"hunter2hu", "nter2\"\n", "next\n"},
			want:   "::add-mask::hunter2hunter2\n  + password = \"***\"\nnext\n",
		},
		{
			name:   "announces each secret once",
			writes: []string{"token = \"abcd1234\"\ntoken = \"abcd1234\"\n"},
			want:   "::add-mask::abcd1234\ntoken = \"***\"\ntoken = \"***\"\n",
		},
		{
			name:   "known secrets are not announced again",
			known:  []string{"correct-horse"},
			writes: []string{"a correct-horse b\n"},
			want:   "a *** b\n",
		},
		{
			name: "private key blocks across lines",
			writes: []string{
				"key = <<EOT\n",
				"-----BEGIN RSA PRIVATE KEY-----\n",
				"  MIIEowIBAAKCAQEA\n",
				"\n",
				"  q83jfSD==\n",
				"-----END RSA PRIVATE KEY-----\n",
				"EOT\n",
			},
			want: "key = <<EOT\n" +
				"-----BEGIN RSA PRIVATE KEY-----***\n" +
				"::add-mask::MIIEowIBAAKCAQEA\n  ***\n" +
				"\n" +
				"::add-mask::q83jfSD==\n  ***\n" +
				"-----END RSA PRIVATE KEY-----\n" +
				"EOT\n",
		},
		{
			name:   "key body on the end line",
			writes: []string{"-----BEGIN PRIVATE KEY-----\n", "abcdEFGH-----END PRIVATE KEY----- tail\n"},
			want:   "-----BEGIN PRIVATE KEY-----***\n::add-mask::abcdEFGH\n***-----END PRIVATE KEY----- tail\n",
		},
		{
			name:   "single line key",
			writes: []string{"pem = \"-----BEGIN EC PRIVATE KEY-----\\nMHcCAQEE\\n-----END EC PRIVATE KEY-----\"\n", "after\n"},
			want:   "::add-mask::\\nMHcCAQEE\\n\npem = \"-----BEGIN EC PRIVATE KEY-----***-----END EC PRIVATE KEY-----\"\nafter\n",
		},
		{
			name:   "partial last line is flushed",
			writes: []string{"done, secret = \"abcdefgh\""},
			want:   "::add-mask::abcdefgh\ndone, secret = \"***\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			m := newMaskWriter(&b, tf.NewRedactor(tt.known), tt.known)
			for _, w := range tt.writes {
				n, err := m.Write([]byte(w))
				require.NoError(t, err)
				assert.Equal(t, len(w), n)
			}
			require.NoError(t, m.Flush())
			require.NoError(t, m.Flush())
			assert.Equal(t, tt.want, b.String())
		})
	}
}

func TestMaskWriterErrors(t *testing.T) {
	m := newMaskWriter(&failingWriter{}, tf.NewRedactor(nil), nil)
	_, err := m.Write([]byte("line\n"))
	require.ErrorContains(t, err, "disk full")

	m = newMaskWriter(&failingWriter{}, tf.NewRedactor(nil), nil)
	_, err = m.Write([]byte("token = \"abcd1234\"\n"))
	require.ErrorContains(t, err, "disk full")

	m = newMaskWriter(&failingWriter{}, tf.NewRedactor(nil), nil)
	_, err = m.Write([]byte("partial"))
	require.NoError(t, err)
	require.ErrorContains(t, m.Flush(), "disk full")
}
