package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/cic/internal/audit"
	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/geocode"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

// Error codes of the settings routes (besides those in commands.go).
const (
	codeUserExists       = "user_exists"
	codeForbiddenChange  = "forbidden_change"
	codeNotFound         = "not_found"
	codeBusy             = "busy"
	maxFullNameRunes     = 80
	settingsWritesPerMin = 30
	statusOK             = "ok"
)

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// deviceUpdate is the device.updated event of a profile change. It serializes exactly as
// devices.Device; becameHidden only steers the viewer streams.
type deviceUpdate struct {
	devices.Device
	becameHidden bool
}

// removedEvent is the data of device.removed.
type removedEvent struct {
	Key    string `json:"key"`
	hidden bool
}

// keyedMutex hands out one mutex per key; entries are dropped once nobody holds or waits.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*keyedEntry
}

type keyedEntry struct {
	sync.Mutex
	refs int
}

// lock blocks until key is free and returns the function that releases it.
func (k *keyedMutex) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedEntry{}
	}
	e := k.m[key]
	if e == nil {
		e = &keyedEntry{}
		k.m[key] = e
	}
	e.refs++
	k.mu.Unlock()
	e.Lock()
	return func() {
		e.Unlock()
		k.mu.Lock()
		if e.refs--; e.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// writeLimiter allows each admin settingsWritesPerMin writes per minute (sliding window).
type writeLimiter struct {
	mu   sync.Mutex
	now  func() time.Time
	hits map[int64][]time.Time
}

func newWriteLimiter(now func() time.Time) *writeLimiter {
	return &writeLimiter{now: now, hits: map[int64][]time.Time{}}
}

// allow records a write by user and reports whether it is within the limit; otherwise it
// returns the seconds until the oldest write leaves the window.
func (l *writeLimiter) allow(user int64) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cut := now.Add(-time.Minute)
	h := slices.DeleteFunc(l.hits[user], func(t time.Time) bool { return !t.After(cut) })
	if len(h) >= settingsWritesPerMin {
		l.hits[user] = h
		return false, max(1, int(h[0].Add(time.Minute).Sub(now).Seconds()+1))
	}
	l.hits[user] = append(h, now)
	if len(l.hits) > 1024 { // forget admins that have been quiet
		for id, hs := range l.hits {
			if len(hs) == 0 || !hs[len(hs)-1].After(cut) {
				delete(l.hits, id)
			}
		}
	}
	return true, 0
}

func (s *server) limitWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := auth.UserFrom(r.Context())
		if ok, retry := s.writes.allow(u.ID); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeCmdError(w, http.StatusTooManyRequests, codeRateLimited, "too many settings changes, try again in a minute")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) settingsRoutes(r chi.Router) {
	r.Get("/users", s.listUsers)
	r.Get("/geocode/usage", s.geocodeUsage)
	r.Get("/places", s.listPlaces)
	r.Group(func(r chi.Router) {
		r.Use(s.limitWrites)
		r.Patch("/devices/{prefix}/{device}", s.patchDevice)
		r.Delete("/devices/{prefix}/{device}", s.deleteDevice)
		r.Post("/places", s.createPlace)
		r.Patch("/places/{id}", s.patchPlace)
		r.Delete("/places/{id}", s.deletePlace)
		r.Post("/users", s.createUser)
		r.Patch("/users/{id}", s.patchUser)
		r.Post("/users/{id}/reset-password", s.resetPassword)
	})
}

// audited writes the intent row of a settings change. ok=false means the audit log could
// not be written: the change must not happen. done sets the final status.
func (s *server) audited(actor, device, action, detail string) (done func(status string), ok bool) {
	id, ok := s.begin(audit.Entry{User: actor, Device: device, Action: action, Detail: detail})
	if !ok {
		return nil, false
	}
	return func(status string) { s.finish(id, action, status, "") }, true
}

// busy answers 503 when the shared argon2 budget stayed exhausted: nothing was changed, and
// the audit row says so without counting a failure.
func busy(w http.ResponseWriter, done func(string)) {
	done("rejected:" + codeBusy)
	w.Header().Set("Retry-After", "5")
	writeCmdError(w, http.StatusServiceUnavailable, codeBusy, codeBusy)
}

// noStorePassword marks a response that carries a one-time password as uncacheable, whatever
// writeJSON does.
func noStorePassword(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (s *server) internalError(w http.ResponseWriter, what string, err error) {
	s.log().Error(what, "err", err)
	writeCmdError(w, http.StatusInternalServerError, codeUnavailable, "internal error")
}

// geocodeUsage reports today's lookups and quota; it carries no key or coordinates.
func (s *server) geocodeUsage(w http.ResponseWriter, r *http.Request) {
	if s.Geocode == nil {
		writeJSON(w, http.StatusOK, geocode.Usage{})
		return
	}
	u, err := s.Geocode.Usage(r.Context())
	if err != nil {
		s.internalError(w, "reading geocode usage", err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// ---- devices ----

func deviceKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	prefix, dev := chi.URLParam(r, "prefix"), chi.URLParam(r, "device")
	if !mqtt.ValidSegment(prefix) || !mqtt.ValidSegment(dev) {
		writeCmdError(w, http.StatusBadRequest, codeInvalid, "invalid prefix or device")
		return "", false
	}
	return prefix + "/" + dev, true
}

// loadDevice answers 404/500 itself.
func (s *server) loadDevice(w http.ResponseWriter, r *http.Request, key string) (devices.Device, bool) {
	d, err := s.Devices.Get(r.Context(), key)
	switch {
	case err == nil:
		return d, true
	case errors.Is(err, store.ErrNotFound):
		writeCmdError(w, http.StatusNotFound, codeUnknownDevice, "unknown device")
	default:
		s.internalError(w, "reading device", err)
	}
	return devices.Device{}, false
}

// invisibleRune reports whether r draws nothing: spaces, controls, format characters,
// line/paragraph separators and the fillers that render blank (Hangul and Braille).
func invisibleRune(r rune) bool {
	switch r {
	case 0x3164, 0x2800, 0x115F, 0x1160, 0xFFA0:
		return true
	}
	return unicode.IsSpace(r) || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zs, unicode.Zl, unicode.Zp)
}

// rejectedNameRune reports runes that may never appear in a full name.
func rejectedNameRune(r rune) bool {
	return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp)
}

// cleanFullName trims name, collapses runs of whitespace to one space and reports whether the
// result is acceptable: no control/format/separator characters, at most maxFullNameRunes
// runes, and either empty (reset to default) or containing a visible character.
func cleanFullName(name string) (string, bool) {
	for _, r := range name {
		if rejectedNameRune(r) {
			return "", false
		}
	}
	name = strings.Join(strings.FieldsFunc(name, unicode.IsSpace), " ")
	if name == "" {
		return "", true
	}
	if utf8.RuneCountInString(name) > maxFullNameRunes || !strings.ContainsFunc(name, func(r rune) bool { return !invisibleRune(r) }) {
		return "", false
	}
	return name, true
}

func (s *server) patchDevice(w http.ResponseWriter, r *http.Request) {
	key, ok := deviceKey(w, r)
	if !ok {
		return
	}
	var body struct {
		FullName *string `json:"full_name"`
		Color    *string `json:"color"`
		Hidden   *bool   `json:"hidden"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	patch := devices.ProfilePatch{Color: body.Color, Hidden: body.Hidden}
	if body.FullName != nil {
		name, ok := cleanFullName(*body.FullName)
		if !ok {
			writeCmdError(w, http.StatusBadRequest, codeInvalid, "full_name must be at most 80 characters, visible, and free of control characters")
			return
		}
		patch.FullName = &name
	}
	if body.Color != nil && *body.Color != "" && !colorRe.MatchString(*body.Color) {
		writeCmdError(w, http.StatusBadRequest, codeInvalid, "color must be empty or #rrggbb")
		return
	}
	// One writer per device from the read below to the publish, so events leave in DB order.
	defer s.devLocks.lock(key)()
	cur, ok := s.loadDevice(w, r, key)
	if !ok {
		return
	}
	// Changed fields: the stored name is "" for the default, which reads back as the device segment.
	var changed []string
	if patch.FullName != nil && *patch.FullName != cur.FullName && !(*patch.FullName == "" && cur.FullName == cur.Device) {
		changed = append(changed, "full_name")
	}
	if patch.Color != nil && *patch.Color != cur.Color {
		changed = append(changed, "color")
	}
	if patch.Hidden != nil && *patch.Hidden != cur.Hidden {
		changed = append(changed, "hidden")
	}
	if len(changed) == 0 {
		writeJSON(w, http.StatusOK, cur)
		return
	}
	u, _ := auth.UserFrom(r.Context())
	done, ok := s.audited(u.Username, key, "device_update", "fields="+strings.Join(changed, ","))
	if !ok {
		writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable; nothing was changed")
		return
	}
	d, wasHidden, err := s.Devices.UpdateProfile(r.Context(), key, patch)
	if errors.Is(err, store.ErrNotFound) {
		done("rejected:" + codeUnknownDevice)
		writeCmdError(w, http.StatusNotFound, codeUnknownDevice, "unknown device")
		return
	}
	if err != nil {
		done("failed")
		s.internalError(w, "updating device", err)
		return
	}
	done(statusOK)
	s.Hub.Publish(events.TypeDeviceUpdated, deviceUpdate{Device: d, becameHidden: d.Hidden && !wasHidden})
	writeJSON(w, http.StatusOK, d)
}

func (s *server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	key, ok := deviceKey(w, r)
	if !ok {
		return
	}
	defer s.devLocks.lock(key)() // the removal hook publishes under this lock
	if _, ok := s.loadDevice(w, r, key); !ok {
		return
	}
	u, _ := auth.UserFrom(r.Context())
	done, ok := s.audited(u.Username, key, "device_delete", "")
	if !ok {
		writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable; nothing was changed")
		return
	}
	// The device.removed event comes from the registry's OnRemove hook (see StartFeeds).
	err := s.Devices.Delete(r.Context(), key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		done("rejected:" + codeUnknownDevice)
		writeCmdError(w, http.StatusNotFound, codeUnknownDevice, "unknown device")
	case err != nil:
		done("failed")
		s.internalError(w, "deleting device", err)
	default:
		done(statusOK)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- users ----

type userItem struct {
	ID                 int64      `json:"id"`
	Username           string     `json:"username"`
	Role               auth.Role  `json:"role"`
	Disabled           bool       `json:"disabled"`
	MustChangePassword bool       `json:"must_change_password"`
	CreatedAt          time.Time  `json:"created_at"`
	LastLogin          *time.Time `json:"last_login"`
}

func itemOf(u auth.User) userItem {
	return userItem{ID: u.ID, Username: u.Username, Role: u.Role, Disabled: u.Disabled,
		MustChangePassword: u.MustChangePassword, CreatedAt: u.CreatedAt, LastLogin: u.LastLogin}
}

func userID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 || raw[0] == '+' {
		writeCmdError(w, http.StatusBadRequest, codeInvalid, "id must be a positive integer")
		return 0, false
	}
	return id, true
}

func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.Auth.Store().ListUsers(r.Context())
	if err != nil {
		s.internalError(w, "listing users", err)
		return
	}
	out := make([]userItem, 0, len(us))
	for _, u := range us {
		out = append(out, itemOf(u))
	}
	slices.SortStableFunc(out, func(a, b userItem) int {
		return strings.Compare(strings.ToLower(a.Username), strings.ToLower(b.Username))
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string  `json:"username"`
		Role     string  `json:"role"`
		Password *string `json:"password"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	role := auth.Role(body.Role)
	switch {
	case !auth.ValidUsername(body.Username):
		writeCmdError(w, http.StatusBadRequest, codeInvalid, auth.ErrBadUsername.Error())
		return
	case !role.Valid():
		writeCmdError(w, http.StatusBadRequest, codeInvalid, auth.ErrBadRole.Error())
		return
	}
	var pw string
	var initial *string
	if body.Password != nil {
		pw = *body.Password
		if err := auth.ValidatePassword(pw, body.Username); err != nil {
			writeCmdError(w, http.StatusBadRequest, codeInvalid, err.Error())
			return
		}
	} else {
		var err error
		if pw, err = auth.GeneratePassword(); err != nil {
			s.internalError(w, "generating password", err)
			return
		}
		initial = &pw
	}
	actor, _ := auth.UserFrom(r.Context())
	done, ok := s.audited(actor.Username, "-", "user_create", "user="+body.Username)
	if !ok {
		writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable; nothing was changed")
		return
	}
	u, err := s.Auth.Store().CreateUser(r.Context(), body.Username, pw, role, true)
	switch {
	case errors.Is(err, auth.ErrBusy):
		busy(w, done)
	case errors.Is(err, auth.ErrUserExists):
		done("rejected:" + codeUserExists)
		writeCmdError(w, http.StatusConflict, codeUserExists, "a user with this name already exists")
	case err != nil:
		done("failed")
		s.internalError(w, "creating user", err)
	default:
		done(statusOK)
		noStorePassword(w)
		writeJSON(w, http.StatusCreated, struct {
			User            userItem `json:"user"`
			InitialPassword *string  `json:"initial_password"`
		}{itemOf(u), initial})
	}
}

func (s *server) resetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	st := s.Auth.Store()
	target, err := st.UserByID(r.Context(), id)
	if errors.Is(err, auth.ErrUserMissing) {
		writeCmdError(w, http.StatusNotFound, codeNotFound, "user not found")
		return
	}
	if err != nil {
		s.internalError(w, "reading user", err)
		return
	}
	pw, err := auth.GeneratePassword()
	if err != nil {
		s.internalError(w, "generating password", err)
		return
	}
	actor, _ := auth.UserFrom(r.Context())
	done, ok := s.audited(actor.Username, "-", "user_reset_password", "user="+target.Username)
	if !ok {
		writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable; nothing was changed")
		return
	}
	// SetPassword also drops every session of the user, which ends their event streams.
	switch err := st.SetPassword(r.Context(), id, target.Username, pw, true); {
	case errors.Is(err, auth.ErrBusy):
		busy(w, done)
	case errors.Is(err, auth.ErrUserMissing):
		done("rejected:" + codeNotFound)
		writeCmdError(w, http.StatusNotFound, codeNotFound, "user not found")
	case err != nil:
		done("failed")
		s.internalError(w, "resetting password", err)
	default:
		done(statusOK)
		noStorePassword(w)
		writeJSON(w, http.StatusOK, struct {
			InitialPassword string `json:"initial_password"`
		}{pw})
	}
}

func (s *server) patchUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	var body struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	var role *auth.Role
	if body.Role != nil {
		rr := auth.Role(*body.Role)
		if !rr.Valid() {
			writeCmdError(w, http.StatusBadRequest, codeInvalid, auth.ErrBadRole.Error())
			return
		}
		role = &rr
	}
	actor, _ := auth.UserFrom(r.Context())
	if actor.ID == id && ((body.Disabled != nil && *body.Disabled) || (role != nil && *role != auth.RoleAdmin)) {
		writeCmdError(w, http.StatusConflict, codeForbiddenChange, "you cannot disable or demote your own account")
		return
	}
	st := s.Auth.Store()
	cur, err := st.UserByID(r.Context(), id)
	if errors.Is(err, auth.ErrUserMissing) {
		writeCmdError(w, http.StatusNotFound, codeNotFound, "user not found")
		return
	}
	if err != nil {
		s.internalError(w, "reading user", err)
		return
	}
	var changed []string
	if role != nil && *role != cur.Role {
		changed = append(changed, "role")
	}
	if body.Disabled != nil && *body.Disabled != cur.Disabled {
		changed = append(changed, "disabled")
	}
	if len(changed) == 0 {
		writeJSON(w, http.StatusOK, itemOf(cur))
		return
	}
	done, ok := s.audited(actor.Username, "-", "user_update", "user="+cur.Username+" fields="+strings.Join(changed, ","))
	if !ok {
		writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable; nothing was changed")
		return
	}
	u, err := st.UpdateUser(r.Context(), id, role, body.Disabled)
	switch {
	case errors.Is(err, auth.ErrLastAdmin):
		done("rejected:" + codeForbiddenChange)
		writeCmdError(w, http.StatusConflict, codeForbiddenChange, "the last active admin cannot be disabled or demoted")
	case errors.Is(err, auth.ErrUserMissing):
		done("rejected:" + codeNotFound)
		writeCmdError(w, http.StatusNotFound, codeNotFound, "user not found")
	case err != nil:
		done("failed")
		s.internalError(w, "updating user", err)
	default:
		done(statusOK)
		writeJSON(w, http.StatusOK, itemOf(u))
	}
}
