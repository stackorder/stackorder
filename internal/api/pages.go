package api

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/stackorder/stackorder/internal/version"
)

//go:embed templates/*.html
var templateFS embed.FS

var pageTemplates = parsePages()

func parsePages() map[string]*template.Template {
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		panic(err)
	}
	out := map[string]*template.Template{}
	for _, name := range names {
		base := path.Base(name)
		if base == "layout.html" {
			continue
		}
		out[strings.TrimSuffix(base, ".html")] = template.Must(template.ParseFS(templateFS, "templates/layout.html", name))
	}
	return out
}

type pageLink struct {
	Href string
	Text string
}

type message struct {
	Heading string
	Lines   []string
	Items   []string
	Links   []pageLink
}

type pageData struct {
	Title   string
	Nonce   string
	Version string
	Body    any
}

func newNonce() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawStdEncoding.EncodeToString(b)
}

func (s *server) renderPage(w http.ResponseWriter, r *http.Request, status int, name, title string, formAction string, body any) {
	t, ok := pageTemplates[name]
	if !ok {
		panic("api: no page template " + name)
	}
	nonce := newNonce()
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", pageData{Title: title, Nonce: nonce, Version: version.Version, Body: body}); err != nil {
		s.log.ErrorContext(r.Context(), "render page", "request_id", requestIDOf(r), "page", name, "error", err.Error())
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if formAction == "" {
		formAction = "'none'"
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'none'",
		"script-src 'nonce-" + nonce + "'",
		"style-src 'nonce-" + nonce + "'",
		"img-src 'self'",
		"form-action " + formAction,
		"base-uri 'none'",
		"frame-ancestors 'none'",
	}, "; "))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

func (s *server) renderMessage(w http.ResponseWriter, r *http.Request, status int, title string, m message) {
	s.renderPage(w, r, status, "message", title, "", m)
}
