package history

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/store"
)

var t0 = time.Unix(1_780_000_000, 0)

const key = "dradis/phone1"

func newRec(t *testing.T, cfg Config) (*Recorder, func() int) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, k := range []string{key, "dradis/other"} {
		addDevice(t, db, k)
	}
	r := New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.now = func() time.Time { return t0 }
	return r, func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM location_points`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
}

func addDevice(t *testing.T, db *sql.DB, k string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO devices(key, prefix, device, last_seen, created_at) VALUES (?, 'dradis', ?, 0, 0)`, k, k); err != nil {
		t.Fatal(err)
	}
}

func cfg() Config {
	return Config{Retention: 24 * time.Hour, MinDistanceM: 25, MinInterval: 5 * time.Minute, MaxPoints: 100}
}

func rec(t *testing.T, r *Recorder, k string, ts int64, lat, lon float64) {
	t.Helper()
	if err := r.Record(context.Background(), k, ts, lat, lon, nil); err != nil {
		t.Fatal(err)
	}
}

func TestThinning(t *testing.T) {
	r, count := newRec(t, cfg())
	base := t0.Unix() - 3600
	rec(t, r, key, base, 32.0, 34.0)
	rec(t, r, key, base+60, 32.00001, 34.0) // ~1 m, 1 min: thinned
	if count() != 1 {
		t.Fatalf("near+recent kept: %d", count())
	}
	rec(t, r, key, base+301, 32.00001, 34.0) // near but 5 min later: kept
	if count() != 2 {
		t.Fatalf("interval point dropped: %d", count())
	}
	rec(t, r, key, base+302, 32.001, 34.0) // ~111 m, 1 s later: kept
	if count() != 3 {
		t.Fatalf("moved point dropped: %d", count())
	}
	rec(t, r, key, base+302, 33, 35) // exact duplicate ts: ignored
	if count() != 3 {
		t.Fatalf("duplicate stored: %d", count())
	}
}

func TestOutOfOrder(t *testing.T) {
	r, count := newRec(t, cfg())
	base := t0.Unix() - 3600
	rec(t, r, key, base, 32, 34)
	rec(t, r, key, base-10, 32, 34)   // older, identical place, 10 s apart: thinned
	rec(t, r, key, base-600, 32, 34)  // older, same place but 10 min away: stored
	rec(t, r, key, base-600, 32, 34)  // duplicate
	rec(t, r, key, base-3000, 32, 34) // far from every neighbour in time: stored
	if count() != 3 {
		t.Fatalf("count = %d", count())
	}
	// Retention window: older than 24h is rejected.
	rec(t, r, key, t0.Unix()-25*3600, 32, 34)
	if count() != 3 {
		t.Fatalf("expired point stored: %d", count())
	}
	// The cache still points at the newest point: a near fix 1 min later is thinned.
	rec(t, r, key, base+60, 32, 34)
	if count() != 3 {
		t.Fatalf("cache lost newest: %d", count())
	}
}

func TestInvalidInput(t *testing.T) {
	r, count := newRec(t, cfg())
	ts := t0.Unix() - 10
	nan, inf := math.NaN(), math.Inf(1)
	for _, c := range []struct {
		ts       int64
		lat, lon float64
	}{
		{ts, nan, 1}, {ts, 1, nan}, {ts, inf, 1}, {ts, 1, -inf}, {ts, 91, 1}, {ts, -91, 1}, {ts, 1, 181}, {ts, 1, -181},
		{0, 1, 1}, {-5, 1, 1}, {t0.Unix() + 6*60, 1, 1},
	} {
		rec(t, r, key, c.ts, c.lat, c.lon)
	}
	if count() != 0 {
		t.Fatalf("invalid input stored: %d", count())
	}
	rec(t, r, key, t0.Unix()+4*60, 90, 180) // within the 5 min future allowance, boundary coords
	if count() != 1 {
		t.Fatal("valid boundary point rejected")
	}
}

func TestAccuracy(t *testing.T) {
	r, _ := newRec(t, cfg())
	a, bad := 12.5, math.NaN()
	ctx := context.Background()
	_ = r.Record(ctx, key, t0.Unix()-100, 1, 1, &a)
	_ = r.Record(ctx, key, t0.Unix()-50, 2, 2, &bad)
	var got, got2 *float64
	r.db.QueryRow(`SELECT accuracy FROM location_points WHERE ts = ?`, t0.Unix()-100).Scan(&got)
	r.db.QueryRow(`SELECT accuracy FROM location_points WHERE ts = ?`, t0.Unix()-50).Scan(&got2)
	if got == nil || *got != 12.5 || got2 != nil {
		t.Fatalf("accuracy = %v, %v", got, got2)
	}
}

func TestPurgeRetentionAndCacheConsistency(t *testing.T) {
	c := cfg()
	r, count := newRec(t, c)
	now := t0
	r.now = func() time.Time { return now }
	rec(t, r, key, t0.Unix()-3600*20, 32, 34)
	rec(t, r, key, t0.Unix()-60, 33, 35)
	if count() != 2 {
		t.Fatal("setup")
	}
	now = t0.Add(10 * time.Hour) // the first point is now 30 h old, the second 10 h
	n, err := r.Purge(context.Background())
	if err != nil || n != 1 || count() != 1 {
		t.Fatalf("purge = %d, %v, count %d", n, err, count())
	}
	// Move far past the second point as well: everything goes, and the cache must not
	// still hold the purged point (else a near fix would be thinned against a ghost).
	now = t0.Add(40 * time.Hour)
	if n, _ := r.Purge(context.Background()); n != 1 || count() != 0 {
		t.Fatalf("second purge = %d, count %d", n, count())
	}
	rec(t, r, key, now.Unix()-30, 33, 35)
	if count() != 1 {
		t.Fatal("point thinned against a purged cache entry")
	}
}

func TestPurgePerDeviceCap(t *testing.T) {
	c := cfg()
	c.MaxPoints = 3
	c.MinInterval = 0
	r, count := newRec(t, c)
	for i := range 6 {
		rec(t, r, key, t0.Unix()-1000+int64(i), 32+float64(i), 34)
	}
	rec(t, r, "dradis/other", t0.Unix()-10, 1, 1)
	n, err := r.Purge(context.Background())
	if err != nil || n != 3 || count() != 4 {
		t.Fatalf("purge = %d, %v, count %d", n, err, count())
	}
	var oldest int64
	r.db.QueryRow(`SELECT MIN(ts) FROM location_points WHERE device_key = ?`, key).Scan(&oldest)
	if oldest != t0.Unix()-1000+3 {
		t.Fatalf("oldest kept = %d", oldest-(t0.Unix()-1000))
	}
}

func TestDescendingReplayIsThinned(t *testing.T) {
	r, count := newRec(t, cfg())
	base := t0.Unix() - 100
	for i := range 1000 {
		rec(t, r, key, base-int64(i), 32, 34)
	}
	if n := count(); n > 5 {
		t.Fatalf("1000 descending replays stored %d rows", n)
	}
	// A legitimate older fix far away in place is stored.
	before := count()
	rec(t, r, key, base-500, 33, 35)
	if count() != before+1 {
		t.Fatal("far older fix was thinned")
	}
}

func TestLazyCapAtInsert(t *testing.T) {
	c := cfg()
	c.MaxPoints = 10
	c.MinInterval = 0
	c.Retention = 48 * time.Hour
	r, count := newRec(t, c)
	for i := range 250 {
		rec(t, r, key, t0.Unix()-int64(i)*10-10, 32+float64(i)*0.01, 34)
	}
	if n := count(); n > 10+capCheckEvery {
		t.Fatalf("cap not enforced lazily: %d rows", n)
	}
}

func TestRecordAfterDeviceDeleteLeavesNoOrphan(t *testing.T) {
	r, count := newRec(t, cfg())
	rec(t, r, key, t0.Unix()-1000, 32, 34) // cache populated
	r.beforeInsert = func() {
		if _, err := r.db.Exec(`DELETE FROM devices WHERE key = ?`, key); err != nil {
			t.Error(err)
		}
	}
	rec(t, r, key, t0.Unix()-100, 33, 35)
	r.beforeInsert = nil
	if _, err := r.db.Exec(`DELETE FROM location_points WHERE device_key = ?`, key); err != nil { // what Registry.Delete does
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatalf("orphan stored: %d", count())
	}
	if _, ok := r.last[key]; ok {
		t.Fatal("cache repopulated for a deleted device")
	}
}

func TestPurgeAndRecordAreMutuallyExclusive(t *testing.T) {
	r, count := newRec(t, cfg())
	r.now = func() time.Time { return t0.Add(-time.Hour) }
	cutoff := t0.Unix() - 24*3600
	rec(t, r, key, cutoff-1, 32, 34)       // still valid then; cached as the last point
	r.now = func() time.Time { return t0 } // now it has just expired
	done := make(chan struct{})
	r.afterDeletes = func() { // inside purge's critical section, after its DELETE
		go func() {
			rec(t, r, key, cutoff+1, 32, 34) // valid, 2 s from the deleted point: thinned if the cache were stale
			close(done)
		}()
		select {
		case <-done:
			t.Error("Record ran inside the purge critical section")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if n, err := r.Purge(context.Background()); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	r.afterDeletes = nil
	<-done
	if count() != 1 {
		t.Fatalf("new fix lost to a stale cache: %d rows", count())
	}
}

func TestPurgeBatches(t *testing.T) {
	r, count := newRec(t, cfg())
	if _, err := r.db.Exec(`WITH RECURSIVE g(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM g WHERE i < 12000)
		INSERT INTO location_points(device_key, ts, lat, lon) SELECT ?, ? + i, 1, 1 FROM g`, key, t0.Unix()-30*3600); err != nil {
		t.Fatal(err)
	}
	batches := 0
	r.afterDeletes = func() { batches++ }
	n, err := r.Purge(context.Background())
	if err != nil || n != 12001 || count() != 0 || batches != 3 {
		t.Fatalf("purge = %d, %v, count %d, batches %d", n, err, count(), batches)
	}
}

func TestPurgeUsesTimeIndex(t *testing.T) {
	r, _ := newRec(t, cfg())
	rows, err := r.db.Query(`EXPLAIN QUERY PLAN DELETE FROM location_points WHERE id IN (SELECT id FROM location_points WHERE ts < ? LIMIT ?)`, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_location_points_ts") {
		t.Errorf("retention purge does not use the ts index:\n%s", plan)
	}
}

func TestJanitorPurgesAfterRecordingDisabled(t *testing.T) {
	r, count := newRec(t, cfg())
	rec(t, r, key, t0.Unix()-3600, 32, 34)
	// A later process start with locations.enabled=false: a fresh Recorder, no writer, only the janitor.
	r2 := New(r.db, cfg(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	r2.now = func() time.Time { return t0.Add(48 * time.Hour) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r2.RunJanitor(ctx, 24*time.Hour, time.Hour, slog.Default()); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for count() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if count() != 0 {
		t.Fatalf("expired points kept: %d", count())
	}
}

func queued(r *Recorder) []int64 {
	r.qmu.Lock()
	defer r.qmu.Unlock()
	var out []int64
	for _, f := range r.queue {
		out = append(out, f.ts)
	}
	return out
}

func TestOnLocationNeverBlocksAndDropsOldest(t *testing.T) {
	r, _ := newRec(t, cfg())
	r.queueSize = 4
	start := time.Now()
	for i := range 10 { // no writer running: the queue is full from the 5th fix on
		r.OnLocation(key, int64(100+i), 1, 1, nil)
	}
	if time.Since(start) > time.Second {
		t.Fatal("OnLocation blocked")
	}
	got := queued(r)
	if len(got) != 4 || got[0] != 106 || got[3] != 109 || r.Dropped() != 6 {
		t.Fatalf("queue %v dropped %d", got, r.Dropped())
	}
}

func TestWriterOrderDrainAndRetry(t *testing.T) {
	r, _ := newRec(t, cfg())
	var mu sync.Mutex
	var order []int64
	busy := 2
	r.backoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	r.write = func(_ context.Context, f fix) error {
		mu.Lock()
		defer mu.Unlock()
		if f.ts == 1 && busy > 0 {
			busy--
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		order = append(order, f.ts)
		return nil
	}
	for i := int64(1); i <= 50; i++ {
		r.OnLocation(key, i, 1, 1, nil) // queued before the writer starts
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.RunWriter(ctx); close(done) }()
	r.OnLocation(key, 51, 1, 1, nil)
	cancel() // shutdown must flush what is queued
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 51 || busy != 0 {
		t.Fatalf("wrote %d fixes, busy left %d", len(order), busy)
	}
	for i, ts := range order {
		if ts != int64(i+1) {
			t.Fatalf("order broken at %d: %v", i, order)
		}
	}
}

func TestPersistGivesUpOnPersistentBusyAndNonBusy(t *testing.T) {
	r, _ := newRec(t, cfg())
	r.backoff = []time.Duration{time.Millisecond, time.Millisecond}
	calls := 0
	r.write = func(context.Context, fix) error { calls++; return errors.New("SQLITE_BUSY") }
	r.persist(context.Background(), fix{key: key})
	if calls != 3 {
		t.Fatalf("busy attempts = %d", calls)
	}
	calls = 0
	r.write = func(context.Context, fix) error { calls++; return errors.New("disk I/O error") }
	r.persist(context.Background(), fix{key: key})
	if calls != 1 {
		t.Fatalf("non-busy attempts = %d", calls)
	}
}

func TestShutdownDuringWriteKeepsTheFix(t *testing.T) {
	r, _ := newRec(t, cfg())
	ctx, cancel := context.WithCancel(context.Background())
	var wrote []int64
	r.write = func(wctx context.Context, f fix) error {
		if f.ts == 2 {
			cancel() // SIGTERM arrives while this fix is being written
			return wctx.Err()
		}
		wrote = append(wrote, f.ts)
		return nil
	}
	for i := int64(1); i <= 4; i++ {
		r.OnLocation(key, i, 1, 1, nil)
	}
	r.drain(ctx)
	got := queued(r)
	if len(got) != 3 || got[0] != 2 || got[1] != 3 || got[2] != 4 {
		t.Fatalf("queue after interrupted drain = %v (wrote %v), want [2 3 4]", got, wrote)
	}
}

func TestQueueStaysBoundedAfterRequeue(t *testing.T) {
	r, _ := newRec(t, cfg())
	r.queueSize = 3
	r.qmu.Lock()
	for i := int64(1); i <= 5; i++ { // as left by a shutdown requeue on top of newer fixes
		r.queue = append(r.queue, fix{key: key, ts: i})
	}
	r.qmu.Unlock()
	r.OnLocation(key, 6, 1, 1, nil)
	if got := queued(r); len(got) != 3 || got[2] != 6 {
		t.Fatalf("queue = %v, want 3 entries ending in 6", got)
	}
}
