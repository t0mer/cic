package geocode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// CacheTTL is how long a cached name stays usable; older entries are purged.
const CacheTTL = 30 * 24 * time.Hour

// Store persists the name cache and the daily usage counters.
type Store struct{ DB *sql.DB }

// CacheKey rounds lat/lon to 3 decimals (~110 m).
func CacheKey(lat, lon float64) string {
	r := func(v float64) string {
		v = math.Round(v*1000) / 1000
		if v == 0 {
			v = 0 // drops the sign of -0
		}
		return strconv.FormatFloat(v, 'f', 3, 64)
	}
	return r(lat) + "," + r(lon)
}

// Get returns the cached name for key. ok is false on a miss or an expired entry; an
// empty name with ok means the provider is known to have no name there.
func (s Store) Get(ctx context.Context, key string, now time.Time) (name string, ok bool, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT name FROM geocode_cache WHERE key = ? AND created_at > ?`,
		key, now.Add(-CacheTTL).Unix()).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading geocode cache: %w", err)
	}
	return name, true, nil
}

// Put stores name for key.
func (s Store) Put(ctx context.Context, key, name string, now time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO geocode_cache(key, name, created_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET name = excluded.name, created_at = excluded.created_at`,
		key, name, now.Unix())
	if err != nil {
		return fmt.Errorf("writing geocode cache: %w", err)
	}
	return nil
}

// Purge deletes cache entries older than CacheTTL and returns how many.
func (s Store) Purge(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM geocode_cache WHERE created_at <= ?`, now.Add(-CacheTTL).Unix())
	if err != nil {
		return 0, fmt.Errorf("purging geocode cache: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DayUsage is one UTC day's counters; LastRemaining is nil until the provider reported one.
type DayUsage struct {
	Calls         int
	CacheHits     int
	LastRemaining *int
}

// Usage returns the counters of day (zero when there is no row).
func (s Store) Usage(ctx context.Context, day string) (DayUsage, error) {
	var u DayUsage
	var rem sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT calls, cache_hits, last_remaining FROM geocode_usage WHERE day = ?`, day).
		Scan(&u.Calls, &u.CacheHits, &rem)
	if errors.Is(err, sql.ErrNoRows) {
		return DayUsage{}, nil
	}
	if err != nil {
		return DayUsage{}, fmt.Errorf("reading geocode usage: %w", err)
	}
	if rem.Valid {
		v := int(rem.Int64)
		u.LastRemaining = &v
	}
	return u, nil
}

// AddCall counts one provider call on day and records the provider's remaining quota when known.
func (s Store) AddCall(ctx context.Context, day string, remaining *int) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO geocode_usage(day, calls, cache_hits, last_remaining) VALUES (?, 1, 0, ?)
		ON CONFLICT(day) DO UPDATE SET calls = calls + 1, last_remaining = COALESCE(excluded.last_remaining, last_remaining)`,
		day, remaining)
	if err != nil {
		return fmt.Errorf("counting geocode call: %w", err)
	}
	return nil
}

// AddHit counts one cache hit on day.
func (s Store) AddHit(ctx context.Context, day string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO geocode_usage(day, calls, cache_hits) VALUES (?, 0, 1)
		ON CONFLICT(day) DO UPDATE SET cache_hits = cache_hits + 1`, day)
	if err != nil {
		return fmt.Errorf("counting geocode cache hit: %w", err)
	}
	return nil
}

// SetBlocked persists the end of a rate-limit backoff in day's usage row.
func (s Store) SetBlocked(ctx context.Context, day string, until time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO geocode_usage(day, calls, cache_hits, blocked_until) VALUES (?, 0, 0, ?)
		ON CONFLICT(day) DO UPDATE SET blocked_until = excluded.blocked_until`, day, until.Unix())
	if err != nil {
		return fmt.Errorf("saving geocode backoff: %w", err)
	}
	return nil
}

// Blocked returns the latest persisted backoff end, or the zero time when there is none.
func (s Store) Blocked(ctx context.Context) (time.Time, error) {
	var v sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT MAX(blocked_until) FROM geocode_usage`).Scan(&v); err != nil {
		return time.Time{}, fmt.Errorf("reading geocode backoff: %w", err)
	}
	if !v.Valid {
		return time.Time{}, nil
	}
	return time.Unix(v.Int64, 0), nil
}
