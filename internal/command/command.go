// Package command parses the comment commands people post on pull requests,
// such as "stackorder apply stacks/prod/vpc", and describes them for help
// output.
package command

import (
	"strings"
	"unicode"

	"github.com/stackorder/stackorder/internal/config"
)

// Prefix is the first token every comment command starts with, matched
// case-insensitively.
const Prefix = "stackorder"

// Verb is the action a comment command asks for.
type Verb string

const (
	// Plan re-plans the affected stacks, or the named subset.
	Plan Verb = "plan"
	// Apply applies the planned stacks wave by wave, or the named subset.
	Apply Verb = "apply"
	// Unlock releases orchestration locks held by the pull request.
	Unlock Verb = "unlock"
	// Help prints the list of commands.
	Help Verb = "help"
)

// Verbs returns every verb in the order the help text lists them.
func Verbs() []Verb { return []Verb{Plan, Apply, Unlock, Help} }

// Valid reports whether v is one of the known verbs.
func (v Verb) Valid() bool {
	switch v {
	case Plan, Apply, Unlock, Help:
		return true
	}
	return false
}

// TakesStacks reports whether the verb accepts stack keys.
func (v Verb) TakesStacks() bool { return v == Plan || v == Apply || v == Unlock }

// Usage returns the command synopsis, such as "stackorder apply [stack…]".
func (v Verb) Usage() string {
	if v.TakesStacks() {
		return Prefix + " " + string(v) + " [stack…]"
	}
	return Prefix + " " + string(v)
}

// Description returns a one-line explanation of what the verb does.
func (v Verb) Description() string {
	switch v {
	case Plan:
		return "Re-plan every affected stack, or only the named ones"
	case Apply:
		return "Apply the planned stacks wave by wave; a named subset still honours dependency order"
	case Unlock:
		return "Release the orchestration locks this pull request holds (write permission required)"
	case Help:
		return "Show this list of commands"
	}
	return ""
}

// Command is one parsed comment command.
type Command struct {
	// Verb is the requested action.
	Verb Verb
	// Stacks lists the normalised stack keys named after the verb, in the
	// order given and without duplicates; nil means every affected stack.
	// A token that normalises to nothing, such as ".", is kept as typed so
	// the server refuses it rather than widening the command to every stack.
	Stacks []string
	// Raw is the matching comment line with surrounding whitespace removed.
	Raw string
}

// String renders the command in its canonical form, such as
// "stackorder apply stacks/prod/vpc".
func (c Command) String() string {
	parts := make([]string, 0, 2+len(c.Stacks))
	parts = append(parts, Prefix, string(c.Verb))
	parts = append(parts, c.Stacks...)
	return strings.Join(parts, " ")
}

// Parse finds the first comment command in a pull request comment body. A
// line is a command when its first whitespace separated token is
// "stackorder" and its second is a known verb, both compared
// case-insensitively. Lines inside fenced code blocks, lines indented by
// four or more columns (indented code blocks) and quoted lines starting
// with ">" are ignored. The remaining tokens are stack keys,
// normalised with config.NormalizePath; commas also separate keys and
// enclosing backticks are dropped. ok is false when no line matches.
func Parse(body string) (cmd *Command, ok bool) {
	var fence fenceState
	for _, line := range strings.Split(body, "\n") {
		if fence.open {
			if fence.closes(line) {
				fence = fenceState{}
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ">") {
			continue
		}
		if f, opened := openFence(trimmed); opened {
			f.indent = indentation(line)
			fence = f
			continue
		}
		if indentation(line) >= 4 {
			continue
		}
		if c, matched := parseLine(trimmed); matched {
			return c, true
		}
	}
	return nil, false
}

func parseLine(line string) (*Command, bool) {
	tokens := strings.Fields(line)
	if len(tokens) < 2 || !strings.EqualFold(tokens[0], Prefix) {
		return nil, false
	}
	verb := Verb(strings.ToLower(tokens[1]))
	if !verb.Valid() {
		return nil, false
	}
	c := &Command{Verb: verb, Raw: line}
	if verb.TakesStacks() {
		c.Stacks = stackKeys(tokens[2:])
	}
	return c, true
}

func stackKeys(tokens []string) []string {
	var keys []string
	seen := map[string]bool{}
	for _, tok := range tokens {
		for _, part := range strings.FieldsFunc(tok, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
			key := config.NormalizePath(strings.Trim(part, "`"))
			if key == "" {
				key = part
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

type fenceState struct {
	open   bool
	char   byte
	width  int
	indent int
}

func openFence(trimmed string) (fenceState, bool) {
	if trimmed == "" || (trimmed[0] != '`' && trimmed[0] != '~') {
		return fenceState{}, false
	}
	c := trimmed[0]
	n := runLength(trimmed, c)
	if n < 3 {
		return fenceState{}, false
	}
	if c == '`' && strings.IndexByte(trimmed[n:], '`') >= 0 {
		return fenceState{}, false
	}
	return fenceState{open: true, char: c, width: n}, true
}

func (f fenceState) closes(line string) bool {
	if in := indentation(line); in >= 4 && in > f.indent {
		return false
	}
	t := strings.TrimSpace(line)
	n := runLength(t, f.char)
	return n >= f.width && n == len(t)
}

func indentation(line string) int {
	col := 0
	for _, r := range line {
		switch r {
		case ' ':
			col++
		case '\t':
			col += 4 - col%4
		default:
			return col
		}
	}
	return col
}

func runLength(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

// HelpText returns the Markdown reply to "stackorder help".
func HelpText() string {
	var b strings.Builder
	b.WriteString("**Stackorder commands**\n\n")
	b.WriteString("Post a command on a line of its own in a pull request comment. ")
	b.WriteString("Stack keys are repository relative paths such as `stacks/prod/vpc`, ")
	b.WriteString("with `:instance` appended to name one instance of the directory; ")
	b.WriteString("without keys a command covers every affected stack.\n\n")
	b.WriteString("| Command | What it does |\n| --- | --- |\n")
	for _, v := range Verbs() {
		b.WriteString("| `" + v.Usage() + "` | " + v.Description() + " |\n")
	}
	b.WriteString("\nLines inside code blocks and quoted replies are ignored.\n")
	return b.String()
}
