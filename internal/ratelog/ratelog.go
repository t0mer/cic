// Package ratelog limits how often a repeating condition is logged.
package ratelog

import (
	"sync"
	"time"
)

// Limiter lets one event through per interval and counts all of them.
type Limiter struct {
	Interval time.Duration
	Now      func() time.Time

	mu    sync.Mutex
	last  time.Time
	total uint64
}

// Hit records an occurrence. ok is true when it should be logged; total is the
// running number of occurrences, including this one.
func (l *Limiter) Hit() (total uint64, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	l.total++
	t := now()
	if !l.last.IsZero() && t.Sub(l.last) < l.Interval {
		return l.total, false
	}
	l.last = t
	return l.total, true
}
