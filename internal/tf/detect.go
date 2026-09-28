package tf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Tool is the Terraform-compatible binary a stack is run with.
type Tool = v1.Tool

const (
	// EnvTerraformBin overrides the terraform binary Detect returns. It may be
	// a bare name looked up on PATH or a path to the binary.
	EnvTerraformBin = "STACKORDER_TERRAFORM_BIN"
	// EnvTofuBin overrides the tofu binary Detect returns, like EnvTerraformBin.
	EnvTofuBin = "STACKORDER_TOFU_BIN"
)

var (
	// ErrToolNotFound reports that the terraform or tofu binary is not on PATH
	// and no usable override is set.
	ErrToolNotFound = errors.New("tf: tool binary not found")
	// ErrUnknownTool reports a tool name other than terraform or tofu.
	ErrUnknownTool = errors.New("tf: unknown tool")
)

// Detect returns the absolute path of the binary for tool, honouring
// EnvTerraformBin and EnvTofuBin. An empty tool means terraform, the
// configuration default.
func Detect(tool v1.Tool) (string, error) {
	var name, envVar string
	switch tool {
	case v1.ToolTerraform, "":
		name, envVar = string(v1.ToolTerraform), EnvTerraformBin
	case v1.ToolTofu:
		name, envVar = string(v1.ToolTofu), EnvTofuBin
	default:
		return "", fmt.Errorf("%w: %q is not one of terraform, tofu", ErrUnknownTool, tool)
	}
	if override := os.Getenv(envVar); override != "" {
		name = override
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrToolNotFound, name, err)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("tf: resolving %s: %w", p, err)
	}
	return abs, nil
}

// Version runs `<binPath> version -json` and returns the reported version and
// whether the binary is OpenTofu. OpenTofu is recognised by the absence of
// the terraform_outdated key, which Terraform always emits and OpenTofu never
// does.
func Version(ctx context.Context, binPath string) (version string, isTofu bool, err error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, binPath, "version", "-json")
	cmd.Env = append(os.Environ(), automationEnv...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("tf: %s version -json: %w: %s", filepath.Base(binPath), err, firstLine(stderr.String()))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return "", false, fmt.Errorf("tf: decoding %s version -json: %w", filepath.Base(binPath), err)
	}
	var v string
	if msg, ok := raw["terraform_version"]; ok {
		if err := json.Unmarshal(msg, &v); err != nil {
			return "", false, fmt.Errorf("tf: decoding terraform_version: %w", err)
		}
	}
	if v == "" {
		return "", false, fmt.Errorf("tf: %s version -json reported no terraform_version", filepath.Base(binPath))
	}
	_, outdated := raw["terraform_outdated"]
	return v, !outdated, nil
}
