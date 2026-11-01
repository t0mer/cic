// Package places stores named circular zones ("safe places") and matches coordinates to them.
package places

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/geo"
)

// Limits of a place.
const (
	MaxPlaces    = 100
	MaxNameRunes = 40
	MinRadiusM   = 10
	MaxRadiusM   = 50000
)

var (
	// ErrInvalid wraps every validation failure (including the place limit).
	ErrInvalid = errors.New("invalid place")
	// ErrDuplicate is returned when another place already has the name (case-insensitive).
	ErrDuplicate = errors.New("a place with this name already exists")
	// ErrNotFound is returned when the place does not exist.
	ErrNotFound = errors.New("place not found")
)

// Place is a named circle.
type Place struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	RadiusM   int     `json:"radius_m"`
}

// Patch is a partial update: nil fields stay unchanged.
type Patch struct {
	Name      *string
	Latitude  *float64
	Longitude *float64
	RadiusM   *int
}

// Store persists places in SQLite and keeps the list cached for Match.
type Store struct {
	db  *sql.DB
	now func() time.Time

	writeMu sync.Mutex // serialises writes and the cache refresh that follows

	mu    sync.RWMutex
	cache []Place
	hooks []func()
}

// NewStore returns a Store with its cache loaded.
func NewStore(ctx context.Context, db *sql.DB) (*Store, error) {
	s := &Store{db: db, now: time.Now}
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// OnChange registers fn to be called after every successful Create, Update or Delete.
func (s *Store) OnChange(fn func()) {
	s.mu.Lock()
	s.hooks = append(s.hooks, fn)
	s.mu.Unlock()
}

func (s *Store) refresh(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, latitude, longitude, radius_m FROM safe_places ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return fmt.Errorf("listing safe places: %w", err)
	}
	defer rows.Close()
	var out []Place
	for rows.Next() {
		var p Place
		if err := rows.Scan(&p.ID, &p.Name, &p.Latitude, &p.Longitude, &p.RadiusM); err != nil {
			return fmt.Errorf("scanning safe place: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("listing safe places: %w", err)
	}
	s.mu.Lock()
	s.cache = out
	s.mu.Unlock()
	return nil
}

func (s *Store) fire() {
	s.mu.RLock()
	hooks := slices.Clone(s.hooks)
	s.mu.RUnlock()
	for _, fn := range hooks {
		fn()
	}
}

// List returns all places ordered by name.
func (s *Store) List() []Place {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.cache)
}

// Get returns the place with id, or ErrNotFound.
func (s *Store) Get(id int64) (Place, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.cache {
		if p.ID == id {
			return p, nil
		}
	}
	return Place{}, ErrNotFound
}

// Match returns the place containing (lat, lon), or nil. A point is inside when its distance
// to the centre is at most the radius; among overlapping places the one with the smallest
// distance/radius ratio wins (first by name on an exact tie).
func (s *Store) Match(lat, lon float64) *Place {
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *Place
	bestRatio := math.Inf(1)
	for i := range s.cache {
		p := &s.cache[i]
		d := geo.Haversine(p.Latitude, p.Longitude, lat, lon)
		if d > float64(p.RadiusM) {
			continue
		}
		if ratio := d / float64(p.RadiusM); ratio < bestRatio {
			best, bestRatio = p, ratio
		}
	}
	if best == nil {
		return nil
	}
	c := *best
	return &c
}

// CleanName trims name and reports whether it is a valid place name.
func CleanName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > MaxNameRunes {
		return "", false
	}
	for _, r := range name {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return "", false
		}
	}
	return name, true
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func validate(p *Place) error {
	name, ok := CleanName(p.Name)
	if !ok {
		return invalid("name must be 1-%d characters without control characters", MaxNameRunes)
	}
	p.Name = name
	if math.IsNaN(p.Latitude) || math.IsInf(p.Latitude, 0) || p.Latitude < -90 || p.Latitude > 90 {
		return invalid("latitude must be between -90 and 90")
	}
	if math.IsNaN(p.Longitude) || math.IsInf(p.Longitude, 0) || p.Longitude < -180 || p.Longitude > 180 {
		return invalid("longitude must be between -180 and 180")
	}
	if p.RadiusM < MinRadiusM || p.RadiusM > MaxRadiusM {
		return invalid("radius_m must be between %d and %d", MinRadiusM, MaxRadiusM)
	}
	return nil
}

// nameTaken reports whether another place (not skipID) uses name. Callers hold writeMu.
func (s *Store) nameTaken(name string, skipID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.ContainsFunc(s.cache, func(p Place) bool { return p.ID != skipID && strings.EqualFold(p.Name, name) })
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Create validates and stores p (its ID is ignored) and returns the stored place.
func (s *Store) Create(ctx context.Context, p Place) (Place, error) {
	p, err := s.create(ctx, p)
	if err == nil {
		s.fire() // after writeMu is released
	}
	return p, err
}

func (s *Store) create(ctx context.Context, p Place) (Place, error) {
	if err := validate(&p); err != nil {
		return Place{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.RLock()
	count := len(s.cache)
	s.mu.RUnlock()
	if count >= MaxPlaces {
		return Place{}, invalid("at most %d places are allowed", MaxPlaces)
	}
	if s.nameTaken(p.Name, 0) {
		return Place{}, ErrDuplicate
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO safe_places (name, latitude, longitude, radius_m, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, p.Name, p.Latitude, p.Longitude, p.RadiusM, now, now)
	if isUnique(err) {
		return Place{}, ErrDuplicate
	}
	if err != nil {
		return Place{}, fmt.Errorf("creating safe place: %w", err)
	}
	if p.ID, err = res.LastInsertId(); err != nil {
		return Place{}, fmt.Errorf("creating safe place: %w", err)
	}
	if err := s.refresh(context.WithoutCancel(ctx)); err != nil {
		return Place{}, err
	}
	return p, nil
}

// Update applies a partial patch and returns the stored place.
func (s *Store) Update(ctx context.Context, id int64, patch Patch) (Place, error) {
	if patch == (Patch{}) {
		return Place{}, invalid("nothing to change")
	}
	p, err := s.update(ctx, id, patch)
	if err == nil {
		s.fire() // after writeMu is released
	}
	return p, err
}

func (s *Store) update(ctx context.Context, id int64, patch Patch) (Place, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	p, err := s.Get(id)
	if err != nil {
		return Place{}, err
	}
	if patch.Name != nil {
		p.Name = *patch.Name
	}
	if patch.Latitude != nil {
		p.Latitude = *patch.Latitude
	}
	if patch.Longitude != nil {
		p.Longitude = *patch.Longitude
	}
	if patch.RadiusM != nil {
		p.RadiusM = *patch.RadiusM
	}
	if err := validate(&p); err != nil {
		return Place{}, err
	}
	if s.nameTaken(p.Name, id) {
		return Place{}, ErrDuplicate
	}
	res, err := s.db.ExecContext(ctx, `UPDATE safe_places SET name = ?, latitude = ?, longitude = ?, radius_m = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Latitude, p.Longitude, p.RadiusM, s.now().Unix(), id)
	if isUnique(err) {
		return Place{}, ErrDuplicate
	}
	if err != nil {
		return Place{}, fmt.Errorf("updating safe place: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Place{}, ErrNotFound
	}
	if err := s.refresh(context.WithoutCancel(ctx)); err != nil {
		return Place{}, err
	}
	return p, nil
}

// Delete removes a place, or returns ErrNotFound.
func (s *Store) Delete(ctx context.Context, id int64) error {
	if err := s.remove(ctx, id); err != nil {
		return err
	}
	s.fire() // after writeMu is released
	return nil
}

func (s *Store) remove(ctx context.Context, id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM safe_places WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting safe place: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := s.refresh(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	return nil
}

// Annotate sets d.SafePlace from the device's last fix. It matches the signature expected by
// devices.Registry.SetAnnotator.
func (s *Store) Annotate(d *devices.Device) {
	d.SafePlace = nil
	if d.Latitude == nil || d.Longitude == nil {
		return
	}
	if p := s.Match(*d.Latitude, *d.Longitude); p != nil {
		d.SafePlace = &devices.SafePlaceRef{ID: p.ID, Name: p.Name}
	}
}
