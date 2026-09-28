package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

type postedResult struct {
	RunID  string
	Key    string
	Result v1.StackResult
}

type postedCheck struct {
	RunID   string
	Key     string
	Name    string
	Verdict v1.CheckVerdict
}

type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	runID     string
	resolve   v1.ResolveResponse
	run       v1.Run
	released  map[string][]v1.LockInfo
	fail      map[string]int
	failCode  map[string]string
	creates   []v1.CreateRunRequest
	graphs    []v1.GraphUploadRequest
	results   []postedResult
	checks    []postedCheck
	unlocks   []v1.UnlockRequest
	checkRuns []checkRunRequest
	auth      map[string][]string
	audiences []string
	hits      map[string]int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{
		t:        t,
		runID:    "run-1",
		released: map[string][]v1.LockInfo{},
		fail:     map[string]int{},
		failCode: map[string]string{},
		auth:     map[string][]string{},
		hits:     map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/runs", f.route("create", func(r *http.Request) (any, error) {
		var req v1.CreateRunRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		f.creates = append(f.creates, req)
		return v1.CreateRunResponse{RunID: f.runID, Status: v1.RunPending}, err
	}))
	mux.HandleFunc("POST /v1/runs/{id}/graph", f.route("graph", func(r *http.Request) (any, error) {
		var req v1.GraphUploadRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		f.graphs = append(f.graphs, req)
		return f.resolve, err
	}))
	mux.HandleFunc("POST /v1/runs/{id}/stacks/{key}/result", f.route("result", func(r *http.Request) (any, error) {
		var res v1.StackResult
		err := json.NewDecoder(r.Body).Decode(&res)
		f.results = append(f.results, postedResult{RunID: r.PathValue("id"), Key: r.PathValue("key"), Result: res})
		return v1.RunStack{Key: r.PathValue("key"), Status: v1.StackPlanned, Summary: res.Summary}, err
	}))
	mux.HandleFunc("POST /v1/runs/{id}/stacks/{key}/checks/{name}", f.route("check", func(r *http.Request) (any, error) {
		var v v1.CheckVerdict
		err := json.NewDecoder(r.Body).Decode(&v)
		f.checks = append(f.checks, postedCheck{RunID: r.PathValue("id"), Key: r.PathValue("key"), Name: r.PathValue("name"), Verdict: v})
		return v1.Check{Name: r.PathValue("name"), Status: v.Status, Summary: v.Summary, UpdatedAt: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}, err
	}))
	mux.HandleFunc("GET /v1/runs/{id}", f.route("run", func(r *http.Request) (any, error) {
		run := f.run
		run.ID = r.PathValue("id")
		return run, nil
	}))
	mux.HandleFunc("POST /v1/unlock", f.route("unlock", func(r *http.Request) (any, error) {
		var req v1.UnlockRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		f.unlocks = append(f.unlocks, req)
		return v1.UnlockResponse{Released: f.released[req.StackKey]}, err
	}))
	mux.HandleFunc("GET /oidc/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.audiences = append(f.audiences, r.URL.Query().Get("audience"))
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer request-token" {
			http.Error(w, "bad request token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"value": "oidc-token"})
	})
	mux.HandleFunc("POST /github/repos/{owner}/{repo}/check-runs", func(w http.ResponseWriter, r *http.Request) {
		var req checkRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.checkRuns = append(f.checkRuns, req)
		status := f.fail["check-runs"]
		f.mu.Unlock()
		if status != 0 {
			http.Error(w, `{"message":"Resource not accessible by integration"}`, status)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) route(name string, fn func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hits[name]++
		f.auth[name] = append(f.auth[name], r.Header.Get("Authorization"))
		if status := f.fail[name]; status != 0 {
			code := f.failCode[name]
			if code == "" {
				code = "internal"
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v1.Error{Code: code, Message: "injected " + code})
			return
		}
		body, err := fn(r)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(v1.Error{Code: "invalid", Message: err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (f *fakeServer) url() string { return f.srv.URL }

func (f *fakeServer) failWith(route string, status int, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[route] = status
	f.failCode[route] = code
}

func (f *fakeServer) setRun(run v1.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run = run
}

func (f *fakeServer) setResolve(resp v1.ResolveResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolve = resp
}

func (f *fakeServer) postedResults() []postedResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]postedResult(nil), f.results...)
}

func (f *fakeServer) lastResult() postedResult {
	f.t.Helper()
	res := f.postedResults()
	require.NotEmpty(f.t, res, "no result was posted")
	return res[len(res)-1]
}

func (f *fakeServer) neutralChecks() []checkRunRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]checkRunRequest(nil), f.checkRuns...)
}

func (f *fakeServer) hitCount(route string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[route]
}
