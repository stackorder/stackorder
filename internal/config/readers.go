package config

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/bmatcuk/doublestar/v4"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const globMetacharacters = `*?[]{}\`

// ReaderAllowed reports whether readers, the resolved outputs.readers of a
// stack of producerRepo, let the stack readerKey of readerRepo read that
// stack's outputs. An entry without "//" is a pattern over the stack keys of
// producerRepo; "owner/repo//pattern" is one over the stack keys of that
// repository. Repositories compare without regard to case. A pattern is a
// doublestar glob over the whole key, where "*" does not cross "/", "**"
// crosses "/" only as a whole path segment and ":" is an ordinary
// character: "path:*" matches every instance of a stack, and
// "stacks/**/*:blue" every blue instance under stacks, while
// "stacks/**:blue" acts as "stacks/*:blue". An invalid entry never matches,
// nor does one spelled unlike a stack key ("./a", "a/", "a:default"), nor
// any entry of a nil or empty list.
func ReaderAllowed(readers []string, producerRepo, readerRepo, readerKey string) bool {
	for _, entry := range readers {
		repo, pattern, err := parseReader(entry)
		if err != nil {
			continue
		}
		if repo == "" {
			repo = producerRepo
		}
		if !strings.EqualFold(repo, readerRepo) {
			continue
		}
		if ok, err := doublestar.Match(pattern, readerKey); err == nil && ok {
			return true
		}
	}
	return false
}

func parseReader(entry string) (repo, pattern string, err error) {
	switch trimmed := strings.TrimSpace(entry); trimmed {
	case "":
		return "", "", errors.New("empty entry")
	case entry:
	default:
		return "", "", fmt.Errorf("%q: never matches a stack key; write %q", entry, trimmed)
	}
	repo, pattern, qualified := strings.Cut(entry, "//")
	if !qualified {
		repo, pattern = "", entry
	}
	owner, name, _ := strings.Cut(repo, "/")
	switch {
	case qualified && (owner == "" || name == "" || strings.Contains(name, "/") || strings.ContainsFunc(repo, unicode.IsSpace)):
		return "", "", fmt.Errorf("%q: repository must be owner/repo", entry)
	case strings.ContainsAny(repo, globMetacharacters):
		return "", "", fmt.Errorf("%q: repository %q must be literal; only the stack key may be a pattern", entry, repo)
	case pattern == "":
		return "", "", fmt.Errorf("%q: empty stack key pattern", entry)
	case !doublestar.ValidatePattern(pattern):
		return "", "", fmt.Errorf("%q: %q is not a valid glob", entry, pattern)
	}
	canonical, ok := canonicalPattern(pattern)
	switch {
	case !ok:
		return "", "", fmt.Errorf("%q: stack key pattern must be repository relative", entry)
	case canonical != pattern:
		if qualified {
			canonical = v1.QualifiedStackKey(repo, canonical)
		}
		return "", "", fmt.Errorf("%q: never matches a stack key; write %q", entry, canonical)
	}
	return repo, pattern, nil
}

func canonicalPattern(pattern string) (string, bool) {
	dir, instance := v1.SplitStackKey(pattern)
	if dir != "" {
		dir = path.Clean(dir)
	}
	if strings.HasPrefix(dir, "/") || dir == "." || dir == ".." || strings.HasPrefix(dir, "../") {
		return "", false
	}
	if strings.EqualFold(instance, v1.DefaultEnvironment) {
		instance = ""
	}
	key := v1.StackKey(dir, instance)
	return key, key != ""
}
