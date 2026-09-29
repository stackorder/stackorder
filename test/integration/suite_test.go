//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/testutil/faketf"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

const (
	acmeInstallation = 7
	acmeOrg          = "acme"
)

type suiteTB struct {
	testing.TB

	mu       sync.Mutex
	current  testing.TB
	cleanups []func()
	failed   bool
	dir      string
}

func (s *suiteTB) attach(t testing.TB) {
	s.mu.Lock()
	prev := s.current
	s.current = t
	s.mu.Unlock()
	t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.current == t {
			s.current = prev
		}
	})
}

func (s *suiteTB) test() testing.TB {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *suiteTB) Helper()      {}
func (s *suiteTB) Name() string { return "integration-suite" }

func (s *suiteTB) Cleanup(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanups = append(s.cleanups, f)
}

func (s *suiteTB) Log(args ...any) { s.log(fmt.Sprintln(args...)) }

func (s *suiteTB) Logf(format string, args ...any) { s.log(fmt.Sprintf(format, args...)) }

func (s *suiteTB) log(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		s.current.Log(strings.TrimRight(msg, "\n"))
	}
}

func (s *suiteTB) Error(args ...any) { s.Errorf("%s", fmt.Sprint(args...)) }

func (s *suiteTB) Errorf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		s.current.Errorf(format, args...)
		return
	}
	s.failed = true
	_, _ = fmt.Fprintf(os.Stderr, "integration suite: "+format+"\n", args...)
}

func (s *suiteTB) Fatal(args ...any) { s.Fatalf("%s", fmt.Sprint(args...)) }

func (s *suiteTB) Fatalf(format string, args ...any) {
	if t := s.test(); t != nil {
		t.Fatalf(format, args...)
		return
	}
	panic(fmt.Sprintf("integration suite: "+format, args...))
}

func (s *suiteTB) Fail() {
	if t := s.test(); t != nil {
		t.Fail()
		return
	}
	s.mu.Lock()
	s.failed = true
	s.mu.Unlock()
}

func (s *suiteTB) FailNow() { s.Fatalf("FailNow") }

func (s *suiteTB) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

func (s *suiteTB) TempDir() string {
	dir, err := os.MkdirTemp(s.dir, "tmp-")
	if err != nil {
		s.Fatalf("temp dir: %v", err)
	}
	return dir
}

func (s *suiteTB) ArtifactDir() string      { return s.TempDir() }
func (s *suiteTB) Context() context.Context { return context.Background() }
func (s *suiteTB) Output() io.Writer        { return io.Discard }
func (s *suiteTB) Attr(string, string)      {}
func (s *suiteTB) Skipped() bool            { return false }
func (s *suiteTB) Setenv(string, string)    { panic("integration suite: Setenv is per test") }
func (s *suiteTB) Chdir(string)             { panic("integration suite: Chdir is per test") }
func (s *suiteTB) Skip(...any)              { panic("integration suite: Skip is per test") }
func (s *suiteTB) SkipNow()                 { panic("integration suite: SkipNow is per test") }
func (s *suiteTB) Skipf(string, ...any)     { panic("integration suite: Skipf is per test") }

func (s *suiteTB) teardown() (failed bool) {
	s.mu.Lock()
	cleanups := s.cleanups
	s.cleanups = nil
	s.current = nil
	s.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
	_ = os.RemoveAll(s.dir)
	return s.Failed()
}

var suite struct {
	once   sync.Once
	tb     *suiteTB
	env    *Env
	tfDir  string
	apiKey string
	failed bool
}

func shared(t *testing.T) *Env {
	t.Helper()
	suite.once.Do(func() { startSuite(t) })
	if suite.env == nil {
		t.Fatal("integration: the shared server did not start; see the log of the first test that asked for it")
	}
	suite.tb.attach(t)
	return suite.env.For(t)
}

func startSuite(t *testing.T) {
	dir, err := os.MkdirTemp("", "stackorder-integration-")
	if err != nil {
		t.Fatalf("integration: %v", err)
	}
	tb := &suiteTB{dir: dir}
	suite.tb = tb
	tb.attach(t)
	env := NewEnv(tb, WithGitHub(func(g *ghfake.Server) {
		g.AddInstallation(acmeInstallation, acmeOrg)
		g.SetTeamMembership(acmeOrg, "platform-eng", reviewer, gh.MembershipActive)
		g.SetOAuthClient(OAuthClientID, OAuthClientSecret)
		g.OnDispatch(func(d ghfake.Dispatch) { titleDispatchedRun(g, d) })
	}))
	suite.tfDir = faketf.Build(tb)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	key, _, err := env.Store.CreateAPIKey(ctx, "integration", "suite")
	if err != nil {
		t.Fatalf("integration: create the API key: %v", err)
	}
	suite.apiKey = key
	suite.env = env
}

func titleDispatchedRun(g *ghfake.Server, d ghfake.Dispatch) {
	for _, run := range g.WorkflowRuns(d.Repo) {
		if run.ID != d.RunID {
			continue
		}
		run.Name = "stackorder run"
		run.DisplayTitle = "stackorder " + d.Inputs["mode"] + " " + d.Inputs["run_id"] + " wave " + d.Inputs["wave"]
		g.AddWorkflowRun(d.Repo, run)
	}
}

func closeSuite() {
	if suite.tb != nil {
		suite.failed = suite.tb.teardown()
	}
}
