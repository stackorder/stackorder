package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func TestCreateAPIKey(t *testing.T) {
	e := newEnv(t)
	admin := e.apiKey("admin")

	rec := e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "  ci  "}), admin))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	got := decodeBody[v1.CreatedAPIKey](t, rec)
	assert.Equal(t, "ci", got.Name, "the name is trimmed")
	assert.True(t, strings.HasPrefix(got.Secret, store.APIKeyPrefix), "the secret is an sk_ key")
	assert.Equal(t, got.Secret[:10], got.Prefix)
	assert.Equal(t, "apikey:admin", got.CreatedBy)
	assert.Nil(t, got.RevokedAt)
	assert.NotEmpty(t, got.ID)

	key := e.db.keysByID[uuid.MustParse(got.ID)]
	require.NotZero(t, key.ID)

	entries := e.db.auditLog()
	require.Len(t, entries, 1)
	assert.Equal(t, "api_key_created", entries[0].Action)
	assert.Equal(t, "apikey:admin", entries[0].Actor)
	assert.Equal(t, "ci", entries[0].Target)
	assert.Equal(t, got.ID, entries[0].Details["key_id"])
}

func TestCreateAPIKeyValidation(t *testing.T) {
	e := newEnv(t)
	admin := e.apiKey("admin")

	cases := []struct {
		name string
		body any
	}{
		{"empty name", v1.CreateAPIKeyRequest{Name: "   "}},
		{"long name", v1.CreateAPIKeyRequest{Name: strings.Repeat("x", maxAPIKeyNameLen+1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(bearer(sameOriginPost(t, "/v1/keys", tc.body), admin))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, codeInvalid, errorOf(t, rec).Code)
		})
	}

	rec := e.do(bearer(sameOriginPost(t, "/v1/keys", nil), admin))
	require.Equal(t, http.StatusBadRequest, rec.Code, "a body is required")
	assert.Empty(t, e.db.auditLog(), "nothing is audited when creation is refused")
}

func TestListAPIKeys(t *testing.T) {
	e := newEnv(t)
	admin := e.apiKey("admin")

	for _, name := range []string{"ci", "deploy"} {
		rec := e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: name}), admin))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	}

	rec := e.do(bearer(newRequest(t, http.MethodGet, "/v1/keys", nil), admin))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list := decodeBody[v1.APIKeyList](t, rec)
	require.Len(t, list.Keys, 2)
	assert.Equal(t, "deploy", list.Keys[0].Name, "newest first")
	assert.Equal(t, "ci", list.Keys[1].Name)
	assert.NotContains(t, rec.Body.String(), "secret", "the list never carries a secret")
}

func TestRevokeAPIKey(t *testing.T) {
	e := newEnv(t)
	admin := e.apiKey("admin")

	rec := e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "ci"}), admin))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decodeBody[v1.CreatedAPIKey](t, rec)

	rec = e.do(bearer(deleteReq(t, "/v1/keys/"+created.ID), admin))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	revoked := decodeBody[v1.APIKey](t, rec)
	require.NotNil(t, revoked.RevokedAt)

	rec = e.do(bearer(newRequest(t, http.MethodGet, "/v1/me", nil), created.Secret))
	require.Equal(t, http.StatusUnauthorized, rec.Code, "a revoked key no longer authenticates")

	entries := e.db.auditLog()
	require.Len(t, entries, 2)
	assert.Equal(t, "api_key_revoked", entries[1].Action)
	assert.Equal(t, created.ID, entries[1].Details["key_id"])
}

func TestRevokeAPIKeyIsAuditedOnce(t *testing.T) {
	e := newEnv(t)
	admin := e.apiKey("admin")

	rec := e.do(bearer(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "ci"}), admin))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decodeBody[v1.CreatedAPIKey](t, rec)

	for i := 0; i < 3; i++ {
		rec = e.do(bearer(deleteReq(t, "/v1/keys/"+created.ID), admin))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	var revokes int
	for _, entry := range e.db.auditLog() {
		if entry.Action == "api_key_revoked" {
			revokes++
		}
	}
	assert.Equal(t, 1, revokes, "only the call that revokes the key is audited")
}

func TestRevokeAPIKeyErrors(t *testing.T) {
	e := newEnv(t)
	admin := e.apiKey("admin")

	rec := e.do(bearer(deleteReq(t, "/v1/keys/"+uuid.NewString()), admin))
	assert.Equal(t, http.StatusNotFound, rec.Code, "unknown id")

	rec = e.do(bearer(deleteReq(t, "/v1/keys/not-a-uuid"), admin))
	assert.Equal(t, http.StatusNotFound, rec.Code, "malformed id")
	assert.Empty(t, e.db.auditLog())
}

func TestAPIKeyManagementRequiresAdminKey(t *testing.T) {
	e := newEnv(t)
	session := e.session("octocat", "acme")

	t.Run("session cannot list", func(t *testing.T) {
		rec := e.do(withCookie(newRequest(t, http.MethodGet, "/v1/keys", nil), session))
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Contains(t, errorOf(t, rec).Message, "administrator API key")
	})

	t.Run("same-origin session cannot create", func(t *testing.T) {
		r := withCookie(sameOriginPost(t, "/v1/keys", v1.CreateAPIKeyRequest{Name: "ci"}), session)
		rec := e.do(r)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Empty(t, e.db.auditLog())
	})

	t.Run("runner token is refused", func(t *testing.T) {
		rec := e.do(bearer(newRequest(t, http.MethodGet, "/v1/keys", nil), "eyJhbGciOiJSUzI1NiJ9.e30.sig"))
		require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	})

	t.Run("no credentials", func(t *testing.T) {
		rec := e.do(newRequest(t, http.MethodGet, "/v1/keys", nil))
		require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	})
}

func deleteReq(t *testing.T, target string) *http.Request {
	t.Helper()
	r := newRequest(t, http.MethodDelete, target, nil)
	r.Header.Set("Origin", testBaseURL)
	return r
}
