// Package webui serves the embedded single-page frontend.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded frontend rooted at dist.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // dist is embedded at compile time
	}
	return sub
}

// Handler serves the embedded frontend with SPA fallback.
func Handler() http.Handler { return NewHandler(FS()) }

// NewHandler serves files from fsys. Only extension-less GET/HEAD paths that
// match no file fall back to index.html; missing /api/ and /assets/ paths and
// missing files with an extension are plain-text 404s.
func NewHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// path.Clean on a rooted path removes any "..", so name cannot escape fsys.
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
			setCache(w, name)
			if name == "index.html" {
				r = cloneWithPath(r, "/")
			} else {
				r = cloneWithPath(r, "/"+name)
			}
			files.ServeHTTP(w, r)
			return
		}
		if isReserved(name) || path.Ext(name) != "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if _, err := fs.Stat(fsys, "index.html"); err != nil {
			http.Error(w, "frontend not built", http.StatusNotFound)
			return
		}
		setCache(w, "index.html")
		files.ServeHTTP(w, cloneWithPath(r, "/"))
	})
}

// isReserved reports whether name is under a prefix that never falls back to the SPA.
func isReserved(name string) bool {
	for _, p := range []string{"api", "assets"} {
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}
	return false
}

func cloneWithPath(r *http.Request, p string) *http.Request {
	r2 := r.Clone(r.Context())
	u := *r.URL
	u.Path = p
	r2.URL = &u
	return r2
}

func setCache(w http.ResponseWriter, name string) {
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
}
