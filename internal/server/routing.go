package server

import (
	"context"
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/otel/attribute"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/worker"
)

type eventService interface {
	HandleInstallation(ctx context.Context, ev *gh.InstallationEvent) error
	HandleInstallationRepositories(ctx context.Context, ev *gh.InstallationRepositoriesEvent) error
	HandlePullRequest(ctx context.Context, ev *gh.PullRequestEvent) error
	HandlePullRequestReview(ctx context.Context, ev *gh.PullRequestReviewEvent) error
	HandleIssueComment(ctx context.Context, ev *gh.IssueCommentEvent) error
	HandlePush(ctx context.Context, ev *gh.PushEvent) error
	HandleCheckRun(ctx context.Context, ev *gh.CheckRunEvent) error
	HandleCheckSuite(ctx context.Context, ev *gh.CheckSuiteEvent) error
	HandleWorkflowRun(ctx context.Context, ev *gh.WorkflowRunEvent) error
	HandleWorkflowJob(ctx context.Context, ev *gh.WorkflowJobEvent) error
	HandleDeploymentProtectionRule(ctx context.Context, ev *gh.DeploymentProtectionRuleEvent) error
}

type jobService interface {
	HandleJob(ctx context.Context, kind string, payload json.RawMessage) error
}

type runService interface {
	eventService
	jobService
}

var _ runService = (*runs.Service)(nil)

type handlers interface {
	OnEvent(kind string, h worker.EventHandler)
	OnJob(kind string, h worker.JobHandler)
}

var _ handlers = (*worker.Pool)(nil)

type eventRoute func(ctx context.Context, ev any) error

func route[E any](handle func(context.Context, *E) error) eventRoute {
	return func(ctx context.Context, ev any) error {
		e, ok := ev.(*E)
		if !ok {
			return fmt.Errorf("server: event decoded as %T, want %T", ev, e)
		}
		return handle(ctx, e)
	}
}

func eventRoutes(svc eventService) map[string]eventRoute {
	return map[string]eventRoute{
		gh.EventInstallation:             route(svc.HandleInstallation),
		gh.EventInstallationRepositories: route(svc.HandleInstallationRepositories),
		gh.EventPullRequest:              route(svc.HandlePullRequest),
		gh.EventPullRequestReview:        route(svc.HandlePullRequestReview),
		gh.EventIssueComment:             route(svc.HandleIssueComment),
		gh.EventPush:                     route(svc.HandlePush),
		gh.EventCheckRun:                 route(svc.HandleCheckRun),
		gh.EventCheckSuite:               route(svc.HandleCheckSuite),
		gh.EventWorkflowRun:              route(svc.HandleWorkflowRun),
		gh.EventWorkflowJob:              route(svc.HandleWorkflowJob),
		gh.EventDeploymentProtectionRule: route(svc.HandleDeploymentProtectionRule),
	}
}

var jobKinds = []string{
	runs.JobDispatchWave,
	runs.JobDrift,
	runs.JobScheduleDrift,
	runs.JobCrossRepoPlan,
	runs.JobReconcile,
	runs.JobPrune,
	runs.JobStaleLocks,
	runs.JobSyncInstallations,
}

func (s *Server) register(svc runService) {
	s.registerOn(s.pool, svc)
}

func (s *Server) registerOn(h handlers, svc runService) {
	for kind, r := range eventRoutes(svc) {
		h.OnEvent(kind, s.eventHandler(kind, r))
	}
	for _, kind := range jobKinds {
		h.OnJob(kind, s.jobHandler(kind, svc))
	}
}

func (s *Server) eventHandler(kind string, r eventRoute) worker.EventHandler {
	return func(ctx context.Context, ev store.Event) error {
		return s.traced(ctx, "event "+kind, func(ctx context.Context) error {
			parsed, err := gh.ParseEvent(kind, ev.Payload)
			if err != nil {
				s.log.WarnContext(ctx, "dropping a webhook event that does not decode", "delivery", ev.ID, "event", kind, "error", err)
				return nil
			}
			return r(ctx, parsed)
		}, AttrEvent.String(kind), AttrDelivery.String(ev.ID))
	}
}

func (s *Server) jobHandler(kind string, svc jobService) worker.JobHandler {
	return func(ctx context.Context, job store.Job) error {
		attrs := []attribute.KeyValue{AttrJob.String(kind), AttrJobID.String(job.ID.String())}
		if id, ok := payloadRunID(job.Payload); ok {
			attrs = append(attrs, AttrRunID.String(id))
		}
		return s.traced(ctx, "job "+kind, func(ctx context.Context) error {
			payload := job.Payload
			if kind == runs.JobPrune {
				payload = s.prunePayload(payload)
			}
			return svc.HandleJob(ctx, kind, payload)
		}, attrs...)
	}
}

func (s *Server) prunePayload(payload json.RawMessage) json.RawMessage {
	var j runs.PruneJob
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &j); err != nil {
			return payload
		}
	}
	if j.PlanText <= 0 {
		j.PlanText = s.cfg.PlanTextRetention
	}
	if j.Events <= 0 {
		j.Events = s.cfg.EventRetention
	}
	if j.Drift <= 0 {
		j.Drift = s.cfg.DriftRetention
	}
	out, err := json.Marshal(j)
	if err != nil {
		return payload
	}
	return out
}
