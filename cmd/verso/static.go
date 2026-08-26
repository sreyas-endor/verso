package main

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// distFS holds the built client. `make build` copies web/dist here before the
// server is compiled; the committed placeholder keeps a first Go build usable.
//
// all: is required — without it, go:embed silently skips Vite's hashed asset
// files whose names begin with an underscore.
//
//go:embed all:dist
var distFS embed.FS

// staticFS returns the filesystem the client is served from: the embedded build
// normally, a directory on disk under -dev.
func staticFS(dev bool, dir string) (fs.FS, error) {
	if dev {
		if _, err := os.Stat(dir); err != nil {
			return nil, err
		}
		return os.DirFS(dir), nil
	}
	return fs.Sub(distFS, "dist")
}

// spaHandler serves the built client with a single-page fallback: any path that
// is not a real file gets index.html, so a deep link to /#ABCDE loads the app
// instead of a 404.
type spaHandler struct {
	fsys  fs.FS
	files http.Handler
}

func newSPAHandler(fsys fs.FS) http.Handler {
	return &spaHandler{fsys: fsys, files: http.FileServerFS(fsys)}
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}

	if info, err := fs.Stat(h.fsys, name); err == nil && !info.IsDir() {
		// Vite fingerprints everything under /assets, so those may be cached
		// forever; index.html must not be, or a rebuilt client never reaches a
		// browser that already has one.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.files.ServeHTTP(w, r)
		return
	}

	// A missing file with an extension is a genuine 404: serving HTML in place
	// of a stylesheet only produces a confusing console error.
	if ext := path.Ext(name); ext != "" && ext != ".html" {
		http.NotFound(w, r)
		return
	}

	index, err := fs.ReadFile(h.fsys, "index.html")
	if err != nil {
		http.Error(w, "client not built", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(index)
	}
}
