package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

const testBaseURL = "https://stackorder.test"

var testSessionKey = bytes.Repeat([]byte{7}, 32)

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

type fakeStore struct {
	dataStore

	mu            sync.Mutex
	pingErr       error
	pingDeadline  time.Duration
	repos         []store.Repo
	keys          map[string]store.APIKey
	sessions      map[string]store.Session
	jtis          map[string]bool
	installations []store.Installation
	stacks        map[uuid.UUID]store.Stack
	runs          map[uuid.UUID]store.Run
	runStacks     map[uuid.UUID][]store.RunStack
	sessionReads  int
	created       []store.NewSession
	deleted       []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		keys:      map[string]store.APIKey{},
		sessions:  map[string]store.Session{},
		jtis:      map[string]bool{},
		stacks:    map[uuid.UUID]store.Stack{},
		runs:      map[uuid.UUID]store.Run{},
		runStacks: map[uuid.UUID][]store.RunStack{},
	}
}

func (f *fakeStore) Ping(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := ctx.Deadline(); ok {
		f.pingDeadline = time.Until(d)
	}
	return f.pingErr
}

func (f *fakeStore) SeenJTI(_ context.Context, jti string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := f.jtis[jti]
	f.jtis[jti] = true
	return seen, nil
}

func (f *fakeStore) VerifyAPIKey(_ context.Context, plaintext string) (store.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[plaintext]
	if !ok || k.RevokedAt != nil {
		return store.APIKey{}, store.ErrNotFound
	}
	return k, nil
}

func (f *fakeStore) GetSession(_ context.Context, token string) (store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionReads++
	s, ok := f.sessions[token]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	return s, nil
}

func (f *fakeStore) CreateSession(_ context.Context, n store.NewSession) (string, store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, n)
	token := "tok" + uuid.NewString()
	s := store.Session{ID: "hash-" + token, Login: n.Login, UserID: n.UserID, AvatarURL: n.AvatarURL, Orgs: n.Orgs}
	f.sessions[token] = s
	return token, s, nil
}

func (f *fakeStore) DeleteSession(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, token)
	delete(f.sessions, token)
	return nil
}

func (f *fakeStore) ListInstallations(context.Context) ([]store.Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Installation(nil), f.installations...), nil
}

func (f *fakeStore) GetRepo(_ context.Context, id int64) (store.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.repos {
		if r.ID == id {
			return r, nil
		}
	}
	return store.Repo{}, store.ErrNotFound
}

func (f *fakeStore) GetRepoByName(_ context.Context, name string) (store.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.repos {
		if strings.EqualFold(r.FullName, name) {
			return r, nil
		}
	}
	return store.Repo{}, store.ErrNotFound
}

func (f *fakeStore) GetStack(_ context.Context, id uuid.UUID) (store.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.stacks[id]
	if !ok {
		return store.Stack{}, store.ErrNotFound
	}
	return s, nil
}

func (f *fakeStore) GetRun(_ context.Context, id uuid.UUID) (store.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		return store.Run{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) GetRunStacks(_ context.Context, runID uuid.UUID) ([]store.RunStack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.RunStack(nil), f.runStacks[runID]...), nil
}

func (f *fakeStore) addRepo(id, installationID int64, account, fullName string) store.Repo {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := store.Repo{ID: id, InstallationID: installationID, Account: account, FullName: fullName, DefaultBranch: "main"}
	f.repos = append(f.repos, r)
	f.installations = append(f.installations, store.Installation{ID: installationID, Account: account})
	return r
}

type testEnv struct {
	t    *testing.T
	cfg  Config
	deps Deps
	db   *fakeStore
	runs *fakeRuns
	logs *syncBuffer
	srv  *server
	h    http.Handler
}

func newEnv(t *testing.T, mutate ...func(*Config, *Deps)) *testEnv {
	t.Helper()
	e := &testEnv{t: t, db: newFakeStore(), runs: &fakeRuns{}, logs: &syncBuffer{}}
	e.cfg = Config{BaseURL: testBaseURL, SessionKey: testSessionKey}
	e.deps = Deps{
		Runs:   e.runs,
		Logger: slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		UI: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<!doctype html><title>ui</title>")
		}),
	}
	for _, m := range mutate {
		m(&e.cfg, &e.deps)
	}
	e.srv = newServer(e.cfg, e.deps, e.db)
	e.h = e.srv.routes()
	return e
}

func (e *testEnv) do(r *http.Request) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func newRequest(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	var rd io.Reader = http.NoBody
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		data, err := json.Marshal(b)
		require.NoError(t, err)
		rd = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, target, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"), rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) v1.Error {
	t.Helper()
	return decodeBody[v1.Error](t, rec)
}

type runsCall struct {
	method   string
	p        principal.Principal
	actor    string
	runID    string
	stackKey string
	stackID  string
	name     string
	login    string
	repoID   int64
	body     any
}

type fakeRuns struct {
	mu    sync.Mutex
	calls []runsCall
	err   error

	createResp *v1.CreateRunResponse
	run        *v1.Run
	canAct     bool
	canActErr  error
}

func (f *fakeRuns) record(c runsCall) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	return f.err
}

func (f *fakeRuns) last() runsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return runsCall{}
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeRuns) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRuns) CreateRun(_ context.Context, p principal.Principal, req v1.CreateRunRequest) (*v1.CreateRunResponse, error) {
	if err := f.record(runsCall{method: "CreateRun", p: p, body: req}); err != nil {
		return nil, err
	}
	if f.createResp != nil {
		return f.createResp, nil
	}
	return &v1.CreateRunResponse{RunID: "7c9e6679-7425-40de-944b-e07fc1f90ae7", Status: v1.RunPending}, nil
}

func (f *fakeRuns) UploadGraph(_ context.Context, p principal.Principal, runID string, req v1.GraphUploadRequest) (*v1.ResolveResponse, error) {
	if err := f.record(runsCall{method: "UploadGraph", p: p, runID: runID, body: req}); err != nil {
		return nil, err
	}
	return &v1.ResolveResponse{RunID: runID, Waves: [][]string{{"stacks/prod/vpc"}}}, nil
}

func (f *fakeRuns) RecordResult(_ context.Context, p principal.Principal, runID, stackKey string, res v1.StackResult) (*v1.RunStack, error) {
	if err := f.record(runsCall{method: "RecordResult", p: p, runID: runID, stackKey: stackKey, body: res}); err != nil {
		return nil, err
	}
	return &v1.RunStack{Key: stackKey, Status: v1.StackPlanned}, nil
}

func (f *fakeRuns) RecordCheck(_ context.Context, p principal.Principal, runID, stackKey, name string, verdict v1.CheckVerdict) (*v1.Check, error) {
	if err := f.record(runsCall{method: "RecordCheck", p: p, runID: runID, stackKey: stackKey, name: name, body: verdict}); err != nil {
		return nil, err
	}
	return &v1.Check{Name: name, Status: verdict.Status}, nil
}

func (f *fakeRuns) GetRunForPrincipal(_ context.Context, p principal.Principal, runID string) (*v1.Run, error) {
	if err := f.record(runsCall{method: "GetRunForPrincipal", p: p, runID: runID}); err != nil {
		return nil, err
	}
	if f.run != nil {
		out := *f.run
		return &out, nil
	}
	return &v1.Run{ID: runID, Repo: "acme/infra", Status: v1.RunPlanning}, nil
}

func (f *fakeRuns) Unlock(_ context.Context, actor string, stackID string, req v1.UnlockRequest) (*v1.UnlockResponse, error) {
	if err := f.record(runsCall{method: "Unlock", actor: actor, stackID: stackID, body: req}); err != nil {
		return nil, err
	}
	return &v1.UnlockResponse{Released: []v1.LockInfo{{StackID: stackID, RunID: "r1"}}}, nil
}

func (f *fakeRuns) UnlockByKey(_ context.Context, actor string, req v1.UnlockRequest) (*v1.UnlockResponse, error) {
	if err := f.record(runsCall{method: "UnlockByKey", actor: actor, body: req}); err != nil {
		return nil, err
	}
	return &v1.UnlockResponse{Released: []v1.LockInfo{{StackKey: req.StackKey, RunID: "r1"}}}, nil
}

func (f *fakeRuns) Rerun(_ context.Context, actor string, runID string) (*v1.Run, error) {
	if err := f.record(runsCall{method: "Rerun", actor: actor, runID: runID}); err != nil {
		return nil, err
	}
	if f.run != nil {
		out := *f.run
		return &out, nil
	}
	return &v1.Run{ID: "5b8e1f2a-3c4d-4e5f-8a9b-0c1d2e3f4a5b", Repo: "acme/infra", Status: v1.RunPending}, nil
}

func (f *fakeRuns) CanActOnRepo(_ context.Context, login string, repoID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, runsCall{method: "CanActOnRepo", login: login, repoID: repoID})
	return f.canAct, f.canActErr
}
