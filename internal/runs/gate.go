package runs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/gh/codeowners"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
)

type gateLayers uint8

const (
	layerAuthorization gateLayers = 1 << iota
	layerApprovals
	layerPlans
	layerChecks
	layerLocks

	layersAll = layerAuthorization | layerApprovals | layerPlans | layerChecks | layerLocks
)

type gateInput struct {
	repo      store.Repo
	client    *gh.Client
	pr        int
	requester string
	keys      []string
	layers    gateLayers
	reviewSHA string
	pull      *gh.PullRequest
	stacks    []store.RunStack
}

type gateResult struct {
	failures []report.GateFailure
	pull     *gh.PullRequest
	view     planView
	keys     []string
}

// EvaluateApplyGate checks the apply gate for stackKeys of the pull
// request run belongs to, or for every stack its head commit planned when
// stackKeys is empty, and returns every failure at once. Policy is read
// from the default branch configuration stored for the repository, never
// from the pull request.
func (s *Service) EvaluateApplyGate(ctx context.Context, run *store.Run, requester string, stackKeys []string) ([]report.GateFailure, error) {
	if run == nil {
		return nil, errors.New("runs: apply gate: nil run")
	}
	repo, err := s.st.GetRepo(ctx, run.RepoID)
	if err != nil {
		return nil, storeErr(err, "repository of run %s", run.ID)
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return nil, err
	}
	res, err := s.gate(ctx, gateInput{repo: repo, client: c, pr: run.PRNumber, requester: requester, keys: stackKeys, layers: layersAll})
	if err != nil {
		return nil, err
	}
	return res.failures, nil
}

func (s *Service) gate(ctx context.Context, in gateInput) (gateResult, error) {
	var res gateResult
	cfg := repoConfig(in.repo)
	pull := in.pull
	if pull == nil && in.pr > 0 {
		p, err := in.client.GetPull(ctx, in.repo.FullName, in.pr)
		if err != nil {
			return res, fmt.Errorf("runs: pull request %s#%d: %w", in.repo.FullName, in.pr, err)
		}
		pull = p
	}
	res.pull = pull
	var stacks []store.RunStack
	if in.stacks != nil {
		stacks = in.stacks
		for _, rs := range stacks {
			res.keys = append(res.keys, rs.Key)
		}
	} else if pull != nil {
		view, err := planViewAt(ctx, s.st, in.repo.ID, in.pr, pull.HeadSHA)
		if err != nil {
			return res, err
		}
		res.view = view
		res.keys = in.keys
		if len(res.keys) == 0 {
			res.keys = view.keys()
		}
	}
	defaults := s.defaultStackConfigs(ctx, in.repo)
	if in.layers&layerAuthorization != 0 {
		f, err := s.gateAuthorization(ctx, in, cfg, defaults, res.keys)
		if err != nil {
			return res, err
		}
		res.failures = append(res.failures, f...)
	}
	if in.layers&layerApprovals != 0 && pull != nil {
		f, err := s.gateApprovals(ctx, in, cfg, pull, res.keys)
		if err != nil {
			return res, err
		}
		res.failures = append(res.failures, f...)
	}
	if in.layers&layerPlans != 0 && pull != nil {
		res.failures = append(res.failures, gatePlans(res.view, pull.HeadSHA, res.keys, cfg)...)
	}
	if in.layers&layerChecks != 0 {
		res.failures = append(res.failures, gateChecks(res.view, res.keys)...)
	}
	if in.layers&layerLocks != 0 {
		f, err := s.gateLocks(ctx, in, res.keys)
		if err != nil {
			return res, err
		}
		res.failures = append(res.failures, f...)
	}
	return res, nil
}

func (s *Service) defaultStackConfigs(ctx context.Context, repo store.Repo) map[string]*v1.StackConfig {
	out := map[string]*v1.StackConfig{}
	g, _, err := s.st.GetDefaultGraph(ctx, repo.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.WarnContext(ctx, "load default graph", "repo", repo.FullName, "error", err)
		}
		return out
	}
	for _, st := range g.Stacks {
		if !st.External && st.Config != nil {
			out[st.Key] = st.Config
		}
	}
	return out
}

func allowedTeams(cfg *v1.RepoConfig, defaults map[string]*v1.StackConfig, key string) []string {
	if sc := defaults[key]; sc != nil && sc.Apply != nil && len(sc.Apply.AllowedTeams) > 0 {
		return sc.Apply.AllowedTeams
	}
	return cfg.Apply.AllowedTeams
}

func splitTeam(owner, team string) (string, string) {
	team = strings.TrimPrefix(team, "@")
	if org, slug, ok := strings.Cut(team, "/"); ok {
		return org, slug
	}
	return owner, team
}

func (s *Service) gateAuthorization(ctx context.Context, in gateInput, cfg *v1.RepoConfig, defaults map[string]*v1.StackConfig, keys []string) ([]report.GateFailure, error) {
	if in.requester == "" || strings.HasPrefix(in.requester, "apikey:") {
		return nil, nil
	}
	owner, _, _ := strings.Cut(in.repo.FullName, "/")
	groups := map[string][]string{}
	var order []string
	add := func(teams []string, key string) {
		teams = slices.Sorted(slices.Values(teams))
		id := strings.Join(teams, ",")
		if _, ok := groups[id]; !ok {
			order = append(order, id)
			groups[id] = nil
		}
		if key != "" {
			groups[id] = append(groups[id], key)
		}
	}
	if len(keys) == 0 {
		add(cfg.Apply.AllowedTeams, "")
	}
	for _, key := range keys {
		add(allowedTeams(cfg, defaults, key), key)
	}
	var out []report.GateFailure
	for _, id := range order {
		stacks := groups[id]
		if id == "" {
			perm, err := s.permission(ctx, in.client, in.repo, in.requester)
			if err != nil {
				return nil, err
			}
			if !gh.HasPushPermission(perm) {
				out = append(out, report.GateFailure{Layer: report.LayerAuthorization, Stacks: stacks,
					Reason: fmt.Sprintf("%s has %s permission on %s; applying needs write access", in.requester, perm, in.repo.FullName)})
			}
			continue
		}
		teams := strings.Split(id, ",")
		member, err := s.inAnyTeam(ctx, in.client, owner, teams, in.requester)
		if err != nil {
			var apiErr *gh.APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
				out = append(out, report.GateFailure{Layer: report.LayerAuthorization, Stacks: stacks,
					Reason: "team membership could not be checked; the App needs the Members: Read permission for apply.allowed_teams"})
				continue
			}
			return nil, err
		}
		if !member {
			names := make([]string, len(teams))
			for i, t := range teams {
				org, slug := splitTeam(owner, t)
				names[i] = "`" + org + "/" + slug + "`"
			}
			out = append(out, report.GateFailure{Layer: report.LayerAuthorization, Stacks: stacks,
				Reason: fmt.Sprintf("%s is not an active member of %s, which apply.allowed_teams requires", in.requester, strings.Join(names, " or "))})
		}
	}
	return out, nil
}

func (s *Service) inAnyTeam(ctx context.Context, c *gh.Client, owner string, teams []string, login string) (bool, error) {
	for _, t := range teams {
		org, slug := splitTeam(owner, t)
		ok, err := s.teamMember(ctx, c, org, slug, login)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) gateApprovals(ctx context.Context, in gateInput, cfg *v1.RepoConfig, pull *gh.PullRequest, keys []string) ([]report.GateFailure, error) {
	var out []report.GateFailure
	switch {
	case pull.Merged || pull.MergedAt != nil:
		out = append(out, report.GateFailure{Layer: report.LayerApprovals, Reason: "the pull request is already merged"})
	case pull.State != gh.IssueOpen:
		out = append(out, report.GateFailure{Layer: report.LayerApprovals, Reason: "the pull request is closed; reopen it before applying"})
	case pull.Mergeable != nil && !*pull.Mergeable || pull.MergeableState == "dirty":
		out = append(out, report.GateFailure{Layer: report.LayerApprovals, Reason: "the pull request is not mergeable; resolve the conflicts with the base branch first"})
	}
	sha := firstNonEmpty(in.reviewSHA, pull.HeadSHA)
	reviews, err := in.client.ListReviews(ctx, in.repo.FullName, pull.Number)
	if err != nil {
		return nil, fmt.Errorf("runs: reviews of %s#%d: %w", in.repo.FullName, pull.Number, err)
	}
	approved, err := s.withPushPermission(ctx, in.client, in.repo, approvers(reviews, sha, pull.User.Login))
	if err != nil {
		return nil, err
	}
	if len(approved) < cfg.Apply.RequireApprovals {
		out = append(out, report.GateFailure{Layer: report.LayerApprovals,
			Reason: fmt.Sprintf("%d of %d required approvals on the head commit %s", len(approved), cfg.Apply.RequireApprovals, shortSHA(sha))})
	}
	if cfg.Apply.FourEyes && in.requester != "" && strings.EqualFold(in.requester, pull.User.Login) {
		out = append(out, report.GateFailure{Layer: report.LayerApprovals, Name: "four eyes",
			Reason: fmt.Sprintf("%s opened this pull request, and apply.four_eyes requires someone else to apply it", in.requester)})
	}
	if cfg.Apply.RequireCodeownerReview {
		f, err := s.gateCodeowners(ctx, in, approved, keys)
		if err != nil {
			return nil, err
		}
		out = append(out, f...)
	}
	return out, nil
}

func (s *Service) withPushPermission(ctx context.Context, c *gh.Client, repo store.Repo, logins []string) ([]string, error) {
	out := make([]string, 0, len(logins))
	for _, login := range logins {
		perm, err := s.permission(ctx, c, repo, login)
		if err != nil {
			return nil, err
		}
		if gh.HasPushPermission(perm) {
			out = append(out, login)
		}
	}
	return out, nil
}

func (s *Service) gateCodeowners(ctx context.Context, in gateInput, approved []string, keys []string) ([]report.GateFailure, error) {
	file, err := in.client.CodeownersFor(ctx, in.repo.FullName, in.repo.DefaultBranch)
	if errors.Is(err, gh.ErrNotFound) {
		return []report.GateFailure{{Layer: report.LayerApprovals, Name: "code owner review",
			Reason: "apply.require_codeowner_review is set but the default branch has no CODEOWNERS file"}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("runs: CODEOWNERS of %s: %w", in.repo.FullName, err)
	}
	var out []report.GateFailure
	for _, key := range keys {
		dir, _ := v1.SplitStackKey(key)
		owners := stackCodeowners(file.File, dir)
		if len(owners) == 0 {
			continue
		}
		ok, err := s.ownerApproved(ctx, in.client, owners, approved)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, report.GateFailure{Layer: report.LayerApprovals, Name: "code owner review", Stacks: []string{key},
				Reason: "needs an approving review on the head commit from a code owner: " + strings.Join(owners, ", ")})
		}
	}
	return out, nil
}

func (s *Service) ownerApproved(ctx context.Context, c *gh.Client, owners, approved []string) (bool, error) {
	for _, owner := range owners {
		if org, team, ok := codeowners.TeamOwner(owner); ok {
			for _, login := range approved {
				member, err := s.teamMember(ctx, c, org, team, login)
				if err != nil {
					return false, err
				}
				if member {
					return true, nil
				}
			}
			continue
		}
		if login, ok := strings.CutPrefix(owner, "@"); ok && slices.Contains(approved, strings.ToLower(login)) {
			return true, nil
		}
	}
	return false, nil
}

func gatePlans(view planView, head string, keys []string, cfg *v1.RepoConfig) []report.GateFailure {
	if len(view.runs) == 0 {
		return []report.GateFailure{{Layer: report.LayerPlans,
			Reason: fmt.Sprintf("no plan exists for the head commit %s; push a commit or comment `stackorder plan`", shortSHA(head))}}
	}
	var missing, failed, running []string
	for _, key := range keys {
		rs, ok := view.row(key)
		switch {
		case !ok:
			missing = append(missing, key)
		case rs.Status == v1.StackPending || rs.Status == v1.StackPlanning:
			running = append(running, key)
		case rs.Status != v1.StackPlanned:
			failed = append(failed, key)
		case cfg.Apply.FromPlanEnabled() && rs.PlanArtifact == "":
			missing = append(missing, key)
		}
	}
	var out []report.GateFailure
	if len(missing) > 0 {
		out = append(out, report.GateFailure{Layer: report.LayerPlans, Stacks: missing,
			Reason: fmt.Sprintf("no successful plan with an artifact for the head commit %s", shortSHA(head))})
	}
	if len(running) > 0 {
		out = append(out, report.GateFailure{Layer: report.LayerPlans, Stacks: running, Reason: "the plan is still running"})
	}
	if len(failed) > 0 {
		out = append(out, report.GateFailure{Layer: report.LayerPlans, Stacks: failed, Reason: "the plan did not succeed; fix it and plan again"})
	}
	return out
}

func gateChecks(view planView, keys []string) []report.GateFailure {
	var out []report.GateFailure
	for _, key := range keys {
		rs, ok := view.row(key)
		if !ok {
			continue
		}
		for _, c := range rs.Checks {
			if c.Status == v1.CheckFail {
				reason := fmt.Sprintf("check `%s` failed", c.Name)
				if c.Summary != "" {
					reason += ": " + report.Escape(c.Summary)
				}
				out = append(out, report.GateFailure{Layer: report.LayerChecks, Stacks: []string{key}, Reason: reason})
			}
		}
	}
	return out
}

func (s *Service) gateLocks(ctx context.Context, in gateInput, keys []string) ([]report.GateFailure, error) {
	locks, err := s.st.ListLocks(ctx, in.repo.ID)
	if err != nil {
		return nil, storeErr(err, "locks of %s", in.repo.FullName)
	}
	var out []report.GateFailure
	for _, l := range locks {
		if !slices.Contains(keys, l.StackKey) || in.pr > 0 && l.PRNumber == in.pr {
			continue
		}
		holder := "a manual run"
		if l.PRNumber > 0 {
			holder = fmt.Sprintf("#%d", l.PRNumber)
		}
		out = append(out, report.GateFailure{Layer: report.LayerLocks, Stacks: []string{l.StackKey},
			Reason: fmt.Sprintf("locked by %s since %s; release it there with `stackorder unlock`", holder, l.TakenAt.UTC().Format("2006-01-02 15:04 UTC"))})
	}
	return out, nil
}
