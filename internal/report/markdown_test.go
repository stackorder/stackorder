package report

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestEscape(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain text", "plain text"},
		{"a | b", `a \| b`},
		{"line\nbreak\r\nand\rtab\t.", "line break and tab ."},
		{"*bold* _it_ `code`", "\\*bold\\* \\_it\\_ \\`code\\`"},
		{"[link](http://x)", `\[link\](http://x)`},
		{"<img src=x>", `\<img src=x\>`},
		{"<!-- hide -->", `\<!-- hide --\>`},
		{"a & b ~~strike~~ # h", `a \& b \~\~strike\~\~ \# h`},
		{`back\slash`, `back\\slash`},
		{"cc @acme/platform", "cc @\u200bacme/platform"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, Escape(tt.in))
		})
	}
}

func TestCodeSpans(t *testing.T) {
	tests := []struct {
		in, code, cell string
	}{
		{"stacks/a", "`stacks/a`", "`stacks/a`"},
		{"a`b", "``a`b``", "``a`b``"},
		{"`edge`", "`` `edge` ``", "`` `edge` ``"},
		{" padded ", "`  padded  `", "`  padded  `"},
		{"x|y", "`x|y`", "`x\\|y`"},
		{"multi\nline", "`multi line`", "`multi line`"},
		{"", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.code, code(tt.in))
			assert.Equal(t, tt.cell, codeCell(tt.in))
		})
	}
}

func TestLinks(t *testing.T) {
	run := baseRun(v1.RunPlanned)
	tests := []struct {
		name      string
		run       v1.Run
		opts      Options
		wantRun   string
		wantRepo  string
		wantPull  string
		wantStack string
	}{
		{name: "no options uses github.com", run: run, wantRepo: "https://github.com/acme/infra", wantPull: "https://github.com/acme/infra/pull/7"},
		{name: "base url trailing slash", run: run, opts: Options{BaseURL: "https://so.example.com/", RepoURL: "https://ghe.example.com/acme/infra/"},
			wantRun: "https://so.example.com/runs/" + runID, wantRepo: "https://ghe.example.com/acme/infra", wantPull: "https://ghe.example.com/acme/infra/pull/7", wantStack: "https://so.example.com/stacks/a%2Fb"},
		{name: "html url wins", run: func() v1.Run { r := run; r.HTMLURL = "https://ui.example.com/r/1"; return r }(), opts: Options{BaseURL: "https://so.example.com"},
			wantRun: "https://ui.example.com/r/1", wantRepo: "https://github.com/acme/infra", wantPull: "https://github.com/acme/infra/pull/7", wantStack: "https://so.example.com/stacks/a%2Fb"},
		{name: "no repo", run: v1.Run{}, wantRepo: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantRun, tt.opts.runURL(tt.run))
			assert.Equal(t, tt.wantRepo, tt.opts.repoURL(tt.run.Repo))
			assert.Equal(t, tt.wantPull, tt.opts.pullURL(tt.run.Repo, 7))
			assert.Equal(t, tt.wantStack, tt.opts.stackURL("a/b"))
		})
	}
	assert.Equal(t, "[x](https://e.com/a%20b%28c%29)", link("x", "https://e.com/a b(c)"))
	assert.Equal(t, "x", link("x", ""))
	assert.Equal(t, "", Options{}.commitURL("", "abc"))
}

func TestNames(t *testing.T) {
	assert.Equal(t, "stackorder/plan: stacks/prod/vpc", StackCheckName(CheckPlan, "stacks/prod/vpc"))
	assert.Equal(t, "stackorder/apply: stacks/prod/vpc:blue", StackCheckName(CheckApply, "stacks/prod/vpc:blue"))
	assert.Equal(t, "stackorder/policy: stacks/prod/vpc", PolicyCheckName("policy", "stacks/prod/vpc"))
}

func TestHelpers(t *testing.T) {
	assert.Equal(t, "abc…", clip("abcdefgh", 6))
	assert.Equal(t, "é…", clip("éééé", 5))
	assert.Equal(t, "é…", clip("éééé", 6))
	assert.Equal(t, "short", clip("short", 10))
	assert.Equal(t, "abc", shortSHA("abc"))
	assert.Empty(t, formatTime(v1.Run{}.CreatedAt))
	assert.Equal(t, "", capitalize(""))
	assert.Equal(t, "", tidy("\n\n"))
	assert.Equal(t, "- a\n- … and 2 more\n", bulletList([]string{"a", "b", "c"}, 1))
	assert.Equal(t, "`a`, and 1 more", codeList([]string{"a", "b"}, 1))
	assert.Equal(t, "a%2Fb%20c", pathEscape("a/b c"))
	assert.Equal(t, 4, waveCount(v1.Run{Stacks: []v1.RunStack{{Wave: 3}, {Wave: 1}}}))
	assert.Equal(t, "pending", runStatusWord(""))
	assert.Equal(t, "pending", stackStatusWord(v1.RunStack{}, Options{}))
	assert.Equal(t, "stk", lockKey(v1.LockInfo{StackID: "stk"}))
	assert.Equal(t, "`a` is locked", lockLine(v1.LockInfo{StackKey: "a"}, "", Options{}))
}

func TestSortLocksIsTotal(t *testing.T) {
	locks := []v1.LockInfo{{StackKey: "b", RunID: "2"}, {StackKey: "a", RunID: "9"}, {StackKey: "b", RunID: "1"}}
	sortLocks(locks)
	assert.Equal(t, []v1.LockInfo{{StackKey: "a", RunID: "9"}, {StackKey: "b", RunID: "1"}, {StackKey: "b", RunID: "2"}}, locks)
}

func TestPhaseFromStackStatuses(t *testing.T) {
	tests := []struct {
		name string
		run  v1.Run
		want phase
	}{
		{"plan run", baseRun(v1.RunPlanned, stack("a", "", 0, 0)), phasePlan},
		{"plan mode with applied stack", baseRun(v1.RunFailed, stack("a", "", 0, 0, withStatus(v1.StackApplied))), phaseApply},
		{"applying status", baseRun(v1.RunApplying), phaseApply},
		{"apply mode", applyRun(v1.RunPending), phaseApply},
		{"drift mode", func() v1.Run { r := applyRun(v1.RunApplied); r.Mode = v1.ModeDrift; return r }(), phaseDrift},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, phaseOf(tt.run))
		})
	}
}
