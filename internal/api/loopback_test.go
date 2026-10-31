package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoopbackOnly(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	tests := []struct {
		name   string
		host   string
		hdr    map[string]string
		status int
	}{
		{"ipv4 loopback", "127.0.0.1:8787", nil, http.StatusNoContent},
		{"localhost", "localhost:8787", nil, http.StatusNoContent},
		{"ipv6 loopback", "[::1]:8787", nil, http.StatusNoContent},
		{"rebound dns name", "evil.example.com:8787", nil, http.StatusForbidden},
		{"lan address", "192.168.0.10:8787", nil, http.StatusForbidden},
		{"cross-site fetch", "127.0.0.1:8787", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-origin fetch", "127.0.0.1:8787", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8787"}, http.StatusNoContent},
		{"foreign origin", "127.0.0.1:8787", map[string]string{"Origin": "http://evil.example.com"}, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/devices", nil)
			req.Host = tt.host
			for k, v := range tt.hdr {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			loopbackOnly(ok).ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
		})
	}
}
