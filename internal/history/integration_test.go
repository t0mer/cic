package history_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/history"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

func TestRegistryApplyRecordsThinnedPoints(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := devices.NewRegistry(db, 5*time.Minute, log)
	rec := history.New(db, history.Config{Retention: 24 * time.Hour, MinDistanceM: 25, MinInterval: 5 * time.Minute}, log)
	reg.OnLocation(rec.OnLocation)
	wctx, stop := context.WithCancel(ctx)
	writerDone := make(chan struct{})
	go func() { rec.RunWriter(wctx); close(writerDone) }()
	defer func() { stop(); <-writerDone }()
	reg.OnRemove(func(d devices.Device) { rec.Forget(d.Key) })

	now := time.Now()
	apply := func(lat float64, fix time.Time) {
		t.Helper()
		ts := fix.Unix()
		err := reg.Apply(ctx, mqtt.Event{Prefix: "dradis", Device: "p1", Leaf: mqtt.LeafLocation, ReceivedAt: now,
			Payload: mqtt.Location{Latitude: lat, Longitude: 34, Time: &ts}})
		if err != nil {
			t.Fatal(err)
		}
	}
	// The writer is asynchronous: wait until the queue has been persisted.
	count := func() (n int) {
		time.Sleep(150 * time.Millisecond)
		db.QueryRow(`SELECT COUNT(*) FROM location_points`).Scan(&n)
		return
	}
	base := now.Add(-time.Hour)
	apply(32, base)
	if count() != 1 {
		t.Fatalf("first fix: %d rows", count())
	}
	apply(32.00001, base.Add(30*time.Second)) // near and soon: thinned
	if count() != 1 {
		t.Fatalf("near fix recorded: %d rows", count())
	}
	apply(32.01, base.Add(-time.Minute)) // stale for the registry: ignored
	if count() != 1 {
		t.Fatalf("stale fix recorded: %d rows", count())
	}
	apply(32.01, base.Add(time.Minute)) // moved ~1 km
	if count() != 2 {
		t.Fatalf("moved fix: %d rows", count())
	}
	if err := reg.Delete(ctx, "dradis/p1"); err != nil || count() != 0 {
		t.Fatalf("delete: %v, %d rows", err, count())
	}
}

func setupReg(t *testing.T) (context.Context, *devices.Registry, *history.Recorder, func(*testing.T) int, func()) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := devices.NewRegistry(db, 5*time.Minute, log)
	rec := history.New(db, history.Config{Retention: 24 * time.Hour, MinDistanceM: 25, MinInterval: 5 * time.Minute}, log)
	reg.OnLocation(rec.OnLocation)
	reg.OnRemove(func(d devices.Device) { rec.Forget(d.Key) })
	count := func(t *testing.T) (n int) {
		t.Helper()
		if err := db.QueryRow(`SELECT COUNT(*) FROM location_points`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	// startWriter runs the writer and stops it at once: the shutdown flush drains everything
	// that is queued, so no sleeping or polling is needed (and the result is deterministic).
	startWriter := func() {
		wctx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { rec.RunWriter(wctx); close(done) }()
		stop()
		<-done
	}
	return ctx, reg, rec, count, startWriter
}

func applyLoc(t *testing.T, ctx context.Context, reg *devices.Registry, fix time.Time) {
	t.Helper()
	ts := fix.Unix()
	err := reg.Apply(ctx, mqtt.Event{Prefix: "dradis", Device: "p1", Leaf: mqtt.LeafLocation, ReceivedAt: time.Now(),
		Payload: mqtt.Location{Latitude: 32, Longitude: 34, Time: &ts}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueuedFixDoesNotSurviveDeleteAndRecreate(t *testing.T) {
	ctx, reg, _, count, drain := setupReg(t)
	old := time.Now().Add(-2 * time.Hour)
	applyLoc(t, ctx, reg, old) // queued, writer not running yet
	if err := reg.Delete(ctx, "dradis/p1"); err != nil {
		t.Fatal(err)
	}
	fresh := time.Now().Add(-time.Hour)
	applyLoc(t, ctx, reg, fresh) // recreates the same key
	drain()
	if count(t) != 1 {
		t.Fatalf("expected only the new incarnation's fix, got %d rows", count(t))
	}
}

func TestFirstFixOfNewDeviceIsStored(t *testing.T) {
	ctx, reg, _, count, drain := setupReg(t)
	applyLoc(t, ctx, reg, time.Now().Add(-time.Minute))
	drain()
	if count(t) != 1 {
		t.Fatalf("first fix of a new device: %d rows", count(t))
	}
}
