package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/t0mer/cic/internal/api"
	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/store"
)

type fakeDevices struct{}

func (fakeDevices) List(context.Context) ([]devices.Device, error) {
	return []devices.Device{{Key: "dradis/a", FullName: "A"}}, nil
}
func (fakeDevices) Get(context.Context, string) (devices.Device, error) {
	return devices.Device{}, store.ErrNotFound
}
func (fakeDevices) OnChange(func(devices.Device)) {}

func TestMain(m *testing.M) {
	auth.UseFastHashingForTests()
	m.Run()
}

func newTestServer(t *testing.T, baseURL string) (*httptest.Server, *auth.Service) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := auth.NewService(auth.Config{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), InsecureCookies: true})
	hub := events.NewHub()
	t.Cleanup(hub.Close)
	d := api.Deps{Devices: fakeDevices{}, DB: db, Hub: hub, Auth: svc}
	srv := httptest.NewServer(newRouter(d, baseURL, nil))
	t.Cleanup(srv.Close)
	return srv, svc
}

func req(t *testing.T, c *http.Client, method, url, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r, _ := http.NewRequest(method, url, rd)
	if method != "GET" {
		r.Header.Set("Origin", "http://"+r.URL.Host) // what a same-origin browser sends
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestEndToEndLoginMeDevicesLogout(t *testing.T) {
	srv, svc := newTestServer(t, "")
	if _, err := svc.Store().CreateUser(context.Background(), "alice", "correct horse battery", auth.RoleAdmin, false); err != nil {
		t.Fatal(err)
	}
	jar := newJar(t)
	c := &http.Client{Jar: jar}

	if resp, _ := req(t, c, "GET", srv.URL+"/api/devices", "", nil); resp.StatusCode != 401 {
		t.Fatalf("anonymous devices: %d", resp.StatusCode)
	}
	resp, body := req(t, c, "POST", srv.URL+"/api/auth/login", `{"username":"alice","password":"correct horse battery"}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal([]byte(body), &login)

	resp, body = req(t, c, "GET", srv.URL+"/api/auth/me", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"username":"alice"`) || !bytes.Contains([]byte(body), []byte(login.CSRF)) {
		t.Fatalf("me: %d %s", resp.StatusCode, body)
	}
	resp, body = req(t, c, "GET", srv.URL+"/api/devices", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "dradis/a") {
		t.Fatalf("devices: %d %s", resp.StatusCode, body)
	}
	if resp, _ := req(t, c, "POST", srv.URL+"/api/auth/logout", "", nil); resp.StatusCode != 403 {
		t.Fatalf("logout without csrf: %d", resp.StatusCode)
	}
	if resp, _ := req(t, c, "POST", srv.URL+"/api/auth/logout", "", map[string]string{auth.CSRFHeader: login.CSRF}); resp.StatusCode != 204 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := req(t, c, "GET", srv.URL+"/api/devices", "", nil); resp.StatusCode != 401 {
		t.Fatalf("after logout: %d", resp.StatusCode)
	}
}

func TestPublicSurfaceAndHeaders(t *testing.T) {
	srv, _ := newTestServer(t, "")
	for _, p := range []string{"/healthz", "/"} {
		resp, _ := req(t, http.DefaultClient, "GET", srv.URL+p, "", nil)
		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode >= 500 {
			t.Errorf("%s must stay public, got %d", p, resp.StatusCode)
		}
		if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s lacks security headers", p)
		}
		if resp.Header.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS over plain HTTP", p)
		}
	}
	// API errors carry the headers too; auth responses are never cached.
	resp, _ := req(t, http.DefaultClient, "GET", srv.URL+"/api/auth/me", "", nil)
	if resp.StatusCode != 401 || resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("auth me: %d %v", resp.StatusCode, resp.Header)
	}
	// SPA fallback route is public as well.
	if resp, _ := req(t, http.DefaultClient, "GET", srv.URL+"/devices", "", nil); resp.StatusCode == 401 {
		t.Error("SPA route must be public")
	}
	// HSTS when the public base URL is https.
	srv2, _ := newTestServer(t, "https://cic.example.com")
	resp, _ = req(t, http.DefaultClient, "GET", srv2.URL+"/healthz", "", nil)
	if resp.Header.Get("Strict-Transport-Security") != "max-age=31536000" {
		t.Errorf("HSTS missing with https base_url: %v", resp.Header)
	}
}

func TestLoginWithoutOriginIsRejected(t *testing.T) {
	srv, svc := newTestServer(t, "")
	if _, err := svc.Store().CreateUser(context.Background(), "alice", "correct horse battery", auth.RoleAdmin, false); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("POST", srv.URL+"/api/auth/login", strings.NewReader(`{"username":"alice","password":"correct horse battery"}`))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("login without Origin/Sec-Fetch-Site: %d", resp.StatusCode)
	}
}

func TestWarnProxyConfig(t *testing.T) {
	_, ten, _ := net.ParseCIDR("10.0.0.0/8")
	cases := []struct {
		name, baseURL string
		proxies       []*net.IPNet
		warn          bool
	}{
		{"https without proxies", "https://cic.example.com", nil, true},
		{"HTTPS mixed case", "HTTPS://cic.example.com", nil, true},
		{"https with proxies", "https://cic.example.com", []*net.IPNet{ten}, false},
		{"http without proxies", "http://cic.local", nil, false},
		{"unset", "", nil, false},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		warnProxyConfig(slog.New(slog.NewTextHandler(&buf, nil)), c.baseURL, c.proxies)
		if got := strings.Contains(buf.String(), "level=WARN"); got != c.warn {
			t.Errorf("%s: warned=%v want %v (%q)", c.name, got, c.warn, buf.String())
		}
	}
}

func TestHSTSFromTrustedProxyHeader(t *testing.T) {
	_, ten, _ := net.ParseCIDR("127.0.0.0/8")
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := events.NewHub()
	defer hub.Close()
	svc := auth.NewService(auth.Config{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), InsecureCookies: true})
	d := api.Deps{Devices: fakeDevices{}, DB: db, Hub: hub, Auth: svc}
	for name, trusted := range map[string][]*net.IPNet{"trusted": {ten}, "untrusted": nil} {
		srv := httptest.NewServer(newRouter(d, "", trusted))
		resp, _ := req(t, http.DefaultClient, "GET", srv.URL+"/healthz", "", map[string]string{"X-Forwarded-Proto": "https"})
		srv.Close()
		if got := resp.Header.Get("Strict-Transport-Security") != ""; got != (name == "trusted") {
			t.Errorf("%s peer: HSTS=%v", name, got)
		}
	}
}
