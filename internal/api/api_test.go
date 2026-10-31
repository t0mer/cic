package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.yaml.in/yaml/v3"

	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

type fakeStore struct {
	mu    sync.Mutex
	devs  []devices.Device
	hooks []func(devices.Device)
}

func (f *fakeStore) List(context.Context) ([]devices.Device, error) { return f.devs, nil }
func (f *fakeStore) Get(_ context.Context, key string) (devices.Device, error) {
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

func newDeps() (Deps, *fakeStore, *fakeBroker) {
	fs := &fakeStore{devs: []devices.Device{
		{Key: "dradis/b", FullName: "Zed"},
		{Key: "dradis/a", FullName: "Zed"},
		{Key: "dradis/c", FullName: "Alpha"},
		{Key: "dradis/h", FullName: "Hidden", Hidden: true},
	}}
	fb := &fakeBroker{st: mqtt.State{Connected: true, Since: time.Unix(1700000000, 0)}}
	return Deps{Devices: fs, Broker: fb, BrokerAddr: "broker:1883", DB: fakeDB{}, Hub: events.NewHub(),
		Heartbeat: 50 * time.Millisecond, PollEvery: 10 * time.Millisecond}, fs, fb
}

func router(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Mount("/api", New(d))
	return r
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestListOrderingAndHidden(t *testing.T) {
	d, _, _ := newDeps()
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
	d, fs, _ := newDeps()
	fs.devs = nil
	if b := strings.TrimSpace(do(router(d), "GET", "/api/devices").Body.String()); b != "[]" {
		t.Fatalf("got %q", b)
	}
}

func TestGetDevice(t *testing.T) {
	d, _, _ := newDeps()
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
	d, _, fb := newDeps()
	fb.set(mqtt.State{LastError: "boom"})
	rec := do(router(d), "GET", "/api/broker")
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if len(m) != 4 || m["host"] != "broker:1883" || m["connected"] != false || m["last_error"] != "boom" || m["since"] != nil {
		t.Fatalf("got %v", m)
	}
	d2, _, _ := newDeps()
	var m2 map[string]any
	_ = json.Unmarshal(do(router(d2), "GET", "/api/broker").Body.Bytes(), &m2)
	if m2["since"] != "2023-11-14T22:13:20Z" {
		t.Fatalf("since: %v", m2["since"])
	}
}

func TestMethodsAndNotFound(t *testing.T) {
	d, _, _ := newDeps()
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
	resp, err := http.Get(srv.URL + "/api/events")
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
	d, fs, fb := newDeps()
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
	d, _, _ := newDeps()
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
	d, _, _ := newDeps()
	rec := httptest.NewRecorder()
	router(d).ServeHTTP(noFlush{rec}, httptest.NewRequest("GET", "/api/events", nil))
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
	d, _, _ := newDeps()
	r := chi.NewRouter()
	r.Mount("/api", New(d))
	routes := map[string]bool{}
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.ReplaceAll(strings.TrimSuffix(route, "/*"), "/*", "")
		if method == "GET" && route != "" && route != "/api" {
			routes[route] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for p := range spec.Paths {
		k := strings.NewReplacer("{prefix}", "{prefix}", "{device}", "{device}").Replace(p)
		if !routes[k] {
			t.Errorf("spec path %s not on router (routes %v)", p, routes)
		}
	}
	for p := range routes {
		if _, ok := spec.Paths[p]; !ok {
			t.Errorf("router path %s not in spec", p)
		}
	}
	if spec.Paths["/api/openapi.yaml"] == nil {
		t.Error("spec endpoint missing")
	}
}

func TestExposureAllowed(t *testing.T) {
	cases := []struct {
		listen   string
		override bool
		want     bool
	}{
		{"127.0.0.1:8080", false, true},
		{"127.5.5.5:1", false, true},
		{"[::1]:8080", false, true},
		{"localhost:8080", false, true},
		{":8080", false, false},
		{"0.0.0.0:8080", false, false},
		{"192.168.1.5:8080", false, false},
		{"[::]:8080", false, false},
		{"example.com:80", false, false},
		{":8080", true, true},
	}
	for _, c := range cases {
		if got := ExposureAllowed(c.listen, c.override); got != c.want {
			t.Errorf("%q override=%v: got %v", c.listen, c.override, got)
		}
	}
}
