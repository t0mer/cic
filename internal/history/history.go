// Package history records a thinned location history of each device in the location_points table.
//
// Persistence never runs on the MQTT event path. OnLocation only enqueues the fix into a bounded
// in-memory queue (queueSize); a single writer goroutine (RunWriter) drains it in order, so fixes
// of a device are stored in the order they arrived. Overload behaviour: when the queue is full the
// OLDEST queued fix is discarded to make room for the new one, the number of discarded fixes is
// counted (Dropped) and logged at warn, rate-limited, with the running total. A write that fails
// with SQLITE_BUSY or "database is locked" is retried a few times with a short backoff before the
// fix is logged and dropped. On shutdown the writer flushes what is queued, for at most
// shutdownFlush.
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
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

	mu   sync.Mutex       // serialises Record and the purge DELETEs with the cache they maintain
	last map[string]point // most recent stored point per device, loaded lazily

	inserts      map[string]int // inserts per device since the last lazy cap check
	beforeInsert func()         // test seam, called under mu just before the insert
	afterDeletes func()         // test seam, called by purge after each DELETE batch, under mu

	gmu  sync.Mutex        // guards gens only; never held across I/O, so OnLocation stays non-blocking
	gens map[string]uint64 // per-key incarnation, bumped by Forget; absent means 0

	qmu       sync.Mutex
	queue     []fix
	queueSize int
	dropped   uint64
	wake      chan struct{}
	dropWarn  ratelog.Limiter
	write     func(ctx context.Context, f fix) error // test seam, defaults to Record
	backoff   []time.Duration
}

type fix struct {
	key      string
	ts       int64
	lat, lon float64
	acc      *float64
	gen      uint64 // the key's generation when the fix was queued
	enq      int64  // enqueue time, unix seconds: a device created later is a new incarnation
}

const (
	// DefaultQueueSize is the capacity of the in-memory queue between the MQTT path and the writer.
	DefaultQueueSize = 1024
	shutdownFlush    = 5 * time.Second
	writeTimeout     = 5 * time.Second
	purgeBatch       = 5000 // rows per retention DELETE, so the write lock is held briefly
)

type point struct {
	ts       int64
	lat, lon float64
}

// New returns a Recorder over db.
func New(db *sql.DB, cfg Config, log *slog.Logger) *Recorder {
	if cfg.Retention <= 0 {
		cfg.Retention = DefaultRetention
	}
	r := &Recorder{db: db, cfg: cfg, now: time.Now, log: log,
		warn: ratelog.Limiter{Interval: time.Minute}, last: map[string]point{}, gens: map[string]uint64{}, inserts: map[string]int{},
		queueSize: DefaultQueueSize, wake: make(chan struct{}, 1), dropWarn: ratelog.Limiter{Interval: time.Minute},
		backoff: []time.Duration{50 * time.Millisecond, 200 * time.Millisecond, 800 * time.Millisecond}}
	r.write = r.record
	return r
}

// Record stores the fix unless it is invalid, outside the retention window, a duplicate of an
// existing (key, ts), or thinned: newer than the last stored point by less than MinInterval and
// closer to it than MinDistanceM. Older fixes are thinned the same way against their nearest stored
// neighbours. A fix for a device that no longer exists is dropped.
func (r *Recorder) Record(ctx context.Context, key string, ts int64, lat, lon float64, accuracy *float64) error {
	f := fix{key: key, ts: ts, lat: lat, lon: lon, acc: accuracy, gen: r.generation(key), enq: math.MaxInt64}
	return r.record(ctx, f)
}

// generation returns the current incarnation counter of key.
func (r *Recorder) generation(key string) uint64 {
	r.gmu.Lock()
	defer r.gmu.Unlock()
	return r.gens[key]
}

// record is Record for a fix carrying the generation and enqueue time it was queued with.
func (r *Recorder) record(ctx context.Context, f fix) error {
	key, ts, lat, lon, accuracy := f.key, f.ts, f.lat, f.lon, f.acc
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
	// Forget bumps the generation under mu, so a stale fix is rejected here and a fix that passes
	// is inserted before any later Forget runs.
	if r.generation(key) != f.gen {
		return nil
	}
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
	// Conditional on the device still existing: a concurrent Registry.Delete must not be followed
	// by an orphan point. created_at <= enq additionally rejects a fix queued before the device
	// was deleted and recreated, in the window before Registry.Delete's OnRemove hook runs
	// (second resolution; a device created by the very event that produced the fix passes).
	// Known limits: a fix whose hook is delayed past a full delete and recreate, a same-second
	// delete+recreate before the hook runs, and a wall clock stepping backwards can each cost or
	// admit one point; they cannot be closed from inside this package.
	res, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO location_points(device_key, ts, lat, lon, accuracy)
		SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM devices WHERE key = ? AND created_at <= ?)`,
		key, ts, lat, lon, acc, key, f.enq)
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

// OnLocation has the signature of the devices.Registry location hook. It only enqueues the fix
// (never blocks, never touches the database); RunWriter persists it. When the queue is full the
// oldest queued fix is dropped.
func (r *Recorder) OnLocation(key string, ts int64, lat, lon float64, accuracy *float64) {
	f := fix{key: key, ts: ts, lat: lat, lon: lon, gen: r.generation(key), enq: r.now().Unix()}
	if accuracy != nil {
		a := *accuracy
		f.acc = &a
	}
	r.qmu.Lock()
	dropped := false
	for len(r.queue) >= r.queueSize { // a loop: the shutdown requeue can leave it above the bound
		copy(r.queue, r.queue[1:])
		r.queue = r.queue[:len(r.queue)-1]
		r.dropped++
		dropped = true
	}
	r.queue = append(r.queue, f)
	total := r.dropped
	r.qmu.Unlock()
	if dropped {
		if _, ok := r.dropWarn.Hit(); ok {
			r.log.Warn("location history queue full, dropped the oldest queued fix", "dropped_total", total)
		}
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Dropped returns how many queued fixes were discarded because the queue was full.
func (r *Recorder) Dropped() uint64 {
	r.qmu.Lock()
	defer r.qmu.Unlock()
	return r.dropped
}

func (r *Recorder) take() []fix {
	r.qmu.Lock()
	defer r.qmu.Unlock()
	b := r.queue
	r.queue = nil
	return b
}

// RunWriter persists queued fixes, in order, until ctx is done, then flushes the rest of the queue
// for at most shutdownFlush. Run exactly one.
func (r *Recorder) RunWriter(ctx context.Context) {
	for {
		r.drain(ctx)
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), shutdownFlush)
			r.drain(fctx)
			cancel()
			if n := len(r.take()); n > 0 {
				r.log.Warn("location history fixes lost at shutdown", "count", n)
			}
			return
		case <-r.wake:
		}
	}
}

func (r *Recorder) drain(ctx context.Context) {
	for ctx.Err() == nil {
		batch := r.take()
		if len(batch) == 0 {
			return
		}
		for i, f := range batch {
			if ctx.Err() != nil {
				// Put the unwritten rest back, ahead of anything newer, for the shutdown flush.
				r.qmu.Lock()
				r.queue = append(batch[i:len(batch):len(batch)], r.queue...)
				r.qmu.Unlock()
				return
			}
			if err := r.persist(ctx, f); err != nil && ctx.Err() != nil {
				// Interrupted by shutdown, not rejected by the database: keep this fix too.
				r.qmu.Lock()
				r.queue = append(batch[i:len(batch):len(batch)], r.queue...)
				r.qmu.Unlock()
				return
			}
		}
	}
}

// persist writes one fix, retrying a busy database a few times, then logs and drops it. It
// returns the final error; a failure caused by ctx being cancelled is not logged (the caller
// requeues the fix).
func (r *Recorder) persist(ctx context.Context, f fix) error {
	var err error
	for attempt := 0; ; attempt++ {
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		err = r.write(wctx, f)
		cancel()
		if err == nil || !isBusy(err) || attempt >= len(r.backoff) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(r.backoff[attempt]):
		}
		if ctx.Err() != nil {
			break
		}
	}
	if err != nil && ctx.Err() == nil {
		if total, ok := r.warn.Hit(); ok {
			r.log.Warn("recording location failed", "device", f.key, "failures", total, "err", err)
		}
	}
	return err
}

func isBusy(err error) bool {
	m := err.Error()
	return strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked") || strings.Contains(m, "SQLITE_LOCKED")
}

// Forget starts a new incarnation of a device whose points were deleted elsewhere: it bumps the
// key's generation (so a fix already taken by the writer is rejected), discards the key's queued
// fixes and drops its cached last point.
func (r *Recorder) Forget(key string) {
	r.mu.Lock()
	r.gmu.Lock()
	r.gens[key]++
	r.gmu.Unlock()
	delete(r.last, key)
	delete(r.inserts, key)
	r.mu.Unlock()
	r.qmu.Lock()
	r.queue = slices.DeleteFunc(r.queue, func(f fix) bool { return f.key == key })
	r.qmu.Unlock()
}

// Purge deletes points older than the configured retention, then the oldest points of every
// device that exceeds MaxPoints. It returns the number of rows deleted.
func (r *Recorder) Purge(ctx context.Context) (int64, error) {
	return r.purge(ctx, r.cfg.Retention)
}

func (r *Recorder) purge(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := r.now().Add(-retention).Unix()
	var total int64
	for {
		n, err := r.deleteExpired(ctx, cutoff)
		total += n
		if err != nil {
			return total, err
		}
		if n < purgeBatch {
			break
		}
	}
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
		r.mu.Lock()
		err := r.trimDevice(ctx, k, &total)
		r.mu.Unlock()
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// deleteExpired deletes one batch of points older than cutoff. It holds r.mu across the DELETE and
// the cache clear, so a concurrent Record can neither thin a new fix against a last point this
// batch just deleted nor re-cache one afterwards. That only stalls the history writer, never MQTT.
func (r *Recorder) deleteExpired(ctx context.Context, cutoff int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, err := r.db.ExecContext(ctx, `DELETE FROM location_points WHERE id IN (
		SELECT id FROM location_points WHERE ts < ? LIMIT ?)`, cutoff, purgeBatch)
	clear(r.last) // a newest point may be among the deleted ones; reloads lazily
	if r.afterDeletes != nil {
		r.afterDeletes()
	}
	if err != nil {
		return 0, fmt.Errorf("purging location history: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
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
