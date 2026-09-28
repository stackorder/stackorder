package oidc

import (
	"fmt"
	"path"
	"strings"
)

// DefaultWorkflowRefPattern pins job_workflow_ref to the canonical reusable
// workflows of stackorder/actions at a v1 tag.
const DefaultWorkflowRefPattern = "stackorder/actions/.github/workflows/*.yml@refs/tags/v1*"

// MatchWorkflowRef reports whether a job_workflow_ref claim, of the form
// "owner/repo/path/to/workflow.yml@ref", matches pattern.
//
// Both the claim and the pattern are split at their first "@" into a path
// part and a ref part, and both parts must match; a pattern or claim
// without "@", or an empty one, never matches.
//
// The path part is matched with path.Match: "*" matches any run of
// characters other than "/", "?" matches one character other than "/",
// "[...]" is a character class and "\" escapes the next character. So
// "stackorder/actions/.github/workflows/*.yml" matches
// "stackorder/actions/.github/workflows/plan.yml" but neither a workflow in
// a subdirectory nor the same file in another repository, such as a locally
// edited copy in the user's own repository.
//
// The ref part uses the same syntax except that "*" and "?" also match "/".
// So "refs/tags/v1*" matches refs/tags/v1, refs/tags/v1.4.2 and
// refs/tags/v1/rc, but also refs/tags/v10.0.0; it does not match
// refs/heads/v1 or refs/tags/v2.0.0.
//
// Matching is case sensitive and a malformed pattern never matches; see
// ValidateWorkflowRefPattern.
func MatchWorkflowRef(jobWorkflowRef, pattern string) bool {
	patternPath, patternRef, ok := strings.Cut(pattern, "@")
	if !ok {
		return false
	}
	refPath, ref, ok := strings.Cut(jobWorkflowRef, "@")
	if !ok || refPath == "" || ref == "" {
		return false
	}
	if matched, err := path.Match(patternPath, refPath); err != nil || !matched {
		return false
	}
	matched, err := path.Match(slashless(patternRef), slashless(ref))
	return err == nil && matched
}

// ValidateWorkflowRefPattern reports whether pattern is usable with
// MatchWorkflowRef: it must contain "@" with a non-empty, well-formed glob
// on each side. Servers call it on STACKORDER_REQUIRED_WORKFLOW_REF at start
// up so that a typo fails loudly instead of rejecting every result.
func ValidateWorkflowRefPattern(pattern string) error {
	patternPath, patternRef, ok := strings.Cut(pattern, "@")
	if !ok || patternPath == "" || patternRef == "" {
		return fmt.Errorf("oidc: workflow ref pattern %q is not of the form <path glob>@<ref glob>", pattern)
	}
	if _, err := path.Match(patternPath, ""); err != nil {
		return fmt.Errorf("oidc: workflow ref pattern %q: path: %w", pattern, err)
	}
	if _, err := path.Match(slashless(patternRef), ""); err != nil {
		return fmt.Errorf("oidc: workflow ref pattern %q: ref: %w", pattern, err)
	}
	return nil
}

// slashless lets path.Match treat "/" as an ordinary character; git refs never contain NUL.
func slashless(s string) string { return strings.ReplaceAll(s, "/", "\x00") }
