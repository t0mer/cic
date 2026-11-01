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
	count := func() (n int) {
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
