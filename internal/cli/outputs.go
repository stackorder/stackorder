package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	// EnvGitHubOutput is the file step outputs are appended to.
	EnvGitHubOutput = "GITHUB_OUTPUT"
	// EnvGitHubStepSummary is the file the Markdown job summary is appended
	// to.
	EnvGitHubStepSummary = "GITHUB_STEP_SUMMARY"
)

type output struct {
	key, value string
}

func formatOutputs(outs []output) (string, error) {
	var b strings.Builder
	for _, o := range outs {
		if !strings.ContainsAny(o.value, "\r\n") {
			fmt.Fprintf(&b, "%s=%s\n", o.key, o.value)
			continue
		}
		delim, err := delimiter(o.value)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s<<%s\n%s\n%s\n", o.key, delim, o.value, delim)
	}
	return b.String(), nil
}

func delimiter(value string) (string, error) {
	for {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return "", fmt.Errorf("generating an output delimiter: %w", err)
		}
		d := "ghadelimiter_" + hex.EncodeToString(buf[:])
		if !strings.Contains(value, d) {
			return d, nil
		}
	}
}

func appendFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (a *app) setOutputs(outs ...output) error {
	path := os.Getenv(EnvGitHubOutput)
	if path == "" {
		return nil
	}
	text, err := formatOutputs(outs)
	if err != nil {
		return err
	}
	if err := appendFile(path, text); err != nil {
		return fmt.Errorf("writing step outputs: %w", err)
	}
	return nil
}

func (a *app) stepSummary(markdown string) {
	path := os.Getenv(EnvGitHubStepSummary)
	if path == "" || markdown == "" {
		return
	}
	if err := appendFile(path, markdown); err != nil {
		a.log.Warn("writing the step summary failed", "error", err)
	}
}

func jsonLine(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(data)
}
