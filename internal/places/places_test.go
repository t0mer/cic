package places

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/t0mer/cic/internal/geo"
	"github.com/t0mer/cic/internal/store"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func home() Place { return Place{Name: "Home", Latitude: 32.0, Longitude: 34.8, RadiusM: 200} }

func TestCRUDAndHook(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	fired := 0
	s.OnChange(func() { fired++ })
	p, err := s.Create(ctx, Place{Name: "  Home  ", Latitude: 32, Longitude: 34.8, RadiusM: 200})
	if err != nil || p.ID == 0 || p.Name != "Home" {
		t.Fatalf("Create = %+v, %v", p, err)
	}
	name, r := "House", 300
	u, err := s.Update(ctx, p.ID, Patch{Name: &name, RadiusM: &r})
	if err != nil || u.Name != "House" || u.RadiusM != 300 || u.Latitude != 32 {
		t.Fatalf("Update = %+v, %v", u, err)
	}
	if got := s.List(); len(got) != 1 || got[0] != u {
		t.Errorf("List = %+v", got)
	}
	if err := s.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 || fired != 3 {
		t.Errorf("list %v fired %d", s.List(), fired)
	}
	if err := s.Delete(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete missing = %v", err)
	}
	if _, err := s.Update(ctx, 99, Patch{RadiusM: new(300)}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing = %v", err)
	}
	if fired != 3 {
		t.Errorf("failed writes fired the hook: %d", fired)
	}
}

func TestCacheSurvivesReopen(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create(context.Background(), home()); err != nil {
		t.Fatal(err)
	}
	s2, err := NewStore(context.Background(), s.db)
	if err != nil || len(s2.List()) != 1 || s2.Match(32, 34.8) == nil {
		t.Errorf("reloaded = %v, %v", s2.List(), err)
	}
}

func TestValidation(t *testing.T) {
	s := newStore(t)
	ok := home()
	tests := map[string]func(*Place){
		"empty name":   func(p *Place) { p.Name = "   " },
		"long name":    func(p *Place) { p.Name = strings.Repeat("x", 41) },
		"control":      func(p *Place) { p.Name = "a\nb" },
		"bidi":         func(p *Place) { p.Name = "a‮b" },
		"lat high":     func(p *Place) { p.Latitude = 90.1 },
		"lat nan":      func(p *Place) { p.Latitude = nan() },
		"lon low":      func(p *Place) { p.Longitude = -180.1 },
		"lon inf":      func(p *Place) { p.Longitude = inf() },
		"radius small": func(p *Place) { p.RadiusM = 9 },
		"radius large": func(p *Place) { p.RadiusM = 50001 },
	}
	for name, mut := range tests {
		p := ok
		mut(&p)
		if _, err := s.Create(context.Background(), p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, p := range []Place{
		{Name: strings.Repeat("é", 40), Latitude: -90, Longitude: 180, RadiusM: 10},
		{Name: "Max", Latitude: 90, Longitude: -180, RadiusM: 50000},
	} {
		if _, err := s.Create(context.Background(), p); err != nil {
			t.Errorf("boundary %q: %v", p.Name, err)
		}
	}
	// Update validates the merged result.
	created, _ := s.Create(context.Background(), home())
	bad := 5
	if _, err := s.Update(context.Background(), created.ID, Patch{RadiusM: &bad}); !errors.Is(err, ErrInvalid) {
		t.Errorf("update radius: %v", err)
	}
}

func TestDuplicates(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, home())
	other := home()
	other.Name = "HOME"
	if _, err := s.Create(ctx, other); !errors.Is(err, ErrDuplicate) {
		t.Errorf("case-insensitive duplicate: %v", err)
	}
	other.Name = "Work"
	b, err := s.Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	n := "home"
	if _, err := s.Update(ctx, b.ID, Patch{Name: &n}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("rename onto existing: %v", err)
	}
	// Renaming to a different case of its own name is fine.
	if _, err := s.Update(ctx, a.ID, Patch{Name: &n}); err != nil {
		t.Errorf("own name: %v", err)
	}
}

func TestCap(t *testing.T) {
	s := newStore(t)
	for i := 0; i < MaxPlaces; i++ {
		if _, err := s.Create(context.Background(), Place{Name: fmt.Sprintf("p%d", i), Latitude: 1, Longitude: 1, RadiusM: 50}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(context.Background(), home()); !errors.Is(err, ErrInvalid) {
		t.Errorf("101st place: %v", err)
	}
}

func TestMatchBoundary(t *testing.T) {
	s := newStore(t)
	// A point exactly 'radius' metres away: build the radius from the real distance.
	lat2, lon2 := 32.001, 34.8
	d := geo.Haversine(32, 34.8, lat2, lon2) // ~111 m
	r := int(d)                              // floor: the point is just outside
	if _, err := s.Create(context.Background(), Place{Name: "Edge", Latitude: 32, Longitude: 34.8, RadiusM: r}); err != nil {
		t.Fatal(err)
	}
	if s.Match(lat2, lon2) != nil {
		t.Error("point beyond the radius matched")
	}
	r2 := r + 1
	if _, err := s.Update(context.Background(), 1, Patch{RadiusM: &r2}); err != nil {
		t.Fatal(err)
	}
	if p := s.Match(lat2, lon2); p == nil || p.Name != "Edge" {
		t.Errorf("point inside = %v", p)
	}
	if s.Match(nan(), 0) != nil {
		t.Error("NaN matched")
	}
	// Exactly on the centre of a minimum-size circle matches (distance 0 <= radius).
	if _, err := s.Create(context.Background(), Place{Name: "Dot", Latitude: -10, Longitude: -10, RadiusM: 10}); err != nil {
		t.Fatal(err)
	}
	if s.Match(-10, -10) == nil {
		t.Error("centre did not match")
	}
}

func TestMatchOverlapPicksSmallestRatio(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	// Big circle centred 100 m from the probe (ratio 0.01), small circle 40 m away (ratio 0.8).
	if _, err := s.Create(ctx, Place{Name: "Campus", Latitude: 32.0, Longitude: 34.8, RadiusM: 10000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, Place{Name: "Kiosk", Latitude: 32.0003, Longitude: 34.8, RadiusM: 50}); err != nil {
		t.Fatal(err)
	}
	// Probe at the kiosk centre: ratio 0 wins over Campus.
	if p := s.Match(32.0003, 34.8); p == nil || p.Name != "Kiosk" {
		t.Errorf("at kiosk centre: %v", p)
	}
	// Probe at the campus centre: Campus ratio 0, kiosk (33 m of 50) also inside, ratio 0.67 -> Campus.
	if p := s.Match(32.0, 34.8); p == nil || p.Name != "Campus" {
		t.Errorf("at campus centre: %v", p)
	}
	// A larger absolute distance can still win through a smaller ratio.
	if p := s.Match(32.0008, 34.8); p == nil || p.Name != "Campus" {
		t.Errorf("outside kiosk: %v", p)
	}
}

func TestMatchTieBreaksByName(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, n := range []string{"Zeta", "Alpha"} {
		if _, err := s.Create(ctx, Place{Name: n, Latitude: 5, Longitude: 5, RadiusM: 100}); err != nil {
			t.Fatal(err)
		}
	}
	if p := s.Match(5, 5); p == nil || p.Name != "Alpha" {
		t.Errorf("tie = %v", p)
	}
}

func nan() float64 { var z float64; return z / z }
func inf() float64 { var z float64; return 1 / z }

func TestEmptyPatchIsInvalid(t *testing.T) {
	s := newStore(t)
	p, err := s.Create(context.Background(), home())
	if err != nil {
		t.Fatal(err)
	}
	fired := 0
	s.OnChange(func() { fired++ })
	if _, err := s.Update(context.Background(), p.ID, Patch{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "nothing to change") {
		t.Errorf("empty patch = %v", err)
	}
	if fired != 0 {
		t.Error("empty patch fired the hook")
	}
}

// flipCtx reports itself cancelled after its Done channel has been requested limit times.
type flipCtx struct {
	context.Context
	calls, limit int
	closed       chan struct{}
}

func (c *flipCtx) Done() <-chan struct{} {
	c.calls++
	if c.calls > c.limit {
		return c.closed
	}
	return nil
}

func (c *flipCtx) Err() error {
	if c.calls > c.limit {
		return context.Canceled
	}
	return nil
}

func newFlip(limit int) *flipCtx {
	ch := make(chan struct{})
	close(ch)
	return &flipCtx{Context: context.Background(), limit: limit, closed: ch}
}

func TestCancelAfterCommitStillRefreshesCache(t *testing.T) {
	s := newStore(t)
	p, err := s.Create(context.Background(), home())
	if err != nil {
		t.Fatal(err)
	}
	// Learn how often the DELETE statement itself polls the context: a missing id runs only it.
	probe := newFlip(1 << 30)
	if err := s.Delete(probe, 999); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// Cancel right after the DELETE has been issued: the cache refresh must still succeed.
	if err := s.Delete(newFlip(probe.calls), p.ID); err != nil {
		t.Fatalf("Delete with context cancelled after commit = %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("stale cache after cancelled request: %+v", got)
	}
}

func TestHookRunsOutsideWriteLock(t *testing.T) {
	s := newStore(t)
	s.OnChange(func() {
		if !s.writeMu.TryLock() {
			t.Error("hook ran while writeMu was held")
			return
		}
		s.writeMu.Unlock()
	})
	if _, err := s.Create(context.Background(), home()); err != nil {
		t.Fatal(err)
	}
}
