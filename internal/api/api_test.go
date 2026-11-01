package api

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.yaml.in/yaml/v3"

	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

type fakeStore struct {
	mu          sync.Mutex
	devs        []devices.Device
	hooks       []func(devices.Device)
	removeHooks []func(devices.Device)
}

func (f *fakeStore) OnRefresh(fn func()) {}

func (f *fakeStore) OnRemove(fn func(devices.Device)) {
	f.mu.Lock()
	f.removeHooks = append(f.removeHooks, fn)
	f.mu.Unlock()
}

func (f *fakeStore) UpdateProfile(_ context.Context, key string, p devices.ProfilePatch) (devices.Device, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.devs {
		if f.devs[i].Key != key {
			continue
		}
		was := f.devs[i].Hidden
		if p.FullName != nil {
			f.devs[i].FullName = *p.FullName
			if *p.FullName == "" {
				f.devs[i].FullName = f.devs[i].Device
			}
		}
		if p.Color != nil {
			f.devs[i].Color = *p.Color
		}
		if p.Hidden != nil {
			f.devs[i].Hidden = *p.Hidden
		}
		return f.devs[i], was, nil
	}
	return devices.Device{}, false, store.ErrNotFound
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	var gone *devices.Device
	for i := range f.devs {
		if f.devs[i].Key == key {
			d := f.devs[i]
			gone = &d
			f.devs = append(f.devs[:i], f.devs[i+1:]...)
			break
		}
	}
	hooks := append([]func(devices.Device){}, f.removeHooks...)
	f.mu.Unlock()
	if gone == nil {
		return store.ErrNotFound
	}
	for _, h := range hooks {
		h(*gone)
	}
	return nil
}

func (f *fakeStore) List(context.Context) ([]devices.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]devices.Device(nil), f.devs...), nil
}
func (f *fakeStore) Get(_ context.Context, key string) (devices.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devs {
		if d.Key == key {
			return d, nil
		}
	}
	return devices.Device{}, store.ErrNotFound
}
func (f *fakeStore) OnChange(fn func(devices.Device)) {
	f.mu.Lock()
	f.hooks = append(f.hooks, fn)
	f.mu.Unlock()
}
func (f *fakeStore) fire(d devices.Device) {
	for _, h := range f.hooks {
		h(d)
	}
}

type fakeBroker struct {
	mu sync.Mutex
	st mqtt.State
}

func (b *fakeBroker) State() mqtt.State { b.mu.Lock(); defer b.mu.Unlock(); return b.st }
func (b *fakeBroker) set(s mqtt.State)  { b.mu.Lock(); b.st = s; b.mu.Unlock() }

type fakeDB struct{ err error }

func (f fakeDB) PingContext(context.Context) error { return f.err }

// testCookie is the session of an admin created by the latest newDeps call; do() and
// connect() send it so existing handler tests run authenticated.
var (
	testCookie *http.Cookie
	testDB     *sql.DB
	testUserID int64
)

func TestMain(m *testing.M) {
	auth.UseFastHashingForTests()
	m.Run()
}

func newDeps(t *testing.T) (Deps, *fakeStore, *fakeBroker) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := auth.NewService(auth.Config{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	u, err := svc.Store().CreateUser(context.Background(), "tester", "test-password-123", auth.RoleAdmin, false)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := svc.Store().CreateSession(context.Background(), u.ID, "go-test")
	if err != nil {
		t.Fatal(err)
	}
	testCookie = &http.Cookie{Name: svc.CookieName(), Value: tok}
	testDB, testUserID = db, u.ID

	fs := &fakeStore{devs: []devices.Device{
		{Key: "dradis/b", FullName: "Zed"},
		{Key: "dradis/a", FullName: "Zed"},
		{Key: "dradis/c", FullName: "Alpha"},
		{Key: "dradis/h", FullName: "Hidden", Hidden: true},
	}}
	fb := &fakeBroker{st: mqtt.State{Connected: true, Since: time.Unix(1700000000, 0)}}
	return Deps{Devices: fs, Broker: fb, BrokerAddr: "broker:1883", DB: fakeDB{}, Hub: events.NewHub(),
		Heartbeat: 50 * time.Millisecond, PollEvery: 10 * time.Millisecond, Auth: svc}, fs, fb
}

func router(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Mount("/api", New(d))
	return r
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := loopbackReq(method, path)
	req.AddCookie(testCookie)
	h.ServeHTTP(rec, req)
	return rec
}

func TestListOrderingAndHidden(t *testing.T) {
	d, _, _ := newDeps(t)
	h := router(d)
	rec := do(h, "GET", "/api/devices")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code=%d cc=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var got []devices.Device
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, x := range got {
		keys = append(keys, x.Key)
	}
	if strings.Join(keys, ",") != "dradis/c,dradis/a,dradis/b" {
		t.Fatalf("order: %v", keys)
	}
	rec = do(h, "GET", "/api/devices?include_hidden=1")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 4 {
		t.Fatalf("include_hidden: %d", len(got))
	}
}

func TestListEmptyIsArray(t *testing.T) {
	d, fs, _ := newDeps(t)
	fs.devs = nil
	if b := strings.TrimSpace(do(router(d), "GET", "/api/devices").Body.String()); b != "[]" {
		t.Fatalf("got %q", b)
	}
}

func TestGetDevice(t *testing.T) {
	d, _, _ := newDeps(t)
	h := router(d)
	if c := do(h, "GET", "/api/devices/dradis/a").Code; c != 200 {
		t.Fatalf("200 expected, got %d", c)
	}
	rec := do(h, "GET", "/api/devices/dradis/nope")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("404: %d %s", rec.Code, rec.Body)
	}
	if c := do(h, "GET", "/api/devices/dra%20dis/a").Code; c != 400 {
		t.Fatalf("400 expected, got %d", c)
	}
}

func TestBrokerJSON(t *testing.T) {
	d, _, fb := newDeps(t)
	fb.set(mqtt.State{LastError: "boom"})
	rec := do(router(d), "GET", "/api/broker")
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if len(m) != 4 || m["host"] != "broker:1883" || m["connected"] != false || m["last_error"] != "boom" || m["since"] != nil {
		t.Fatalf("got %v", m)
	}
	d2, _, _ := newDeps(t)
	var m2 map[string]any
	_ = json.Unmarshal(do(router(d2), "GET", "/api/broker").Body.Bytes(), &m2)
	if m2["since"] != "2023-11-14T22:13:20Z" {
		t.Fatalf("since: %v", m2["since"])
	}
}

func TestMethodsAndNotFound(t *testing.T) {
	d, _, _ := newDeps(t)
	h := router(d)
	for _, p := range []string{"/api/devices", "/api/devices/dradis/a", "/api/broker", "/api/events"} {
		rec := do(h, "POST", p)
		if rec.Code != 405 || rec.Header().Get("Allow") == "" {
			t.Fatalf("%s: %d allow=%q", p, rec.Code, rec.Header().Get("Allow"))
		}
	}
	rec := do(h, "GET", "/api/nothing")
	if rec.Code != 404 || rec.Body.String() != "{\"error\":\"not found\"}\n" {
		t.Fatalf("404: %d %q", rec.Code, rec.Body)
	}
}

func TestReadyz(t *testing.T) {
	up := &fakeBroker{st: mqtt.State{Connected: true}}
	down := &fakeBroker{}
	cases := []struct {
		name   string
		db     error
		broker BrokerState
		code   int
		body   string
	}{
		{"ok", nil, up, 200, "ready"},
		{"broker down", nil, down, 503, "broker not connected"},
		{"not configured", nil, nil, 503, "broker not configured"},
		{"db down", errors.New("x"), up, 503, "database unavailable"},
	}
	for _, c := range cases {
		d := Deps{DB: fakeDB{c.db}}
		if c.broker != nil {
			d.Broker = c.broker
		}
		rec := do(Readyz(d), "GET", "/readyz")
		if rec.Code != c.code || strings.TrimSpace(rec.Body.String()) != c.body {
			t.Errorf("%s: %d %q", c.name, rec.Code, rec.Body)
		}
	}
}

type sseClient struct {
	resp *http.Response
	sc   *bufio.Scanner
}

func connect(t *testing.T, srv *httptest.Server) *sseClient {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	req.AddCookie(testCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", resp.Header)
	}
	return &sseClient{resp, bufio.NewScanner(resp.Body)}
}

// until reads lines until one has the given prefix, returning it.
func (c *sseClient) until(t *testing.T, prefix string) string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		for c.sc.Scan() {
			if strings.HasPrefix(c.sc.Text(), prefix) {
				done <- c.sc.Text()
				return
			}
		}
		done <- ""
	}()
	select {
	case s := <-done:
		if s == "" {
			t.Fatalf("stream ended before %q", prefix)
		}
		return s
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %q", prefix)
	}
	return ""
}

func TestSSEEvents(t *testing.T) {
	d, fs, fb := newDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartFeeds(ctx, d)
	srv := httptest.NewServer(router(d))
	defer srv.Close()
	defer d.Hub.Close() // runs first: ends the open stream so srv.Close can return
	c := connect(t, srv)

	c.until(t, "event: hello")
	if l := c.until(t, "data:"); !strings.Contains(l, `"connected":true`) {
		t.Fatalf("hello: %s", l)
	}
	fs.fire(devices.Device{Key: "dradis/a", FullName: "A"})
	c.until(t, "event: device.updated")
	if l := c.until(t, "data:"); !strings.Contains(l, `"key":"dradis/a"`) {
		t.Fatalf("device: %s", l)
	}
	fb.set(mqtt.State{Connected: false, LastError: "lost", Since: time.Now()})
	c.until(t, "event: broker.state")
	if l := c.until(t, "data:"); !strings.Contains(l, `"last_error":"lost"`) {
		t.Fatalf("broker: %s", l)
	}
	c.until(t, ": ping")
}

func TestSSEEndsOnHubClose(t *testing.T) {
	d, _, _ := newDeps(t)
	srv := httptest.NewServer(router(d))
	defer srv.Close()
	c := connect(t, srv)
	c.until(t, "event: hello")
	d.Hub.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, c.resp.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not end after Hub.Close")
	}
}

type noFlush struct{ http.ResponseWriter }

func TestSSENoFlusher(t *testing.T) {
	d, _, _ := newDeps(t)
	rec := httptest.NewRecorder()
	req := loopbackReq("GET", "/api/events")
	req.AddCookie(testCookie)
	router(d).ServeHTTP(noFlush{rec}, req)
	if rec.Code != 500 {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestSpecParity(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(openAPISpec, &spec); err != nil {
		t.Fatal(err)
	}
	d, _, _ := newDeps(t)
	r := chi.NewRouter()
	r.Mount("/api", New(d))
	routes := map[string]bool{}
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.ReplaceAll(strings.TrimSuffix(route, "/*"), "/*", "")
		if route != "" && route != "/api" {
			routes[strings.ToLower(method)+" "+route] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	specRoutes := map[string]bool{}
	for p, ops := range spec.Paths {
		for m := range ops {
			specRoutes[m+" "+p] = true
		}
	}
	for k := range specRoutes {
		if !routes[k] {
			t.Errorf("spec operation %q not on router (routes %v)", k, routes)
		}
	}
	for k := range routes {
		if !specRoutes[k] {
			t.Errorf("router operation %q not in spec", k)
		}
	}
	for _, k := range []string{"post /api/auth/login", "post /api/auth/logout", "get /api/auth/me", "post /api/auth/change-password", "get /api/openapi.yaml",
		"post /api/devices/{prefix}/{device}/ping", "post /api/devices/{prefix}/{device}/ping/stop", "post /api/devices/{prefix}/{device}/notify",
		"post /api/devices/{prefix}/{device}/sms", "post /api/devices/{prefix}/{device}/refresh-status", "post /api/devices/{prefix}/{device}/refresh-location", "get /api/activity", "get /api/ui-config",
		"patch /api/devices/{prefix}/{device}", "delete /api/devices/{prefix}/{device}", "get /api/users", "post /api/users", "patch /api/users/{id}", "post /api/users/{id}/reset-password"} {
		if !specRoutes[k] {
			t.Errorf("spec lacks %s", k)
		}
	}
}

// Every route except login is unreachable without a session; viewers are read-only
// is enforced per route with RequireRole when mutating routes exist.
func TestAPIRequiresAuth(t *testing.T) {
	d, _, _ := newDeps(t)
	h := router(d)
	for _, p := range []string{"/api/devices", "/api/devices/dradis/a", "/api/broker", "/api/events", "/api/openapi.yaml", "/api/auth/me", "/api/activity", "/api/ui-config"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, loopbackReq("GET", p))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous: %d", p, rec.Code)
		}
	}
	// A bogus cookie is also 401.
	rec := httptest.NewRecorder()
	req := loopbackReq("GET", "/api/devices")
	req.AddCookie(&http.Cookie{Name: auth.CookieNameSecure, Value: "bogus"})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bogus cookie: %d", rec.Code)
	}
	// Login is public (it answers 401 for bad credentials, not for a missing session).
	rec = httptest.NewRecorder()
	req = loopbackReq("POST", "/api/auth/login")
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	req.Body = io.NopCloser(strings.NewReader(`{"username":"x","password":"y"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid credentials") {
		t.Errorf("login: %d %s", rec.Code, rec.Body)
	}
}

func TestNewPanicsWithoutAuth(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New must refuse to build without Auth")
		}
	}()
	New(Deps{})
}

func TestSSEEndsWhenSessionEnds(t *testing.T) {
	d, _, _ := newDeps(t)
	srv := httptest.NewServer(router(d))
	defer srv.Close()
	defer d.Hub.Close()
	c := connect(t, srv)
	c.until(t, "event: hello")
	if err := d.Auth.Store().DeleteSession(context.Background(), testCookie.Value); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, c.resp.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream outlived its session")
	}
}

func loopbackReq(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Host = "127.0.0.1:8787"
	return r
}

func TestOpenAPIIsNoStore(t *testing.T) {
	d, _, _ := newDeps(t)
	rec := do(router(d), "GET", "/api/openapi.yaml")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code=%d cache-control=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

// waitEnd fails unless the stream closes within limit.
func (c *sseClient) waitEnd(t *testing.T, limit time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, c.resp.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatal("stream still open")
	}
}

// With a one-hour heartbeat and recheck, only the immediate end-of-session signal can close
// the stream within the test's deadline.
func TestSSEEndsImmediatelyWhenSessionEnds(t *testing.T) {
	ends := map[string]func(d Deps){
		"logout": func(d Deps) { _ = d.Auth.Store().DeleteSession(context.Background(), testCookie.Value) },
		"disable": func(d Deps) {
			_ = d.Auth.Store().SetDisabled(context.Background(), testUserID, true)
		},
		"password change": func(d Deps) {
			_ = d.Auth.Store().SetPassword(context.Background(), testUserID, "tester", "a different long password", false)
		},
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			d, _, _ := newDeps(t)
			d.Heartbeat, d.SessionRecheck = time.Hour, time.Hour
			srv := httptest.NewServer(router(d))
			defer srv.Close()
			defer d.Hub.Close()
			c := connect(t, srv)
			c.until(t, "event: hello")
			end(d)
			c.waitEnd(t, 500*time.Millisecond)
		})
	}
}

// Even without a signal (the session vanished behind our back, e.g. via the CLI), no event
// is written to a stream whose session is gone.
func TestSSEChecksSessionBeforeEveryEvent(t *testing.T) {
	d, fs, _ := newDeps(t)
	d.Heartbeat, d.SessionRecheck = time.Hour, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartFeeds(ctx, d)
	srv := httptest.NewServer(router(d))
	defer srv.Close()
	defer d.Hub.Close()
	c := connect(t, srv)
	c.until(t, "event: hello")
	if _, err := testDB.Exec(`DELETE FROM sessions`); err != nil { // silent: no watcher signal
		t.Fatal(err)
	}
	fs.fire(devices.Device{Key: "dradis/a", FullName: "A"})
	body := make(chan string, 1)
	go func() { b, _ := io.ReadAll(c.resp.Body); body <- string(b) }()
	select {
	case b := <-body:
		if strings.Contains(b, "device.updated") {
			t.Fatalf("event leaked to a dead session: %q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream still open")
	}
}

func TestSSEStreamsPerUserCap(t *testing.T) {
	d, _, _ := newDeps(t)
	srv := httptest.NewServer(router(d))
	defer srv.Close()
	defer d.Hub.Close()
	for i := 0; i < auth.MaxStreamsPerUser; i++ {
		connect(t, srv).until(t, "event: hello")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	req.AddCookie(testCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("6th stream: %d", resp.StatusCode)
	}
}
