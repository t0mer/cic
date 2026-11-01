// Package history records a thinned location history of each device in the location_points table.
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/t0mer/cic/internal/geo"
	"github.com/t0mer/cic/internal/ratelog"
)

// DefaultRetention is used when the configured retention is zero or negative.
const DefaultRetention = 2160 * time.Hour

// maxFuture is how far ahead of now a fix time may be before the fix is rejected.
const maxFuture = 5 * time.Minute

// Config tunes the recorder.
type Config struct {
	Retention    time.Duration // points older than this are dropped (<= 0: DefaultRetention)
	MinDistanceM float64       // a newer fix closer than this to the last stored point is thinned...
	MinInterval  time.Duration // ...unless at least this much time has passed
	MaxPoints    int           // per-device cap enforced by Purge, oldest first; <= 0 means no cap
}

// Recorder stores location fixes, keeping the volume sane by thinning.
type Recorder struct {
	db   *sql.DB
	cfg  Config
	now  func() time.Time
	log  *slog.Logger
	warn ratelog.Limiter

	mu   sync.Mutex
	last map[string]point // most recent stored point per device, loaded lazily

	inserts      map[string]int // inserts per device since the last lazy cap check
	beforeInsert func()         // test seam, called under mu just before the insert
	afterDeletes func()         // test seam, called by purge after its DELETEs, outside mu
}

type point struct {
	ts       int64
	lat, lon float64
}

// New returns a Recorder over db.
func New(db *sql.DB, cfg Config, log *slog.Logger) *Recorder {
	if cfg.Retention <= 0 {
		cfg.Retention = DefaultRetention
	}
	return &Recorder{db: db, cfg: cfg, now: time.Now, log: log,
		warn: ratelog.Limiter{Interval: time.Minute}, last: map[string]point{}, inserts: map[string]int{}}
}

// Record stores the fix unless it is invalid, outside the retention window, a duplicate of an
// existing (key, ts), or thinned: newer than the last stored point by less than MinInterval and
// closer to it than MinDistanceM. Older fixes are thinned the same way against their nearest stored
// neighbours. A fix for a device that no longer exists is dropped.
func (r *Recorder) Record(ctx context.Context, key string, ts int64, lat, lon float64, accuracy *float64) error {
	if key == "" || math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) ||
		lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil
	}
	now := r.now()
	if ts <= 0 || ts > now.Add(maxFuture).Unix() || ts < now.Add(-r.cfg.Retention).Unix() {
		return nil
	}
	var acc any
	if accuracy != nil && !math.IsNaN(*accuracy) && !math.IsInf(*accuracy, 0) && *accuracy >= 0 {
		acc = *accuracy
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.last[key]
	if !ok {
		var err error
		if last, ok, err = r.loadLast(ctx, key); err != nil {
			return err
		}
		if ok {
			r.last[key] = last
		}
	}
	newer := !ok || ts > last.ts
	if ok && newer {
		if r.thinned(last, ts, lat, lon) {
			return nil
		}
	} else if ok {
		// An older fix is thinned against its nearest stored neighbours by time.
		prev, hasPrev, err := r.neighbour(ctx, key, ts, true)
		if err != nil {
			return err
		}
		next, hasNext, err := r.neighbour(ctx, key, ts, false)
		if err != nil {
			return err
		}
		if (hasPrev && r.thinned(prev, ts, lat, lon)) || (hasNext && r.thinned(next, ts, lat, lon)) {
			return nil
		}
	}
	if r.beforeInsert != nil {
		r.beforeInsert()
	}
	// Conditional on the device still existing: a concurrent Registry.Delete must not be
	// followed by an orphan point.
	res, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO location_points(device_key, ts, lat, lon, accuracy)
		SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM devices WHERE key = ?)`, key, ts, lat, lon, acc, key)
	if err != nil {
		return fmt.Errorf("recording location of %q: %w", key, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var exists bool
		if err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM devices WHERE key = ?)`, key).Scan(&exists); err == nil && !exists {
			delete(r.last, key)
			delete(r.inserts, key)
		}
		return nil
	}
	if newer {
		r.last[key] = point{ts: ts, lat: lat, lon: lon}
	}
	return r.enforceCap(ctx, key)
}

// thinned reports whether a fix at ts is both near and close in time to the stored point p.
func (r *Recorder) thinned(p point, ts int64, lat, lon float64) bool {
	d := ts - p.ts
	if d < 0 {
		d = -d
	}
	return geo.Haversine(p.lat, p.lon, lat, lon) < r.cfg.MinDistanceM && time.Duration(d)*time.Second < r.cfg.MinInterval
}

// neighbour returns the stored point just before (or after) ts, via the (device_key, ts) index.
func (r *Recorder) neighbour(ctx context.Context, key string, ts int64, before bool) (point, bool, error) {
	q := `SELECT ts, lat, lon FROM location_points WHERE device_key = ? AND ts > ? ORDER BY ts ASC LIMIT 1`
	if before {
		q = `SELECT ts, lat, lon FROM location_points WHERE device_key = ? AND ts < ? ORDER BY ts DESC LIMIT 1`
	}
	var p point
	err := r.db.QueryRowContext(ctx, q, key, ts).Scan(&p.ts, &p.lat, &p.lon)
	if errors.Is(err, sql.ErrNoRows) {
		return point{}, false, nil
	}
	if err != nil {
		return point{}, false, fmt.Errorf("reading neighbouring location of %q: %w", key, err)
	}
	return p, true, nil
}

// capCheckEvery is how many inserts per device pass between lazy cap checks.
const capCheckEvery = 100

// enforceCap trims the device to MaxPoints (oldest first) every capCheckEvery inserts, so the
// cap holds between janitor runs. The caller holds r.mu.
func (r *Recorder) enforceCap(ctx context.Context, key string) error {
	if r.cfg.MaxPoints <= 0 {
		return nil
	}
	r.inserts[key]++
	if r.inserts[key] < capCheckEvery {
		return nil
	}
	r.inserts[key] = 0
	return r.trimDevice(ctx, key, nil)
}

func (r *Recorder) trimDevice(ctx context.Context, key string, total *int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM location_points WHERE device_key = ? AND id IN (
		SELECT id FROM location_points WHERE device_key = ? ORDER BY ts DESC, id DESC LIMIT -1 OFFSET ?)`,
		key, key, r.cfg.MaxPoints)
	if err != nil {
		return fmt.Errorf("enforcing point cap of %q: %w", key, err)
	}
	if total != nil {
		n, _ := res.RowsAffected()
		*total += n
	}
	return nil
}

func (r *Recorder) loadLast(ctx context.Context, key string) (point, bool, error) {
	var p point
	err := r.db.QueryRowContext(ctx, `SELECT ts, lat, lon FROM location_points
		WHERE device_key = ? ORDER BY ts DESC LIMIT 1`, key).Scan(&p.ts, &p.lat, &p.lon)
	if errors.Is(err, sql.ErrNoRows) {
		return point{}, false, nil
	}
	if err != nil {
		return point{}, false, fmt.Errorf("reading last location of %q: %w", key, err)
	}
	return p, true, nil
}

// OnLocation has the signature of the devices.Registry location hook. A failure is logged at
// warn, rate-limited, and never returned: recording must not disturb the MQTT path.
func (r *Recorder) OnLocation(key string, ts int64, lat, lon float64, accuracy *float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Record(ctx, key, ts, lat, lon, accuracy); err != nil {
		if total, ok := r.warn.Hit(); ok {
			r.log.Warn("recording location failed", "device", key, "failures", total, "err", err)
		}
	}
}

// Forget drops the cached last point of a device (its points were deleted elsewhere).
func (r *Recorder) Forget(key string) {
	r.mu.Lock()
	delete(r.last, key)
	delete(r.inserts, key)
	r.mu.Unlock()
}

// DeleteDevice removes all points of a device.
func (r *Recorder) DeleteDevice(ctx context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.last, key)
	delete(r.inserts, key)
	if _, err := r.db.ExecContext(ctx, `DELETE FROM location_points WHERE device_key = ?`, key); err != nil {
		return fmt.Errorf("deleting locations of %q: %w", key, err)
	}
	return nil
}

// Purge deletes points older than the configured retention, then the oldest points of every
// device that exceeds MaxPoints. It returns the number of rows deleted.
func (r *Recorder) Purge(ctx context.Context) (int64, error) {
	return r.purge(ctx, r.cfg.Retention)
}

func (r *Recorder) purge(ctx context.Context, retention time.Duration) (int64, error) {
	// The DELETEs run outside r.mu so ingestion is not stalled; the cache is cleared afterwards
	// (a newest point may be among the deleted ones) and reloads lazily.
	defer func() {
		r.mu.Lock()
		clear(r.last)
		r.mu.Unlock()
	}()
	if r.afterDeletes != nil {
		defer r.afterDeletes()
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM location_points WHERE ts < ?`, r.now().Add(-retention).Unix())
	if err != nil {
		return 0, fmt.Errorf("purging location history: %w", err)
	}
	total, _ := res.RowsAffected()
	if r.cfg.MaxPoints <= 0 {
		return total, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT device_key FROM location_points GROUP BY device_key HAVING COUNT(*) > ?`, r.cfg.MaxPoints)
	if err != nil {
		return total, fmt.Errorf("finding devices over the point cap: %w", err)
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return total, fmt.Errorf("reading device over the point cap: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return total, fmt.Errorf("finding devices over the point cap: %w", err)
	}
	rows.Close()
	for _, k := range keys {
		if err := r.trimDevice(ctx, k, &total); err != nil {
			return total, err
		}
	}
	return total, nil
}

// RunJanitor purges now and then every `every` until ctx is done. A retention of zero or less
// means DefaultRetention, never "forever".
func (r *Recorder) RunJanitor(ctx context.Context, retention, every time.Duration, log *slog.Logger) {
	if retention <= 0 {
		retention = DefaultRetention
	}
	run := func() {
		n, err := r.purge(ctx, retention)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("location history purge failed", "err", err)
		case n > 0:
			log.Info("location points purged", "rows", n)
		}
	}
	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
