package tf

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// Mask replaces every redacted value.
	Mask = "***"
	// MinSecretLength is the shortest extra literal NewRedactor and
	// MaskCommands act on; shorter values would mask unrelated text.
	MinSecretLength = 4
	// MaxPlanText is the cap on plan text sent to the server.
	MaxPlanText = 256 * 1024
)

const quotedValue = `"(?:[^"\\\n]|\\.)+"`

var (
	pemBegin   = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)
	pemEnd     = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)
	assignment = regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.-]*(?:password|passwd|secret|token)"?\s*[:=]\s*)` + quotedValue + `(\s*->\s*)?(` + quotedValue + `)?`)
)

var redactions = []func(string) string{
	maskPEM,
	replaceAll(`\beyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`, Mask),
	replaceAll(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`, Mask),
	replaceAll(`(?i)(\b(?:aws_?)?secret_?access_?key"?\s*[:=]\s*"?)[A-Za-z0-9/+=]{16,}`, "${1}"+Mask),
	replaceAll(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}\b`, Mask),
	replaceAll(`\bgithub_pat_[A-Za-z0-9_]{30,}\b`, Mask),
	replaceAll(`\bxox[abprs]-[A-Za-z0-9-]{10,}`, Mask),
	maskAssignments,
	replaceAll(`\b([A-Z0-9_]*(?:PASSWORD|PASSWD|SECRET|TOKEN)=)[^\s"']+`, "${1}"+Mask),
}

func replaceAll(pattern, repl string) func(string) string {
	re := regexp.MustCompile(pattern)
	return func(text string) string { return re.ReplaceAllString(text, repl) }
}

func maskPEM(text string) string {
	var b strings.Builder
	for {
		begin := pemBegin.FindStringIndex(text)
		if begin == nil {
			b.WriteString(text)
			return b.String()
		}
		b.WriteString(text[:begin[1]])
		b.WriteString(Mask)
		rest := text[begin[1]:]
		end := pemEnd.FindStringIndex(rest)
		if end == nil {
			return b.String()
		}
		b.WriteString(rest[end[0]:end[1]])
		text = rest[end[1]:]
	}
}

func maskAssignments(text string) string {
	return assignment.ReplaceAllStringFunc(text, func(m string) string {
		sub := assignment.FindStringSubmatch(m)
		out := sub[1] + `"` + Mask + `"`
		switch {
		case sub[2] != "" && sub[3] != "":
			out += sub[2] + `"` + Mask + `"`
		case sub[2] != "":
			out += sub[2]
		}
		return out
	})
}

// Redactor masks secrets in text bound for the server, the PR comment or the
// step summary. It is safe for concurrent use, and Redact is idempotent.
type Redactor struct {
	literals *strings.Replacer
}

// NewRedactor returns a Redactor that masks the built-in secret patterns and
// every extra literal of at least MinSecretLength bytes, including each line
// of a multi-line literal and its JSON-escaped form.
func NewRedactor(extra []string) *Redactor {
	seen := map[string]bool{}
	var lits []string
	add := func(s string) {
		if len(s) >= MinSecretLength && !seen[s] {
			seen[s] = true
			lits = append(lits, s)
		}
	}
	for _, s := range extra {
		add(s)
		if q := strconv.Quote(s); len(q) > 2 {
			add(q[1 : len(q)-1])
		}
		for _, line := range strings.Split(s, "\n") {
			add(strings.TrimSuffix(line, "\r"))
		}
	}
	slices.SortFunc(lits, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b))
	})
	r := &Redactor{}
	if len(lits) > 0 {
		pairs := make([]string, 0, 2*len(lits))
		for _, l := range lits {
			pairs = append(pairs, l, Mask)
		}
		r.literals = strings.NewReplacer(pairs...)
	}
	return r
}

// Redact returns text with AWS access key ids, AWS secret keys in key=value
// and JSON contexts, GitHub and Slack tokens, private key PEM blocks, JWTs,
// quoted values assigned to keys ending in password, passwd, secret or token
// in JSON and HCL, the same keys in upper case KEY=value form, and every
// extra literal replaced by Mask. A PEM block with no END line is masked to
// the end of the text.
func (r *Redactor) Redact(text string) string {
	if r != nil && r.literals != nil {
		text = r.literals.Replace(text)
	}
	for _, redact := range redactions {
		text = redact(text)
	}
	return text
}

// MaskCommands returns one `::add-mask::` workflow command per distinct
// line of at least MinSecretLength bytes across secrets, with the value
// escaped as the Actions runner expects.
func MaskCommands(secrets []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range secrets {
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if len(line) < MinSecretLength || seen[line] {
				continue
			}
			seen[line] = true
			out = append(out, "::add-mask::"+escapeCommandData(line))
		}
	}
	return out
}

func escapeCommandData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

const truncateNote = "\n[stackorder: output truncated, %d of %d bytes shown; the full text is in the job log]\n"

// Truncate caps text at maxBytes, cutting at the last line boundary that
// leaves room for a trailing note, and reports whether it cut anything. The
// result, note included, never exceeds maxBytes. A single line longer than
// the budget is cut at a UTF-8 boundary.
func Truncate(text string, maxBytes int) (string, bool) {
	if len(text) <= maxBytes {
		return text, false
	}
	budget := maxBytes - len(fmt.Sprintf(truncateNote, len(text), len(text)))
	if budget <= 0 {
		return "", true
	}
	cut := text[:budget]
	if i := strings.LastIndexByte(cut, '\n'); i >= 0 {
		cut = cut[:i]
	} else {
		for budget > 0 && !utf8.RuneStart(text[budget]) {
			budget--
		}
		cut = text[:budget]
	}
	return cut + fmt.Sprintf(truncateNote, len(cut), len(text)), true
}
