// Package devices keeps the registry of DRADIS devices discovered over MQTT.
package devices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/t0mer/cic/internal/geo"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/ratelog"
	"github.com/t0mer/cic/internal/store"
)

// Device is a registered device. Pointer fields are nil while unknown.
type Device struct {
	Key           string   `json:"key"`
	Prefix        string   `json:"prefix"`
	Device        string   `json:"device"`
	FullName      string   `json:"full_name"`
	Color         string   `json:"color"`
	Hidden        bool     `json:"hidden"`
	Online        bool     `json:"online"` // status 1, seen live, and within offline_after
	Status        *bool    `json:"status"`
	Version       *string  `json:"version"`
	BatteryLevel  *int     `json:"battery_level"`
	Charging      *bool    `json:"charging"`
	ChargeType    *string  `json:"charge_type"`
	WifiConnected *bool    `json:"wifi_connected"`
	SSID          *string  `json:"ssid"`
	Latitude      *float64 `json:"latitude"`
	Longitude     *float64 `json:"longitude"`
	GPSAccuracy   *float64 `json:"gps_accuracy"`
	LocationTime  *int64   `json:"location_time"`
	// LocationName is the reverse-geocoded place name of the last fix; nil while unresolved.
	LocationName   *string `json:"location_name"`
	LocationNameAt *int64  `json:"location_name_at"` // unix seconds the name was resolved
	// GeocodedLat/Lon are the coordinates LocationName was resolved for; internal bookkeeping.
	GeocodedLat *float64 `json:"-"`
	GeocodedLon *float64 `json:"-"`
	// SafePlace is the safe place the last fix lies in; computed on read, never stored. Nil when
	// the device is in none or has no location.
	SafePlace    *SafePlaceRef `json:"safe_place"`
	DeviceInfo   *string       `json:"device_info"`
	ScreenLocked *bool         `json:"screen_locked"`
	LastSeen     int64         `json:"last_seen"`
	CreatedAt    int64         `json:"created_at"`
}

// SafePlaceRef names the safe place a device is currently in.
type SafePlaceRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ErrDeviceLimit is returned by Apply when a new device would exceed the configured cap.
var ErrDeviceLimit = errors.New("device limit reached")

// ErrDeviceVanished is returned by Apply when the device row disappeared mid-update.
var ErrDeviceVanished = errors.New("device vanished during update")

// Registry stores devices in SQLite and tracks their liveness.
type Registry struct {
	db           *sql.DB
	offlineAfter time.Duration
	maxDevices   int
	now          func() time.Time
	log          *slog.Logger
	limitWarn    ratelog.Limiter

	mu           sync.RWMutex
	hooks        []func(Device)
	annotate     func(*Device)
	removeHooks  []func(Device)
	locHooks     []func(key string, ts int64, lat, lon float64, accuracy *float64)
	refreshHooks []func()
}

// NewRegistry returns a Registry; see Device.Online for the liveness rule.
func NewRegistry(db *sql.DB, offlineAfter time.Duration, log *slog.Logger) *Registry {
	return &Registry{db: db, offlineAfter: offlineAfter, now: time.Now, log: log,
		limitWarn: ratelog.Limiter{Interval: 10 * time.Second}}
}

// SetMaxDevices caps the number of devices; 0 means unlimited. Call before use.
func (r *Registry) SetMaxDevices(n int) {
	r.maxDevices = n
}

// SetAnnotator sets a function applied to every device read from the store (list, get, notify),
// used to attach computed fields such as SafePlace. Call before use.
func (r *Registry) SetAnnotator(fn func(*Device)) {
	r.mu.Lock()
	r.annotate = fn
	r.mu.Unlock()
}

// Renotify calls the OnRefresh hooks once, so listeners know computed fields (for example
// SafePlace after a place was edited) changed on many devices without one event per device.
func (r *Registry) Renotify() {
	r.mu.RLock()
	hooks := slices.Clone(r.refreshHooks)
	r.mu.RUnlock()
	for _, fn := range hooks {
		fn()
	}
}

// OnRefresh registers fn to be called by Renotify.
func (r *Registry) OnRefresh(fn func()) {
	r.mu.Lock()
	r.refreshHooks = append(r.refreshHooks, fn)
	r.mu.Unlock()
}

// OnChange registers fn to be called after every successful Apply.
func (r *Registry) OnChange(fn func(Device)) {
	r.mu.Lock()
	r.hooks = append(r.hooks, fn)
	r.mu.Unlock()
}

// OnRemove registers fn to be called, with the removed device, after every successful Delete.
func (r *Registry) OnRemove(fn func(Device)) {
	r.mu.Lock()
	r.removeHooks = append(r.removeHooks, fn)
	r.mu.Unlock()
}

// OnLocation registers fn to be called, synchronously after the commit, for every location
// update that was applied (not for stale fixes). ts is the phone's own fix time. Keep fn quick.
func (r *Registry) OnLocation(fn func(key string, ts int64, lat, lon float64, accuracy *float64)) {
	r.mu.Lock()
	r.locHooks = append(r.locHooks, fn)
	r.mu.Unlock()
}

// Handle implements mqtt.Sink.
func (r *Registry) Handle(ev mqtt.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := r.Apply(ctx, ev)
	switch {
	case err == nil:
	case errors.Is(err, ErrDeviceLimit):
		if total, ok := r.limitWarn.Hit(); ok {
			r.log.Warn("device limit reached, ignoring new device", "device", ev.Prefix+"/"+ev.Device,
				"max_devices", r.maxDevices, "ignored_total", total)
		}
	default:
		r.log.Error("applying mqtt event", "device", ev.Device, "leaf", ev.Leaf, "err", err)
	}
}

// Apply records ev, creating the device on first sight and updating only the
// fields the event carries, in one transaction.
//
// Liveness: a live (non-retained) message of any leaf marks the device online
// and bumps last_seen; a live status sets the state from its payload. A
// retained status only seeds the state of a device never seen live, and no
// retained message bumps last_seen.
func (r *Registry) Apply(ctx context.Context, ev mqtt.Event) error {
	if ev.Leaf == mqtt.LeafSMSResult {
		return nil
	}
	set, args, err := updateFor(ev)
	if err != nil {
		return err
	}
	key := ev.Prefix + "/" + ev.Device
	seen := ev.ReceivedAt.Unix()
	if ev.Retained {
		seen = 0
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting update of device %q: %w", key, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	// Inserts only when the device exists already or the cap allows another one.
	res, err := tx.ExecContext(ctx, `INSERT INTO devices(key, prefix, device, last_seen, created_at)
		SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM devices WHERE key = ?)
			OR ? <= 0 OR (SELECT COUNT(*) FROM devices) < ?
		ON CONFLICT(key) DO NOTHING`,
		key, ev.Prefix, ev.Device, seen, ev.ReceivedAt.Unix(), key, r.maxDevices, r.maxDevices)
	if err != nil {
		return fmt.Errorf("creating device %q: %w", key, err)
	}
	inserted, _ := res.RowsAffected()

	var applied *mqtt.Location
	if loc, ok := ev.Payload.(mqtt.Location); ok {
		if stale, err := staleLocation(ctx, tx, key, locationTime(ev, loc)); err != nil {
			return err
		} else if stale {
			r.log.Debug("stale location ignored", "device", key, "location_time", locationTime(ev, loc))
			set, args = "", nil
		} else {
			applied = &loc
			if far, err := movedFarFromName(ctx, tx, key, loc.Latitude, loc.Longitude); err != nil {
				return err
			} else if far {
				set += `, location_name = NULL, location_name_at = NULL, geocoded_lat = NULL, geocoded_lon = NULL`
			}
		}
	}
	parts, args := withLiveness(ev, set, args, seen)
	args = append(args, key)
	res, err = tx.ExecContext(ctx, `UPDATE devices SET `+parts+` WHERE key = ?`, args...)
	if err != nil {
		return fmt.Errorf("updating device %q: %w", key, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if inserted == 0 {
			return fmt.Errorf("device %q: %w", key, ErrDeviceLimit)
		}
		return fmt.Errorf("device %q: %w", key, ErrDeviceVanished)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing update of device %q: %w", key, err)
	}
	r.notify(ctx, key)
	if applied != nil {
		r.mu.RLock()
		hooks := slices.Clone(r.locHooks)
		r.mu.RUnlock()
		for _, fn := range hooks {
			fn(key, locationTime(ev, *applied), applied.Latitude, applied.Longitude, applied.GPSAccuracy)
		}
	}
	return nil
}

func locationTime(ev mqtt.Event, loc mqtt.Location) int64 {
	if loc.Time != nil {
		return *loc.Time
	}
	return ev.ReceivedAt.Unix()
}

// nameKeepMeters is how far a device may move from the point its place name was resolved
// for before that name is dropped as stale.
const nameKeepMeters = 1000

// movedFarFromName reports whether (lat, lon) is more than nameKeepMeters from the
// coordinates the stored place name was resolved for.
func movedFarFromName(ctx context.Context, tx *sql.Tx, key string, lat, lon float64) (bool, error) {
	var gLat, gLon sql.NullFloat64
	err := tx.QueryRowContext(ctx, `SELECT geocoded_lat, geocoded_lon FROM devices WHERE key = ?`, key).Scan(&gLat, &gLon)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !(gLat.Valid && gLon.Valid)) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading geocoded point of device %q: %w", key, err)
	}
	return geo.Haversine(gLat.Float64, gLon.Float64, lat, lon) > nameKeepMeters, nil
}

func staleLocation(ctx context.Context, tx *sql.Tx, key string, t int64) (bool, error) {
	var stored sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT location_time FROM devices WHERE key = ?`, key).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading location time of device %q: %w", key, err)
	}
	return stored.Valid && t < stored.Int64, nil
}

// withLiveness appends the status and last_seen assignments to a leaf's SET clause.
func withLiveness(ev mqtt.Event, set string, args []any, seen int64) (string, []any) {
	var parts []string
	if set != "" {
		parts = append(parts, set)
	}
	switch p := ev.Payload.(type) {
	case mqtt.Status:
		if ev.Retained {
			parts = append(parts, `status = CASE WHEN last_seen = 0 THEN ? ELSE status END`)
		} else {
			parts = append(parts, `status = ?`)
		}
		args = append(args, p.Online)
	default:
		if !ev.Retained {
			parts = append(parts, `status = 1`)
		}
	}
	if !ev.Retained {
		parts = append(parts, `last_seen = MAX(last_seen, ?)`)
		args = append(args, seen)
	}
	if len(parts) == 0 {
		return "last_seen = last_seen", args
	}
	return strings.Join(parts, ", "), args
}

// updateFor returns the SET clause for the data a leaf carries; status is handled by withLiveness.
func updateFor(ev mqtt.Event) (set string, args []any, err error) {
	switch p := ev.Payload.(type) {
	case mqtt.Status:
		return "", nil, nil
	case mqtt.Version:
		return `version = ?`, []any{p.Version}, nil
	case mqtt.Battery:
		return `battery_level = COALESCE(?, battery_level), charging = COALESCE(?, charging), charge_type = COALESCE(?, charge_type)`,
			[]any{p.Level, p.Charging, p.ChargeType}, nil
	case mqtt.Wifi:
		switch {
		case p.Connected == nil:
			return `ssid = COALESCE(?, ssid)`, []any{p.SSID}, nil
		case *p.Connected:
			return `wifi_connected = 1, ssid = COALESCE(?, ssid)`, []any{p.SSID}, nil
		}
		return `wifi_connected = 0, ssid = NULL`, nil, nil
	case mqtt.Location:
		return `latitude = ?, longitude = ?, gps_accuracy = ?, location_time = ?`,
			[]any{p.Latitude, p.Longitude, p.GPSAccuracy, locationTime(ev, p)}, nil
	case mqtt.DeviceInfo:
		return `device_info = COALESCE(?, device_info), screen_locked = COALESCE(?, screen_locked)`,
			[]any{p.Info, p.ScreenLocked}, nil
	}
	return "", nil, fmt.Errorf("unsupported payload %T for leaf %q", ev.Payload, ev.Leaf)
}

func (r *Registry) notify(ctx context.Context, key string) {
	r.mu.RLock()
	hooks := slices.Clone(r.hooks)
	r.mu.RUnlock()
	if len(hooks) == 0 {
		return
	}
	d, err := r.Get(ctx, key)
	if err != nil {
		return
	}
	for _, fn := range hooks {
		fn(d)
	}
}

const columns = `key, prefix, device, full_name, color, hidden, status, version, battery_level, charging,
	charge_type, wifi_connected, ssid, latitude, longitude, gps_accuracy, location_time, device_info,
	screen_locked, last_seen, created_at, location_name, location_name_at, geocoded_lat, geocoded_lon`

type scanner interface{ Scan(...any) error }

func (r *Registry) scan(s scanner) (Device, error) {
	var (
		d                               Device
		hidden                          bool
		status, charging, wifi, locked  sql.NullBool
		version, chargeType, ssid, info sql.NullString
		locName                         sql.NullString
		level, locTime, locNameAt       sql.NullInt64
		lat, lon, acc, gLat, gLon       sql.NullFloat64
	)
	if err := s.Scan(&d.Key, &d.Prefix, &d.Device, &d.FullName, &d.Color, &hidden, &status, &version, &level,
		&charging, &chargeType, &wifi, &ssid, &lat, &lon, &acc, &locTime, &info, &locked, &d.LastSeen, &d.CreatedAt,
		&locName, &locNameAt, &gLat, &gLon); err != nil {
		return Device{}, err
	}
	d.Hidden = hidden
	d.Status = nullable(status.Bool, status.Valid)
	d.Version = nullable(version.String, version.Valid)
	d.BatteryLevel = nullable(int(level.Int64), level.Valid)
	d.Charging = nullable(charging.Bool, charging.Valid)
	d.ChargeType = nullable(chargeType.String, chargeType.Valid)
	d.WifiConnected = nullable(wifi.Bool, wifi.Valid)
	d.SSID = nullable(ssid.String, ssid.Valid)
	d.Latitude = nullable(lat.Float64, lat.Valid)
	d.Longitude = nullable(lon.Float64, lon.Valid)
	d.GPSAccuracy = nullable(acc.Float64, acc.Valid)
	d.LocationTime = nullable(locTime.Int64, locTime.Valid)
	d.LocationName = nullable(locName.String, locName.Valid)
	d.LocationNameAt = nullable(locNameAt.Int64, locNameAt.Valid)
	d.GeocodedLat = nullable(gLat.Float64, gLat.Valid)
	d.GeocodedLon = nullable(gLon.Float64, gLon.Valid)
	d.DeviceInfo = nullable(info.String, info.Valid)
	d.ScreenLocked = nullable(locked.Bool, locked.Valid)
	if d.FullName == "" {
		d.FullName = d.Device
	}
	// Online = status is 1, the device was seen live at least once, and the last live message is at
	// most offline_after old. Retained messages never count as "seen" (see Apply).
	age := r.now().Sub(time.Unix(d.LastSeen, 0))
	d.Online = d.Status != nil && *d.Status && d.LastSeen > 0 && age <= r.offlineAfter
	r.mu.RLock()
	annotate := r.annotate
	r.mu.RUnlock()
	if annotate != nil {
		annotate(&d)
	}
	return d, nil
}

func nullable[T any](v T, valid bool) *T {
	if !valid {
		return nil
	}
	return &v
}

// List returns all devices ordered by key.
func (r *Registry) List(ctx context.Context) ([]Device, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM devices ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("listing devices: %w", err)
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := r.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning device: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing devices: %w", err)
	}
	return out, nil
}

// Get returns the device with key, or store.ErrNotFound.
func (r *Registry) Get(ctx context.Context, key string) (Device, error) {
	d, err := r.scan(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM devices WHERE key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, store.ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("reading device %q: %w", key, err)
	}
	return d, nil
}

// SetProfile updates the user-editable fields of a device.
func (r *Registry) SetProfile(ctx context.Context, key, fullName, color string, hidden bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE devices SET full_name = ?, color = ?, hidden = ? WHERE key = ?`,
		fullName, color, hidden, key)
	return affected(res, err, "updating profile of", key)
}

// SetLocationName stores the place name resolved for the coordinates lat/lon and calls the
// OnChange hooks so clients see it. It only applies while the device is still at those
// coordinates: if it moved meanwhile (or was deleted) it returns store.ErrNotFound and the
// newer fix is geocoded on its own.
func (r *Registry) SetLocationName(ctx context.Context, key, name string, lat, lon float64, at time.Time) error {
	// An empty name records "resolved, nothing here": the column is NULLed, never left as the
	// name of some other place.
	res, err := r.db.ExecContext(ctx, `UPDATE devices SET location_name = NULLIF(?, ''), location_name_at = ?,
		geocoded_lat = ?, geocoded_lon = ? WHERE key = ? AND latitude = ? AND longitude = ?`,
		name, at.Unix(), lat, lon, key, lat, lon)
	if err := affected(res, err, "naming location of", key); err != nil {
		return err
	}
	r.notify(ctx, key)
	return nil
}

// ProfilePatch is a partial update of the user-editable fields; nil fields stay unchanged.
type ProfilePatch struct {
	FullName *string
	Color    *string
	Hidden   *bool
}

// UpdateProfile applies p to the device with key in one transaction and returns the updated
// device together with the hidden flag it had before, or store.ErrNotFound. Reading the old
// value inside the transaction that writes the new one makes the transition exact even when
// several edits race. It does not call the OnChange hooks: the caller announces it.
func (r *Registry) UpdateProfile(ctx context.Context, key string, p ProfilePatch) (d Device, wasHidden bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, false, fmt.Errorf("updating profile of device %q: %w", key, err)
	}
	defer tx.Rollback() //nolint:errcheck
	// A no-op write first: it takes SQLite's write lock up front (waiting on busy_timeout)
	// instead of upgrading a read snapshot that a concurrent writer may have invalidated.
	res, err := tx.ExecContext(ctx, `UPDATE devices SET hidden = hidden WHERE key = ?`, key)
	if err := affected(res, err, "updating profile of", key); err != nil {
		return Device{}, false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT hidden FROM devices WHERE key = ?`, key).Scan(&wasHidden); err != nil {
		return Device{}, false, fmt.Errorf("reading device %q: %w", key, err)
	}
	var sets []string
	var args []any
	if p.FullName != nil {
		sets, args = append(sets, "full_name = ?"), append(args, *p.FullName)
	}
	if p.Color != nil {
		sets, args = append(sets, "color = ?"), append(args, *p.Color)
	}
	if p.Hidden != nil {
		sets, args = append(sets, "hidden = ?"), append(args, *p.Hidden)
	}
	if len(sets) > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE devices SET `+strings.Join(sets, ", ")+` WHERE key = ?`, append(args, key)...)
		if err := affected(res, err, "updating profile of", key); err != nil {
			return Device{}, false, err
		}
	}
	d, err = r.scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM devices WHERE key = ?`, key))
	if err != nil {
		return Device{}, false, fmt.Errorf("reading device %q: %w", key, err)
	}
	if err := tx.Commit(); err != nil {
		return Device{}, false, fmt.Errorf("updating profile of device %q: %w", key, err)
	}
	return d, wasHidden, nil
}

// Delete removes a device and calls the OnRemove hooks; it reappears on its next message.
// The row is read and deleted in one statement, so the hooks see the state that was removed.
func (r *Registry) Delete(ctx context.Context, key string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting delete of device %q: %w", key, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	d, err := r.scan(tx.QueryRowContext(ctx, `DELETE FROM devices WHERE key = ? RETURNING `+columns, key))
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("deleting device %q: %w", key, err)
	}
	// The recorded location history goes with the device, atomically.
	if _, err := tx.ExecContext(ctx, `DELETE FROM location_points WHERE device_key = ?`, key); err != nil {
		return fmt.Errorf("deleting location history of device %q: %w", key, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing delete of device %q: %w", key, err)
	}
	r.mu.RLock()
	hooks := slices.Clone(r.removeHooks)
	r.mu.RUnlock()
	for _, fn := range hooks {
		fn(d)
	}
	return nil
}

func affected(res sql.Result, err error, what, key string) error {
	if err != nil {
		return fmt.Errorf("%s device %q: %w", what, key, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}
