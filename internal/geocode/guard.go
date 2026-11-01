package geocode

import (
	"errors"
	"sync"
	"time"

	"github.com/t0mer/cic/internal/geo"
)

// Why a lookup is not allowed right now.
var (
	ErrDailyCap = errors.New("geocode: daily cap reached")
	ErrReserve  = errors.New("geocode: provider quota below reserve")
	ErrBackoff  = errors.New("geocode: backing off after rate limit")
)

// Config are the quota and movement thresholds (cfg.Geocode).
type Config struct {
	MinMoveMeters int           // geocode again only after moving this far
	MaxAge        time.Duration // ... or when the name is older than this
	DailyCap      int           // local calls per UTC day; 0 allows none
	Reserve       int           // stop while the provider reports fewer calls left
}

// Guard decides whether a lookup is worthwhile and whether quota allows it. It holds no
// usage counters (those live in SQLite); only the 429 backoff, which is in memory.
type Guard struct {
	cfg Config
	now func() time.Time

	mu      sync.Mutex
	blocked time.Time
}

// NewGuard returns a Guard; now defaults to time.Now.
func NewGuard(cfg Config, now func() time.Time) *Guard {
	if now == nil {
		now = time.Now
	}
	return &Guard{cfg: cfg, now: now}
}

// Day returns the UTC date key (YYYY-MM-DD) of t.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Point is a coordinate pair.
type Point struct{ Lat, Lon float64 }

// ShouldLookup reports whether a device at cur needs a (new) name: it has none yet (no
// previous geocoded point), it moved more than MinMoveMeters since that point, or the
// name is older than MaxAge. prev and nameAt are nil while unknown.
func (g *Guard) ShouldLookup(prev *Point, nameAt *time.Time, cur Point) bool {
	if prev == nil || nameAt == nil {
		return true
	}
	if Haversine(*prev, cur) > float64(g.cfg.MinMoveMeters) {
		return true
	}
	return g.now().Sub(*nameAt) > g.cfg.MaxAge
}

// Allow reports whether one provider call may be made now, given the calls already made
// today and the provider's last reported remaining quota for today (nil when unknown).
func (g *Guard) Allow(calls int, lastRemaining *int) error {
	if g.BlockedUntil() != nil {
		return ErrBackoff
	}
	if calls >= g.cfg.DailyCap {
		return ErrDailyCap
	}
	if lastRemaining != nil && *lastRemaining < g.cfg.Reserve {
		return ErrReserve
	}
	return nil
}

// NextMidnight returns the start of the UTC day after t.
func NextMidnight(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
}

// RateLimited starts the backoff after a 429 and returns its end: now+retryAfter when the
// provider sent one, otherwise the next UTC midnight; never later than that midnight.
func (g *Guard) RateLimited(retryAfter time.Duration) time.Time {
	now := g.now()
	end := NextMidnight(now)
	if retryAfter > 0 && now.Add(retryAfter).Before(end) {
		end = now.Add(retryAfter)
	}
	g.SetBlockedUntil(end)
	return end
}

// SetBlockedUntil restores a persisted backoff end.
func (g *Guard) SetBlockedUntil(t time.Time) {
	g.mu.Lock()
	g.blocked = t
	g.mu.Unlock()
}

// BlockedUntil returns the end of the backoff, or nil when none is active.
func (g *Guard) BlockedUntil() *time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.now().Before(g.blocked) {
		return nil
	}
	t := g.blocked
	return &t
}

// Haversine returns the great-circle distance in meters between a and b.
func Haversine(a, b Point) float64 {
	return geo.Haversine(a.Lat, a.Lon, b.Lat, b.Lon)
}
