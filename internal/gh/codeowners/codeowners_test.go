package codeowners

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const githubExample = `# These owners will be the default owners for everything in
# the repo. Unless a later match takes precedence,
# @global-owner1 and @global-owner2 will be requested for
# review when someone opens a pull request.
*       @global-owner1 @global-owner2

# Order is important; the last matching pattern takes the most
# precedence. When someone opens a pull request that only
# modifies JS files, only @js-owner and not the global
# owner(s) will be requested for a review.
*.js    @js-owner #This is an inline comment.

# You can also use email addresses if you prefer. They'll be
# used to look up users just like we do for commit author
# emails.
*.go docs@example.com

# Teams can be specified as code owners as well. Teams should
# be identified in the format @org/team-name. Teams must have
# explicit write access to the repository. In this example,
# the octocats team in the octo-org organization owns all .txt files.
*.txt @octo-org/octocats

# In this example, @doctocat owns any files in the build/logs
# directory at the root of the repository and any of its
# subdirectories.
/build/logs/ @doctocat

# The 'docs/*' pattern will match files like
# 'docs/getting-started.md' but not further nested files like
# 'docs/build-app/troubleshooting.md'.
docs/*  docs@example.com

# In this example, @octocat owns any file in an apps directory
# anywhere in your repository.
apps/ @octocat

# In this example, @doctocat owns any file in the '/docs'
# directory in the root of your repository and any of its
# subdirectories.
/docs/ @doctocat

# In this example, any change inside the '/scripts' directory
# will require approval from @doctocat or @octocat.
/scripts/ @doctocat @octocat

# In this example, @octocat owns any file in a '/logs' directory such as
# '/build/logs', '/scripts/logs', and '/deeply/nested/logs'. Any changes
# in a '/logs' directory will require approval from @octocat.
**/logs @octocat

# In this example, @octocat owns any file in the '/apps'
# directory in the root of your repository except for the '/apps/github'
# subdirectory, as its owners are left empty. Without an owner, changes
# to 'apps/github' can be made with the approval of any user who has
# write access to the repository.
/apps/ @octocat
/apps/github

# In this example, @octocat owns any file in the '/apps'
# directory in the root of your repository except for the '/apps/github'
# subdirectory, as this subdirectory has its own owner @doctocat
/apps/ @octocat
/apps/github @doctocat
`

func TestOwnersForGitHubDocumentationExample(t *testing.T) {
	f, err := Parse(strings.NewReader(githubExample))
	require.NoError(t, err)
	require.Empty(t, f.Errors)

	tests := []struct {
		path string
		want []string
	}{
		{"README.md", []string{"@global-owner1", "@global-owner2"}},
		{"src/app.js", []string{"@js-owner"}},
		{"main.go", []string{"docs@example.com"}},
		{"internal/pkg/file.go", []string{"docs@example.com"}},
		{"notes/todo.txt", []string{"@octo-org/octocats"}},
		{"build/logs/today.log", []string{"@octocat"}},
		{"build/other/today.log", []string{"@global-owner1", "@global-owner2"}},
		{"docs/getting-started.md", []string{"@doctocat"}},
		{"docs/build-app/troubleshooting.md", []string{"@doctocat"}},
		{"web/apps/index.html", []string{"@octocat"}},
		{"scripts/deploy.sh", []string{"@doctocat", "@octocat"}},
		{"scripts/logs/out.log", []string{"@octocat"}},
		{"deeply/nested/logs/x.log", []string{"@octocat"}},
		{"apps/github/main.rb", []string{"@doctocat"}},
		{"apps/other/main.rb", []string{"@octocat"}},
		{"./apps/other/main.rb", []string{"@octocat"}},
		{"/apps/other/main.rb", []string{"@octocat"}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, f.OwnersFor(tt.path))
		})
	}
}

func TestOwnersForPatternSemantics(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		path    string
		want    []string
		matched bool
	}{
		{"direct children only", "docs/* @d", "docs/a.md", []string{"@d"}, true},
		{"nested not matched by star", "docs/* @d", "docs/sub/a.md", nil, false},
		{"anchored middle slash", "stacks/prod @p", "stacks/prod/vpc/main.tf", []string{"@p"}, true},
		{"anchored does not float", "stacks/prod @p", "other/stacks/prod/main.tf", nil, false},
		{"unanchored name anywhere", "logs @l", "a/b/logs/x", []string{"@l"}, true},
		{"unanchored file name", "Makefile @m", "sub/Makefile", []string{"@m"}, true},
		{"directory pattern needs directory", "apps/ @a", "apps", nil, false},
		{"directory pattern matches contents", "apps/ @a", "x/apps/y/z", []string{"@a"}, true},
		{"double star middle zero dirs", "a/**/b @x", "a/b", []string{"@x"}, true},
		{"double star middle many dirs", "a/**/b @x", "a/1/2/b/c", []string{"@x"}, true},
		{"double star trailing", "a/** @x", "a/1/2", []string{"@x"}, true},
		{"double star alone", "** @x", "any/where", []string{"@x"}, true},
		{"leading double star", "**/prod/** @x", "stacks/prod/vpc/main.tf", []string{"@x"}, true},
		{"double star directory", "**/ @x", "stacks/prod/main.tf", []string{"@x"}, true},
		{"repeated double star", "**/** @x", "stacks/prod/main.tf", []string{"@x"}, true},
		{"repeated leading double star nested", "**/**/vpc @x", "stacks/prod/vpc/main.tf", []string{"@x"}, true},
		{"repeated leading double star at root", "**/**/vpc @x", "vpc/main.tf", []string{"@x"}, true},
		{"repeated middle double star", "a/**/**/b @x", "a/b", []string{"@x"}, true},
		{"stack directory under double star", "stacks/prod/** @p", "stacks/prod/vpc", []string{"@p"}, true},
		{"question mark", "file?.tf @q", "dir/file1.tf", []string{"@q"}, true},
		{"question mark single char", "file?.tf @q", "dir/file12.tf", nil, false},
		{"star inside segment", "stacks/*/vpc @s", "stacks/prod/vpc/main.tf", []string{"@s"}, true},
		{"star does not cross slash", "stacks/*/vpc @s", "stacks/prod/eu/vpc/main.tf", nil, false},
		{"escaped hash", "\\#notes @h", "#notes", []string{"@h"}, true},
		{"escaped space", "my\\ dir/ @s", "my dir/file", []string{"@s"}, true},
		{"case sensitive", "/Docs/ @d", "docs/a.md", nil, false},
		{"last match wins", "* @a\n/stacks/ @b", "stacks/x", []string{"@b"}, true},
		{"later unowned", "* @a\n/vendor/", "vendor/x", nil, true},
		{"literal dot", "*.tf @t", "maintf", nil, false},
		{"tab separated", "*.tf\t@t", "main.tf", []string{"@t"}, true},
		{"windows line endings", "*.tf @t\r\n", "main.tf", []string{"@t"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := Parse(strings.NewReader(tt.file))
			require.NoError(t, err)
			require.Empty(t, f.Errors)
			assert.Equal(t, tt.want, f.OwnersFor(tt.path))
			_, ok := f.RuleFor(tt.path)
			assert.Equal(t, tt.matched, ok)
		})
	}
}

func TestParseSkipsInvalidLines(t *testing.T) {
	src := strings.Join([]string{
		"* @everyone",
		"!negated @x",
		"[ab].tf @x",
		"/ @root",
		"*.tf not-an-owner",
		"*.md @",
		"*.sh @org/",
		"*.py @org/team/extra",
		"*.rb nobody@",
		"*.ts @acme/web user@example.com",
		"   # indented comment",
		"",
	}, "\n")
	f, err := Parse(strings.NewReader(src))
	require.NoError(t, err)

	lines := make([]int, 0, len(f.Errors))
	for _, e := range f.Errors {
		lines = append(lines, e.Line)
		assert.Contains(t, e.Error(), "CODEOWNERS line")
	}
	assert.Equal(t, []int{2, 3, 4, 5, 6, 7, 8, 9}, lines)
	require.Len(t, f.Rules, 2)
	assert.Equal(t, 1, f.Rules[0].Line)
	assert.Equal(t, 10, f.Rules[1].Line)
	assert.Equal(t, []string{"@acme/web", "user@example.com"}, f.OwnersFor("src/a.ts"))
	assert.Equal(t, []string{"@everyone"}, f.OwnersFor("negated"))
}

func TestParseReadError(t *testing.T) {
	_, err := Parse(iotest.ErrReader(errors.New("boom")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestOwnersForReturnsCopy(t *testing.T) {
	f, err := Parse(strings.NewReader("* @a @b"))
	require.NoError(t, err)
	got := f.OwnersFor("x")
	got[0] = "@mutated"
	assert.Equal(t, []string{"@a", "@b"}, f.OwnersFor("x"))
}

func TestNilFile(t *testing.T) {
	var f *File
	assert.Nil(t, f.OwnersFor("x"))
	_, ok := f.RuleFor("x")
	assert.False(t, ok)
}

func TestRuleMatch(t *testing.T) {
	f, err := Parse(strings.NewReader("/stacks/prod/ @acme/platform-prod"))
	require.NoError(t, err)
	require.Len(t, f.Rules, 1)
	r := f.Rules[0]
	assert.True(t, r.Match("stacks/prod/vpc/main.tf"))
	assert.False(t, r.Match("stacks/staging/vpc/main.tf"))
	assert.False(t, (&Rule{}).Match("x"))
}

func TestTeamOwner(t *testing.T) {
	tests := []struct {
		owner    string
		org, tm  string
		wantTeam bool
	}{
		{"@acme/platform-prod", "acme", "platform-prod", true},
		{"@octocat", "", "", false},
		{"docs@example.com", "", "", false},
		{"@acme/", "", "", false},
		{"@/team", "", "", false},
		{"@acme/a/b", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.owner, func(t *testing.T) {
			org, team, ok := TeamOwner(tt.owner)
			assert.Equal(t, tt.wantTeam, ok)
			assert.Equal(t, tt.org, org)
			assert.Equal(t, tt.tm, team)
		})
	}
}
