package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
)

func outputsTarget(repo, key string, query url.Values) string {
	target := "/v1/repos/" + repo + "/stacks/" + url.PathEscape(key) + "/outputs"
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	return target
}

func sampleOutputs(repo, key string) *v1.StackOutputs {
	return &v1.StackOutputs{
		Repo: repo, Stack: key, Serial: 12, Lineage: "3f0c8a52-6d1e-4b7a-9c2d-5e8f1a0b3c4d", TerraformVersion: "1.9.8",
		Outputs: map[string]v1.StackOutput{
			"vpc_id":  {Value: json.RawMessage(`"vpc-0a1b2c3d"`), Type: json.RawMessage(`"string"`)},
			"subnets": {Value: json.RawMessage(`["subnet-1","subnet-2"]`), Type: json.RawMessage(`["list","string"]`)},
		},
		Withheld: []string{"db_password"},
	}
}

func TestStackOutputsRoute(t *testing.T) {
	runID := uuid.NewString()
	cases := []struct {
		name   string
		target string
		repo   string
		key    string
		runID  string
		reader string
	}{
		{
			name:   "escaped key with an instance",
			target: "/v1/repos/acme/infra/stacks/stacks%2Fprod%2Fapps:blue/outputs?run=" + runID + "&reader=stacks%2Fprod%2Feks",
			repo:   "acme/infra", key: "stacks/prod/apps:blue", runID: runID, reader: "stacks/prod/eks",
		},
		{
			name:   "producer in another repository",
			target: outputsTarget("acme/network", "stacks/prod/vpc", url.Values{"run": {runID}, "reader": {"stacks/prod/apps:green"}}),
			repo:   "acme/network", key: "stacks/prod/vpc", runID: runID, reader: "stacks/prod/apps:green",
		},
		{
			name:   "without run or reader",
			target: outputsTarget("acme/infra", "stacks/prod/vpc", nil),
			repo:   "acme/infra", key: "stacks/prod/vpc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRunnerEnv(t)
			want := sampleOutputs(tc.repo, tc.key)
			e.runs.outputs = want
			rec := e.do(bearer(newRequest(t, http.MethodGet, tc.target, nil), e.planToken()))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			assert.Equal(t, *want, decodeBody[v1.StackOutputs](t, rec))

			require.Equal(t, 1, e.runs.count())
			call := e.runs.last()
			assert.Equal(t, "StackOutputs", call.method)
			assert.Equal(t, tc.repo, call.repo)
			assert.Equal(t, tc.key, call.stackKey, "the %2F-escaped key is decoded")
			assert.Equal(t, tc.runID, call.runID)
			assert.Equal(t, tc.reader, call.reader)
			assert.Equal(t, principal.OIDC, call.p.Kind)
			require.NotNil(t, call.p.Claims)
			assert.Equal(t, "acme/infra", call.p.Claims.Repository)
		})
	}
}

func TestStackOutputsRefusesSessions(t *testing.T) {
	e := newRunnerEnv(t)
	target := outputsTarget("acme/infra", "stacks/prod/vpc", url.Values{"run": {uuid.NewString()}, "reader": {"stacks/prod/eks"}})
	rec := e.do(withCookie(newRequest(t, http.MethodGet, target, nil), e.session("octocat", "acme")))
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, "a runner OIDC token or an API key is required", errorOf(t, rec).Message)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Zero(t, e.runs.count(), "the run service is never called")
}

func TestStackOutputsAcceptsAPIKeys(t *testing.T) {
	e := newRunnerEnv(t)
	rec := e.do(bearer(newRequest(t, http.MethodGet, outputsTarget("acme/infra", "stacks/prod/vpc", nil), nil), e.apiKey("ci")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, v1.StackOutputs{Repo: "acme/infra", Stack: "stacks/prod/vpc", Outputs: map[string]v1.StackOutput{}},
		decodeBody[v1.StackOutputs](t, rec))
	call := e.runs.last()
	assert.Equal(t, "StackOutputs", call.method)
	assert.Equal(t, principal.APIKey, call.p.Kind)
	assert.Equal(t, "ci", call.p.Login)
	assert.Nil(t, call.p.Claims)
	assert.Empty(t, call.runID)
	assert.Empty(t, call.reader)
}

func TestStackOutputsRunnerTokenIsSingleUse(t *testing.T) {
	e := newRunnerEnv(t)
	target := outputsTarget("acme/infra", "stacks/prod/vpc", url.Values{"run": {uuid.NewString()}, "reader": {"stacks/prod/eks"}})
	token := e.planToken()
	rec := e.do(bearer(newRequest(t, http.MethodGet, target, nil), token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = e.do(bearer(newRequest(t, http.MethodGet, target, nil), token))
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Contains(t, errorOf(t, rec).Message, "token replayed")
	assert.Equal(t, 1, e.runs.count())
}

func TestStackOutputsErrorCodes(t *testing.T) {
	coded := func(kind error, code string) error {
		return &principal.CodedError{Kind: kind, Code: code, Message: "refused: " + code}
	}
	cases := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{name: "disabled", err: coded(principal.ErrNotFound, v1.ErrCodeOutputsDisabled), status: http.StatusNotFound, code: v1.ErrCodeOutputsDisabled},
		{name: "not shared", err: coded(principal.ErrForbidden, v1.ErrCodeOutputsNotShared), status: http.StatusForbidden, code: v1.ErrCodeOutputsNotShared},
		{name: "location unknown", err: coded(principal.ErrConflict, v1.ErrCodeStateLocationUnknown), status: http.StatusConflict, code: v1.ErrCodeStateLocationUnknown},
		{name: "bucket not allowed", err: coded(principal.ErrForbidden, v1.ErrCodeStateBucketNotAllowed), status: http.StatusForbidden, code: v1.ErrCodeStateBucketNotAllowed},
		{name: "state not found", err: coded(principal.ErrNotFound, v1.ErrCodeStateNotFound), status: http.StatusNotFound, code: v1.ErrCodeStateNotFound},
		{name: "access denied", err: coded(principal.ErrForbidden, v1.ErrCodeStateAccessDenied), status: http.StatusForbidden, code: v1.ErrCodeStateAccessDenied},
		{name: "encrypted", err: coded(principal.ErrUnprocessable, v1.ErrCodeStateEncrypted), status: http.StatusUnprocessableEntity, code: v1.ErrCodeStateEncrypted},
		{name: "unsupported", err: coded(principal.ErrUnprocessable, v1.ErrCodeStateUnsupported), status: http.StatusUnprocessableEntity, code: v1.ErrCodeStateUnsupported},
		{name: "too large", err: coded(principal.ErrUnprocessable, v1.ErrCodeStateTooLarge), status: http.StatusUnprocessableEntity, code: v1.ErrCodeStateTooLarge},
		{name: "unreadable", err: coded(principal.ErrUnprocessable, v1.ErrCodeStateUnreadable), status: http.StatusUnprocessableEntity, code: v1.ErrCodeStateUnreadable},
		{name: "across repositories", err: coded(principal.ErrNotFound, codeNotFound), status: http.StatusNotFound, code: codeNotFound},
		{
			name: "wrapped coded error keeps its own message", err: fmt.Errorf("read outputs: %w", coded(principal.ErrForbidden, v1.ErrCodeOutputsNotShared)),
			status: http.StatusForbidden, code: v1.ErrCodeOutputsNotShared,
		},
		{
			name: "state temporarily unreadable", err: principal.Wrap(principal.ErrUnavailable, "the state object of stacks/prod/vpc could not be read; try again"),
			status: http.StatusServiceUnavailable, code: codeUnavailable, message: "unavailable: the state object of stacks/prod/vpc could not be read; try again",
		},
		{
			name: "reader outside the run", err: principal.Wrap(principal.ErrForbidden, "stacks/prod/eks is not a stack of run r1"),
			status: http.StatusForbidden, code: codeForbidden, message: "forbidden: stacks/prod/eks is not a stack of run r1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRunnerEnv(t)
			e.runs.err = tc.err
			rec := e.do(bearer(newRequest(t, http.MethodGet, outputsTarget("acme/infra", "stacks/prod/vpc", nil), nil), e.apiKey("ci")))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			body := errorOf(t, rec)
			assert.Equal(t, tc.code, body.Code)
			message := tc.message
			if message == "" {
				message = "refused: " + tc.code
			}
			assert.Equal(t, message, body.Message)
			assert.Nil(t, body.Details)
			assert.Equal(t, 1, e.runs.count())
		})
	}
}
