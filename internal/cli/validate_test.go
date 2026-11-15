package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func bindValidate(t *testing.T, report *v1.ValidationReport, err error) {
	t.Helper()
	old := validateRepo
	t.Cleanup(func() { validateRepo = old })
	Bind(Deps{Validate: func(context.Context, string, string, string) (*v1.ValidationReport, error) {
		return report, err
	}})
}

func report(diags ...v1.Diagnostic) *v1.ValidationReport {
	r := &v1.ValidationReport{Diagnostics: diags, Valid: true}
	for _, d := range diags {
		switch d.Severity {
		case v1.SeverityError:
			r.Errors++
		case v1.SeverityWarning:
			r.Warnings++
		}
	}
	r.Valid = r.Errors == 0
	return r
}

func TestValidateValid(t *testing.T) {
	h := newHarness(t)
	bindValidate(t, report(), nil)
	r := h.run("validate")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Equal(t, "valid: no problems found\n", r.stdout)
}

func TestValidateErrorsExitOne(t *testing.T) {
	h := newHarness(t)
	bindValidate(t, report(
		v1.Diagnostic{Severity: v1.SeverityError, File: "stackorder.yaml", Line: 3, Message: `unknown field "bogus"`},
		v1.Diagnostic{Severity: v1.SeverityError, Message: "dependency cycle: stacks/a -> stacks/b -> stacks/a"},
	), nil)
	r := h.run("validate")
	require.Equal(t, 1, r.code)
	assert.Contains(t, r.stdout, `stackorder.yaml:3: error: unknown field "bogus"`)
	assert.Contains(t, r.stdout, "error: dependency cycle: stacks/a -> stacks/b -> stacks/a")
	assert.Contains(t, r.stdout, "invalid: 2 errors, 0 warnings")
}

func TestValidateWarningsExitZeroUnlessStrict(t *testing.T) {
	warn := report(v1.Diagnostic{Severity: v1.SeverityWarning, Message: "stacks/app: a var file is missing"})

	h := newHarness(t)
	bindValidate(t, warn, nil)
	r := h.run("validate")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "warning: stacks/app: a var file is missing")
	assert.Contains(t, r.stdout, "valid: 0 errors, 1 warning")

	h2 := newHarness(t)
	bindValidate(t, warn, nil)
	r2 := h2.run("validate", "--strict")
	require.Equal(t, 1, r2.code)
	assert.Contains(t, r2.stdout, "failing: 0 errors, 1 warning (--strict)")
	assert.NotContains(t, r2.stdout, "valid:")
}

func TestValidateJSON(t *testing.T) {
	h := newHarness(t)
	want := report(v1.Diagnostic{Severity: v1.SeverityError, File: "stackorder.yaml", Line: 3, Message: `unknown field "bogus"`})
	bindValidate(t, want, nil)
	r := h.run("--format", "json", "validate")
	require.Equal(t, 1, r.code)
	var got v1.ValidationReport
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &got))
	assert.Equal(t, *want, got)
}

func TestValidateRejectsDotFormat(t *testing.T) {
	h := newHarness(t)
	bindValidate(t, report(), nil)
	r := h.run("--format", "dot", "validate")
	require.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "--format dot is only supported by graph and affected")
}

func TestValidateUnlinked(t *testing.T) {
	old := validateRepo
	t.Cleanup(func() { validateRepo = old })
	Bind(Deps{})
	_, err := validateRepo(context.Background(), ".", "", "")
	assert.ErrorIs(t, err, ErrScanNotLinked)
}

func TestValidateAnnotations(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	var buf bytes.Buffer
	a := &app{stdout: &buf, stderr: &buf, format: formatText}
	a.annotateDiagnostic(v1.Diagnostic{Severity: v1.SeverityError, File: "stacks/a/.stackorder.yaml", Line: 4, Column: 2, Message: "bad: value"})
	a.annotateDiagnostic(v1.Diagnostic{Severity: v1.SeverityWarning, Message: "a warning"})
	out := buf.String()
	assert.Contains(t, out, "::error file=stacks/a/.stackorder.yaml,line=4,col=2::bad: value")
	assert.Contains(t, out, "::warning::a warning")
}

func TestValidateNoAnnotationsOutsideActions(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	var buf bytes.Buffer
	a := &app{stdout: &buf, stderr: &buf, format: formatText}
	a.annotateDiagnostic(v1.Diagnostic{Severity: v1.SeverityError, Message: "x"})
	assert.Empty(t, buf.String())
}
