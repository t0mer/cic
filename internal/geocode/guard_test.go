package geocode

import (
	"errors"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestShouldLookup(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	g := NewGuard(Config{MinMoveMeters: 150, MaxAge: 24 * time.Hour}, func() time.Time { return now })
	here := Point{32.0853, 34.7818}
	fresh := now.Add(-time.Hour)
	cases := []struct {
		name string
		prev *Point
		at   *time.Time
		cur  Point
		want bool
	}{
		{"no previous point", nil, nil, here, true},
		{"unmoved and fresh", &here, &fresh, here, false},
		{"moved 100 m", &here, &fresh, Point{32.0853 + 0.0009, 34.7818}, false},
		{"moved 200 m", &here, &fresh, Point{32.0853 + 0.0018, 34.7818}, true},
		{"name too old", &here, ptr(now.Add(-25 * time.Hour)), here, true},
	}
	for _, c := range cases {
		if got := g.ShouldLookup(c.prev, c.at, c.cur); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestHaversine(t *testing.T) {
	d := Haversine(Point{0, 0}, Point{0, 1})
	if d < 111000 || d > 111400 {
		t.Fatalf("1 degree of longitude at the equator: %v", d)
	}
}

func TestAllowCapReserveBackoffAndRollover(t *testing.T) {
	now := time.Date(2026, 5, 1, 23, 30, 0, 0, time.UTC)
	g := NewGuard(Config{DailyCap: 10, Reserve: 20}, func() time.Time { return now })
	if err := g.Allow(9, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.Allow(10, nil); !errors.Is(err, ErrDailyCap) {
		t.Fatalf("cap: %v", err)
	}
	if err := g.Allow(0, ptr(19)); !errors.Is(err, ErrReserve) {
		t.Fatalf("reserve: %v", err)
	}
	if err := g.Allow(0, ptr(20)); err != nil {
		t.Fatalf("at reserve: %v", err)
	}
	g.RateLimited(0)
	if err := g.Allow(0, nil); !errors.Is(err, ErrBackoff) {
		t.Fatalf("backoff: %v", err)
	}
	if u := g.BlockedUntil(); u == nil || !u.Equal(time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("blocked until %v", u)
	}
	now = now.Add(31 * time.Minute) // past UTC midnight
	if err := g.Allow(0, nil); err != nil {
		t.Fatalf("after rollover: %v", err)
	}
	if g.BlockedUntil() != nil {
		t.Fatal("still blocked")
	}
	if Day(now) != "2026-05-02" {
		t.Fatalf("day %s", Day(now))
	}
}
