package tf

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAccessKey = "AKIAIOSFODNN7EXAMPLE"
	testSecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	testJWT       = "eyJhbGciOiJSUzI1NiIsImtpZCI6IjEifQ.eyJzdWIiOiJyZXBvOmFjbWUvaW5mcmEiLCJhdWQiOiJzdGFja29yZGVyIn0.c2lnbmF0dXJlLWJ5dGVz"
	testGHP       = "ghp_R4nd0mT0k3nV4lu3F0rT3st1ng0nlyAbCd"
)

func TestRedact(t *testing.T) {
	r := NewRedactor([]string{"s3cr3t-db-pass", "multi\nline-secret", "abc", "", `quote"inside`})
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "aws access key id", in: "access_key = " + testAccessKey, want: "access_key = ***"},
		{name: "aws session key id", in: "key ASIAY34FZKBOKMUTVV7A in use", want: "key *** in use"},
		{name: "aws secret key env", in: "AWS_SECRET_ACCESS_KEY=" + testSecretKey, want: "AWS_SECRET_ACCESS_KEY=***"},
		{name: "aws secret key ini", in: "aws_secret_access_key = " + testSecretKey, want: "aws_secret_access_key = ***"},
		{name: "aws secret key json", in: `{"secret_access_key": "` + testSecretKey + `"}`, want: `{"secret_access_key": "***"}`},
		{name: "aws secret key sts json", in: `"SecretAccessKey": "` + testSecretKey + `",`, want: `"SecretAccessKey": "***",`},
		{name: "aws secret key hcl", in: `  + secret_access_key = "` + testSecretKey + `"`, want: `  + secret_access_key = "***"`},
		{name: "github classic token", in: "token " + testGHP + " leaked", want: "token *** leaked"},
		{name: "github oauth token", in: "gho_16C7e42F292c6912E7710c838347Ae178B4a", want: "***"},
		{name: "github user token", in: "ghu_16C7e42F292c6912E7710c838347Ae178B4a", want: "***"},
		{name: "github installation token", in: "Authorization: token ghs_16C7e42F292c6912E7710c838347Ae178B4a", want: "Authorization: token ***"},
		{name: "github refresh token", in: "ghr_1B4a2e77838347a7E420ce178F2E7c6912E169246c34E1ccbF66C46812d16D5B1A9Dc86A1498", want: "***"},
		{name: "github fine grained token", in: "github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ012345", want: "***"},
		{name: "slack bot token", in: "SLACK=xoxb-123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx", want: "SLACK=***"},
		{name: "slack user token", in: "xoxp-123456789012-123456789012-123456789012-abcdef0123456789abcdef0123456789", want: "***"},
		{name: "slack other prefixes", in: "xoxa-2-1234567890 xoxr-1234567890ab xoxs-1234567890ab", want: "*** *** ***"},
		{
			name: "rsa private key heredoc",
			in:   "  + private_key_pem = <<-EOT\n        -----BEGIN RSA PRIVATE KEY-----\n        MIIEpAIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF8PbnGy0AHB7MhgHcT\n        -----END RSA PRIVATE KEY-----\n    EOT\n",
			want: "  + private_key_pem = <<-EOT\n        -----BEGIN RSA PRIVATE KEY-----***-----END RSA PRIVATE KEY-----\n    EOT\n",
		},
		{
			name: "openssh private key in json string",
			in:   `{"key": "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n"}`,
			want: `{"key": "-----BEGIN OPENSSH PRIVATE KEY-----***-----END OPENSSH PRIVATE KEY-----\n"}`,
		},
		{
			name: "two pkcs8 keys",
			in:   "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\nkeep me\n-----BEGIN ENCRYPTED PRIVATE KEY-----\nBBBB\n-----END ENCRYPTED PRIVATE KEY-----",
			want: "-----BEGIN PRIVATE KEY-----***-----END PRIVATE KEY-----\nkeep me\n-----BEGIN ENCRYPTED PRIVATE KEY-----***-----END ENCRYPTED PRIVATE KEY-----",
		},
		{
			name: "unterminated private key",
			in:   "before\n-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIIrYSSNQFaA2Hwf1duRSxKtLYX5CB04fSeQ6tF1aY/PuoAoGCCqGSM49\n",
			want: "before\n-----BEGIN EC PRIVATE KEY-----***",
		},
		{name: "jwt", in: "Bearer " + testJWT, want: "Bearer ***"},
		{name: "jwt with empty signature", in: "tok=eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.", want: "tok=***"},
		{name: "json password", in: `{"password": "hunter2"}`, want: `{"password": "***"}`},
		{name: "json prefixed secret key", in: `"client_secret":"abc123xyz"`, want: `"client_secret":"***"`},
		{name: "json escaped quote in value", in: `{"db_password": "a\"b\\c"}`, want: `{"db_password": "***"}`},
		{name: "hcl password", in: `    password = "hunter2"`, want: `    password = "***"`},
		{name: "hcl api token", in: `  + api_token   = "tok-9f8e7d"`, want: `  + api_token   = "***"`},
		{name: "hcl update old and new", in: `  ~ password = "old-value" -> "new-value"`, want: `  ~ password = "***" -> "***"`},
		{name: "hcl update to null", in: `  - master_password = "old-value" -> null`, want: `  - master_password = "***" -> null`},
		{name: "hcl quoted map key", in: `      + "DB_PASSWORD" = "p@ss"`, want: `      + "DB_PASSWORD" = "***"`},
		{name: "hcl session header", in: `"x-amz-security-token" = "FwoGZXIvYXdzEBYaDH"`, want: `"x-amz-security-token" = "***"`},
		{name: "env style token", in: "export GITHUB_TOKEN=abcdef123456 && run", want: "export GITHUB_TOKEN=*** && run"},
		{name: "env style password", in: "DB_PASSWORD=hunter2", want: "DB_PASSWORD=***"},
		{name: "extra literal", in: "connecting with s3cr3t-db-pass to db", want: "connecting with *** to db"},
		{name: "extra literal each line", in: "a line-secret b multi c", want: "a *** b *** c"},
		{name: "extra literal json escaped", in: `"value": "quote\"inside"`, want: `"value": "***"`},
		{name: "several secrets on one line", in: testAccessKey + " " + testGHP + " " + testJWT, want: "*** *** ***"},

		{name: "resource address with secret", in: "  # module.secrets.aws_secretsmanager_secret.db will be created", want: "  # module.secrets.aws_secretsmanager_secret.db will be created"},
		{name: "resource address with token", in: `resource "aws_ssm_parameter" "api_token" {`, want: `resource "aws_ssm_parameter" "api_token" {`},
		{name: "sensitive value marker", in: "  + password = (sensitive value)", want: "  + password = (sensitive value)"},
		{name: "known after apply", in: "  ~ token = (known after apply)", want: "  ~ token = (known after apply)"},
		{name: "numeric setting", in: "  + minimum_password_length = 16", want: "  + minimum_password_length = 16"},
		{name: "key not ending in keyword", in: `  + secret_id = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:db"`, want: `  + secret_id = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:db"`},
		{name: "token type", in: `{"token_type": "Bearer", "expires_in": 3600}`, want: `{"token_type": "Bearer", "expires_in": 3600}`},
		{name: "password policy", in: `"password_policy": "strict"`, want: `"password_policy": "strict"`},
		{name: "empty password", in: `"password": ""`, want: `"password": ""`},
		{name: "prose token", in: "Error: the token expired; request a new token and retry", want: "Error: the token expired; request a new token and retry"},
		{name: "prose password colon", in: "password: must be at least 8 characters", want: "password: must be at least 8 characters"},
		{name: "sha256 hash", in: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{name: "git sha", in: "commit 9fceb02d0ae598e95dc970b74767f19372d61af8", want: "commit 9fceb02d0ae598e95dc970b74767f19372d61af8"},
		{name: "uuid", in: "run 3fa85f64-5717-4562-b3fc-2c963f66afa6", want: "run 3fa85f64-5717-4562-b3fc-2c963f66afa6"},
		{name: "iam arn", in: `role_arn = "arn:aws:iam::123456789012:role/stackorder-plan"`, want: `role_arn = "arn:aws:iam::123456789012:role/stackorder-plan"`},
		{name: "short access key lookalike", in: "AKIAEXAMPLE is not a key", want: "AKIAEXAMPLE is not a key"},
		{name: "access key inside word", in: "XAKIAIOSFODNN7EXAMPLEX", want: "XAKIAIOSFODNN7EXAMPLEX"},
		{name: "github prefix in prose", in: "classic tokens start with ghp_ and are 40 characters", want: "classic tokens start with ghp_ and are 40 characters"},
		{name: "slack prefix in prose", in: "Slack bot tokens look like xoxb-...", want: "Slack bot tokens look like xoxb-..."},
		{name: "dotted identifier", in: "module.vpc.aws_subnet.private[0].id", want: "module.vpc.aws_subnet.private[0].id"},
		{name: "public key block", in: "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE\n-----END PUBLIC KEY-----", want: "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE\n-----END PUBLIC KEY-----"},
		{name: "base64 user data", in: `user_data = "IyEvYmluL2Jhc2gKZWNobyBoZWxsbw=="`, want: `user_data = "IyEvYmluL2Jhc2gKZWNobyBoZWxsbw=="`},
		{name: "short extra literal ignored", in: "abc and abcd", want: "abc and abcd"},
		{name: "lower case env style not masked", in: "db_password=hunter2 in docs", want: "db_password=hunter2 in docs"},
	}
	require.GreaterOrEqual(t, len(tests), 30)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Redact(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, r.Redact(got), "Redact must be idempotent")
		})
	}
}

func TestRedactNilAndEmpty(t *testing.T) {
	tests := []struct {
		name string
		r    *Redactor
	}{
		{name: "nil redactor", r: nil},
		{name: "no extras", r: NewRedactor(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, "key "+Mask, tt.r.Redact("key "+testAccessKey))
			assert.Equal(t, "plain text", tt.r.Redact("plain text"))
		})
	}
}

func TestRedactLongestLiteralFirst(t *testing.T) {
	r := NewRedactor([]string{"pass", "password-long-secret"})
	assert.Equal(t, "x *** y", r.Redact("x password-long-secret y"))
}

func TestMaskCommands(t *testing.T) {
	tests := []struct {
		name    string
		secrets []string
		want    []string
	}{
		{name: "none", secrets: nil, want: nil},
		{name: "single", secrets: []string{"s3cr3t-value"}, want: []string{"::add-mask::s3cr3t-value"}},
		{name: "dedup and short skipped", secrets: []string{"s3cr3t-value", "abc", "", "s3cr3t-value"}, want: []string{"::add-mask::s3cr3t-value"}},
		{name: "multi line", secrets: []string{"line-one\r\nline-two\n\nline-one"}, want: []string{"::add-mask::line-one", "::add-mask::line-two"}},
		{name: "percent escaped", secrets: []string{"100%0Asecret"}, want: []string{"::add-mask::100%250Asecret"}},
		{name: "carriage return inside", secrets: []string{"a\rbcdef"}, want: []string{"::add-mask::a%0Dbcdef"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MaskCommands(tt.secrets))
		})
	}
}

func TestTruncate(t *testing.T) {
	lines := strings.Repeat("0123456789\n", 100)
	tests := []struct {
		name     string
		text     string
		max      int
		wantCut  bool
		wantKeep string
	}{
		{name: "fits", text: "short\n", max: 100, wantKeep: "short\n"},
		{name: "exact fit", text: lines, max: len(lines), wantKeep: lines},
		{name: "cut at line boundary", text: lines, max: 400, wantCut: true},
		{name: "single long line", text: strings.Repeat("x", 1000), max: 300, wantCut: true},
		{name: "multibyte single line", text: strings.Repeat("é", 1000), max: 301, wantCut: true},
		{name: "budget smaller than note", text: lines, max: 10, wantCut: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cut := Truncate(tt.text, tt.max)
			assert.Equal(t, tt.wantCut, cut)
			assert.LessOrEqual(t, len(got), tt.max)
			assert.True(t, utf8.ValidString(got))
			if !tt.wantCut {
				assert.Equal(t, tt.wantKeep, got)
				return
			}
			if tt.max < 100 {
				assert.Empty(t, got)
				return
			}
			kept, _, found := strings.Cut(got, "\n[stackorder: output truncated")
			require.True(t, found, "missing note in %q", got)
			assert.True(t, strings.HasPrefix(tt.text, kept))
			assert.Contains(t, got, fmt.Sprintf("%d of %d bytes shown", len(kept), len(tt.text)))
			assert.Contains(t, got, "the full text is in the job log")
			if strings.Contains(tt.text, "\n") {
				assert.True(t, strings.HasSuffix(kept, "0123456789"), "cut must fall on a line boundary: %q", kept)
			}
		})
	}
}

func TestTruncatePlanTextCap(t *testing.T) {
	text := strings.Repeat("  + resource \"terraform_data\" \"x\" {}\n", 20000)
	got, cut := Truncate(text, MaxPlanText)
	assert.True(t, cut)
	assert.LessOrEqual(t, len(got), MaxPlanText)
	assert.Greater(t, len(got), MaxPlanText-200)
}
