package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/worker"
)

type call struct {
	method  string
	event   any
	kind    string
	payload string
}

type fakeRuns struct {
	mu    sync.Mutex
	calls []call
	err   error
}

func (f *fakeRuns) record(method string, ev any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{method: method, event: ev})
	return f.err
}

func (f *fakeRuns) last(t *testing.T) call {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.calls)
	return f.calls[len(f.calls)-1]
}

func (f *fakeRuns) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRuns) HandleInstallation(_ context.Context, ev *gh.InstallationEvent) error {
	return f.record("HandleInstallation", ev)
}

func (f *fakeRuns) HandleInstallationRepositories(_ context.Context, ev *gh.InstallationRepositoriesEvent) error {
	return f.record("HandleInstallationRepositories", ev)
}

func (f *fakeRuns) HandlePullRequest(_ context.Context, ev *gh.PullRequestEvent) error {
	return f.record("HandlePullRequest", ev)
}

func (f *fakeRuns) HandlePullRequestReview(_ context.Context, ev *gh.PullRequestReviewEvent) error {
	return f.record("HandlePullRequestReview", ev)
}

func (f *fakeRuns) HandleIssueComment(_ context.Context, ev *gh.IssueCommentEvent) error {
	return f.record("HandleIssueComment", ev)
}

func (f *fakeRuns) HandlePush(_ context.Context, ev *gh.PushEvent) error {
	return f.record("HandlePush", ev)
}

func (f *fakeRuns) HandleCheckRun(_ context.Context, ev *gh.CheckRunEvent) error {
	return f.record("HandleCheckRun", ev)
}

func (f *fakeRuns) HandleCheckSuite(_ context.Context, ev *gh.CheckSuiteEvent) error {
	return f.record("HandleCheckSuite", ev)
}

func (f *fakeRuns) HandleWorkflowRun(_ context.Context, ev *gh.WorkflowRunEvent) error {
	return f.record("HandleWorkflowRun", ev)
}

func (f *fakeRuns) HandleWorkflowJob(_ context.Context, ev *gh.WorkflowJobEvent) error {
	return f.record("HandleWorkflowJob", ev)
}

func (f *fakeRuns) HandleDeploymentProtectionRule(_ context.Context, ev *gh.DeploymentProtectionRuleEvent) error {
	return f.record("HandleDeploymentProtectionRule", ev)
}

func (f *fakeRuns) HandleJob(_ context.Context, kind string, payload json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{method: "HandleJob", kind: kind, payload: string(payload)})
	return f.err
}

type registry struct {
	events map[string]worker.EventHandler
	jobs   map[string]worker.JobHandler
}

func (r *registry) OnEvent(kind string, h worker.EventHandler) { r.events[kind] = h }
func (r *registry) OnJob(kind string, h worker.JobHandler)     { r.jobs[kind] = h }

func routed(t *testing.T, svc runService) (*Server, *registry) {
	t.Helper()
	s := &Server{
		log: slog.New(slog.DiscardHandler),
		cfg: Config{PlanTextRetention: 10 * time.Hour, EventRetention: 20 * time.Hour, DriftRetention: 30 * time.Hour},
	}
	r := &registry{events: map[string]worker.EventHandler{}, jobs: map[string]worker.JobHandler{}}
	s.registerOn(r, svc)
	return s, r
}

func TestEveryEventKindHasItsHandler(t *testing.T) {
	tests := []struct {
		kind    string
		payload string
		method  string
		check   func(t *testing.T, ev any)
	}{
		{gh.EventInstallation, `{"action":"created","installation":{"id":7,"account":{"login":"acme"}}}`, "HandleInstallation", func(t *testing.T, ev any) {
			assert.Equal(t, int64(7), ev.(*gh.InstallationEvent).Installation.ID)
		}},
		{gh.EventInstallationRepositories, `{"action":"added","installation":{"id":7},"repositories_added":[{"id":1,"full_name":"acme/infra"}]}`, "HandleInstallationRepositories", func(t *testing.T, ev any) {
			assert.Equal(t, "acme/infra", ev.(*gh.InstallationRepositoriesEvent).RepositoriesAdded[0].FullName)
		}},
		{gh.EventPullRequest, `{"action":"opened","number":12}`, "HandlePullRequest", func(t *testing.T, ev any) {
			assert.Equal(t, 12, ev.(*gh.PullRequestEvent).Number)
		}},
		{gh.EventPullRequestReview, `{"action":"submitted","review":{"state":"approved"}}`, "HandlePullRequestReview", func(t *testing.T, ev any) {
			assert.Equal(t, "submitted", ev.(*gh.PullRequestReviewEvent).Action)
		}},
		{gh.EventIssueComment, `{"action":"created","comment":{"body":"stackorder plan"}}`, "HandleIssueComment", func(t *testing.T, ev any) {
			assert.Equal(t, "stackorder plan", ev.(*gh.IssueCommentEvent).Comment.Body)
		}},
		{gh.EventPush, `{"ref":"refs/heads/main","after":"abc"}`, "HandlePush", func(t *testing.T, ev any) {
			assert.Equal(t, "refs/heads/main", ev.(*gh.PushEvent).Ref)
		}},
		{gh.EventCheckRun, `{"action":"rerequested","check_run":{"id":5,"name":"stackorder/plan"}}`, "HandleCheckRun", func(t *testing.T, ev any) {
			assert.Equal(t, "rerequested", ev.(*gh.CheckRunEvent).Action)
		}},
		{gh.EventCheckSuite, `{"action":"completed"}`, "HandleCheckSuite", func(t *testing.T, ev any) {
			assert.Equal(t, "completed", ev.(*gh.CheckSuiteEvent).Action)
		}},
		{gh.EventWorkflowRun, `{"action":"completed","workflow_run":{"id":99}}`, "HandleWorkflowRun", func(t *testing.T, ev any) {
			assert.Equal(t, int64(99), ev.(*gh.WorkflowRunEvent).WorkflowRun.ID)
		}},
		{gh.EventWorkflowJob, `{"action":"in_progress","workflow_job":{"id":98,"run_id":99}}`, "HandleWorkflowJob", func(t *testing.T, ev any) {
			assert.Equal(t, int64(99), ev.(*gh.WorkflowJobEvent).WorkflowJob.RunID)
		}},
		{gh.EventDeploymentProtectionRule, `{"action":"requested","environment":"production"}`, "HandleDeploymentProtectionRule", func(t *testing.T, ev any) {
			assert.Equal(t, "production", ev.(*gh.DeploymentProtectionRuleEvent).Environment)
		}},
	}
	svc := &fakeRuns{}
	_, r := routed(t, svc)

	kinds := make([]string, 0, len(tests))
	for _, tt := range tests {
		kinds = append(kinds, tt.kind)
		t.Run(tt.kind, func(t *testing.T) {
			h, ok := r.events[tt.kind]
			require.True(t, ok, "no handler for %s", tt.kind)
			require.NoError(t, h(t.Context(), store.Event{ID: "delivery-" + tt.kind, Kind: tt.kind, Payload: json.RawMessage(tt.payload)}))
			got := svc.last(t)
			assert.Equal(t, tt.method, got.method)
			tt.check(t, got.event)
		})
	}

	subscribed := append([]string{gh.EventInstallation, gh.EventInstallationRepositories}, gh.DefaultEvents...)
	sort.Strings(subscribed)
	sort.Strings(kinds)
	assert.Equal(t, subscribed, kinds, "the table covers every event the App receives")
	registered := make([]string, 0, len(r.events))
	for k := range r.events {
		registered = append(registered, k)
	}
	sort.Strings(registered)
	assert.Equal(t, subscribed, registered, "exactly the subscribed events have handlers")
}

func TestUnhandledEventsAreLeftToThePool(t *testing.T) {
	_, r := routed(t, &fakeRuns{})
	for _, kind := range []string{gh.EventPing, "meta", "installation_target", "security_advisory"} {
		_, ok := r.events[kind]
		assert.False(t, ok, "%s must have no handler, so the pool completes it as ignored", kind)
	}
}

func TestUndecodableEventsAreDropped(t *testing.T) {
	svc := &fakeRuns{}
	_, r := routed(t, svc)
	err := r.events[gh.EventPullRequest](t.Context(), store.Event{ID: "d1", Kind: gh.EventPullRequest, Payload: json.RawMessage(`{"number":"twelve"}`)})
	require.NoError(t, err, "a payload that never decodes is not retried")
	assert.Zero(t, svc.count())
}

func TestHandlerErrorsReachThePool(t *testing.T) {
	boom := errors.New("github is down")
	svc := &fakeRuns{err: boom}
	_, r := routed(t, svc)
	err := r.events[gh.EventPush](t.Context(), store.Event{ID: "d1", Kind: gh.EventPush, Payload: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, boom, "the pool retries failed handlers")
	err = r.jobs[runs.JobReconcile](t.Context(), store.Job{ID: uuid.New(), Kind: runs.JobReconcile})
	require.ErrorIs(t, err, boom)
}

func TestEveryJobKindReachesTheRunService(t *testing.T) {
	kinds := []string{
		runs.JobDispatchWave,
		runs.JobDrift,
		runs.JobScheduleDrift,
		runs.JobCrossRepoPlan,
		runs.JobReconcile,
		runs.JobPrune,
		runs.JobStaleLocks,
		runs.JobSyncInstallations,
	}
	svc := &fakeRuns{}
	_, r := routed(t, svc)
	registered := make([]string, 0, len(r.jobs))
	for k := range r.jobs {
		registered = append(registered, k)
	}
	assert.ElementsMatch(t, kinds, registered, "every job kind of internal/runs has a handler")

	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			if kind == runs.JobPrune {
				return
			}
			payload := fmt.Sprintf(`{"run_id":"%s","wave":1}`, uuid.NewString())
			require.NoError(t, r.jobs[kind](t.Context(), store.Job{ID: uuid.New(), Kind: kind, Payload: json.RawMessage(payload)}))
			got := svc.last(t)
			assert.Equal(t, "HandleJob", got.method)
			assert.Equal(t, kind, got.kind)
			assert.JSONEq(t, payload, got.payload, "the payload is handed over unchanged")
		})
	}
}

func TestPruneJobsTakeTheConfiguredRetention(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    runs.PruneJob
	}{
		{"scheduled with no payload", "", runs.PruneJob{PlanText: 10 * time.Hour, Events: 20 * time.Hour, Drift: 30 * time.Hour}},
		{"empty object", "{}", runs.PruneJob{PlanText: 10 * time.Hour, Events: 20 * time.Hour, Drift: 30 * time.Hour}},
		{"explicit values win", `{"plan_text":3600000000000}`, runs.PruneJob{PlanText: time.Hour, Events: 20 * time.Hour, Drift: 30 * time.Hour}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeRuns{}
			_, r := routed(t, svc)
			require.NoError(t, r.jobs[runs.JobPrune](t.Context(), store.Job{ID: uuid.New(), Kind: runs.JobPrune, Payload: json.RawMessage(tt.payload)}))
			got := svc.last(t)
			var job runs.PruneJob
			require.NoError(t, json.Unmarshal([]byte(got.payload), &job))
			assert.Equal(t, tt.want, job)
		})
	}

	svc := &fakeRuns{}
	_, r := routed(t, svc)
	require.NoError(t, r.jobs[runs.JobPrune](t.Context(), store.Job{ID: uuid.New(), Kind: runs.JobPrune, Payload: json.RawMessage(`[1]`)}))
	assert.Equal(t, `[1]`, svc.last(t).payload, "an undecodable payload is left for the run service to refuse")
}

func TestPayloadRunID(t *testing.T) {
	id := uuid.NewString()
	tests := []struct {
		payload string
		want    string
		ok      bool
	}{
		{`{"run_id":"` + id + `","wave":2}`, id, true},
		{`{"repo_id":7}`, "", false},
		{``, "", false},
		{`not json`, "", false},
	}
	for _, tt := range tests {
		got, ok := payloadRunID(json.RawMessage(tt.payload))
		assert.Equal(t, tt.ok, ok, tt.payload)
		assert.Equal(t, tt.want, got, tt.payload)
	}
}
