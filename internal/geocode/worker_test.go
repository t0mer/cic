package geocode

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	reg   *devices.Registry
	st    Store
	calls *atomic.Int32
	srv   *httptest.Server
	w     *Worker
	now   *time.Time
}

// newEnv wires a real sqlite registry to a fake provider whose status is set by *status.
func newEnv(t *testing.T, status *atomic.Int32, cfg Config) *env {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	body := fixture(t)
	calls := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if s := int(status.Load()); s != 200 {
			w.WriteHeader(s)
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "250")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	reg := devices.NewRegistry(db, 5*time.Minute, quiet)
	st := Store{DB: db}
	w := NewWorker(reg, NewClient(srv.URL, "latlng_k"), st, cfg, quiet, func() time.Time { return now })
	return &env{reg: reg, st: st, calls: calls, srv: srv, w: w, now: &now}
}

var testCfg = Config{MinMoveMeters: 150, MaxAge: 24 * time.Hour, DailyCap: 5, Reserve: 2}

func (e *env) locate(t *testing.T, dev string, lat, lon float64) {
	t.Helper()
	at := e.now.Unix()
	err := e.reg.Apply(context.Background(), mqtt.Event{Prefix: "dradis", Device: dev, Leaf: mqtt.LeafLocation,
		Payload: mqtt.Location{Latitude: lat, Longitude: lon, Time: &at}, ReceivedAt: *e.now})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) device(t *testing.T, dev string) devices.Device {
	t.Helper()
	d, err := e.reg.Get(context.Background(), "dradis/"+dev)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWorkerEndToEnd(t *testing.T) {
	status := new(atomic.Int32)
	status.Store(200)
	e := newEnv(t, status, testCfg)
	changes := make(chan devices.Device, 8)
	e.reg.OnChange(func(d devices.Device) { changes <- d })
	e.reg.OnChange(e.w.Enqueue)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.w.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	e.locate(t, "p1", 32.0744874, 34.7918382)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case d := <-changes:
			if d.LocationName == nil {
				continue
			}
			if *d.LocationName != `SOFT, תל אביב–יפו` || d.LocationNameAt == nil {
				t.Fatalf("name %v at %v", *d.LocationName, d.LocationNameAt)
			}
			if got := e.calls.Load(); got != 1 {
				t.Fatalf("calls %d", got)
			}
			u, err := e.w.Usage(ctx)
			if err != nil || u.CallsToday != 1 || u.Remaining != 4 || u.LastRemaining == nil || *u.LastRemaining != 250 {
				t.Fatalf("usage %+v %v", u, err)
			}
			// Cancelling the context stops the worker.
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("worker did not stop")
			}
			return
		case <-deadline:
			t.Fatal("no name arrived")
		}
	}
}

func TestCacheHitCostsNoCall(t *testing.T) {
	status := new(atomic.Int32)
	status.Store(200)
	e := newEnv(t, status, testCfg)
	ctx := context.Background()
	e.locate(t, "a", 32.0744874, 34.7918382)
	e.locate(t, "b", 32.0744000, 34.7918000) // same 3-decimal cell
	e.w.process(ctx, "dradis/a")
	e.w.process(ctx, "dradis/b")
	if e.calls.Load() != 1 {
		t.Fatalf("calls %d, want 1", e.calls.Load())
	}
	if n := e.device(t, "b").LocationName; n == nil || *n == "" {
		t.Fatal("b not named from cache")
	}
	u, _ := e.w.Usage(ctx)
	if u.CallsToday != 1 || u.CacheHitsToday != 1 || u.CacheHitRatio != 0.5 {
		t.Fatalf("usage %+v", u)
	}
}

func TestUnmovedDeviceIsNotLookedUpAgain(t *testing.T) {
	status := new(atomic.Int32)
	status.Store(200)
	e := newEnv(t, status, testCfg)
	ctx := context.Background()
	e.locate(t, "a", 32.0744874, 34.7918382)
	e.w.process(ctx, "dradis/a")
	e.locate(t, "a", 32.0744880, 34.7918390)
	e.w.process(ctx, "dradis/a")
	*e.now = e.now.Add(25 * time.Hour) // name too old: refreshed, served by the cache
	e.w.process(ctx, "dradis/a")
	if e.calls.Load() != 1 {
		t.Fatalf("calls %d", e.calls.Load())
	}
	u, _ := e.w.Usage(ctx)
	if u.CacheHitsToday != 1 {
		t.Fatalf("hits %d", u.CacheHitsToday)
	}
}

func TestRateLimitBacksOffAndUnauthorizedDisables(t *testing.T) {
	status := new(atomic.Int32)
	status.Store(429)
	e := newEnv(t, status, testCfg)
	ctx := context.Background()
	e.locate(t, "a", 32.0, 34.0)
	e.locate(t, "b", 33.0, 35.0)
	e.w.process(ctx, "dradis/a")
	e.w.process(ctx, "dradis/b")
	if e.calls.Load() != 1 {
		t.Fatalf("calls during backoff: %d", e.calls.Load())
	}
	u, _ := e.w.Usage(ctx)
	if u.BlockedUntil == nil || *u.BlockedUntil != "2026-05-02T00:00:00Z" {
		t.Fatalf("usage %+v", u)
	}

	status.Store(401)
	e2 := newEnv(t, status, testCfg)
	e2.locate(t, "a", 32.0, 34.0)
	e2.w.process(ctx, "dradis/a")
	if u, _ := e2.w.Usage(ctx); u.Enabled {
		t.Fatal("still enabled after 401")
	}
	e2.w.Enqueue(e2.device(t, "a"))
	if len(e2.w.queue) != 0 {
		t.Fatal("queued after 401")
	}
}

func TestDailyCapStopsCalls(t *testing.T) {
	status := new(atomic.Int32)
	status.Store(200)
	cfg := testCfg
	cfg.DailyCap = 2
	e := newEnv(t, status, cfg)
	for i, d := range []string{"a", "b", "c"} {
		e.locate(t, d, 30+float64(i), 34)
		e.w.process(context.Background(), "dradis/"+d)
	}
	if e.calls.Load() != 2 {
		t.Fatalf("calls %d", e.calls.Load())
	}
	// Next UTC day the counter starts over.
	*e.now = e.now.Add(24 * time.Hour)
	e.w.process(context.Background(), "dradis/c")
	if e.calls.Load() != 3 {
		t.Fatalf("calls after rollover %d", e.calls.Load())
	}
}

func TestQueueDedupeAndBound(t *testing.T) {
	w := NewWorker(nil, nil, Store{}, testCfg, quiet, nil)
	mk := func(key string) devices.Device {
		return devices.Device{Key: key, Latitude: ptr(1.0), Longitude: ptr(2.0)}
	}
	w.Enqueue(mk("k"))
	w.Enqueue(mk("k"))
	w.Enqueue(devices.Device{Key: "noloc"})
	if len(w.queue) != 1 {
		t.Fatalf("queue %v", w.queue)
	}
	for i := range maxQueue + 10 {
		w.Enqueue(mk("d" + strings.Repeat("x", i%7) + string(rune('A'+i%26)) + string(rune('a'+i/26))))
	}
	if len(w.queue) > maxQueue || len(w.inQ) != len(w.queue) {
		t.Fatalf("queue %d inQ %d", len(w.queue), len(w.inQ))
	}
	if _, ok := w.inQ["k"]; ok {
		t.Fatal("oldest entry not dropped")
	}
}

func TestPurge(t *testing.T) {
	status := new(atomic.Int32)
	e := newEnv(t, status, testCfg)
	ctx := context.Background()
	if err := e.st.Put(ctx, "old", "x", e.now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Put(ctx, "new", "y", e.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.st.Get(ctx, "old", *e.now); ok {
		t.Fatal("expired entry served")
	}
	if n, err := e.st.Purge(ctx, *e.now); err != nil || n != 1 {
		t.Fatalf("purged %d %v", n, err)
	}
	if _, ok, _ := e.st.Get(ctx, "new", *e.now); !ok {
		t.Fatal("fresh entry purged")
	}
	if CacheKey(-0.0001, 32.0744874) != "0.000,32.074" {
		t.Fatalf("key %s", CacheKey(-0.0001, 32.0744874))
	}
}

type fakeRev struct {
	err   error
	name  string
	calls int
}

func (f *fakeRev) Reverse(context.Context, float64, float64) (Result, Info, error) {
	f.calls++
	return Result{Name: f.name}, Info{}, f.err
}

// withRev swaps the provider of e's worker for a scripted one.
func withRev(e *env, f *fakeRev) {
	e.w.api = f
}

func TestRetryAfterHonouredAndPersisted(t *testing.T) {
	status := new(atomic.Int32)
	status.Store(200)
	e := newEnv(t, status, testCfg)
	ctx := context.Background()
	f := &fakeRev{err: &RateLimitError{RetryAfter: 90 * time.Second}}
	withRev(e, f)
	e.locate(t, "a", 32.0, 34.0)
	e.w.process(ctx, "dradis/a")
	want := e.now.Add(90 * time.Second)
	if b := e.w.guard.BlockedUntil(); b == nil || !b.Equal(want) {
		t.Fatalf("blocked until %v, want %v", b, want)
	}
	// A restart (new worker on the same db) restores the block before calling again.
	w2 := NewWorker(e.reg, f, e.st, testCfg, quiet, func() time.Time { return *e.now })
	w2.api = f
	ctx2, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { w2.Run(ctx2); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if f.calls != 1 {
		t.Fatalf("provider called during persisted block: %d", f.calls)
	}
	if b := w2.guard.BlockedUntil(); b == nil || !b.Equal(want) {
		t.Fatalf("restored block %v", b)
	}
}

func TestRetryAfterCappedAtMidnight(t *testing.T) {
	now := time.Date(2026, 5, 1, 23, 50, 0, 0, time.UTC)
	g := NewGuard(testCfg, func() time.Time { return now })
	if end := g.RateLimited(2 * time.Hour); !end.Equal(time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("end %v", end)
	}
}

func TestSkippedDevicesRetryWhenBlockLifts(t *testing.T) {
	status := new(atomic.Int32)
	status.Store(200)
	cfg := testCfg
	cfg.DailyCap = 1
	e := newEnv(t, status, cfg)
	ctx := context.Background()
	e.locate(t, "a", 30, 34)
	e.locate(t, "b", 31, 34)
	e.w.process(ctx, "dradis/a")
	e.w.process(ctx, "dradis/b") // cap reached: held back
	if _, ok := e.w.pending["dradis/b"]; !ok {
		t.Fatal("b not pending")
	}
	e.w.release()
	if len(e.w.queue) != 0 {
		t.Fatal("released before the block lifted")
	}
	*e.now = e.now.Add(13 * time.Hour) // past UTC midnight
	e.w.release()
	k, ok := e.w.pop()
	if !ok || k != "dradis/b" {
		t.Fatalf("queue after release: %q %v", k, ok)
	}
	e.w.process(ctx, k)
	if n := e.device(t, "b").LocationName; n == nil {
		t.Fatal("b still unnamed")
	}
}

func TestSweepQueuesUnnamedDevices(t *testing.T) {
	status := new(atomic.Int32)
	status.Store(200)
	e := newEnv(t, status, testCfg)
	e.locate(t, "a", 30, 34)
	e.w.sweep(context.Background())
	if k, ok := e.w.pop(); !ok || k != "dradis/a" {
		t.Fatalf("sweep queued %q %v", k, ok)
	}
}

func TestEmptyNameIsRecordedAndNotRepeated(t *testing.T) {
	status := new(atomic.Int32)
	status.Store(200)
	e := newEnv(t, status, testCfg)
	ctx := context.Background()
	f := &fakeRev{err: ErrNoName}
	withRev(e, f)
	e.locate(t, "a", 32.0, 34.0)
	e.locate(t, "b", 32.0001, 34.0001) // same cache cell
	e.w.process(ctx, "dradis/a")
	d := e.device(t, "a")
	if d.LocationName != nil || d.LocationNameAt == nil || d.GeocodedLat == nil {
		t.Fatalf("empty result not recorded: %+v", d)
	}
	if e.w.needsName(d) {
		t.Fatal("device still needs a name")
	}
	e.w.process(ctx, "dradis/b") // served by the cached emptiness
	if f.calls != 1 {
		t.Fatalf("calls %d", f.calls)
	}
	if e.w.needsName(e.device(t, "b")) {
		t.Fatal("b still needs a name")
	}
	if u, _ := e.w.Usage(ctx); u.CacheHitsToday != 0 || u.CallsToday != 1 {
		t.Fatalf("usage %+v", u)
	}
}

func TestFailedCallsCountAgainstCap(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{&StatusError{Code: 503}, 1},
		{context.DeadlineExceeded, 1},
		{ErrUnauthorized, 0},
		{&RateLimitError{}, 0},
	}
	for _, c := range cases {
		status := new(atomic.Int32)
		status.Store(200)
		e := newEnv(t, status, testCfg)
		withRev(e, &fakeRev{err: c.err})
		e.locate(t, "a", 32.0, 34.0)
		e.w.process(context.Background(), "dradis/a")
		if u, _ := e.w.Usage(context.Background()); u.CallsToday != c.want {
			t.Errorf("%v: calls %d, want %d", c.err, u.CallsToday, c.want)
		}
	}
}
