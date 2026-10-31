package commands

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// limiter is an in-memory sliding-window rate limiter keyed by an arbitrary
// string. Idle keys are pruned so the map stays bounded by the active keys.
type limiter struct {
	mu     sync.Mutex
	window time.Duration
	hits   map[string][]time.Time
	calls  int
}

// sweepEvery is how many allow calls pass between full prunes of idle keys.
const sweepEvery = 256

func newLimiter(window time.Duration) *limiter {
	return &limiter{window: window, hits: make(map[string][]time.Time)}
}

// trim drops hits at or before the window start; the caller holds l.mu.
func (l *limiter) trim(h []time.Time, now time.Time) []time.Time {
	cut := now.Add(-l.window)
	i := 0
	for i < len(h) && !h[i].After(cut) {
		i++
	}
	return h[i:]
}

// allow records a hit for key at now unless max hits already fall in the window.
func (l *limiter) allow(key string, max int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls++; l.calls%sweepEvery == 0 {
		for k, h := range l.hits {
			if len(l.trim(h, now)) == 0 {
				delete(l.hits, k)
			}
		}
	}
	h := l.trim(l.hits[key], now)
	if len(h) == 0 {
		delete(l.hits, key)
	}
	if len(h) >= max {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, now)
	return true
}

// undo removes the most recent hit recorded at exactly at, giving the slot
// back when the guarded operation provably did not happen.
func (l *limiter) undo(key string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := l.hits[key]
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Equal(at) {
			h = append(h[:i], h[i+1:]...)
			break
		}
	}
	if len(h) == 0 {
		delete(l.hits, key)
		return
	}
	l.hits[key] = h
}

// UsageStore counts billed SMS segments per UTC day.
type UsageStore interface {
	// Reserve atomically takes n units of day's budget, reporting false (and
	// taking nothing) when that would exceed cap.
	Reserve(ctx context.Context, day string, n, cap int) (bool, error)
	// Release gives back n units taken by Reserve, never going below zero.
	Release(ctx context.Context, day string, n int) error
}

// SQLUsage implements UsageStore on the sms_usage table.
type SQLUsage struct{ DB *sql.DB }

// Reserve implements UsageStore.
func (s SQLUsage) Reserve(ctx context.Context, day string, n, cap int) (bool, error) {
	if n < 1 || n > cap {
		return false, nil
	}
	var got int
	err := s.DB.QueryRowContext(ctx, `INSERT INTO sms_usage(day, count) VALUES (?, ?)
		ON CONFLICT(day) DO UPDATE SET count = count + ? WHERE count + ? <= ?
		RETURNING count`, day, n, n, n, cap).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reserving sms budget: %w", err)
	}
	return true, nil
}

// Release implements UsageStore.
func (s SQLUsage) Release(ctx context.Context, day string, n int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sms_usage SET count = MAX(count - ?, 0) WHERE day = ?`, n, day)
	if err != nil {
		return fmt.Errorf("releasing sms budget: %w", err)
	}
	return nil
}
