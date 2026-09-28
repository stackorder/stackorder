// Package ui serves the single-page web UI that `make ui` builds from the ui/
// directory into dist and that the server binary embeds.
package ui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

const (
	indexFile       = "index.html"
	assetsDir       = "assets/"
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
)

const notBuiltMessage = `The Stackorder web UI was not built into this binary.

Build it from the repository root with:

    make ui

then rebuild the server. The JSON API under /v1 works without it.
`

var staticExtensions = map[string]bool{
	".avif": true, ".css": true, ".gif": true, ".html": true, ".ico": true,
	".jpeg": true, ".jpg": true, ".js": true, ".json": true, ".map": true,
	".mjs": true, ".otf": true, ".png": true, ".svg": true, ".ttf": true,
	".txt": true, ".wasm": true, ".webmanifest": true, ".webp": true,
	".woff": true, ".woff2": true, ".xml": true,
}

// Handler serves the UI embedded in the binary at build time.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(fmt.Errorf("ui: embedded dist: %w", err))
	}
	return HandlerFS(sub)
}

// HandlerFS serves a built UI from fsys, whose root holds index.html and the
// assets directory. A path naming a file in fsys is served as that file;
// files under assets/ are content hashed and cached as immutable, others
// are revalidated. No path with a segment starting with a dot is served as
// a file. A missing file under assets/, or a missing top-level or dotted
// path with a static file extension, is 404. Every other path is a
// client-side route and gets index.html with no-cache, so a route such as
// /repos/acme/chart.js still loads the app. When fsys has no index.html,
// routes get a plain-text page explaining how to build the UI, with status
// 503.
func HandlerFS(fsys fs.FS) http.Handler {
	return &handler{fsys: fsys}
}

type handler struct {
	fsys fs.FS
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	immutable := strings.HasPrefix(name, assetsDir)
	if fs.ValidPath(name) && !hidden(name) {
		cache := cacheRevalidate
		if immutable {
			cache = cacheImmutable
		}
		err := h.serveFile(w, r, name, cache)
		if err == nil {
			return
		}
		if !errors.Is(err, fs.ErrNotExist) {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
	}
	static := staticExtensions[strings.ToLower(path.Ext(name))]
	if immutable || (static && (hidden(name) || !strings.Contains(name, "/"))) {
		http.NotFound(w, r)
		return
	}
	h.serveIndex(w, r)
}

func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	err := h.serveFile(w, r, indexFile, cacheRevalidate)
	if err == nil {
		return
	}
	if !errors.Is(err, fs.ErrNotExist) {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(notBuiltMessage))
	}
}

func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, name, cache string) error {
	info, err := fs.Stat(h.fsys, name)
	if err != nil {
		return fmt.Errorf("stat %s: %w", name, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory: %w", name, fs.ErrNotExist)
	}
	data, err := fs.ReadFile(h.fsys, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	sum := sha256.Sum256(data)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:12])+`"`)
	w.Header().Set("Cache-Control", cache)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	return nil
}

func hidden(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}
