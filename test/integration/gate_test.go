//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/report"
)

const codeowners = "stacks/prod/** @acme/platform-prod\nstacks/staging/** @acme/platform-eng\nmodules/** @acme/platform-eng\n"

func TestApplyGateRefusals(t *testing.T) {
	tests := []struct {
		name  string
		opts  []fixtureOption
		setup func(t *testing.T, f *fixture, ev *gh.PullRequestEvent, p *planned)
		want  []string
	}{
		{
			name: "missing approvals",
			want: []string{"**Layer 2, approvals**: 0 of 1 required approvals on the head commit "},
		},
		{
			name: "no code owner approval",
			opts: []fixtureOption{
				withConfig(func(s string) string {
					return strings.Replace(s, "require_approvals: 1\n", "require_approvals: 1\n  require_codeowner_review: true\n", 1)
				}),
				withContents(".github/CODEOWNERS", codeowners),
			},
			setup: func(t *testing.T, f *fixture, ev *gh.PullRequestEvent, _ *planned) {
				f.approve(ev.Number, reviewer, ev.PullRequest.HeadSHA)
			},
			want: []string{"**Layer 2, code owner review** (`stacks/prod/apps`): needs an approving review on the head commit from a code owner: @acme/platform-prod"},
		},
		{
			name: "stale plan",
			setup: func(t *testing.T, f *fixture, ev *gh.PullRequestEvent, _ *planned) {
				f.co.at(ev.PullRequest.HeadSHA)
				next := f.co.edit("modules/vpc/variables.tf", "fix(vpc): describe the zones", func(s string) string { return s + "\n# Zones a to d.\n" })
				f.push(ev, next)
				f.approve(ev.Number, reviewer, next)
			},
			want: []string{"**Layer 3, plans for the head commit**: no plan exists for the head commit "},
		},
		{
			name: "failing policy check",
			setup: func(t *testing.T, f *fixture, ev *gh.PullRequestEvent, p *planned) {
				res := p.w.run("check", "--stack", prodVPC, "--run-id", p.runID, "--name", "policy", "--status", "fail", "--summary", "subnet d has no NACL")
				requireExit(t, 0, res)
				c := f.check(ev.PullRequest.HeadSHA, report.PolicyCheckName("policy", prodVPC))
				assert.Equal(t, report.ConclusionFailure, c.Conclusion)
				assert.Equal(t, "fail: subnet d has no NACL", c.Output.Title)
				f.approve(ev.Number, reviewer, ev.PullRequest.HeadSHA)
			},
			want: []string{"**Layer 4, policy checks** (`stacks/prod/vpc`): check `policy` failed: subnet d has no NACL"},
		},
		{
			name: "stack locked by another pull request",
			setup: func(t *testing.T, f *fixture, ev *gh.PullRequestEvent, _ *planned) {
				other := f.openPR(ev.Number+1, secondBranch(f), "feature/vpc-outputs")
				f.plan(other)
				f.approve(other.Number, reviewer, other.PullRequest.HeadSHA)
				cmd := f.comment(other.Number, applier, applyCommand)
				require.Equal(t, []string{gh.ReactionEyes, gh.ReactionRocket}, f.e.GH.Reactions(cmd.ID))
				require.Len(t, f.locks(), 5, "the other pull request holds every affected stack")
				f.approve(ev.Number, reviewer, ev.PullRequest.HeadSHA)
			},
			want: []string{"**Layer 5, locks** (`stacks/prod/vpc`): locked by #21 since "},
		},
		{
			name: "not in allowed teams",
			opts: []fixtureOption{withConfig(func(s string) string {
				return strings.Replace(s, "require_approvals: 1\n", "require_approvals: 1\n  allowed_teams: [platform-eng]\n", 1)
			})},
			setup: func(t *testing.T, f *fixture, ev *gh.PullRequestEvent, _ *planned) {
				f.approve(ev.Number, reviewer, ev.PullRequest.HeadSHA)
			},
			want: []string{"**Layer 1, authorization**", "carol is not an active member of `acme/platform-eng`, which apply.allowed_teams requires"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := shared(t)
			f := newFixture(t, e, "gate-"+strings.ReplaceAll(tt.name, " ", "-"), tt.opts...)
			const pr = 20
			ev := f.openPR(pr, f.co.head, "feature/vpc-subnet")
			p := f.plan(ev)
			if tt.setup != nil {
				tt.setup(t, f, ev, p)
			}
			dispatched := len(f.dispatches(""))
			cmd := f.comment(pr, applier, applyCommand)
			replies := f.botComments(pr, cmd.ID)
			require.Len(t, replies, 1, "one reply to the command")
			body := replies[0].Body
			assert.True(t, strings.HasPrefix(body, "**`stackorder apply` was refused.** Nothing was dispatched."), body)
			for _, want := range tt.want {
				assert.Contains(t, body, want)
			}
			assert.Equal(t, []string{gh.ReactionEyes}, e.GH.Reactions(cmd.ID), "a refused command is seen, not dispatched")
			assert.Len(t, f.dispatches(""), dispatched, "a refused apply dispatches nothing")
			var runs int
			require.NoError(t, e.Store.Pool().QueryRow(t.Context(),
				`SELECT count(*) FROM runs WHERE repo_id = $1 AND pr_number = $2 AND mode = 'apply'`, f.id, pr).Scan(&runs))
			assert.Zero(t, runs, "no apply run is created")
		})
	}

	t.Run("no push permission", func(t *testing.T) {
		e := shared(t)
		f := newFixture(t, e, "gate-reader")
		ev := f.openPR(30, f.co.head, "feature/vpc-subnet")
		f.plan(ev)
		f.approve(30, reviewer, f.co.head)
		cmd := f.comment(30, reader, applyCommand)
		assert.Empty(t, f.botComments(30, cmd.ID), "a command from a reader is ignored")
		assert.Empty(t, e.GH.Reactions(cmd.ID))
		assert.Empty(t, f.dispatches(""))
		entries := f.audit("command_ignored")
		require.Len(t, entries, 1)
		assert.Equal(t, reader, entries[0].Actor)
		assert.Equal(t, "no write permission", entries[0].Details["reason"])
	})

	t.Run("rate limit", func(t *testing.T) {
		e := shared(t)
		f := newFixture(t, e, "gate-rate-limit")
		ev := f.openPR(31, f.co.head, "feature/vpc-subnet")
		f.plan(ev)
		var last gh.Comment
		for i := range 11 {
			last = f.comment(31, applier, "stackorder help")
			if i < 10 {
				assert.Equal(t, []string{gh.ReactionEyes}, e.GH.Reactions(last.ID), "command %d is accepted", i+1)
			}
		}
		assert.Empty(t, e.GH.Reactions(last.ID), "the 11th command in a minute is dropped")
		replies := f.botComments(31, last.ID)
		require.Len(t, replies, 1)
		assert.Contains(t, replies[0].Body, "**`stackorder help` was refused.** This pull request sent more than 10 Stackorder commands in the last minute")
		accepted, limited := 0, 0
		for _, a := range f.audit("command") {
			switch a.Details["accepted"] {
			case true:
				accepted++
			case false:
				assert.Equal(t, "rate limited", a.Details["reason"])
				limited++
			}
		}
		assert.Equal(t, 10, accepted)
		assert.Equal(t, 1, limited)
	})
}
