package ratelog

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := &Limiter{Interval: 10 * time.Second, Now: func() time.Time { return now }}
	steps := []struct {
		advance   time.Duration
		wantTotal uint64
		wantOK    bool
	}{
		{0, 1, true},
		{time.Second, 2, false},
		{8 * time.Second, 3, false},
		{time.Second, 4, true},
		{time.Second, 5, false},
	}
	for i, s := range steps {
		now = now.Add(s.advance)
		total, ok := l.Hit()
		if total != s.wantTotal || ok != s.wantOK {
			t.Errorf("step %d: Hit = %d, %v; want %d, %v", i, total, ok, s.wantTotal, s.wantOK)
		}
	}
}
