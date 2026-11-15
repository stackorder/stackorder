package validate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/validate"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
}

const backend = "terraform {\n  backend \"s3\" {}\n}\n"

func run(t *testing.T, dir string) *v1.ValidationReport {
	t.Helper()
	report, err := validate.Run(t.Context(), dir, validate.Options{Repo: "acme/infra"})
	require.NoError(t, err)
	return report
}

func messages(report *v1.ValidationReport, sev v1.DiagnosticSeverity) []string {
	var out []string
	for _, d := range report.Diagnostics {
		if d.Severity == sev {
			out = append(out, d.Message)
		}
	}
	return out
}

func TestValidRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 1\n")
	write(t, dir, "stacks/app/main.tf", backend)

	report := run(t, dir)
	assert.True(t, report.Valid)
	assert.Equal(t, 0, report.Errors)
	assert.Equal(t, 0, report.Warnings)
	assert.Equal(t, []v1.Diagnostic{}, report.Diagnostics)
}

func TestMissingRootFileIsValid(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stacks/app/main.tf", backend)

	report := run(t, dir)
	assert.True(t, report.Valid)
	assert.Empty(t, report.Diagnostics)
}

func TestRootConfigValueErrors(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 2\ntool: nope\n")
	write(t, dir, "stacks/app/main.tf", backend)

	report := run(t, dir)
	assert.False(t, report.Valid)
	for _, d := range report.Diagnostics {
		assert.Equal(t, "stackorder.yaml", d.File)
		assert.Equal(t, v1.SeverityError, d.Severity)
	}
	assert.ElementsMatch(t, []string{
		`version: unsupported value 2; only 1 is supported`,
		`tool: "nope" is not one of terraform, tofu`,
	}, messages(report, v1.SeverityError))
}

func TestUnknownFieldWithLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 1\nbogus: x\n")
	write(t, dir, "stacks/app/main.tf", backend)

	report := run(t, dir)
	require.Len(t, report.Diagnostics, 1)
	d := report.Diagnostics[0]
	assert.Equal(t, v1.SeverityError, d.Severity)
	assert.Equal(t, "stackorder.yaml", d.File)
	assert.Equal(t, 2, d.Line)
	assert.Equal(t, `unknown field "bogus"`, d.Message)
}

func TestErrorsAcrossMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 1\nbogus: x\n")
	write(t, dir, "stacks/a/main.tf", backend)
	write(t, dir, "stacks/a/.stackorder.yaml", "tool: badtool\n")
	write(t, dir, "stacks/b/main.tf", backend)
	write(t, dir, "stacks/b/.stackorder.yaml", "unknown_key: 1\n")

	report := run(t, dir)
	assert.False(t, report.Valid)
	files := map[string]bool{}
	for _, d := range report.Diagnostics {
		files[d.File] = true
	}
	assert.True(t, files["stackorder.yaml"])
	assert.True(t, files["stacks/a/.stackorder.yaml"])
	assert.True(t, files["stacks/b/.stackorder.yaml"])
}

func TestConfigErrorsSkipTheScan(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 1\n")
	write(t, dir, "stacks/a/main.tf", backend)
	write(t, dir, "stacks/a/.stackorder.yaml", "depends_on:\n  - stacks/ghost\n")
	write(t, dir, "stacks/b/main.tf", backend)
	write(t, dir, "stacks/b/.stackorder.yaml", "tool: badtool\n")

	report := run(t, dir)
	assert.False(t, report.Valid)
	assert.Empty(t, messages(report, v1.SeverityWarning), "the graph is not scanned while a file is invalid")
	assert.Equal(t, []string{`tool: "badtool" is not one of terraform, tofu`}, messages(report, v1.SeverityError))
}

func TestCycleIsAnError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 1\n")
	write(t, dir, "stacks/a/main.tf", backend)
	write(t, dir, "stacks/a/.stackorder.yaml", "depends_on:\n  - stacks/b\n")
	write(t, dir, "stacks/b/main.tf", backend)
	write(t, dir, "stacks/b/.stackorder.yaml", "depends_on:\n  - stacks/a\n")

	report := run(t, dir)
	assert.False(t, report.Valid)
	require.Len(t, report.Diagnostics, 1)
	d := report.Diagnostics[0]
	assert.Equal(t, v1.SeverityError, d.Severity)
	assert.Empty(t, d.File)
	assert.Contains(t, d.Message, "dependency cycle")
	assert.Contains(t, d.Message, "stacks/a")
	assert.Contains(t, d.Message, "stacks/b")
}

func TestScannerWarning(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 1\n")
	write(t, dir, "stacks/app/main.tf", backend)
	write(t, dir, "stacks/app/.stackorder.yaml", "depends_on:\n  - stacks/ghost\n")

	report := run(t, dir)
	assert.True(t, report.Valid)
	assert.Equal(t, 0, report.Errors)
	require.Len(t, report.Diagnostics, 1)
	assert.Equal(t, v1.SeverityWarning, report.Diagnostics[0].Severity)
	assert.Contains(t, report.Diagnostics[0].Message, "stacks/ghost")
}

func TestStackConfigElsewhereIsValidated(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 1\n")
	write(t, dir, "stacks/app/main.tf", backend)
	write(t, dir, "modules/vpc/.stackorder.yaml", "bogus: 1\n")

	report := run(t, dir)
	assert.False(t, report.Valid)
	require.Len(t, report.Diagnostics, 1)
	assert.Equal(t, "modules/vpc/.stackorder.yaml", report.Diagnostics[0].File)
}

func TestSkipsVendorDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 1\n")
	write(t, dir, "stacks/app/main.tf", backend)
	write(t, dir, ".terraform/modules/.stackorder.yaml", "bogus: 1\n")
	write(t, dir, "node_modules/pkg/.stackorder.yaml", "bogus: 1\n")

	report := run(t, dir)
	assert.True(t, report.Valid, "config files under skipped directories are not validated")
}

func TestDiagnosticsAreSorted(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 1\n")
	write(t, dir, "stacks/z/main.tf", backend)
	write(t, dir, "stacks/z/.stackorder.yaml", "bogus: 1\n")
	write(t, dir, "stacks/a/main.tf", backend)
	write(t, dir, "stacks/a/.stackorder.yaml", "bogus: 1\n")

	report := run(t, dir)
	require.Len(t, report.Diagnostics, 2)
	assert.Equal(t, "stacks/a/.stackorder.yaml", report.Diagnostics[0].File)
	assert.Equal(t, "stacks/z/.stackorder.yaml", report.Diagnostics[1].File)
}
