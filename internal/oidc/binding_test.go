package oidc

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	headSHA  = "0123456789abcdef0123456789abcdef01234567"
	otherSHA = "fedcba9876543210fedcba9876543210fedcba98"
)

func planClaims() *Claims {
	return &Claims{
		Repository:     "acme/infra",
		RepositoryID:   "42",
		EventName:      "pull_request",
		Ref:            "refs/pull/7/merge",
		SHA:            headSHA,
		RunID:          "1001",
		JobWorkflowRef: "stackorder/actions/.github/workflows/plan.yml@refs/tags/v1",
	}
}

func dispatchClaims() *Claims {
	return &Claims{
		Repository:     "acme/infra",
		RepositoryID:   "42",
		EventName:      "workflow_dispatch",
		Ref:            "refs/heads/main",
		SHA:            headSHA,
		RunID:          "2002",
		RunAttempt:     "1",
		Environment:    "production",
		JobWorkflowRef: "stackorder/actions/.github/workflows/run.yml@refs/tags/v1.3.0",
	}
}

var planBinding = PlanBinding{Repository: "acme/infra", RepositoryID: "42", SHA: headSHA, PRNumber: 7}

var dispatchBinding = DispatchBinding{
	Repository: "acme/infra", RepositoryID: "42", RunID: 2002, RunAttempt: 1, Environment: "production", DefaultBranch: "main", SHA: headSHA,
}

func TestBindPlan(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Claims)
		binding    func(*PlanBinding)
		wantReason string
		wantGot    string
	}{
		{name: "match"},
		{name: "repository case differs", mutate: func(c *Claims) { c.Repository = "Acme/Infra" }},
		{name: "sha case differs", mutate: func(c *Claims) { c.SHA = "0123456789ABCDEF0123456789ABCDEF01234567" }},
		{name: "repository", mutate: func(c *Claims) { c.Repository = "acme/other" }, wantReason: "repository", wantGot: "acme/other"},
		{name: "repository_id", mutate: func(c *Claims) { c.RepositoryID = "43" }, wantReason: "repository_id", wantGot: "43"},
		{name: "event_name push", mutate: func(c *Claims) { c.EventName = "push" }, wantReason: "event_name", wantGot: "push"},
		{name: "event_name pull_request_target", mutate: func(c *Claims) { c.EventName = "pull_request_target" }, wantReason: "event_name", wantGot: "pull_request_target"},
		{name: "event_name workflow_dispatch", mutate: func(c *Claims) { c.EventName = "workflow_dispatch" }, wantReason: "event_name", wantGot: "workflow_dispatch"},
		{name: "ref of another PR", mutate: func(c *Claims) { c.Ref = "refs/pull/8/merge" }, wantReason: "ref", wantGot: "refs/pull/8/merge"},
		{name: "ref head", mutate: func(c *Claims) { c.Ref = "refs/pull/7/head" }, wantReason: "ref", wantGot: "refs/pull/7/head"},
		{name: "ref branch", mutate: func(c *Claims) { c.Ref = "refs/heads/main" }, wantReason: "ref", wantGot: "refs/heads/main"},
		{name: "sha", mutate: func(c *Claims) { c.SHA = otherSHA }, wantReason: "sha", wantGot: otherSHA},
		{name: "empty sha", mutate: func(c *Claims) { c.SHA = "" }, wantReason: "sha", wantGot: ""},
		{name: "binding for another PR", binding: func(b *PlanBinding) { b.PRNumber = 70 }, wantReason: "ref", wantGot: "refs/pull/7/merge"},
		{name: "first mismatch wins", mutate: func(c *Claims) { c.RepositoryID = "1"; c.SHA = otherSHA }, wantReason: "repository_id", wantGot: "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := planClaims()
			if tt.mutate != nil {
				tt.mutate(c)
			}
			b := planBinding
			if tt.binding != nil {
				tt.binding(&b)
			}
			err := BindPlan(c, b)
			if tt.wantReason == "" {
				require.NoError(t, err)
				return
			}
			var be *ErrBinding
			require.ErrorAs(t, err, &be)
			assert.Equal(t, tt.wantReason, be.Reason)
			assert.Equal(t, tt.wantGot, be.Got)
			assert.ErrorIs(t, err, &ErrBinding{})
			assert.ErrorIs(t, err, &ErrBinding{Reason: tt.wantReason})
			assert.NotErrorIs(t, err, ErrInvalidToken)
		})
	}
}

func TestBindDispatch(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Claims)
		binding    func(*DispatchBinding)
		wantReason string
		wantGot    string
	}{
		{name: "match"},
		{name: "branch given as ref", binding: func(b *DispatchBinding) { b.DefaultBranch = "refs/heads/main" }},
		{name: "environment case differs", mutate: func(c *Claims) { c.Environment = "Production" }},
		{name: "no environment expected", binding: func(b *DispatchBinding) { b.Environment = "" }, mutate: func(c *Claims) { c.Environment = "" }},
		{name: "no sha expected", binding: func(b *DispatchBinding) { b.SHA = "" }, mutate: func(c *Claims) { c.SHA = otherSHA }},
		{name: "no run_attempt expected", binding: func(b *DispatchBinding) { b.RunAttempt = 0 }, mutate: func(c *Claims) { c.RunAttempt = "3" }},
		{name: "repository", mutate: func(c *Claims) { c.Repository = "evil/infra" }, wantReason: "repository", wantGot: "evil/infra"},
		{name: "repository_id", mutate: func(c *Claims) { c.RepositoryID = "7" }, wantReason: "repository_id", wantGot: "7"},
		{name: "event_name", mutate: func(c *Claims) { c.EventName = "pull_request" }, wantReason: "event_name", wantGot: "pull_request"},
		{name: "run_id", mutate: func(c *Claims) { c.RunID = "2003" }, wantReason: "run_id", wantGot: "2003"},
		{name: "run_id not canonical", mutate: func(c *Claims) { c.RunID = "02002" }, wantReason: "run_id", wantGot: "02002"},
		{name: "run_id missing", mutate: func(c *Claims) { c.RunID = "" }, wantReason: "run_id", wantGot: ""},
		{name: "run_attempt of a re-run", mutate: func(c *Claims) { c.RunAttempt = "2" }, wantReason: "run_attempt", wantGot: "2"},
		{name: "run_attempt missing", mutate: func(c *Claims) { c.RunAttempt = "" }, wantReason: "run_attempt", wantGot: ""},
		{name: "binding for a re-run", binding: func(b *DispatchBinding) { b.RunAttempt = 2 }, wantReason: "run_attempt", wantGot: "1"},
		{name: "run_id checked before run_attempt", mutate: func(c *Claims) { c.RunID = "2003"; c.RunAttempt = "2" }, wantReason: "run_id", wantGot: "2003"},
		{name: "ref of a feature branch", mutate: func(c *Claims) { c.Ref = "refs/heads/feature" }, wantReason: "ref", wantGot: "refs/heads/feature"},
		{name: "ref of a PR", mutate: func(c *Claims) { c.Ref = "refs/pull/7/merge" }, wantReason: "ref", wantGot: "refs/pull/7/merge"},
		{name: "ref of a tag", mutate: func(c *Claims) { c.Ref = "refs/tags/main" }, wantReason: "ref", wantGot: "refs/tags/main"},
		{name: "environment", mutate: func(c *Claims) { c.Environment = "staging" }, wantReason: "environment", wantGot: "staging"},
		{name: "job outside any environment", mutate: func(c *Claims) { c.Environment = "" }, wantReason: "environment", wantGot: ""},
		{name: "sha", mutate: func(c *Claims) { c.SHA = otherSHA }, wantReason: "sha", wantGot: otherSHA},
		{name: "binding for another run", binding: func(b *DispatchBinding) { b.RunID = 1 }, wantReason: "run_id", wantGot: "2002"},
		{name: "binding for another branch", binding: func(b *DispatchBinding) { b.DefaultBranch = "trunk" }, wantReason: "ref", wantGot: "refs/heads/main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := dispatchClaims()
			if tt.mutate != nil {
				tt.mutate(c)
			}
			b := dispatchBinding
			if tt.binding != nil {
				tt.binding(&b)
			}
			err := BindDispatch(c, b)
			if tt.wantReason == "" {
				require.NoError(t, err)
				return
			}
			var be *ErrBinding
			require.ErrorAs(t, err, &be)
			assert.Equal(t, tt.wantReason, be.Reason)
			assert.Equal(t, tt.wantGot, be.Got)
			assert.ErrorIs(t, err, &ErrBinding{Reason: tt.wantReason})
		})
	}
}

func TestBindRequiresExpectedValues(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "plan nil claims", err: BindPlan(nil, planBinding), want: "oidc: plan binding: nil claims"},
		{name: "plan empty binding", err: BindPlan(planClaims(), PlanBinding{}), want: "oidc: plan binding is missing Repository, RepositoryID, SHA, PRNumber"},
		{name: "plan without sha", err: BindPlan(planClaims(), PlanBinding{Repository: "acme/infra", RepositoryID: "42", PRNumber: 7}), want: "oidc: plan binding is missing SHA"},
		{name: "plan negative PR", err: BindPlan(planClaims(), PlanBinding{Repository: "acme/infra", RepositoryID: "42", SHA: headSHA, PRNumber: -7}), want: "oidc: plan binding is missing PRNumber"},
		{name: "dispatch nil claims", err: BindDispatch(nil, dispatchBinding), want: "oidc: dispatch binding: nil claims"},
		{name: "dispatch empty binding", err: BindDispatch(dispatchClaims(), DispatchBinding{}), want: "oidc: dispatch binding is missing Repository, RepositoryID, RunID, DefaultBranch"},
		{name: "dispatch bare refs/heads/", err: BindDispatch(dispatchClaims(), DispatchBinding{Repository: "acme/infra", RepositoryID: "42", RunID: 2002, DefaultBranch: "refs/heads/"}), want: "oidc: dispatch binding is missing DefaultBranch"},
		{name: "workflow ref nil claims", err: BindWorkflowRef(nil, DefaultWorkflowRefPattern), want: "oidc: workflow ref binding: nil claims"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.EqualError(t, tt.err, tt.want)
			assert.NotErrorIs(t, tt.err, &ErrBinding{})
		})
	}
}

func TestBindWorkflowRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		pattern string
		wantErr bool
	}{
		{name: "pinned and canonical", ref: "stackorder/actions/.github/workflows/plan.yml@refs/tags/v1", pattern: DefaultWorkflowRefPattern},
		{name: "not pinned", ref: "acme/infra/.github/workflows/stackorder-plan.yml@refs/pull/7/merge"},
		{name: "locally edited copy", ref: "acme/infra/.github/workflows/stackorder-plan.yml@refs/pull/7/merge", pattern: DefaultWorkflowRefPattern, wantErr: true},
		{name: "missing claim", ref: "", pattern: DefaultWorkflowRefPattern, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := BindWorkflowRef(&Claims{JobWorkflowRef: tt.ref}, tt.pattern)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			var be *ErrBinding
			require.ErrorAs(t, err, &be)
			assert.Equal(t, &ErrBinding{Reason: "job_workflow_ref", Want: tt.pattern, Got: tt.ref}, be)
		})
	}
}

func TestErrBinding(t *testing.T) {
	err := error(&ErrBinding{Reason: "sha", Want: headSHA, Got: otherSHA})
	assert.EqualError(t, err, `oidc: claim sha is "`+otherSHA+`", want "`+headSHA+`"`)

	wrapped := fmt.Errorf("posting result: %w", err)
	assert.ErrorIs(t, wrapped, &ErrBinding{})
	assert.ErrorIs(t, wrapped, &ErrBinding{Reason: "sha"})
	assert.NotErrorIs(t, wrapped, &ErrBinding{Reason: "ref"})
	assert.NotErrorIs(t, wrapped, errors.New("sha"))
}
