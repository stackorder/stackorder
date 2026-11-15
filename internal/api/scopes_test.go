package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

func fillPath(pattern string) (method, path string) {
	method, rest, _ := strings.Cut(pattern, " ")
	segs := strings.Split(rest, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") {
			segs[i] = "x"
		}
	}
	return method, strings.Join(segs, "/")
}

// TestReadOnlyKeyRefusesEveryMutatingRoute enumerates the router rather than a
// hand-written list: every authenticated route an API key may present whose
// method is not safe must refuse a read-only key, including a route added
// later, because the refusal lives in with(), not in each handler.
func TestReadOnlyKeyRefusesEveryMutatingRoute(t *testing.T) {
	e := newEnv(t)
	ro := e.scopedKey("reader", func(k *store.APIKey) { k.ReadOnly = true })

	var mutating int
	for _, rt := range authRoutes(e.srv) {
		if rt.mode&viaAPIKey == 0 || safeMethod(rt.method()) {
			continue
		}
		mutating++
		method, path := fillPath(rt.pattern)
		t.Run(rt.pattern, func(t *testing.T) {
			r := bearer(newRequest(t, method, path, nil), ro)
			r.Header.Set("Origin", testBaseURL)
			rec := e.do(r)
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Equal(t, codeForbidden, errorOf(t, rec).Code)
			assert.Contains(t, errorOf(t, rec).Message, "read-only")
		})
	}
	require.NotZero(t, mutating, "the router must carry mutating API-key routes to assert on")
	assert.Empty(t, e.db.auditLog(), "a refused read-only request mutates nothing")
}

func TestReadOnlyKeyAllowsReads(t *testing.T) {
	e := newEnv(t)
	ro := e.scopedKey("reader", func(k *store.APIKey) { k.ReadOnly = true })

	rec := e.do(bearer(newRequest(t, http.MethodGet, "/v1/me", nil), ro))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	who := decodeBody[v1.Whoami](t, rec)
	assert.Equal(t, "apikey:reader", who.Login)
	assert.False(t, who.Admin, "a read-only key is not an administrator")
}

func TestInstallationScopedKeyIsAnOracleFreeNotFound(t *testing.T) {
	e := newEnv(t)
	own := e.db.addRepo(100, 7, "acme", "acme/infra")
	other := e.db.addRepo(200, 9, "globex", "globex/infra")
	key := e.scopedKey("acme-ci", func(k *store.APIKey) { k.InstallationID = ptr(int64(7)) })

	ownRun := uuid.New()
	otherRun := uuid.New()
	e.db.runs[ownRun] = store.Run{ID: ownRun, RepoID: own.ID}
	e.db.runs[otherRun] = store.Run{ID: otherRun, RepoID: other.ID}

	t.Run("sees its own installation's run", func(t *testing.T) {
		rec := e.do(bearer(newRequest(t, http.MethodGet, "/v1/runs/"+ownRun.String(), nil), key))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	otherBody := func(id string) string {
		rec := e.do(bearer(newRequest(t, http.MethodGet, "/v1/runs/"+id, nil), key))
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		// The requested id is the caller's own input; normalise it out so what
		// remains is only what the server reveals about existence.
		return strings.ReplaceAll(rec.Body.String(), id, "ID")
	}
	t.Run("another installation's run is indistinguishable from an unknown one", func(t *testing.T) {
		assert.Equal(t, otherBody(uuid.New().String()), otherBody(otherRun.String()),
			"the body for a run of another installation must match the body for a run that does not exist")
	})

	ownStack := uuid.New()
	otherStack := uuid.New()
	e.db.stacks[ownStack] = store.Stack{ID: ownStack, RepoID: own.ID, Key: "stacks/vpc"}
	e.db.stacks[otherStack] = store.Stack{ID: otherStack, RepoID: other.ID, Key: "stacks/vpc"}
	stackBody := func(id string) (int, string) {
		rec := e.do(bearer(newRequest(t, http.MethodGet, "/v1/stacks/"+id+"/resources", nil), key))
		return rec.Code, strings.ReplaceAll(rec.Body.String(), id, "ID")
	}
	t.Run("another installation's stack is indistinguishable from an unknown one", func(t *testing.T) {
		otherCode, otherResp := stackBody(otherStack.String())
		unknownCode, unknownResp := stackBody(uuid.New().String())
		assert.Equal(t, http.StatusNotFound, otherCode)
		assert.Equal(t, unknownCode, otherCode)
		assert.Equal(t, unknownResp, otherResp)
	})
}

func TestInstallationScopedKeyWhoseInstallationIsGoneSeesNothing(t *testing.T) {
	e := newEnv(t)
	repo := e.db.addRepo(100, 7, "acme", "acme/infra")
	// A key bound to an installation id that no longer has an installation row.
	key := e.scopedKey("stale", func(k *store.APIKey) { k.InstallationID = ptr(int64(999)) })

	run := uuid.New()
	e.db.runs[run] = store.Run{ID: run, RepoID: repo.ID}
	rec := e.do(bearer(newRequest(t, http.MethodGet, "/v1/runs/"+run.String(), nil), key))
	require.Equal(t, http.StatusNotFound, rec.Code, "a key whose installation is gone sees nothing, not everything")
}

func TestScopedKeyCannotManageKeys(t *testing.T) {
	e := newEnv(t)
	e.db.addRepo(100, 7, "acme", "acme/infra")
	readOnly := e.scopedKey("reader", func(k *store.APIKey) { k.ReadOnly = true })
	scoped := e.scopedKey("acme-ci", func(k *store.APIKey) { k.InstallationID = ptr(int64(7)) })

	for _, key := range []string{readOnly, scoped} {
		rec := e.do(bearer(newRequest(t, http.MethodGet, "/v1/keys", nil), key))
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Contains(t, errorOf(t, rec).Message, "administrator")

		rec = e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "x"}), key))
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

		me := decodeBody[v1.Whoami](t, e.do(bearer(newRequest(t, http.MethodGet, "/v1/me", nil), key)))
		assert.False(t, me.Admin)
	}
	assert.Empty(t, e.db.auditLog(), "a scoped key creates no keys")
}

func TestExpiringKeyCannotManageKeys(t *testing.T) {
	e := newEnv(t)
	e.db.addRepo(100, 7, "acme", "acme/infra")
	exp := time.Now().Add(time.Hour)
	expiring := e.scopedKey("temporary", func(k *store.APIKey) { k.ExpiresAt = &exp })

	rec := e.do(bearer(newRequest(t, http.MethodGet, "/v1/keys", nil), expiring))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Contains(t, errorOf(t, rec).Message, "administrator")

	rec = e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "perma"}), expiring))
	require.Equal(t, http.StatusForbidden, rec.Code, "a time-boxed key cannot mint a permanent one")

	me := decodeBody[v1.Whoami](t, e.do(bearer(newRequest(t, http.MethodGet, "/v1/me", nil), expiring)))
	assert.False(t, me.Admin, "an expiring key is not an administrator")
	assert.Empty(t, e.db.auditLog(), "an expiring key creates no keys")
}

// TestHandInsertedInstallationZeroIsScoped guards the ambiguity between the
// NULL installation_id that means "unscoped" and a real zero id: a key
// hand-inserted with installation_id = 0 must be treated as scoped, so it is
// neither an administrator nor able to act on any installation, matching the
// read paths that already see nothing.
func TestHandInsertedInstallationZeroIsScoped(t *testing.T) {
	zero := int64(0)
	id := identity{
		Principal: principal.Principal{Kind: principal.APIKey, InstallationID: &zero},
		scope:     &keyScope{known: false},
	}
	assert.False(t, id.isAdmin(), "a zero-installation key must not be an administrator")
	assert.False(t, id.SeesInstallation(999), "a zero-installation key sees no other installation")
	assert.False(t, id.sees("acme"), "a zero-installation key is blind on read paths")

	scoped := store.APIKey{InstallationID: &zero}
	assert.False(t, scoped.Admin(), "store and server agree a zero-installation key is scoped")
}

func TestCreateScopedKey(t *testing.T) {
	e := newEnv(t)
	e.db.addRepo(100, 7, "acme", "acme/infra")
	admin := e.apiKey("admin")

	t.Run("read-only", func(t *testing.T) {
		rec := e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "reader", ReadOnly: true}), admin))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		got := decodeBody[v1.CreatedAPIKey](t, rec)
		assert.True(t, got.ReadOnly)
		assert.Nil(t, got.InstallationID)

		// The minted read-only key cannot mutate.
		rec = e.do(bearer(sameOriginPost(t, "/v1/unlock", v1.UnlockRequest{Repo: "acme/infra", StackKey: "stacks/vpc"}), got.Secret))
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})

	t.Run("installation scoped", func(t *testing.T) {
		rec := e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "acme-ci", InstallationID: 7}), admin))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		got := decodeBody[v1.CreatedAPIKey](t, rec)
		require.NotNil(t, got.InstallationID)
		assert.Equal(t, int64(7), *got.InstallationID)
	})

	t.Run("unknown installation", func(t *testing.T) {
		rec := e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "x", InstallationID: 404}), admin))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Equal(t, "installation_id", errorOf(t, rec).Details.(map[string]any)["field"])
	})

	t.Run("expiry in the future", func(t *testing.T) {
		exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		rec := e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "temp", ExpiresAt: &exp}), admin))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		got := decodeBody[v1.CreatedAPIKey](t, rec)
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, got.ExpiresAt.Equal(exp))
	})

	t.Run("expiry in the past", func(t *testing.T) {
		exp := time.Now().Add(-time.Hour)
		rec := e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "x", ExpiresAt: &exp}), admin))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Equal(t, "expires_at", errorOf(t, rec).Details.(map[string]any)["field"])
	})
}

func ptr[T any](v T) *T { return &v }
