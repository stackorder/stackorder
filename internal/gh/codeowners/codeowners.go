// Package codeowners parses GitHub CODEOWNERS files and answers which owners
// GitHub requests for a path, following GitHub's documented semantics: the
// last matching pattern wins, patterns follow gitignore rules except that
// negation and character ranges are not supported, and a pattern with no
// owners leaves matching paths explicitly unowned.
package codeowners

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// Rule is one pattern line of a CODEOWNERS file.
type Rule struct {
	// Pattern is the path pattern exactly as written, escapes included.
	Pattern string
	// Owners lists @user, @org/team and email owners in file order. It is
	// empty for a pattern that removes ownership.
	Owners []string
	// Line is the 1-based line number in the file.
	Line int

	re *regexp.Regexp
}

// Match reports whether the rule's pattern matches a repository relative
// file path.
func (r *Rule) Match(path string) bool {
	return r.re != nil && r.re.MatchString(normalize(path))
}

// ParseError describes a line GitHub would skip as invalid.
type ParseError struct {
	Line   int
	Text   string
	Reason string
}

// Error implements the error interface.
func (e ParseError) Error() string {
	return fmt.Sprintf("CODEOWNERS line %d: %s: %q", e.Line, e.Reason, e.Text)
}

// File is a parsed CODEOWNERS file.
type File struct {
	// Rules are the valid pattern lines in file order.
	Rules []Rule
	// Errors lists the lines that were skipped, as GitHub skips them.
	Errors []ParseError
}

// Parse reads a CODEOWNERS file. Invalid lines are recorded in File.Errors
// and skipped; the returned error is only for read failures.
func Parse(r io.Reader) (*File, error) {
	f := &File{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		tokens := tokenize(line)
		if len(tokens) == 0 {
			continue
		}
		rule, reason := parseRule(tokens)
		if reason != "" {
			f.Errors = append(f.Errors, ParseError{Line: n, Text: strings.TrimSpace(line), Reason: reason})
			continue
		}
		rule.Line = n
		f.Rules = append(f.Rules, rule)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("codeowners: read: %w", err)
	}
	return f, nil
}

// OwnersFor returns the owners GitHub assigns to path: those of the last
// matching rule. It returns nil when no rule matches or when the last
// matching rule has no owners.
func (f *File) OwnersFor(path string) []string {
	r, ok := f.RuleFor(path)
	if !ok || len(r.Owners) == 0 {
		return nil
	}
	return append([]string(nil), r.Owners...)
}

// RuleFor returns the last rule matching path, which is the one GitHub
// applies.
func (f *File) RuleFor(path string) (*Rule, bool) {
	if f == nil {
		return nil, false
	}
	p := normalize(path)
	for i := len(f.Rules) - 1; i >= 0; i-- {
		if f.Rules[i].re.MatchString(p) {
			return &f.Rules[i], true
		}
	}
	return nil, false
}

// TeamOwner splits an "@org/team" owner into its organisation and team slug.
// ok is false for users and email owners.
func TeamOwner(owner string) (org, team string, ok bool) {
	rest, found := strings.CutPrefix(owner, "@")
	if !found {
		return "", "", false
	}
	org, team, found = strings.Cut(rest, "/")
	if !found || org == "" || team == "" || strings.Contains(team, "/") {
		return "", "", false
	}
	return org, team, true
}

func normalize(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	for strings.HasPrefix(path, "./") {
		path = path[2:]
	}
	return strings.TrimLeft(path, "/")
}

func tokenize(line string) []string {
	var tokens []string
	var cur strings.Builder
	inToken := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line):
			cur.WriteByte(c)
			cur.WriteByte(line[i+1])
			i++
			inToken = true
		case c == ' ' || c == '\t':
			if inToken {
				tokens = append(tokens, cur.String())
				cur.Reset()
				inToken = false
			}
		case c == '#' && !inToken:
			return tokens
		default:
			cur.WriteByte(c)
			inToken = true
		}
	}
	if inToken {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

func parseRule(tokens []string) (Rule, string) {
	pattern := tokens[0]
	if strings.HasPrefix(pattern, "!") {
		return Rule{}, "negated patterns are not supported"
	}
	if strings.ContainsAny(unescaped(pattern), "[]") {
		return Rule{}, "character ranges are not supported"
	}
	if strings.Trim(pattern, "/") == "" {
		return Rule{}, "empty pattern"
	}
	owners := tokens[1:]
	for _, o := range owners {
		if !validOwner(o) {
			return Rule{}, fmt.Sprintf("invalid owner %q", o)
		}
	}
	re, err := compile(pattern)
	if err != nil {
		return Rule{}, err.Error()
	}
	return Rule{Pattern: pattern, Owners: append([]string(nil), owners...), re: re}, ""
}

func unescaped(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' {
			i++
			continue
		}
		b.WriteByte(pattern[i])
	}
	return b.String()
}

func validOwner(o string) bool {
	if name, ok := strings.CutPrefix(o, "@"); ok {
		if name == "" {
			return false
		}
		if org, team, isTeam := strings.Cut(name, "/"); isTeam {
			return org != "" && team != "" && !strings.Contains(team, "/")
		}
		return true
	}
	local, domain, ok := strings.Cut(o, "@")
	return ok && local != "" && strings.Contains(domain, ".")
}

func compile(pattern string) (*regexp.Regexp, error) {
	segs := strings.Split(pattern, "/")
	switch {
	case segs[0] == "":
		segs = segs[1:]
	case len(segs) == 1 || (len(segs) == 2 && segs[1] == ""):
		if segs[0] != "**" {
			segs = append([]string{"**"}, segs...)
		}
	}
	if len(segs) > 1 && segs[len(segs)-1] == "" {
		segs[len(segs)-1] = "**"
	}
	segs = slices.CompactFunc(segs, func(a, b string) bool { return a == "**" && b == "**" })

	var re strings.Builder
	re.WriteString(`\A`)
	last := len(segs) - 1
	needSlash := false
	for i, seg := range segs {
		switch seg {
		case "**":
			switch {
			case last == 0:
				re.WriteString(`.+`)
			case i == 0:
				re.WriteString(`(?:.+/)?`)
				needSlash = false
			case i == last:
				re.WriteString(`/.*`)
			default:
				re.WriteString(`(?:/.+)?`)
				needSlash = true
			}
		case "*":
			if needSlash {
				re.WriteString(`/`)
			}
			re.WriteString(`[^/]+`)
			needSlash = true
		default:
			if needSlash {
				re.WriteString(`/`)
			}
			writeSegment(&re, seg)
			if i == last {
				re.WriteString(`(?:/.*)?`)
			}
			needSlash = true
		}
	}
	re.WriteString(`\z`)
	compiled, err := regexp.Compile(re.String())
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	return compiled, nil
}

func writeSegment(re *strings.Builder, seg string) {
	escape := false
	for _, ch := range seg {
		if escape {
			escape = false
			re.WriteString(regexp.QuoteMeta(string(ch)))
			continue
		}
		switch ch {
		case '\\':
			escape = true
		case '*':
			re.WriteString(`[^/]*`)
		case '?':
			re.WriteString(`[^/]`)
		default:
			re.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
}
