package api

import (
	"net"
	"net/http"
	"strings"

	"github.com/t0mer/cic/internal/auth"
)

// buildCSP returns the Content-Security-Policy for every response. style-src needs
// 'unsafe-inline' because Sonner injects a runtime <style> tag and MapLibre sets inline
// styles. mapOrigins (bare https origins derived from config) are appended to img-src,
// connect-src and font-src and nowhere else.
//
// A map style JSON usually references tile, sprite and glyph hosts of its own, and the
// browser enforces the CSP on all of them: custom styles must be served from, and only
// reference, an origin that is allowed here (the style URLs' own origins, the provider's
// built-in origins, or map.extra_origins).
func buildCSP(mapOrigins []string) string {
	seen := map[string]bool{}
	var extra string
	for _, o := range mapOrigins {
		if o == "" || seen[o] || strings.ContainsAny(o, " ;,'\"\r\n") {
			continue
		}
		seen[o] = true
		extra += " " + o
	}
	return "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:" + extra + "; connect-src 'self'" + extra + "; " +
		"font-src 'self'" + extra + "; worker-src 'self' blob:; frame-ancestors 'none'; object-src 'none'; base-uri 'none'; form-action 'self'"
}

// SecurityHeaders sets hardening headers on every response. HSTS is added only when the
// request arrived over TLS, the public base URL is https, or the direct peer is one of the
// trusted proxies and reports X-Forwarded-Proto: https (an untrusted peer cannot trigger it).
func SecurityHeaders(baseURL string, trusted []*net.IPNet, mapOrigins []string) func(http.Handler) http.Handler {
	httpsBase := strings.HasPrefix(strings.ToLower(strings.TrimSpace(baseURL)), "https://")
	csp := buildCSP(mapOrigins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
			h.Set("X-Frame-Options", "DENY")
			if httpsBase || r.TLS != nil || auth.ViaTrustedProxyHTTPS(r, trusted) {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}
