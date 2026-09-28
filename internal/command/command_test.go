package command

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		want   *Command
		wantOK bool
	}{
		{name: "empty body", body: ""},
		{name: "prose only", body: "Looks good to me, ship it."},
		{name: "bare plan", body: "stackorder plan", wantOK: true,
			want: &Command{Verb: Plan, Raw: "stackorder plan"}},
		{name: "bare apply", body: "stackorder apply", wantOK: true,
			want: &Command{Verb: Apply, Raw: "stackorder apply"}},
		{name: "bare unlock", body: "stackorder unlock", wantOK: true,
			want: &Command{Verb: Unlock, Raw: "stackorder unlock"}},
		{name: "help", body: "stackorder help", wantOK: true,
			want: &Command{Verb: Help, Raw: "stackorder help"}},
		{name: "help ignores trailing tokens", body: "stackorder help me please", wantOK: true,
			want: &Command{Verb: Help, Raw: "stackorder help me please"}},
		{name: "apply with stacks", body: "stackorder apply stacks/prod/vpc stacks/prod/eks", wantOK: true,
			want: &Command{Verb: Apply, Stacks: []string{"stacks/prod/vpc", "stacks/prod/eks"}, Raw: "stackorder apply stacks/prod/vpc stacks/prod/eks"}},
		{name: "prefix and verb case insensitive", body: "StackOrder APPLY stacks/prod/vpc", wantOK: true,
			want: &Command{Verb: Apply, Stacks: []string{"stacks/prod/vpc"}, Raw: "StackOrder APPLY stacks/prod/vpc"}},
		{name: "surrounding whitespace trimmed", body: "   stackorder   plan\t stacks/a  \r\n", wantOK: true,
			want: &Command{Verb: Plan, Stacks: []string{"stacks/a"}, Raw: "stackorder   plan\t stacks/a"}},
		{name: "indented code block", body: "Try this:\n\n    stackorder apply stacks/prod/vpc\n"},
		{name: "tab indented code block", body: "\tstackorder apply"},
		{name: "spaces and tab reach the code indent", body: "  \tstackorder apply"},
		{name: "indented code then real command", body: "    stackorder apply\n\nstackorder plan", wantOK: true,
			want: &Command{Verb: Plan, Raw: "stackorder plan"}},
		{name: "keys normalised", body: "stackorder plan ./stacks/prod/vpc/ stacks\\staging\\vpc /stacks/dev//vpc", wantOK: true,
			want: &Command{Verb: Plan, Stacks: []string{"stacks/prod/vpc", "stacks/staging/vpc", "stacks/dev/vpc"}, Raw: "stackorder plan ./stacks/prod/vpc/ stacks\\staging\\vpc /stacks/dev//vpc"}},
		{name: "workspace suffix kept", body: "stackorder apply stacks/prod/vpc:blue stacks/prod/vpc:default", wantOK: true,
			want: &Command{Verb: Apply, Stacks: []string{"stacks/prod/vpc:blue", "stacks/prod/vpc"}, Raw: "stackorder apply stacks/prod/vpc:blue stacks/prod/vpc:default"}},
		{name: "duplicates removed", body: "stackorder plan stacks/a ./stacks/a stacks/a/", wantOK: true,
			want: &Command{Verb: Plan, Stacks: []string{"stacks/a"}, Raw: "stackorder plan stacks/a ./stacks/a stacks/a/"}},
		{name: "commas and backticks", body: "stackorder unlock `stacks/a`, stacks/b,stacks/c", wantOK: true,
			want: &Command{Verb: Unlock, Stacks: []string{"stacks/a", "stacks/b", "stacks/c"}, Raw: "stackorder unlock `stacks/a`, stacks/b,stacks/c"}},
		{name: "keys that normalise to nothing are kept as typed", body: "stackorder apply . , / ./", wantOK: true,
			want: &Command{Verb: Apply, Stacks: []string{".", "/", "./"}, Raw: "stackorder apply . , / ./"}},
		{name: "empty backticks are kept as typed", body: "stackorder apply ``", wantOK: true,
			want: &Command{Verb: Apply, Stacks: []string{"``"}, Raw: "stackorder apply ``"}},
		{name: "separators alone name no stack", body: "stackorder plan , ,,", wantOK: true,
			want: &Command{Verb: Plan, Raw: "stackorder plan , ,,"}},
		{name: "command after prose", body: "Thanks for the review.\n\nstackorder apply stacks/prod/vpc\n", wantOK: true,
			want: &Command{Verb: Apply, Stacks: []string{"stacks/prod/vpc"}, Raw: "stackorder apply stacks/prod/vpc"}},
		{name: "first matching line wins", body: "stackorder plan stacks/a\nstackorder apply stacks/b", wantOK: true,
			want: &Command{Verb: Plan, Stacks: []string{"stacks/a"}, Raw: "stackorder plan stacks/a"}},
		{name: "unknown verb skipped for a later line", body: "stackorder aplly\nstackorder apply", wantOK: true,
			want: &Command{Verb: Apply, Raw: "stackorder apply"}},
		{name: "prose starting with the name", body: "Stackorder is great at this."},
		{name: "prefix alone", body: "stackorder"},
		{name: "prefix not first token", body: "please stackorder apply"},
		{name: "prefix glued to punctuation", body: "stackorder: apply\n@stackorder apply\n/stackorder apply"},
		{name: "prefix inside inline code", body: "Run `stackorder apply` when ready."},
		{name: "list item", body: "- stackorder apply"},
		{name: "quoted reply", body: "> stackorder apply stacks/prod/vpc\n\nWhy did this fail?"},
		{name: "nested quote with leading space", body: "  >> stackorder apply"},
		{name: "quote then real command", body: "> stackorder apply\n\nstackorder plan", wantOK: true,
			want: &Command{Verb: Plan, Raw: "stackorder plan"}},
		{name: "backtick fence", body: "```\nstackorder apply\n```"},
		{name: "tilde fence with info string", body: "~~~ text\nstackorder apply\n~~~"},
		{name: "command after fence", body: "```sh\nstackorder apply stacks/a\n```\nstackorder plan stacks/b", wantOK: true,
			want: &Command{Verb: Plan, Stacks: []string{"stacks/b"}, Raw: "stackorder plan stacks/b"}},
		{name: "longer fence needs longer close", body: "````\n```\nstackorder apply\n````\nstackorder unlock", wantOK: true,
			want: &Command{Verb: Unlock, Raw: "stackorder unlock"}},
		{name: "mismatched fence char does not close", body: "```\n~~~\nstackorder apply\n"},
		{name: "closing fence with trailing text does not close", body: "```\n``` not closed\nstackorder apply\n"},
		{name: "unclosed fence swallows the rest", body: "```hcl\nresource {}\nstackorder apply"},
		{name: "indented fence in a list", body: "- step:\n  ```\n  stackorder apply\n  ```\n"},
		{name: "fence line indented as code does not close", body: "```\n    ```\nstackorder apply\n```\n"},
		{name: "closing fence indented up to three spaces", body: "```\nstackorder apply\n   ```\nstackorder plan", wantOK: true,
			want: &Command{Verb: Plan, Raw: "stackorder plan"}},
		{name: "fence nested in a list closes at its own indent", body: "1. a\n   - b\n\n     ```\n     stackorder apply\n     ```\n\nstackorder unlock", wantOK: true,
			want: &Command{Verb: Unlock, Raw: "stackorder unlock"}},
		{name: "backticks in info string are not a fence", body: "``` `x`\nstackorder plan", wantOK: true,
			want: &Command{Verb: Plan, Raw: "stackorder plan"}},
		{name: "two backticks are not a fence", body: "``\nstackorder help", wantOK: true,
			want: &Command{Verb: Help, Raw: "stackorder help"}},
		{name: "quoted fence does not open", body: "> ```\nstackorder help", wantOK: true,
			want: &Command{Verb: Help, Raw: "stackorder help"}},
		{name: "windows line endings", body: "hi\r\nstackorder apply stacks/a\r\n", wantOK: true,
			want: &Command{Verb: Apply, Stacks: []string{"stacks/a"}, Raw: "stackorder apply stacks/a"}},
		{name: "non-breaking space separates tokens", body: "stackorder plan stacks/a", wantOK: true,
			want: &Command{Verb: Plan, Stacks: []string{"stacks/a"}, Raw: "stackorder plan stacks/a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.body)
			require.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestVerbs(t *testing.T) {
	tests := []struct {
		verb        Verb
		valid       bool
		takesStacks bool
		usage       string
	}{
		{Plan, true, true, "stackorder plan [stack…]"},
		{Apply, true, true, "stackorder apply [stack…]"},
		{Unlock, true, true, "stackorder unlock [stack…]"},
		{Help, true, false, "stackorder help"},
		{Verb("destroy"), false, false, "stackorder destroy"},
		{Verb(""), false, false, "stackorder "},
	}
	for _, tt := range tests {
		t.Run(string(tt.verb), func(t *testing.T) {
			assert.Equal(t, tt.valid, tt.verb.Valid())
			assert.Equal(t, tt.takesStacks, tt.verb.TakesStacks())
			assert.Equal(t, tt.usage, tt.verb.Usage())
			if tt.valid {
				assert.NotEmpty(t, tt.verb.Description())
			} else {
				assert.Empty(t, tt.verb.Description())
			}
		})
	}
	assert.Equal(t, []Verb{Plan, Apply, Unlock, Help}, Verbs())
}

func TestCommandString(t *testing.T) {
	tests := []struct {
		cmd  Command
		want string
	}{
		{Command{Verb: Plan}, "stackorder plan"},
		{Command{Verb: Apply, Stacks: []string{"stacks/a", "stacks/b:blue"}}, "stackorder apply stacks/a stacks/b:blue"},
		{Command{Verb: Help}, "stackorder help"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cmd.String())
			parsed, ok := Parse(tt.cmd.String())
			require.True(t, ok)
			assert.Equal(t, tt.cmd.Verb, parsed.Verb)
			assert.Equal(t, tt.cmd.Stacks, parsed.Stacks)
		})
	}
}

func TestHelpText(t *testing.T) {
	help := HelpText()
	for _, v := range Verbs() {
		assert.Contains(t, help, "`"+v.Usage()+"`")
		assert.Contains(t, help, v.Description())
	}
	_, ok := Parse(help)
	assert.False(t, ok, "help text must not parse as a command")
	assert.True(t, strings.HasSuffix(help, "\n"))
}
