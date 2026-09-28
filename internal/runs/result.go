package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
)

// RecordResult records the outcome of one stack's plan, apply or drift
// check, updates its check run, blocks the dependents of a failed apply,
// and moves the run on: the next wave, the end of the run, drift issues.
// Posting the same result twice changes nothing.
func (s *Service) RecordResult(ctx context.Context, p principal.Principal, runID, stackKey string, res v1.StackResult) (*v1.RunStack, error) {
	run, repo, err := s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	stack, err := s.st.GetStackByKey(ctx, run.RepoID, stackKey)
	if err != nil {
		return nil, storeErr(err, "stack %s of %s", stackKey, repo.FullName)
	}
	row, err := s.st.GetRunStack(ctx, run.ID, stack.ID)
	if err != nil {
		return nil, storeErr(err, "stack %s in run %s", stackKey, run.ID)
	}
	claimRunID, err := s.authorizeResult(ctx, p, run, repo, &row)
	if err != nil {
		return nil, err
	}
	if res.Mode != run.Mode {
		return nil, &principal.InvalidError{Field: "mode", Reason: fmt.Sprintf("is %q, run %s is a %s run", res.Mode, run.ID, run.Mode)}
	}
	switch res.Status {
	case v1.ResultSuccess, v1.ResultFailure, v1.ResultError:
	default:
		return nil, &principal.InvalidError{Field: "status", Reason: fmt.Sprintf("%q is not success, failure or error", res.Status)}
	}
	target := resultStatus(run.Mode, res)
	if duplicateResult(row, target, res) {
		if err := s.advance(ctx, run.ID); err != nil {
			return nil, err
		}
		s.renderQuiet(ctx, run.ID, renderOpts{stacks: []uuid.UUID{stack.ID}})
		return s.runStackView(ctx, run.ID, stack.ID)
	}
	allowed := updatableStatuses(run.Mode)
	if !slices.Contains(allowed, row.Status) {
		return nil, principal.Wrap(principal.ErrConflict, "stack %s is already %s in run %s", stackKey, row.Status, run.ID)
	}
	patch := s.resultPatch(ctx, run, row, res, target, claimRunID)
	patch.IfStatus = allowed
	updated, err := s.st.UpdateRunStack(ctx, run.ID, stack.ID, patch)
	if errors.Is(err, store.ErrConflict) {
		current, gerr := s.st.GetRunStack(ctx, run.ID, stack.ID)
		if gerr == nil && duplicateResult(current, target, res) {
			return s.runStackView(ctx, run.ID, stack.ID)
		}
		return nil, principal.Wrap(principal.ErrConflict, "stack %s changed while its result was recorded", stackKey)
	}
	if err != nil {
		return nil, storeErr(err, "record result of %s", stackKey)
	}
	s.m.StackFinished(run.Mode, updated.Status, stackDuration(updated, res))
	changed := []uuid.UUID{stack.ID}
	if run.Mode == v1.ModeApply && updated.Status != v1.StackApplied {
		blocked, err := s.blockDependents(ctx, run, updated)
		if err != nil {
			return nil, err
		}
		changed = append(changed, blocked...)
	}
	if run.Mode == v1.ModeDrift && res.Status == v1.ResultSuccess && !res.Unconfirmed {
		if err := s.recordDrift(ctx, repo, run, stack, res); err != nil {
			return nil, err
		}
	}
	if err := s.advance(ctx, run.ID); err != nil {
		return nil, err
	}
	s.renderQuiet(ctx, run.ID, renderOpts{stacks: changed})
	return s.runStackView(ctx, run.ID, stack.ID)
}

func (s *Service) authorizeResult(ctx context.Context, p principal.Principal, run store.Run, repo store.Repo, row *store.RunStack) (int64, error) {
	if run.Status == v1.RunSuperseded {
		return 0, principal.Wrap(principal.ErrSuperseded, "run %s was superseded by a newer commit", run.ID)
	}
	switch p.Kind {
	case principal.OIDC:
		c, err := claimsOf(p)
		if err != nil {
			return 0, err
		}
		wr, _ := c.RunIDInt()
		switch c.EventName {
		case eventPullRequest:
			if err := checkRepoClaims(c, repo); err != nil {
				return 0, err
			}
			if run.Mode != v1.ModePlan {
				return 0, principal.Wrap(principal.ErrForbidden, "a pull_request job may only report plans")
			}
			return wr, checkPullClaims(c, run)
		case eventWorkflowDispatch:
			return wr, s.bindJob(ctx, run, repo, c, row)
		}
		return 0, principal.Wrap(principal.ErrForbidden, "claim event_name %q cannot report results", c.EventName)
	case principal.APIKey:
		if run.Trigger != v1.TriggerManual {
			return 0, principal.Wrap(principal.ErrForbidden, "an API key may only report results of manual runs")
		}
		return 0, nil
	}
	return 0, principal.Wrap(principal.ErrForbidden, "%s principals cannot report results", p.Kind)
}

func resultStatus(mode v1.RunMode, res v1.StackResult) v1.StackStatus {
	switch {
	case res.Unconfirmed:
		return v1.StackUnconfirmed
	case res.Status != v1.ResultSuccess:
		return v1.StackFailed
	case mode == v1.ModeApply:
		return v1.StackApplied
	}
	return v1.StackPlanned
}

func updatableStatuses(mode v1.RunMode) []v1.StackStatus {
	if mode == v1.ModeApply {
		return []v1.StackStatus{v1.StackPending, v1.StackPlanned, v1.StackApplying, v1.StackUnknown}
	}
	statuses := []v1.StackStatus{v1.StackPending, v1.StackPlanning, v1.StackUnknown}
	if mode == v1.ModePlan {
		statuses = append(statuses, v1.StackFailed)
	}
	return statuses
}

func duplicateResult(row store.RunStack, target v1.StackStatus, res v1.StackResult) bool {
	if row.Status != target || row.ExitCode == nil || *row.ExitCode != res.ExitCode || row.HasChanges != res.HasChanges {
		return false
	}
	if res.Artifact != "" && row.Mode != v1.ModeApply && row.PlanArtifact != res.Artifact {
		return false
	}
	return res.Summary == nil || reflect.DeepEqual(row.Summary, res.Summary)
}

func (s *Service) resultPatch(ctx context.Context, run store.Run, row store.RunStack, res v1.StackResult, target v1.StackStatus, claimRunID int64) store.RunStackPatch {
	p := store.RunStackPatch{
		Status:     &target,
		HasChanges: &res.HasChanges,
		ExitCode:   &res.ExitCode,
		ErrorText:  &res.ErrorText,
		Summary:    res.Summary,
	}
	if res.JobURL != "" {
		p.JobURL = &res.JobURL
	}
	if run.Mode != v1.ModeApply {
		if res.Artifact != "" {
			p.PlanArtifact = &res.Artifact
		}
		if claimRunID > 0 {
			p.PlanRunID = &claimRunID
		}
	}
	if res.DurationMS > 0 && row.StartedAt == nil {
		started := s.now().Add(-time.Duration(res.DurationMS) * time.Millisecond)
		p.StartedAt = &started
	}
	text := res.PlanText
	if row.PlanOutput == string(v1.PlanOutputSummary) {
		text = ""
	}
	if text == "" && res.PlanText == "" {
		return p
	}
	truncated := res.Truncated
	if text != "" && s.artifacts != nil {
		if url, ok := s.storeArtifacts(ctx, run, row.Key, text, res.Summary); ok {
			text, truncated = cutUTF8(text, artifactTextLimit), true
			p.PlanURL = &url
		}
	}
	text = cutUTF8(text, report.MaxPlanText)
	p.PlanText, p.PlanTextTruncated = &text, &truncated
	return p
}

func (s *Service) storeArtifacts(ctx context.Context, run store.Run, key, text string, summary *v1.PlanSummary) (string, bool) {
	prefix := "runs/" + run.ID.String() + "/" + strings.ReplaceAll(key, "/", "-") + "/"
	url, err := s.artifacts.Put(ctx, prefix+"plan.txt", "text/plain; charset=utf-8", []byte(text))
	if err != nil {
		s.log.WarnContext(ctx, "store plan text", "run_id", run.ID, "stack", key, "error", err)
		return "", false
	}
	if summary != nil {
		body, err := json.Marshal(summary)
		if err == nil {
			_, err = s.artifacts.Put(ctx, prefix+"plan.json", "application/json", body)
		}
		if err != nil {
			s.log.WarnContext(ctx, "store plan json", "run_id", run.ID, "stack", key, "error", err)
		}
	}
	return url, true
}

func stackDuration(rs store.RunStack, res v1.StackResult) time.Duration {
	if rs.StartedAt != nil && rs.FinishedAt != nil && rs.FinishedAt.After(*rs.StartedAt) {
		return rs.FinishedAt.Sub(*rs.StartedAt)
	}
	return time.Duration(res.DurationMS) * time.Millisecond
}

func (s *Service) runStackView(ctx context.Context, runID, stackID uuid.UUID) (*v1.RunStack, error) {
	rs, err := s.st.GetRunStack(ctx, runID, stackID)
	if err != nil {
		return nil, storeErr(err, "stack row of run %s", runID)
	}
	checks, err := s.st.ListChecks(ctx, runID)
	if err != nil {
		return nil, storeErr(err, "checks of run %s", runID)
	}
	out := rs.ToV1()
	for _, c := range checks {
		if c.StackID == stackID {
			out.Checks = append(out.Checks, c.ToV1())
		}
	}
	return &out, nil
}

func (s *Service) blockDependents(ctx context.Context, run store.Run, failed store.RunStack) ([]uuid.UUID, error) {
	if run.GraphID == nil {
		return nil, nil
	}
	g, err := s.st.GetGraphByID(ctx, *run.GraphID)
	if err != nil {
		return nil, storeErr(err, "graph of run %s", run.ID)
	}
	rows, err := s.st.GetRunStacks(ctx, run.ID)
	if err != nil {
		return nil, storeErr(err, "stacks of run %s", run.ID)
	}
	var out []uuid.UUID
	for _, rs := range blockedByFailure(g, rows, failed) {
		blocked := v1.StackBlocked
		by := rs.BlockedBy
		guard := []v1.StackStatus{v1.StackPending, v1.StackPlanned}
		if rs.Status == v1.StackBlocked {
			guard = []v1.StackStatus{v1.StackBlocked}
		}
		_, err := s.st.UpdateRunStack(ctx, run.ID, rs.StackID, store.RunStackPatch{Status: &blocked, BlockedBy: &by, IfStatus: guard})
		switch {
		case errors.Is(err, store.ErrConflict):
			continue
		case err != nil:
			return nil, storeErr(err, "block %s", rs.Key)
		}
		if rs.Status != v1.StackBlocked {
			s.m.StackFinished(run.Mode, v1.StackBlocked, 0)
		}
		out = append(out, rs.StackID)
	}
	return out, nil
}

// RecordCheck records the verdict of a named policy or cost check on a
// stack of a run and shows it as its own check run.
func (s *Service) RecordCheck(ctx context.Context, p principal.Principal, runID, stackKey, name string, verdict v1.CheckVerdict) (*v1.Check, error) {
	run, repo, err := s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if !validCheckName(name) {
		return nil, &principal.InvalidError{Field: "name", Reason: fmt.Sprintf("%q must be letters, digits, '-', '_' or '.'", name)}
	}
	switch verdict.Status {
	case v1.CheckPass, v1.CheckFail, v1.CheckWarn:
	default:
		return nil, &principal.InvalidError{Field: "status", Reason: fmt.Sprintf("%q is not pass, fail or warn", verdict.Status)}
	}
	stack, err := s.st.GetStackByKey(ctx, run.RepoID, stackKey)
	if err != nil {
		return nil, storeErr(err, "stack %s of %s", stackKey, repo.FullName)
	}
	row, err := s.st.GetRunStack(ctx, run.ID, stack.ID)
	if err != nil {
		return nil, storeErr(err, "stack %s in run %s", stackKey, run.ID)
	}
	if _, err := s.authorizeResult(ctx, p, run, repo, &row); err != nil {
		return nil, err
	}
	stored, err := s.st.UpsertCheck(ctx, store.Check{
		RunID: run.ID, StackID: stack.ID, Name: name, Status: verdict.Status,
		Summary: verdict.Summary, Details: verdict.Details, DetailsURL: verdict.DetailsURL,
	})
	if err != nil {
		return nil, storeErr(err, "record check %s on %s", name, stackKey)
	}
	s.renderPolicyCheck(ctx, run, repo, stored)
	s.renderQuiet(ctx, run.ID, renderOpts{stacks: []uuid.UUID{stack.ID}})
	out := stored.ToV1()
	return &out, nil
}

func validCheckName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !checkNameRune(r) {
			return false
		}
	}
	return true
}

func checkNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return r == '-' || r == '_' || r == '.'
}

func (s *Service) renderPolicyCheck(ctx context.Context, run store.Run, repo store.Repo, c store.Check) {
	if run.Trigger == v1.TriggerManual {
		return
	}
	conclusion := report.ConclusionSuccess
	switch c.Status {
	case v1.CheckFail:
		conclusion = report.ConclusionFailure
	case v1.CheckWarn:
		conclusion = report.ConclusionNeutral
	}
	title := string(c.Status)
	if c.Summary != "" {
		title += ": " + c.Summary
	}
	summary := "Verdict of the `" + report.Escape(c.Name) + "` check on `" + report.Escape(c.StackKey) + "`: **" + string(c.Status) + "**."
	if c.DetailsURL != "" {
		summary += " [Details](" + c.DetailsURL + ")"
	}
	out := report.CheckOutput{Title: title, Summary: summary, Text: c.Details, Status: report.StatusCompleted, Conclusion: conclusion}
	name := report.PolicyCheckName(c.Name, c.StackKey)
	cl, err := s.client(ctx, repo)
	if err != nil {
		s.log.WarnContext(ctx, "update policy check run", "run_id", run.ID, "check", name, "error", err)
		return
	}
	var ghErrs []error
	err = s.st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockKey(ctx, renderKey(run)); err != nil {
			return err
		}
		fresh, err := tx.GetRun(ctx, run.ID)
		if err != nil {
			return err
		}
		w := &checkWriter{s: s, tx: tx, c: cl, repo: repo}
		w.write(ctx, &fresh, name, out)
		ghErrs = w.errs
		return nil
	})
	if err = errors.Join(err, errors.Join(ghErrs...)); err != nil {
		s.log.WarnContext(ctx, "update policy check run", "run_id", run.ID, "check", name, "error", err)
	}
}
