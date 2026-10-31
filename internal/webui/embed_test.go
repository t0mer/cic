package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":  {Data: []byte("<html>app</html>")},
		"assets/a.js": {Data: []byte("js")},
		"favicon.svg": {Data: []byte("svg")},
	}
	h := NewHandler(fsys)
	tests := []struct {
		path, body, cache string
		code              int
	}{
		{"/", "app", "no-cache", 200},
		{"/index.html", "app", "no-cache", 200},
		{"/assets/a.js", "js", "public, max-age=31536000, immutable", 200},
		{"/favicon.svg", "svg", "no-cache", 200},
		{"/devices/42", "app", "no-cache", 200},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.body) {
				t.Fatalf("code %d body %q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.cache {
				t.Fatalf("cache %q want %q", got, tt.cache)
			}
		})
	}
}

func TestHandlerNotFoundAndMethods(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":  {Data: []byte("<html>app</html>")},
		"assets/a.js": {Data: []byte("js")},
		"secret.txt":  {Data: []byte("top")},
	}
	h := NewHandler(fsys)
	tests := []struct {
		method, path string
		code         int
		allow        string
		wantHTML     bool
	}{
		{"GET", "/api/nope", 404, "", false},
		{"GET", "/api", 404, "", false},
		{"GET", "/assets/nope.js", 404, "", false},
		{"GET", "/assets/nope", 404, "", false},
		{"GET", "/assets", 404, "", false},
		{"GET", "/missing.js", 404, "", false},
		{"GET", "/deep/path/x.css", 404, "", false},
		{"HEAD", "/missing.js", 404, "", false},
		{"GET", "/devices/42", 200, "", true},
		{"HEAD", "/devices/42", 200, "", false},
		{"POST", "/", 405, "GET, HEAD", false},
		{"PUT", "/devices/42", 405, "GET, HEAD", false},
		{"DELETE", "/api/x", 405, "GET, HEAD", false},
		{"GET", "/../etc/passwd", 200, "", true},
		{"GET", "/%2e%2e/x", 200, "", true},
		{"GET", "/%2e%2e/%2e%2e/etc/passwd", 200, "", true},
		{"GET", "/assets/../secret.txt", 200, "", false},
		{"GET", "/..%2fetc/passwd", 200, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.code {
				t.Fatalf("code %d want %d body %q", rec.Code, tt.code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != tt.allow {
				t.Fatalf("Allow %q want %q", got, tt.allow)
			}
			if tt.code == 404 && strings.Contains(rec.Body.String(), "<html>") {
				t.Fatalf("404 returned HTML: %q", rec.Body.String())
			}
			if tt.wantHTML && !strings.Contains(rec.Body.String(), "app") {
				t.Fatalf("expected index.html, got %q", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "root:") {
				t.Fatalf("escaped the FS: %q", rec.Body.String())
			}
		})
	}
}

func TestHandlerNoIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code %d", rec.Code)
	}
}
