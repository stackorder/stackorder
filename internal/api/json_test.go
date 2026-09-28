package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRecorderFor(t *testing.T, e *testEnv, err error) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.srv.writeError(rec, newRequest(t, http.MethodGet, "/v1/x", nil), err)
	return rec
}

type sample struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestDecodeJSON(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		required    bool
		limit       int64
		want        sample
		status      int
		message     string
	}{
		{name: "valid", contentType: "application/json", body: `{"name":"a","count":2}`, want: sample{Name: "a", Count: 2}},
		{name: "unknown fields are ignored", contentType: "application/json; charset=utf-8", body: `{"name":"a","future":true}`, want: sample{Name: "a"}},
		{name: "vendor json", contentType: "application/vnd.stackorder+json", body: `{"count":1}`, want: sample{Count: 1}},
		{name: "no content type", body: `{"name":"b"}`, want: sample{Name: "b"}},
		{name: "trailing whitespace", body: "{\"name\":\"c\"}\n\t ", want: sample{Name: "c"}},
		{name: "empty optional", body: "", want: sample{}},
		{name: "empty required", body: "", required: true, status: 400, message: "a JSON request body is required"},
		{name: "form", contentType: "application/x-www-form-urlencoded", body: "name=a", status: 400, message: "the request body must be application/json"},
		{name: "text", contentType: "text/plain", body: `{"name":"a"}`, status: 400, message: "the request body must be application/json"},
		{name: "bad media type", contentType: ";;", body: `{}`, status: 400, message: "the request body must be application/json"},
		{name: "not json", body: "hello", status: 400, message: "the request body is not valid JSON"},
		{name: "truncated", body: `{"name":`, status: 400, message: "the request body is not valid JSON"},
		{name: "wrong type", body: `{"count":"x"}`, status: 400, message: "field count: cannot use a JSON string as int"},
		{name: "not an object", body: `[1]`, status: 400, message: "field body: cannot use a JSON array"},
		{name: "second value", body: `{"name":"a"} {"name":"b"}`, status: 400, message: "the request body has data after the JSON value"},
		{name: "garbage after", body: `{"name":"a"} x`, status: 400, message: "the request body is not valid JSON"},
		{name: "too large", body: `{"name":"` + strings.Repeat("x", 100) + `"}`, limit: 32, status: 413, message: "the request body exceeds 32 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(tc.body))
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			if tc.limit > 0 {
				r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, tc.limit)
			}
			var got sample
			err := decodeJSON(r, &got, tc.required)
			if tc.status == 0 {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
				return
			}
			e, unexpected := toAPIError(err)
			assert.False(t, unexpected)
			assert.Equal(t, tc.status, e.status)
			assert.Equal(t, codeInvalid, e.body.Code)
			assert.Contains(t, e.body.Message, tc.message)
		})
	}
}

func TestLimitBody(t *testing.T) {
	var got error
	h := limitBody(8, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var v map[string]string
		got = decodeJSON(r, &v, true)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":"0123456789"}`)))
	e, _ := toAPIError(got)
	assert.Equal(t, http.StatusRequestEntityTooLarge, e.status)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":""}`)))
	assert.NoError(t, got)
}
