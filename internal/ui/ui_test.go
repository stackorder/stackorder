package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const (
	testIndex = `<!doctype html><html><head><script type="module" src="/assets/index-3f2a1b.js"></script></head><body><div id="app"></div></body></html>`
	testJS    = `console.log("stackorder")`
	testCSS   = `body{margin:0}`
	testIcon  = `<svg xmlns="http://www.w3.org/2000/svg"></svg>`
)

func builtFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte(testIndex)},
		"favicon.svg":             {Data: []byte(testIcon)},
		"assets/index-3f2a1b.js":  {Data: []byte(testJS)},
		"assets/index-9c8d7e.css": {Data: []byte(testCSS)},
		".gitkeep":                {Data: nil},
		".secret/key.json":        {Data: []byte(`{"k":"v"}`)},
	}
}

func serve(t *testing.T, h http.Handler, method, target string, header http.Header) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestHandlerFSServesBuiltUI(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		target      string
		status      int
		body        string
		contentType string
		cache       string
	}{
		{"root serves index", http.MethodGet, "/", http.StatusOK, testIndex, "text/html; charset=utf-8", cacheRevalidate},
		{"explicit index", http.MethodGet, "/index.html", http.StatusOK, testIndex, "text/html; charset=utf-8", cacheRevalidate},
		{"route falls back to index", http.MethodGet, "/runs/5b0c6a52-8d1e-4a57-9a52-1f3f0d6f2a10", http.StatusOK, testIndex, "text/html; charset=utf-8", cacheRevalidate},
		{"nested route falls back", http.MethodGet, "/repos/acme/infra", http.StatusOK, testIndex, "text/html; charset=utf-8", cacheRevalidate},
		{"route with query falls back", http.MethodGet, "/repos/acme/infra?run=abc&ref=main", http.StatusOK, testIndex, "text/html; charset=utf-8", cacheRevalidate},
		{"dotted repo name is a route", http.MethodGet, "/repos/acme/acme.github.io", http.StatusOK, testIndex, "text/html; charset=utf-8", cacheRevalidate},
		{"dot repo name is a route", http.MethodGet, "/repos/acme/.github", http.StatusOK, testIndex, "text/html; charset=utf-8", cacheRevalidate},
		{"traversal is cleaned to a route", http.MethodGet, "/../../etc/passwd", http.StatusOK, testIndex, "text/html; charset=utf-8", cacheRevalidate},
		{"assets directory is a route", http.MethodGet, "/assets/", http.StatusOK, testIndex, "text/html; charset=utf-8", cacheRevalidate},
		{"hashed script is immutable", http.MethodGet, "/assets/index-3f2a1b.js", http.StatusOK, testJS, "text/javascript; charset=utf-8", cacheImmutable},
		{"hashed stylesheet is immutable", http.MethodGet, "/assets/index-9c8d7e.css", http.StatusOK, testCSS, "text/css; charset=utf-8", cacheImmutable},
		{"unhashed root file revalidates", http.MethodGet, "/favicon.svg", http.StatusOK, testIcon, "image/svg+xml", cacheRevalidate},
		{"missing asset is 404", http.MethodGet, "/assets/index-000000.js", http.StatusNotFound, "404 page not found\n", "text/plain; charset=utf-8", ""},
		{"missing extension file is 404", http.MethodGet, "/logo.png", http.StatusNotFound, "404 page not found\n", "text/plain; charset=utf-8", ""},
		{"missing file under assets without extension is 404", http.MethodGet, "/assets/chunk", http.StatusNotFound, "404 page not found\n", "text/plain; charset=utf-8", ""},
		{"hidden file is 404", http.MethodGet, "/.secret/key.json", http.StatusNotFound, "404 page not found\n", "text/plain; charset=utf-8", ""},
		{"head has no body", http.MethodHead, "/", http.StatusOK, "", "text/html; charset=utf-8", cacheRevalidate},
		{"post is rejected", http.MethodPost, "/", http.StatusMethodNotAllowed, "Method Not Allowed\n", "text/plain; charset=utf-8", ""},
	}
	h := HandlerFS(builtFS())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := serve(t, h, tt.method, tt.target, nil)
			body := readBody(t, res)
			if res.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d (body %q)", res.StatusCode, tt.status, body)
			}
			if body != tt.body {
				t.Errorf("body = %q, want %q", body, tt.body)
			}
			if got := res.Header.Get("Content-Type"); got != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tt.contentType)
			}
			if got := res.Header.Get("Cache-Control"); got != tt.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.cache)
			}
			if got := res.Header.Get("X-Content-Type-Options"); tt.status != http.StatusMethodNotAllowed && got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if tt.status == http.StatusMethodNotAllowed && res.Header.Get("Allow") != "GET, HEAD" {
				t.Errorf("Allow = %q, want %q", res.Header.Get("Allow"), "GET, HEAD")
			}
		})
	}
}

func TestHandlerFSConditionalRequests(t *testing.T) {
	h := HandlerFS(builtFS())
	tests := []struct {
		name   string
		target string
	}{
		{"index", "/"},
		{"route", "/stacks/1b6f0c1e-3d0a-4f7e-8c55-0f3e2d1c9a77"},
		{"asset", "/assets/index-3f2a1b.js"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := serve(t, h, http.MethodGet, tt.target, nil)
			readBody(t, first)
			etag := first.Header.Get("ETag")
			if etag == "" {
				t.Fatal("no ETag on first response")
			}
			second := serve(t, h, http.MethodGet, tt.target, http.Header{"If-None-Match": {etag}})
			readBody(t, second)
			if second.StatusCode != http.StatusNotModified {
				t.Fatalf("status = %d, want %d", second.StatusCode, http.StatusNotModified)
			}
		})
	}
}

func TestHandlerFSNotBuilt(t *testing.T) {
	tests := []struct {
		name   string
		fsys   fstest.MapFS
		target string
		status int
		body   string
	}{
		{"empty dist root", fstest.MapFS{}, "/", http.StatusServiceUnavailable, notBuiltMessage},
		{"gitkeep only root", fstest.MapFS{".gitkeep": {}}, "/", http.StatusServiceUnavailable, notBuiltMessage},
		{"gitkeep only route", fstest.MapFS{".gitkeep": {}}, "/runs/abc", http.StatusServiceUnavailable, notBuiltMessage},
		{"gitkeep only asset", fstest.MapFS{".gitkeep": {}}, "/assets/index-3f2a1b.js", http.StatusNotFound, "404 page not found\n"},
		{"index is a directory", fstest.MapFS{"index.html/x": {}}, "/", http.StatusServiceUnavailable, notBuiltMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := serve(t, HandlerFS(tt.fsys), http.MethodGet, tt.target, nil)
			body := readBody(t, res)
			if res.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tt.status)
			}
			if body != tt.body {
				t.Errorf("body = %q, want %q", body, tt.body)
			}
			if got := res.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/plain", got)
			}
		})
	}
	if !strings.Contains(notBuiltMessage, "make ui") {
		t.Errorf("not-built message does not say how to build: %q", notBuiltMessage)
	}
}

func TestHandlerEmbedded(t *testing.T) {
	res := serve(t, Handler(), http.MethodGet, "/", nil)
	body := readBody(t, res)
	switch res.StatusCode {
	case http.StatusOK:
		if !strings.Contains(body, `<div id="app"></div>`) {
			t.Errorf("embedded index.html has no app root: %q", body)
		}
	case http.StatusServiceUnavailable:
		if body != notBuiltMessage {
			t.Errorf("body = %q, want the not-built message", body)
		}
	default:
		t.Fatalf("status = %d, want 200 with a build or 503 without", res.StatusCode)
	}
}
