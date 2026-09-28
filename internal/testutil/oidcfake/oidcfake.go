// Package oidcfake is a fake GitHub Actions OIDC issuer for tests. An Issuer
// signs tokens with RSA keys it publishes on an httptest server, keeps its
// own clock, and can emulate the runner's token request endpoint so that
// CLI code can be tested end to end without GitHub.
package oidcfake

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stackorder/stackorder/internal/oidc"
)

const (
	// DefaultAudience is the aud of minted tokens until Config is called.
	DefaultAudience = "https://stackorder.test"
	// TokenLifetime is the default exp - iat of minted tokens, as on GitHub.
	TokenLifetime = 5 * time.Minute
	// DiscoveryPath serves the OpenID configuration document.
	DiscoveryPath = "/.well-known/openid-configuration"
	// PlanWorkflowRef is the job_workflow_ref PlanClaims sets.
	PlanWorkflowRef = "stackorder/actions/.github/workflows/plan.yml@refs/tags/v1"
	// RunWorkflowRef is the job_workflow_ref DispatchClaims sets.
	RunWorkflowRef = "stackorder/actions/.github/workflows/run.yml@refs/tags/v1"
	// Actor is the actor PlanClaims and DispatchClaims set.
	Actor = "octocat"
	// KeyBits is the size of generated RSA keys.
	KeyBits = 2048
)

// Epoch is where the clock of every new Issuer starts.
var Epoch = time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)

type signingKey struct {
	kid       string
	key       *rsa.PrivateKey
	published bool
}

// Issuer is a fake token.actions.githubusercontent.com. It is safe for
// concurrent use.
type Issuer struct {
	srv *httptest.Server

	mu         sync.Mutex
	now        time.Time
	keys       []*signingKey
	audience   string
	jwksStatus int
	jwksHits   int
	nextRunID  int64
}

// New starts an issuer with one RSA signing key and registers its shutdown
// with t.Cleanup.
func New(t testing.TB) *Issuer {
	t.Helper()
	i := &Issuer{now: Epoch, audience: DefaultAudience, nextRunID: 17000000000}
	i.keys = append(i.keys, &signingKey{kid: "kid-1", key: GenerateKey(t), published: true})
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+DiscoveryPath, i.serveDiscovery)
	mux.HandleFunc("GET "+oidc.JWKSPath, i.serveJWKS)
	i.srv = httptest.NewServer(mux)
	t.Cleanup(i.srv.Close)
	return i
}

// GenerateKey returns a new RSA key of KeyBits bits, for WithSigningKey.
func GenerateKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, KeyBits)
	if err != nil {
		t.Fatalf("oidcfake: generating RSA key: %v", err)
	}
	return key
}

// URL is the issuer, i.e. the iss claim of minted tokens.
func (i *Issuer) URL() string { return i.srv.URL }

// JWKSURL is where the issuer publishes its keys.
func (i *Issuer) JWKSURL() string { return i.srv.URL + oidc.JWKSPath }

// Config returns a verifier configuration for this issuer with the given
// audience and the issuer's clock. Tokens minted afterwards default to that
// audience.
func (i *Issuer) Config(audience string) oidc.Config {
	i.mu.Lock()
	i.audience = audience
	i.mu.Unlock()
	return oidc.Config{
		Issuer:     i.URL(),
		JWKSURL:    i.JWKSURL(),
		Audience:   audience,
		HTTPClient: i.srv.Client(),
		Clock:      i.Now,
	}
}

// Now returns the issuer's clock.
func (i *Issuer) Now() time.Time {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.now
}

// Advance moves the issuer's clock forward by d.
func (i *Issuer) Advance(d time.Duration) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.now = i.now.Add(d)
}

// KID returns the kid of the key new tokens are signed with.
func (i *Issuer) KID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.keys[len(i.keys)-1].kid
}

// Rotate adds a new signing key to the JWKS and signs new tokens with it.
// Earlier keys stay published until Retire. It returns the new kid.
func (i *Issuer) Rotate() string {
	key, err := rsa.GenerateKey(rand.Reader, KeyBits)
	if err != nil {
		panic(fmt.Sprintf("oidcfake: generating RSA key: %v", err))
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	sk := &signingKey{kid: "kid-" + strconv.Itoa(len(i.keys)+1), key: key, published: true}
	i.keys = append(i.keys, sk)
	return sk.kid
}

// Retire removes kid from the published JWKS. Tokens can still be signed
// with it through WithKID, which is how tests present a rotated-out key.
func (i *Issuer) Retire(kid string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, k := range i.keys {
		if k.kid == kid {
			k.published = false
		}
	}
}

// FailJWKS makes the JWKS endpoint answer with status, or restores it when
// status is 0 or 200.
func (i *Issuer) FailJWKS(status int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.jwksStatus = status
}

// JWKSRequests returns how many times the JWKS endpoint was requested.
func (i *Issuer) JWKSRequests() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.jwksHits
}

func (i *Issuer) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                i.URL(),
		"jwks_uri":                              i.JWKSURL(),
		"subject_types_supported":               []string{"public", "pairwise"},
		"response_types_supported":              []string{"id_token"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid"},
		"claims_supported": []string{
			"sub", "aud", "exp", "iat", "iss", "jti", "nbf", "ref", "sha", "repository",
			"repository_id", "repository_owner", "repository_owner_id", "run_id", "run_number",
			"run_attempt", "actor", "actor_id", "workflow", "workflow_ref", "workflow_sha",
			"head_ref", "base_ref", "event_name", "ref_type", "environment", "job_workflow_ref",
			"job_workflow_sha", "repository_visibility", "runner_environment", "enterprise",
		},
	})
}

func (i *Issuer) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	i.mu.Lock()
	i.jwksHits++
	status := i.jwksStatus
	type jwk struct {
		Kty string `json:"kty"`
		Alg string `json:"alg"`
		Use string `json:"use"`
		Kid string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
	}
	keys := make([]jwk, 0, len(i.keys))
	for _, k := range i.keys {
		if !k.published {
			continue
		}
		keys = append(keys, jwk{
			Kty: "RSA",
			Alg: "RS256",
			Use: "sig",
			Kid: k.kid,
			N:   base64.RawURLEncoding.EncodeToString(k.key.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.key.E)).Bytes()),
		})
	}
	i.mu.Unlock()
	if status != 0 && status != http.StatusOK {
		http.Error(w, "jwks unavailable", status)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// TokenOption changes how Token signs a token, for negative tests.
type TokenOption func(*tokenOptions)

type tokenOptions struct {
	kid     *string
	alg     string
	key     *rsa.PrivateKey
	header  map[string]any
	set     map[string]any
	without []string
}

// WithKID sets the kid header; an empty kid omits the header.
func WithKID(kid string) TokenOption {
	return func(o *tokenOptions) { o.kid = &kid }
}

// WithAlg signs with another algorithm: "none", HS256/384/512 keyed with
// the issuer's public key in PEM form (the algorithm confusion attack),
// RS256/384/512 or PS256/384/512 with the RSA key, or ES256/384/512 with a
// throwaway ECDSA key. Any other value panics.
func WithAlg(alg string) TokenOption {
	return func(o *tokenOptions) { o.alg = alg }
}

// WithSigningKey signs with key instead of the issuer's current key while
// keeping the current kid, unless WithKID says otherwise.
func WithSigningKey(key *rsa.PrivateKey) TokenOption {
	return func(o *tokenOptions) { o.key = key }
}

// WithHeader sets an extra JOSE header field.
func WithHeader(name string, value any) TokenOption {
	return func(o *tokenOptions) {
		if o.header == nil {
			o.header = map[string]any{}
		}
		o.header[name] = value
	}
}

// WithClaim sets a payload field after defaults are applied, with any JSON
// value, e.g. a number where GitHub sends a string.
func WithClaim(name string, value any) TokenOption {
	return func(o *tokenOptions) {
		if o.set == nil {
			o.set = map[string]any{}
		}
		o.set[name] = value
	}
}

// WithoutClaim removes a payload field after defaults are applied, e.g.
// "exp" or "jti".
func WithoutClaim(name string) TokenOption {
	return func(o *tokenOptions) { o.without = append(o.without, name) }
}

// Token mints a signed token. Zero registered claims are filled in: iss is
// URL(), aud the audience of the last Config call (DefaultAudience before
// one), iat and nbf the issuer's clock, exp iat + TokenLifetime, jti a
// random value and sub the value GitHub derives from repository,
// environment, event and ref. A single audience is encoded as a JSON string
// and several as an array, as GitHub does. Token panics when an option
// cannot be honoured.
func (i *Issuer) Token(claims oidc.Claims, opts ...TokenOption) string {
	raw, err := i.mint(claims, opts...)
	if err != nil {
		panic(err)
	}
	return raw
}

func (i *Issuer) mint(claims oidc.Claims, opts ...TokenOption) (string, error) {
	var o tokenOptions
	for _, opt := range opts {
		opt(&o)
	}
	i.mu.Lock()
	now, aud, current := i.now, i.audience, i.keys[len(i.keys)-1]
	signer := current
	if o.kid != nil {
		for _, k := range i.keys {
			if k.kid == *o.kid {
				signer = k
			}
		}
	}
	i.mu.Unlock()

	if claims.Issuer == "" {
		claims.Issuer = i.URL()
	}
	if len(claims.Audience) == 0 {
		claims.Audience = jwt.ClaimStrings{aud}
	}
	if claims.IssuedAt == nil {
		claims.IssuedAt = jwt.NewNumericDate(now)
	}
	if claims.NotBefore == nil {
		claims.NotBefore = claims.IssuedAt
	}
	if claims.ExpiresAt == nil {
		claims.ExpiresAt = jwt.NewNumericDate(claims.IssuedAt.Add(TokenLifetime))
	}
	if claims.ID == "" {
		claims.ID = rand.Text()
	}
	if claims.Subject == "" {
		claims.Subject = Subject(claims)
	}
	payload, err := payloadOf(claims)
	if err != nil {
		return "", err
	}
	for k, v := range o.set {
		payload[k] = v
	}
	for _, k := range o.without {
		delete(payload, k)
	}

	kid := signer.kid
	if o.kid != nil {
		kid = *o.kid
	}
	key := signer.key
	if o.key != nil {
		key = o.key
	}
	alg := o.alg
	if alg == "" {
		alg = jwt.SigningMethodRS256.Alg()
	}
	method, signingKey, err := signerFor(alg, key)
	if err != nil {
		return "", err
	}
	tok := jwt.NewWithClaims(method, payload)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	for k, v := range o.header {
		tok.Header[k] = v
	}
	raw, err := tok.SignedString(signingKey)
	if err != nil {
		return "", fmt.Errorf("oidcfake: signing %s token: %w", alg, err)
	}
	return raw, nil
}

// Subject returns the sub claim GitHub derives for c with the default
// subject template.
func Subject(c oidc.Claims) string {
	prefix := "repo:" + c.Repository + ":"
	switch {
	case c.Environment != "":
		return prefix + "environment:" + c.Environment
	case c.EventName == "pull_request" || c.EventName == "pull_request_target":
		return prefix + "pull_request"
	default:
		return prefix + "ref:" + c.Ref
	}
}

func payloadOf(c oidc.Claims) (jwt.MapClaims, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("oidcfake: encoding claims: %w", err)
	}
	var m jwt.MapClaims
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("oidcfake: decoding claims: %w", err)
	}
	if aud, ok := m["aud"].([]any); ok && len(aud) == 1 {
		m["aud"] = aud[0]
	}
	return m, nil
}

func signerFor(alg string, key *rsa.PrivateKey) (jwt.SigningMethod, any, error) {
	method := jwt.GetSigningMethod(alg)
	switch m := method.(type) {
	case *jwt.SigningMethodRSA, *jwt.SigningMethodRSAPSS:
		return m, key, nil
	case *jwt.SigningMethodHMAC:
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			return nil, nil, fmt.Errorf("oidcfake: encoding public key: %w", err)
		}
		return m, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
	case *jwt.SigningMethodECDSA:
		curves := map[string]elliptic.Curve{"ES256": elliptic.P256(), "ES384": elliptic.P384(), "ES512": elliptic.P521()}
		ec, err := ecdsa.GenerateKey(curves[alg], rand.Reader)
		if err != nil {
			return nil, nil, fmt.Errorf("oidcfake: generating ECDSA key: %w", err)
		}
		return m, ec, nil
	}
	if alg == jwt.SigningMethodNone.Alg() {
		return jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, nil
	}
	return nil, nil, fmt.Errorf("oidcfake: unsupported alg %q", alg)
}

// PlanClaims returns the claims GitHub puts in a token for the plan job of
// stackorder-plan.yml on pull request pr at head commit sha. Each call uses
// a new run_id.
func (i *Issuer) PlanClaims(repo, repoID string, pr int, sha string) oidc.Claims {
	ref := "refs/pull/" + strconv.Itoa(pr) + "/merge"
	c := i.baseClaims(repo, repoID, sha)
	c.Ref = ref
	c.RefType = "branch"
	c.EventName = "pull_request"
	c.BaseRef = "main"
	c.HeadRef = "feature/pr-" + strconv.Itoa(pr)
	c.Workflow = "stackorder plan"
	c.WorkflowRef = repo + "/.github/workflows/stackorder-plan.yml@" + ref
	c.JobWorkflowRef = PlanWorkflowRef
	return c
}

// DispatchClaims returns the claims GitHub puts in a token for a job of
// stackorder-run.yml dispatched as Actions run runID on branch, running
// under environment env (none when empty) at commit sha.
func (i *Issuer) DispatchClaims(repo, repoID string, runID int64, env, branch, sha string) oidc.Claims {
	ref := "refs/heads/" + branch
	c := i.baseClaims(repo, repoID, sha)
	c.RunID = strconv.FormatInt(runID, 10)
	c.Ref = ref
	c.RefType = "branch"
	c.EventName = "workflow_dispatch"
	c.Environment = env
	c.Workflow = "stackorder run"
	c.WorkflowRef = repo + "/.github/workflows/stackorder-run.yml@" + ref
	c.JobWorkflowRef = RunWorkflowRef
	return c
}

func (i *Issuer) baseClaims(repo, repoID, sha string) oidc.Claims {
	i.mu.Lock()
	i.nextRunID++
	runID := i.nextRunID
	i.mu.Unlock()
	owner, _, _ := strings.Cut(repo, "/")
	return oidc.Claims{
		Repository:           repo,
		RepositoryID:         repoID,
		RepositoryOwner:      owner,
		RepositoryOwnerID:    "9919",
		RunID:                strconv.FormatInt(runID, 10),
		RunNumber:            "1",
		RunAttempt:           "1",
		SHA:                  sha,
		WorkflowSHA:          sha,
		JobWorkflowSHA:       "5f1c0f3d9a3b2e7c4d6a8b0e1f2a3b4c5d6e7f80",
		Actor:                Actor,
		ActorID:              "583231",
		RunnerEnvironment:    "github-hosted",
		RepositoryVisibility: "private",
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
