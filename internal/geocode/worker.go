package geocode

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/store"
)

const (
	maxQueue      = 256
	purgeInterval = 24 * time.Hour
	sweepInterval = 10 * time.Minute
	opTimeout     = 15 * time.Second
)

// Devices is the part of the registry the worker uses.
type Devices interface {
	List(ctx context.Context) ([]devices.Device, error)
	Get(ctx context.Context, key string) (devices.Device, error)
	SetLocationName(ctx context.Context, key, name string, lat, lon float64, at time.Time) error
}

// Reverser resolves coordinates; *Client implements it.
type Reverser interface {
	Reverse(ctx context.Context, lat, lon float64) (Result, Info, error)
}

// Presence tells the worker whether anyone is watching the UI. Provider calls are made only
// while Subscribers() > 0; OnFirstSubscriber(fn) must call fn (without blocking the caller)
// when the count goes 0 -> 1. *events.Hub implements it.
type Presence interface {
	Subscribers() int
	OnFirstSubscriber(fn func())
}

// Worker is the single background goroutine that names device locations.
type Worker struct {
	devs   Devices
	api    Reverser
	store  Store
	guard  *Guard
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
	wake   chan struct{}
	kick   chan struct{} // a viewer appeared: sweep now
	pres   Presence      // nil means always present
	failed atomic.Bool   // the key was rejected: no more lookups until restart

	mu    sync.Mutex
	queue []string            // device keys, oldest first
	inQ   map[string]struct{} // dedupe: a queued device is read afresh when its turn comes

	pending map[string]struct{} // devices the guard refused; re-queued when retryAt passes
	retryAt time.Time           // when the block behind pending lifts; zero when nothing waits
}

// NewWorker returns a Worker. now defaults to time.Now.
func NewWorker(devs Devices, api Reverser, st Store, cfg Config, log *slog.Logger, now func() time.Time) *Worker {
	if now == nil {
		now = time.Now
	}
	return &Worker{devs: devs, api: api, store: st, guard: NewGuard(cfg, now), cfg: cfg, log: log, now: now,
		wake: make(chan struct{}, 1), kick: make(chan struct{}, 1), inQ: map[string]struct{}{}, pending: map[string]struct{}{}}
}

// SetPresence gates provider calls on UI presence (see Presence). Call it before Run.
func (w *Worker) SetPresence(p Presence) {
	w.pres = p
	p.OnFirstSubscriber(func() {
		select {
		case w.kick <- struct{}{}:
		default:
		}
	})
}

// watched reports whether a provider call is allowed right now.
func (w *Worker) watched() bool { return w.pres == nil || w.pres.Subscribers() > 0 }

// Enqueue is the Registry.OnChange hook. It never blocks: it only queues devices that have
// a location and look like they need a name, and drops the oldest entry beyond maxQueue.
func (w *Worker) Enqueue(d devices.Device) {
	if w.failed.Load() || d.Latitude == nil || d.Longitude == nil || !w.needsName(d) {
		return
	}
	w.queueKey(d.Key)
}

func (w *Worker) queueKey(key string) {
	w.mu.Lock()
	if _, ok := w.inQ[key]; !ok {
		w.inQ[key] = struct{}{}
		w.queue = append(w.queue, key)
		if len(w.queue) > maxQueue {
			delete(w.inQ, w.queue[0])
			w.queue = w.queue[1:]
		}
	}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Worker) needsName(d devices.Device) bool {
	var prev *Point
	var at *time.Time
	if d.GeocodedLat != nil && d.GeocodedLon != nil {
		prev = &Point{*d.GeocodedLat, *d.GeocodedLon}
	}
	if d.LocationNameAt != nil {
		t := time.Unix(*d.LocationNameAt, 0)
		at = &t
	}
	return w.guard.ShouldLookup(prev, at, Point{*d.Latitude, *d.Longitude})
}

func (w *Worker) pop() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) == 0 {
		return "", false
	}
	k := w.queue[0]
	w.queue = w.queue[1:]
	delete(w.inQ, k)
	return k, true
}

// holdBack remembers a device the guard refused and when the block lifts.
func (w *Worker) holdBack(key string, until time.Time) {
	w.mu.Lock()
	w.pending[key] = struct{}{}
	if w.retryAt.IsZero() || until.Before(w.retryAt) {
		w.retryAt = until
	}
	w.mu.Unlock()
}

// release re-queues the pending devices once their block has lifted.
func (w *Worker) release() {
	w.mu.Lock()
	if w.retryAt.IsZero() || w.now().Before(w.retryAt) {
		w.mu.Unlock()
		return
	}
	keys := make([]string, 0, len(w.pending))
	for k := range w.pending {
		keys = append(keys, k)
	}
	w.pending = map[string]struct{}{}
	w.retryAt = time.Time{}
	w.mu.Unlock()
	for _, k := range keys {
		w.queueKey(k)
	}
}

// sweep queues every device that has a location but needs a (new) name.
func (w *Worker) sweep(ctx context.Context) {
	all, err := w.devs.List(ctx)
	if err != nil {
		w.log.Warn("geocode: listing devices", "err", err)
		return
	}
	for _, d := range all {
		w.Enqueue(d)
	}
}

// nextWake is how long Run may sleep before pending devices are due; ok is false when none wait.
func (w *Worker) nextWake() (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.retryAt.IsZero() {
		return 0, false
	}
	return max(time.Second, w.retryAt.Sub(w.now())), true
}

// Run processes the queue until ctx ends. At start it restores a persisted backoff and
// queues devices that need a name; it then re-sweeps every sweepInterval, re-runs devices
// held back by the guard when the block lifts, and purges old cache entries daily.
func (w *Worker) Run(ctx context.Context) {
	w.purge(ctx)
	if until, err := w.store.Blocked(ctx); err != nil {
		w.log.Warn("geocode: restoring backoff", "err", err)
	} else if until.After(w.now()) {
		w.guard.SetBlockedUntil(until)
	}
	w.sweep(ctx)
	tick := time.NewTicker(purgeInterval)
	defer tick.Stop()
	sweepTick := time.NewTicker(sweepInterval)
	defer sweepTick.Stop()
	for {
		w.release()
		for {
			key, ok := w.pop()
			if !ok {
				break
			}
			if ctx.Err() != nil {
				return
			}
			w.process(ctx, key)
		}
		var due <-chan time.Time
		var timer *time.Timer
		if d, ok := w.nextWake(); ok {
			timer = time.NewTimer(d)
			due = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-w.wake:
		case <-due:
		case <-w.kick:
			w.sweep(ctx)
		case <-sweepTick.C:
			if w.watched() {
				w.sweep(ctx)
			}
		case <-tick.C:
			w.purge(ctx)
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (w *Worker) purge(ctx context.Context) {
	if _, err := w.store.Purge(ctx, w.now()); err != nil {
		w.log.Warn("geocode: purging cache", "err", err)
	}
}

// process names one device from the cache or, quota allowing, from the provider. The device
// is re-read so the latest coordinates win however many events were queued meanwhile.
func (w *Worker) process(ctx context.Context, key string) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	d, err := w.devs.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			w.log.Warn("geocode: reading device", "device", key, "err", err)
		}
		return
	}
	if d.Latitude == nil || d.Longitude == nil || !w.needsName(d) {
		return
	}
	lat, lon := *d.Latitude, *d.Longitude
	now := w.now()
	ck := CacheKey(lat, lon)

	name, hit, err := w.store.Get(ctx, ck, now)
	if err != nil {
		w.log.Warn("geocode: cache read", "err", err)
	}
	if hit {
		if name != "" { // only a name actually served counts as a hit
			if err := w.store.AddHit(ctx, Day(now)); err != nil {
				w.log.Warn("geocode: counting cache hit", "err", err)
			}
		}
	} else {
		var ok bool
		if name, ok = w.lookup(ctx, key, ck, lat, lon, now); !ok {
			return
		}
	}
	// An empty name ("no name here") is recorded too, so the device does not need a name
	// again until it moves or the record ages.
	w.save(ctx, key, name, lat, lon, now)
}

// lookup asks the provider once, if quota allows, and caches the answer. A device the
// guard refuses is held back until the block lifts.
func (w *Worker) lookup(ctx context.Context, key, ck string, lat, lon float64, now time.Time) (string, bool) {
	if !w.watched() { // nobody is looking: the device stays unnamed until the sweep on the next viewer
		return "", false
	}
	day := Day(now)
	u, err := w.store.Usage(ctx, day)
	if err != nil {
		w.log.Warn("geocode: reading usage", "err", err)
		return "", false
	}
	if err := w.guard.Allow(u.Calls, u.LastRemaining); err != nil {
		w.log.Debug("geocode: lookup skipped", "reason", err)
		until := NextMidnight(now)
		if b := w.guard.BlockedUntil(); b != nil {
			until = *b
		}
		w.holdBack(key, until)
		return "", false
	}
	res, info, err := w.api.Reverse(ctx, lat, lon)
	var rem *int
	if info.RemainingKnown {
		rem = &info.Remaining
	}
	count := func() {
		if cerr := w.store.AddCall(ctx, day, rem); cerr != nil {
			w.log.Warn("geocode: counting call", "err", cerr)
		}
	}
	var rl *RateLimitError
	switch {
	case err == nil, errors.Is(err, ErrNoName): // the provider answered: that call is spent
		count()
		name := res.Name
		if err != nil {
			name = "" // remember the emptiness so this place costs nothing again
		}
		if cerr := w.store.Put(ctx, ck, name, now); cerr != nil {
			w.log.Warn("geocode: cache write", "err", cerr)
		}
		return name, true
	case errors.As(err, &rl) || errors.Is(err, ErrRateLimited):
		var after time.Duration
		if rl != nil {
			after = rl.RetryAfter
		}
		until := w.guard.RateLimited(after)
		if serr := w.store.SetBlocked(ctx, day, until); serr != nil {
			w.log.Warn("geocode: saving backoff", "err", serr)
		}
		w.holdBack(key, until)
		w.log.Warn("geocode: rate limited, pausing", "until", until)
	case errors.Is(err, ErrUnauthorized):
		if w.failed.CompareAndSwap(false, true) {
			w.log.Error("geocode: api key rejected, geocoding disabled until restart")
		}
	default:
		// Timeouts and 5xx may still have used quota at the provider: count them.
		if ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			count()
		}
		w.log.Warn("geocode: lookup failed", "err", err)
	}
	return "", false
}

func (w *Worker) save(ctx context.Context, key, name string, lat, lon float64, now time.Time) {
	err := w.devs.SetLocationName(ctx, key, name, lat, lon, now)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		w.log.Warn("geocode: saving name", "device", key, "err", err)
	}
}

// Usage is the geocoding usage report of GET /api/geocode/usage.
type Usage struct {
	Enabled        bool    `json:"enabled"`
	CallsToday     int     `json:"calls_today"`
	CacheHitsToday int     `json:"cache_hits_today"`
	DailyCap       int     `json:"daily_cap"`
	Remaining      int     `json:"remaining"` // min(cap - calls, provider remaining when known)
	LastRemaining  *int    `json:"last_remaining"`
	BlockedUntil   *string `json:"blocked_until"` // RFC 3339; set while backing off after a 429
	CacheHitRatio  float64 `json:"cache_hit_ratio"`
}

// Usage reports today's (UTC) counters. A rejected key reads as disabled.
func (w *Worker) Usage(ctx context.Context) (Usage, error) {
	now := w.now()
	u, err := w.store.Usage(ctx, Day(now))
	if err != nil {
		return Usage{}, err
	}
	out := Usage{Enabled: !w.failed.Load(), CallsToday: u.Calls, CacheHitsToday: u.CacheHits,
		DailyCap: w.cfg.DailyCap, LastRemaining: u.LastRemaining, Remaining: max(0, w.cfg.DailyCap-u.Calls)}
	if u.LastRemaining != nil {
		out.Remaining = max(0, min(out.Remaining, *u.LastRemaining))
	}
	if t := w.guard.BlockedUntil(); t != nil {
		s := t.Format(time.RFC3339)
		out.BlockedUntil = &s
	}
	if total := u.Calls + u.CacheHits; total > 0 {
		out.CacheHitRatio = float64(u.CacheHits) / float64(total)
	}
	return out, nil
}
