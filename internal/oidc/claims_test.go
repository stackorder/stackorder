package oidc

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullRequestNumber(t *testing.T) {
	tests := []struct {
		ref    string
		want   int
		wantOK bool
	}{
		{ref: "refs/pull/7/merge", want: 7, wantOK: true},
		{ref: "refs/pull/12345/merge", want: 12345, wantOK: true},
		{ref: "refs/pull/7/head"},
		{ref: "refs/pull/0/merge"},
		{ref: "refs/pull/-1/merge"},
		{ref: "refs/pull/+7/merge"},
		{ref: "refs/pull/07/merge"},
		{ref: "refs/pull//merge"},
		{ref: "refs/pull/x/merge"},
		{ref: "refs/pull/7/merge/extra"},
		{ref: "refs/heads/main"},
		{ref: "refs/pull/99999999999999999999/merge"},
		{ref: ""},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got, ok := Claims{Ref: tt.ref}.PullRequestNumber()
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRunIDInt(t *testing.T) {
	tests := []struct {
		runID   string
		want    int64
		wantErr bool
	}{
		{runID: "1", want: 1},
		{runID: "17000000123", want: 17000000123},
		{runID: "9223372036854775807", want: 9223372036854775807},
		{runID: "", wantErr: true},
		{runID: "0", wantErr: true},
		{runID: "-4", wantErr: true},
		{runID: "12a", wantErr: true},
		{runID: "9223372036854775808", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.runID, func(t *testing.T) {
			got, err := Claims{RunID: tt.runID}.RunIDInt()
			if tt.wantErr {
				require.ErrorIs(t, err, ErrMalformed)
				require.ErrorIs(t, err, ErrInvalidToken)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClaimsDecodeGitHubPayload(t *testing.T) {
	payload := `{
		"jti": "example-id",
		"sub": "repo:octo-org/octo-repo:environment:prod",
		"environment": "prod",
		"aud": "https://stackorder.example.com",
		"ref": "refs/heads/main",
		"sha": "0123456789abcdef0123456789abcdef01234567",
		"repository": "octo-org/octo-repo",
		"repository_owner": "octo-org",
		"actor_id": "12",
		"repository_visibility": "private",
		"repository_id": "74",
		"repository_owner_id": "65",
		"run_id": "4200",
		"run_number": "10",
		"run_attempt": "2",
		"runner_environment": "github-hosted",
		"actor": "octocat",
		"workflow": "stackorder run",
		"head_ref": "",
		"base_ref": "",
		"event_name": "workflow_dispatch",
		"ref_type": "branch",
		"ref_protected": "true",
		"workflow_ref": "octo-org/octo-repo/.github/workflows/stackorder-run.yml@refs/heads/main",
		"workflow_sha": "0123456789abcdef0123456789abcdef01234567",
		"job_workflow_ref": "stackorder/actions/.github/workflows/run.yml@refs/tags/v1.2.0",
		"job_workflow_sha": "89abcdef0123456789abcdef0123456789abcdef",
		"enterprise": "octo-enterprise",
		"iss": "https://token.actions.githubusercontent.com",
		"nbf": 1632492967,
		"exp": 1632493867,
		"iat": 1632493567
	}`
	var c Claims
	require.NoError(t, json.Unmarshal([]byte(payload), &c))

	assert.Equal(t, "example-id", c.ID)
	assert.Equal(t, "repo:octo-org/octo-repo:environment:prod", c.Subject)
	assert.Equal(t, []string{"https://stackorder.example.com"}, []string(c.Audience))
	assert.Equal(t, DefaultIssuer, c.Issuer)
	assert.Equal(t, int64(1632493567), c.IssuedAt.Unix())
	assert.Equal(t, int64(1632493867), c.ExpiresAt.Unix())
	assert.Equal(t, int64(1632492967), c.NotBefore.Unix())
	assert.Equal(t, "octo-org/octo-repo", c.Repository)
	assert.Equal(t, "74", c.RepositoryID)
	assert.Equal(t, "octo-org", c.RepositoryOwner)
	assert.Equal(t, "65", c.RepositoryOwnerID)
	assert.Equal(t, "4200", c.RunID)
	assert.Equal(t, "10", c.RunNumber)
	assert.Equal(t, "2", c.RunAttempt)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", c.SHA)
	assert.Equal(t, "refs/heads/main", c.Ref)
	assert.Equal(t, "branch", c.RefType)
	assert.Empty(t, c.BaseRef)
	assert.Empty(t, c.HeadRef)
	assert.Equal(t, "workflow_dispatch", c.EventName)
	assert.Equal(t, "prod", c.Environment)
	assert.Equal(t, "stackorder/actions/.github/workflows/run.yml@refs/tags/v1.2.0", c.JobWorkflowRef)
	assert.Equal(t, "89abcdef0123456789abcdef0123456789abcdef", c.JobWorkflowSHA)
	assert.Equal(t, "octo-org/octo-repo/.github/workflows/stackorder-run.yml@refs/heads/main", c.WorkflowRef)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", c.WorkflowSHA)
	assert.Equal(t, "stackorder run", c.Workflow)
	assert.Equal(t, "octocat", c.Actor)
	assert.Equal(t, "12", c.ActorID)
	assert.Equal(t, "github-hosted", c.RunnerEnvironment)
	assert.Equal(t, "octo-enterprise", c.Enterprise)
	assert.Equal(t, "private", c.RepositoryVisibility)

	id, err := c.RunIDInt()
	require.NoError(t, err)
	assert.Equal(t, int64(4200), id)
}

func TestSentinelsMatchErrInvalidToken(t *testing.T) {
	sentinels := []error{
		ErrMalformed, ErrInvalidSignature, ErrUnknownKey, ErrIssuer, ErrAudience,
		ErrExpired, ErrNotYetValid, ErrTooOld, ErrReplay,
	}
	for _, s := range sentinels {
		t.Run(s.Error(), func(t *testing.T) {
			assert.ErrorIs(t, s, ErrInvalidToken)
			assert.NotErrorIs(t, ErrInvalidToken, s)
			assert.NotErrorIs(t, errors.New(s.Error()), ErrInvalidToken)
		})
	}
}
