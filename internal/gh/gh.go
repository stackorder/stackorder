// Package gh is Stackorder's GitHub App client, written directly on net/http.
//
// An App authenticates with a short lived JWT signed by the App's private key
// and exchanges it for installation tokens, cached per installation until
// five minutes before they expire. A Client is scoped to one installation
// (or to one user OAuth token for the human sign-in flow) and exposes only
// the endpoints and fields Stackorder needs: check runs, the sticky PR
// comment, reviews, permissions and team membership, workflow dispatch and
// run reconciliation, contents, tags, drift issues and deployment protection
// rules. Every request retries network errors, 5xx responses and rate limits
// with backoff, honouring Retry-After and X-RateLimit-Reset.
package gh

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the REST API root of github.com.
	DefaultBaseURL = "https://api.github.com"
	// DefaultWebURL is the web root of github.com, used for OAuth.
	DefaultWebURL = "https://github.com"
	// APIVersion is the REST API version sent with every request.
	APIVersion = "2022-11-28"
	// MediaType is the Accept header sent with every request.
	MediaType = "application/vnd.github+json"
)

var (
	// ErrNotFound is matched by errors.Is for any 404 response.
	ErrNotFound = errors.New("gh: not found")
	// ErrRateLimited is matched by errors.Is when GitHub refused a request
	// for rate limiting and retrying did not help within the allowed wait.
	ErrRateLimited = errors.New("gh: rate limited")
	// ErrUnsupportedEvent is returned by ParseEvent for event names this
	// package does not decode.
	ErrUnsupportedEvent = errors.New("gh: unsupported event")
)

// Metrics receives request and rate limit observations. Implementations
// must be safe for concurrent use.
type Metrics interface {
	// ObserveRequest records one HTTP attempt. route is the endpoint
	// template with ids replaced by placeholders, and status is 0 when no
	// response was received.
	ObserveRequest(method, route string, status int, d time.Duration)
	// ObserveRateLimit records the rate limit headers of a response.
	ObserveRateLimit(remaining int, reset time.Time)
}

// Config configures an App or a token Client.
type Config struct {
	// AppID is the numeric GitHub App id.
	AppID int64
	// PrivateKey signs the App JWT.
	PrivateKey *rsa.PrivateKey
	// BaseURL is the REST API root; DefaultBaseURL when empty. GitHub
	// Enterprise Server uses https://HOST/api/v3.
	BaseURL string
	// HTTPClient sends requests; a client with a 30 s timeout when nil.
	HTTPClient *http.Client
	// UserAgent is sent with every request; "stackorder" when empty.
	UserAgent string
	// Metrics is optional.
	Metrics Metrics
	// Clock returns the current time; time.Now when nil. It drives JWT
	// claims, token expiry and rate limit reset arithmetic.
	Clock func() time.Time
	// MaxAttempts bounds the attempts per request, 5 when zero.
	MaxAttempts int
	// RetryBaseDelay is the first backoff delay, doubled on every retry;
	// 1 s when zero.
	RetryBaseDelay time.Duration
	// MaxRetryWait is the longest single wait accepted before a retry. A
	// rate limit that resets later fails at once with ErrRateLimited.
	// 1 minute when zero.
	MaxRetryWait time.Duration
}

// APIError is a non-2xx response from GitHub.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Message is GitHub's error message.
	Message string
	// DocumentationURL is GitHub's link to the relevant documentation.
	DocumentationURL string
	// Method and Route identify the endpoint, with ids as placeholders.
	Method string
	Route  string
	// Errors carries GitHub's validation details for 422 responses.
	Errors []ErrorDetail

	rateLimited bool
}

// ErrorDetail is one entry of the errors array of a validation failure.
type ErrorDetail struct {
	Resource string `json:"resource,omitempty"`
	Field    string `json:"field,omitempty"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
}

// Error implements the error interface.
func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "gh: %s %s: %d", e.Method, e.Route, e.Status)
	if e.Message != "" {
		b.WriteString(" ")
		b.WriteString(e.Message)
	}
	for _, d := range e.Errors {
		switch {
		case d.Message != "":
			fmt.Fprintf(&b, "; %s", d.Message)
		case d.Field != "":
			fmt.Fprintf(&b, "; %s.%s %s", d.Resource, d.Field, d.Code)
		}
	}
	return b.String()
}

// Is lets errors.Is match ErrNotFound and ErrRateLimited.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrRateLimited:
		return e.rateLimited
	}
	return false
}

// RateLimited reports whether GitHub refused the request for rate limiting.
func (e *APIError) RateLimited() bool { return e.rateLimited }
