package history

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"math"
	"path/filepath"
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

func TestDeleteDevice(t *testing.T) {
	r, count := newRec(t, cfg())
	rec(t, r, key, t0.Unix()-100, 32, 34)
	rec(t, r, "dradis/other", t0.Unix()-100, 32, 34)
	if err := r.DeleteDevice(context.Background(), key); err != nil || count() != 1 {
		t.Fatalf("delete: %v, count %d", err, count())
	}
	rec(t, r, key, t0.Unix()-90, 32, 34) // cache cleared: stored, not thinned
	if count() != 2 {
		t.Fatal("cache not cleared on delete")
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

func TestRecordNotBlockedByPurge(t *testing.T) {
	r, count := newRec(t, cfg())
	rec(t, r, key, t0.Unix()-100, 32, 34)
	done := make(chan struct{})
	r.afterDeletes = func() { // runs inside purge, after its DELETEs
		go func() {
			rec(t, r, key, t0.Unix()-50, 40, 40)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Record blocked while purge was running")
		}
	}
	if _, err := r.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Fatalf("count = %d", count())
	}
	if len(r.last) != 0 {
		t.Fatal("cache not cleared after purge")
	}
}
