package api

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	_, ten, _ := net.ParseCIDR("10.0.0.0/8")
	trusted := []*net.IPNet{ten}
	cases := []struct {
		name, baseURL string
		tls           bool
		peer, xfp     string // direct peer and X-Forwarded-Proto
		hsts          bool
	}{
		{"plain http", "", false, "", "", false},
		{"http base url", "http://cic.local", false, "", "", false},
		{"https base url", "https://cic.example.com", false, "", "", true},
		{"https base url, mixed case", "HTTPS://cic.example.com", false, "", "", true},
		{"direct tls", "", true, "", "", true},
		{"trusted proxy says https", "", false, "10.0.0.5:1", "https", true},
		{"trusted proxy says http", "", false, "10.0.0.5:1", "http", false},
		{"untrusted peer claims https", "", false, "198.51.100.7:1", "https", false},
		{"no peer claims https", "", false, "", "https", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if c.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if c.peer != "" {
				req.RemoteAddr = c.peer
			}
			if c.xfp != "" {
				req.Header.Set("X-Forwarded-Proto", c.xfp)
			}
			rec := httptest.NewRecorder()
			SecurityHeaders(c.baseURL, trusted, []string{"https://tiles.openfreemap.org"})(ok).ServeHTTP(rec, req)
			h := rec.Header()
			want := map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "no-referrer",
				"Permissions-Policy":     "geolocation=(), camera=(), microphone=()",
				"X-Frame-Options":        "DENY",
			}
			for k, v := range want {
				if h.Get(k) != v {
					t.Errorf("%s = %q", k, h.Get(k))
				}
			}
			csp := h.Get("Content-Security-Policy")
			for _, d := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'", "form-action 'self'",
				"worker-src 'self' blob:", "img-src 'self' data: https://tiles.openfreemap.org", "connect-src 'self' https://tiles.openfreemap.org"} {
				if !strings.Contains(csp, d) {
					t.Errorf("CSP lacks %q: %s", d, csp)
				}
			}
			if strings.Contains(csp, "script-src 'self' 'unsafe") {
				t.Error("script-src must not allow unsafe-*")
			}
			if got := h.Get("Strict-Transport-Security") != ""; got != c.hsts {
				t.Errorf("HSTS present = %v, want %v", got, c.hsts)
			}
		})
	}
}

func TestBuildCSP(t *testing.T) {
	cases := []struct {
		name    string
		origins []string
		want    []string
		absent  []string
	}{
		{"none", nil, []string{"img-src 'self' data:;", "connect-src 'self';", "font-src 'self';"}, []string{"https://"}},
		{"one", []string{"https://a.example"}, []string{"img-src 'self' data: https://a.example;", "connect-src 'self' https://a.example;", "font-src 'self' https://a.example;"}, nil},
		{"dedupe and ports", []string{"https://a.example", "https://a.example", "https://b.example:8443"},
			[]string{"img-src 'self' data: https://a.example https://b.example:8443;"}, []string{"https://a.example https://a.example"}},
		{"injection ignored", []string{"https://a.example; script-src *", "x y", ""}, []string{"img-src 'self' data:;"}, []string{"script-src *", "x y"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			csp := buildCSP(c.origins)
			for _, w := range c.want {
				if !strings.Contains(csp, w) {
					t.Errorf("CSP lacks %q: %s", w, csp)
				}
			}
			for _, w := range c.absent {
				if strings.Contains(csp, w) {
					t.Errorf("CSP has %q: %s", w, csp)
				}
			}
			// Nothing else loosens.
			for _, w := range []string{"default-src 'self';", "script-src 'self';", "style-src 'self' 'unsafe-inline';", "worker-src 'self' blob:;",
				"frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'", "form-action 'self'"} {
				if !strings.Contains(csp, w) {
					t.Errorf("CSP lacks %q: %s", w, csp)
				}
			}
			if strings.Count(csp, "https://") != 3*countValid(c.origins) {
				t.Errorf("origins must appear exactly in img/connect/font: %s", csp)
			}
		})
	}
}

func countValid(o []string) int {
	seen := map[string]bool{}
	for _, v := range o {
		if v != "" && !strings.ContainsAny(v, " ;,'\"") {
			seen[v] = true
		}
	}
	return len(seen)
}
