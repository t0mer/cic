package api

import (
	"net"
	"net/http"
	"strings"

	"github.com/t0mer/cic/internal/auth"
)

// csp is the Content-Security-Policy for every response. style-src needs 'unsafe-inline'
// because Sonner injects a runtime <style> tag and MapLibre sets inline styles. The tile
// host is hard-coded for now and will become config-driven in the map step.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https://tiles.openfreemap.org; connect-src 'self' https://tiles.openfreemap.org; " +
	"font-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; object-src 'none'; base-uri 'none'; form-action 'self'"

// SecurityHeaders sets hardening headers on every response. HSTS is added only when the
// request arrived over TLS, the public base URL is https, or the direct peer is one of the
// trusted proxies and reports X-Forwarded-Proto: https (an untrusted peer cannot trigger it).
func SecurityHeaders(baseURL string, trusted []*net.IPNet) func(http.Handler) http.Handler {
	httpsBase := strings.HasPrefix(strings.ToLower(strings.TrimSpace(baseURL)), "https://")
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
