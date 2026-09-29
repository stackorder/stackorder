//go:build unix

package tf

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const fakeScript = `#!/bin/sh
trap 'kill "$sleeper" 2>/dev/null; printf "interrupted\n" >&2; exit 130' INT
{
  printf 'cwd=%s\n' "$PWD"
  for a in "$@"; do printf 'arg=%s\n' "$a"; done
  env | grep -E '^(TF_|CHECKPOINT_|FAKE_EXTRA)' | sort | sed 's/^/env=/'
  printf -- '--\n'
} >> "$FAKE_TF_LOG"
if [ -n "$FAKE_TF_STDOUT" ]; then cat "$FAKE_TF_STDOUT"; fi
if [ -n "$FAKE_TF_STDERR" ]; then printf '%s\n' "$FAKE_TF_STDERR" >&2; fi
if [ -n "$FAKE_TF_SLEEP" ]; then
  sleep "$FAKE_TF_SLEEP" >/dev/null 2>&1 &
  sleeper=$!
  wait "$sleeper"
fi
exit "${FAKE_TF_EXIT:-0}"
`

var fakeBinDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stackorder-faketf-")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{"terraform", "tofu"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(fakeScript), 0o755); err != nil {
			panic(err)
		}
	}
	fakeBinDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

const (
	execTimeout      = 30 * time.Second
	interruptTimeout = 5 * time.Second
)

func execContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), execTimeout)
	t.Cleanup(cancel)
	return ctx
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fakeTF struct {
	t      *testing.T
	bin    string
	dir    string
	log    string
	output *syncBuffer
}

type invocation struct {
	Dir  string
	Args []string
	Env  map[string]string
}

func newFakeTF(t *testing.T, name string) *fakeTF {
	t.Helper()
	f := &fakeTF{
		t:      t,
		bin:    filepath.Join(fakeBinDir, name),
		dir:    t.TempDir(),
		log:    filepath.Join(t.TempDir(), "argv.log"),
		output: &syncBuffer{},
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		argv, _ := os.ReadFile(f.log)
		t.Logf("fake %s invocations:\n%s\noutput:\n%s", name, argv, f.output.String())
	})
	return f
}

func (f *fakeTF) ctx() context.Context {
	return execContext(f.t)
}

func (f *fakeTF) runner(env ...string) *Runner {
	return &Runner{
		Bin:              f.bin,
		Dir:              f.dir,
		Env:              append([]string{"FAKE_TF_LOG=" + f.log}, env...),
		Stdout:           f.output,
		Stderr:           f.output,
		InterruptTimeout: interruptTimeout,
	}
}

func (f *fakeTF) stdoutFile(content string) string {
	f.t.Helper()
	p := filepath.Join(f.t.TempDir(), "stdout")
	require.NoError(f.t, os.WriteFile(p, []byte(content), 0o600))
	return "FAKE_TF_STDOUT=" + p
}

func (f *fakeTF) calls() []invocation {
	f.t.Helper()
	data, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(f.t, err)
	var out []invocation
	cur := invocation{Env: map[string]string{}}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "--":
			out = append(out, cur)
			cur = invocation{Env: map[string]string{}}
		case strings.HasPrefix(line, "cwd="):
			cur.Dir = strings.TrimPrefix(line, "cwd=")
		case strings.HasPrefix(line, "arg="):
			cur.Args = append(cur.Args, strings.TrimPrefix(line, "arg="))
		case strings.HasPrefix(line, "env="):
			k, v, _ := strings.Cut(strings.TrimPrefix(line, "env="), "=")
			cur.Env[k] = v
		}
	}
	return out
}

func (f *fakeTF) lastCall() invocation {
	f.t.Helper()
	calls := f.calls()
	require.NotEmpty(f.t, calls, "fake binary was not invoked")
	return calls[len(calls)-1]
}
