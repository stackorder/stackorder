// Package live plans and runs the live-GitHub variant of the end-to-end
// suite. Instead of the in-memory fake of internal/testutil/ghfake it uses a
// real organisation where the Stackorder App is already installed on all
// repositories, and a Stackorder server GitHub can reach: it creates a
// throwaway repository, sets the server URL as a repository variable,
// pushes the example monorepo to main and a change to a branch, opens a
// pull request and waits for the server's check runs on its head commit.
// It installs nothing and never touches the server directly.
//
// Plan is pure so the sequence can be tested without credentials; Runner
// executes a plan against the GitHub REST API and git.
package live

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Environment variables of the live variant. Token, Org and ServerURL are
// required together; the others are optional.
const (
	EnvToken       = "STACKORDER_E2E_GITHUB_TOKEN" //nolint:gosec
	EnvOrg         = "STACKORDER_E2E_ORG"
	EnvServerURL   = "STACKORDER_E2E_SERVER_URL"
	EnvPlanRoleARN = "STACKORDER_E2E_PLAN_ROLE_ARN"
	EnvAPIURL      = "STACKORDER_E2E_GITHUB_API_URL"
	EnvWebURL      = "STACKORDER_E2E_GITHUB_URL"
	EnvKeepRepo    = "STACKORDER_E2E_KEEP_REPO"
)

// Defaults for github.com.
const (
	DefaultAPIURL = "https://api.github.com"
	DefaultWebURL = "https://github.com"
	// RepoPrefix starts the name of every throwaway repository.
	RepoPrefix = "stackorder-e2e-"
	// MainBranch is the default branch of the throwaway repository.
	MainBranch = "main"
)

// ErrPartialConfig is returned by FromEnv when only some of the required
// variables are set.
var ErrPartialConfig = errors.New("live: " + EnvToken + ", " + EnvOrg + " and " + EnvServerURL + " must be set together")

// Config is where the live variant runs.
type Config struct {
	// Token is a GitHub token that may create and delete repositories in
	// Org and set their Actions variables.
	Token string
	// Org is the organisation the App is installed in.
	Org string
	// ServerURL is the Stackorder server's public base URL, set as the
	// repository variable STACKORDER_SERVER_URL.
	ServerURL string
	// PlanRoleARN, when set, becomes the repository variable
	// STACKORDER_PLAN_ROLE_ARN so the plan jobs can reach the state bucket.
	PlanRoleARN string
	// APIURL and WebURL are the REST API and git hosts.
	APIURL string
	WebURL string
	// Repo is the name of the throwaway repository inside Org.
	Repo string
	// KeepRepo leaves the repository in place after the run.
	KeepRepo bool
}

// FromEnv reads the configuration through getenv and names the throwaway
// repository RepoPrefix + suffix. ok is false when none of the required
// variables is set.
func FromEnv(getenv func(string) string, suffix string) (cfg Config, ok bool, err error) {
	cfg = Config{
		Token:       strings.TrimSpace(getenv(EnvToken)),
		Org:         strings.TrimSpace(getenv(EnvOrg)),
		ServerURL:   strings.TrimRight(strings.TrimSpace(getenv(EnvServerURL)), "/"),
		PlanRoleARN: strings.TrimSpace(getenv(EnvPlanRoleARN)),
		APIURL:      strings.TrimRight(strings.TrimSpace(getenv(EnvAPIURL)), "/"),
		WebURL:      strings.TrimRight(strings.TrimSpace(getenv(EnvWebURL)), "/"),
		Repo:        RepoPrefix + suffix,
	}
	keep, err := parseBool(getenv(EnvKeepRepo))
	if err != nil {
		return Config{}, false, fmt.Errorf("live: %s: %w", EnvKeepRepo, err)
	}
	cfg.KeepRepo = keep
	set := 0
	for _, v := range []string{cfg.Token, cfg.Org, cfg.ServerURL} {
		if v != "" {
			set++
		}
	}
	switch set {
	case 0:
		return Config{}, false, nil
	case 3:
	default:
		return Config{}, false, ErrPartialConfig
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	if cfg.WebURL == "" {
		cfg.WebURL = DefaultWebURL
	}
	if u, err := url.Parse(cfg.ServerURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return Config{}, false, fmt.Errorf("live: %s must be an https URL GitHub can reach, not %q", EnvServerURL, cfg.ServerURL)
	}
	return cfg, true, nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true, nil
	case "", "0", "false", "no":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean", v)
}

// FullName is the throwaway repository as "org/repo".
func (c Config) FullName() string { return c.Org + "/" + c.Repo }

// CloneURL is the https URL git pushes to.
func (c Config) CloneURL() string { return c.WebURL + "/" + c.FullName() + ".git" }

// Kind tells how a Step runs.
type Kind string

// Step kinds.
const (
	// KindAPI is one GitHub REST call.
	KindAPI Kind = "api"
	// KindGit is one git command in the local repository.
	KindGit Kind = "git"
	// KindWaitChecks polls the check runs of a commit until the named ones
	// complete.
	KindWaitChecks Kind = "wait_checks"
)

// Step is one action of a plan.
type Step struct {
	Kind Kind
	// Name describes the step in logs and errors.
	Name string
	// Method, Path and Body describe a KindAPI call; Want lists the
	// statuses that count as success.
	Method string
	Path   string
	Body   map[string]any
	Want   []int
	// Git holds the arguments of a KindGit command, without credentials.
	Git []string
	// SHA and Checks describe a KindWaitChecks step.
	SHA    string
	Checks []string
}

// Change is the pull request the plan opens.
type Change struct {
	// BaseSHA is pushed as main; HeadSHA is pushed as Branch.
	BaseSHA string
	HeadSHA string
	Branch  string
	Title   string
	// Checks are the check runs to wait for on HeadSHA.
	Checks []string
}

// Plan returns the steps of a live run: create the repository, set its
// variables, push main and the branch, open the pull request and wait for
// the checks. Cleanup is separate so it runs whatever happens.
func Plan(cfg Config, ch Change) []Step {
	repo := cfg.FullName()
	vars := [][2]string{{"STACKORDER_SERVER_URL", cfg.ServerURL}}
	if cfg.PlanRoleARN != "" {
		vars = append(vars, [2]string{"STACKORDER_PLAN_ROLE_ARN", cfg.PlanRoleARN})
	}
	steps := make([]Step, 0, len(vars)+5)
	steps = append(steps, Step{
		Kind: KindAPI, Name: "create " + repo, Method: http.MethodPost, Path: "/orgs/" + cfg.Org + "/repos",
		Body: map[string]any{
			"name": cfg.Repo, "private": true, "auto_init": false, "has_issues": true,
			"description": "Throwaway repository of the Stackorder end-to-end suite",
		},
		Want: []int{http.StatusCreated},
	})
	for _, v := range vars {
		steps = append(steps, Step{
			Kind: KindAPI, Name: "set " + v[0], Method: http.MethodPost, Path: "/repos/" + repo + "/actions/variables",
			Body: map[string]any{"name": v[0], "value": v[1]}, Want: []int{http.StatusCreated},
		})
	}
	steps = append(steps,
		Step{Kind: KindGit, Name: "push " + MainBranch, Git: []string{"push", "--quiet", cfg.CloneURL(), ch.BaseSHA + ":refs/heads/" + MainBranch}},
		Step{Kind: KindGit, Name: "push " + ch.Branch, Git: []string{"push", "--quiet", cfg.CloneURL(), ch.HeadSHA + ":refs/heads/" + ch.Branch}},
		Step{
			Kind: KindAPI, Name: "open the pull request", Method: http.MethodPost, Path: "/repos/" + repo + "/pulls",
			Body: map[string]any{"title": ch.Title, "head": ch.Branch, "base": MainBranch, "body": "Opened by the Stackorder end-to-end suite."},
			Want: []int{http.StatusCreated},
		},
		Step{Kind: KindWaitChecks, Name: "wait for the checks", SHA: ch.HeadSHA, Checks: slices.Clone(ch.Checks)},
	)
	return steps
}

// Cleanup returns the step that deletes the throwaway repository.
func Cleanup(cfg Config) Step {
	return Step{
		Kind: KindAPI, Name: "delete " + cfg.FullName(), Method: http.MethodDelete, Path: "/repos/" + cfg.FullName(),
		Want: []int{http.StatusNoContent, http.StatusNotFound},
	}
}

// CheckRun is the state of one check run the plan waited for.
type CheckRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

// Result is what a run observed.
type Result struct {
	// PullNumber is the pull request the run opened.
	PullNumber int
	// Checks holds the completed check runs by name.
	Checks map[string]CheckRun
}

// Runner executes a plan.
type Runner struct {
	Config Config
	// Dir is the local git repository holding the pushed commits.
	Dir string
	// HTTP sends the API calls; nil uses a client with a one minute timeout.
	HTTP *http.Client
	// Git runs git in Dir; nil runs the git binary.
	Git func(ctx context.Context, dir string, args ...string) error
	// Poll and Timeout bound KindWaitChecks; zero means 10 s and 20 min.
	Poll    time.Duration
	Timeout time.Duration
	// Logf receives progress lines; nil discards them.
	Logf func(format string, args ...any)
}

// Run executes steps in order and stops at the first failure.
func (r *Runner) Run(ctx context.Context, steps []Step) (*Result, error) {
	res := &Result{Checks: map[string]CheckRun{}}
	for _, st := range steps {
		r.logf("live: %s", st.Name)
		var err error
		switch st.Kind {
		case KindAPI:
			var body []byte
			if body, err = r.call(ctx, st); err == nil && st.Path == "/repos/"+r.Config.FullName()+"/pulls" {
				var pr struct {
					Number int `json:"number"`
				}
				if err = json.Unmarshal(body, &pr); err == nil {
					res.PullNumber = pr.Number
				}
			}
		case KindGit:
			err = r.git(ctx, st.Git)
		case KindWaitChecks:
			err = r.waitChecks(ctx, st, res)
		default:
			err = fmt.Errorf("unknown step kind %q", st.Kind)
		}
		if err != nil {
			return res, fmt.Errorf("live: %s: %w", st.Name, err)
		}
	}
	return res, nil
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func (r *Runner) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: time.Minute}
}

func (r *Runner) call(ctx context.Context, st Step) ([]byte, error) {
	var body io.Reader
	if st.Body != nil {
		b, err := json.Marshal(st.Body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, st.Method, r.Config.APIURL+st.Path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Config.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if !slices.Contains(st.Want, resp.StatusCode) {
		return nil, fmt.Errorf("%s %s: status %d: %s", st.Method, st.Path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (r *Runner) git(ctx context.Context, args []string) error {
	auth := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+r.Config.Token))
	full := append([]string{"-c", "http." + r.Config.WebURL + "/.extraheader=" + auth}, args...)
	run := r.Git
	if run == nil {
		run = runGit
	}
	if err := run(ctx, r.Dir, full...); err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), auth, "AUTHORIZATION: basic ***"))
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[len(args)-1], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *Runner) waitChecks(ctx context.Context, st Step, res *Result) error {
	poll, timeout := r.Poll, r.Timeout
	if poll <= 0 {
		poll = 10 * time.Second
	}
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	path := "/repos/" + r.Config.FullName() + "/commits/" + st.SHA + "/check-runs?per_page=100"
	for {
		body, err := r.call(ctx, Step{Method: http.MethodGet, Path: path, Want: []int{http.StatusOK}})
		if err != nil && ctx.Err() == nil {
			return err
		}
		if err == nil {
			done, err := completed(body, st.Checks, res)
			if err != nil || done {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("checks %s on %s did not complete: %w", strings.Join(missing(st.Checks, res), ", "), st.SHA, ctx.Err())
		case <-time.After(poll):
		}
	}
}

func completed(body []byte, want []string, res *Result) (bool, error) {
	var page struct {
		CheckRuns []CheckRun `json:"check_runs"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return false, fmt.Errorf("decode check runs: %w", err)
	}
	for _, c := range page.CheckRuns {
		if c.Status == "completed" && slices.Contains(want, c.Name) {
			res.Checks[c.Name] = c
		}
	}
	return len(missing(want, res)) == 0, nil
}

func missing(want []string, res *Result) []string {
	var out []string
	for _, name := range want {
		if _, ok := res.Checks[name]; !ok {
			out = append(out, name)
		}
	}
	return out
}
