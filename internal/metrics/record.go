package metrics

import (
	"net/http"
	"strconv"
	"time"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// RunStatusChanged counts a run entering status.
func (r *Registry) RunStatusChanged(status v1.RunStatus, trigger v1.Trigger, mode v1.RunMode) {
	if r == nil {
		return
	}
	r.runs.WithLabelValues(string(status), string(trigger), string(mode)).Inc()
}

// StackFinished counts a stack reaching a terminal status and observes how
// long its job took when d is positive.
func (r *Registry) StackFinished(mode v1.RunMode, status v1.StackStatus, d time.Duration) {
	if r == nil {
		return
	}
	r.stackFinished.WithLabelValues(string(mode), string(status)).Inc()
	if d > 0 {
		r.stackDuration.WithLabelValues(string(mode)).Observe(d.Seconds())
	}
}

// Dispatched counts one workflow_dispatch call and whether GitHub accepted
// it.
func (r *Registry) Dispatched(mode v1.RunMode, ok bool) {
	if r == nil {
		return
	}
	result := "ok"
	if !ok {
		result = "error"
	}
	r.dispatches.WithLabelValues(string(mode), result).Inc()
}

// SetDriftedStacks sets the number of stacks whose latest drift check found
// drift.
func (r *Registry) SetDriftedStacks(n int) {
	if r == nil {
		return
	}
	r.driftedStacks.Set(float64(n))
}

// SetLocksHeld sets the number of orchestration locks held.
func (r *Registry) SetLocksHeld(n int) {
	if r == nil {
		return
	}
	r.locksHeld.Set(float64(n))
}

// CommandReceived counts a PR comment command and whether it was accepted.
func (r *Registry) CommandReceived(verb string, accepted bool) {
	if r == nil {
		return
	}
	r.commands.WithLabelValues(verb, strconv.FormatBool(accepted)).Inc()
}

// WebhookReceived counts a delivery of event whose signature verified.
func (r *Registry) WebhookReceived(event string) {
	if r == nil {
		return
	}
	r.webhookReceived.WithLabelValues(event).Inc()
}

// WebhookDuplicate counts a delivery whose id was already queued.
func (r *Registry) WebhookDuplicate() {
	if r == nil {
		return
	}
	r.webhookDuplicates.Inc()
}

// ObserveWebhookLag records the time between receiving a webhook and its
// first claim by a worker.
func (r *Registry) ObserveWebhookLag(d time.Duration) {
	if r == nil {
		return
	}
	r.webhookLag.Observe(max(d, 0).Seconds())
}

// EventProcessed counts one processing outcome of a queued event of kind;
// ResultDead also counts towards events_dead_total.
func (r *Registry) EventProcessed(kind string, result Result) {
	if r == nil {
		return
	}
	r.eventsProcessed.WithLabelValues(kind, string(result)).Inc()
	if result == ResultDead {
		r.eventsDead.WithLabelValues(kind).Inc()
	}
}

// JobProcessed counts one processing outcome of a queued job of kind.
func (r *Registry) JobProcessed(kind string, result Result) {
	if r == nil {
		return
	}
	r.jobsProcessed.WithLabelValues(kind, string(result)).Inc()
}

// SetQueueDepth sets the backlog of queue, QueueEvents or QueueJobs.
func (r *Registry) SetQueueDepth(queue string, n int64) {
	if r == nil {
		return
	}
	r.queueDepth.WithLabelValues(queue).Set(float64(n))
}

// SetSchedulerLeader records whether this instance leads the scheduler.
func (r *Registry) SetSchedulerLeader(leader bool) {
	if r == nil {
		return
	}
	v := 0.0
	if leader {
		v = 1
	}
	r.schedulerLeader.Set(v)
}

// ObserveRequest records one GitHub API attempt; it implements gh.Metrics.
// The route label is the method and the route template, such as
// "POST /repos/{owner}/{repo}/check-runs", matching the ServeMux pattern
// style used for the server's own routes.
func (r *Registry) ObserveRequest(method, route string, status int, d time.Duration) {
	if r == nil {
		return
	}
	label := normalizeMethod(method) + " " + route
	r.githubRequests.WithLabelValues(label, strconv.Itoa(status)).Inc()
	r.githubDuration.WithLabelValues(label).Observe(max(d, 0).Seconds())
}

// ObserveRateLimit records the remaining GitHub rate limit; it implements
// gh.Metrics.
func (r *Registry) ObserveRateLimit(remaining int, _ time.Time) {
	if r == nil {
		return
	}
	r.githubRateLimit.Set(float64(remaining))
}

func normalizeMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return m
	default:
		return "OTHER"
	}
}
