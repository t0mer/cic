package api

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/geo"
)

// Limits of the location history routes.
const (
	historyDefaultRange   = 24 * time.Hour
	historyMaxRange       = 31 * 24 * time.Hour
	historyDefaultLimit   = 5000
	historyMaxLimit       = 20000
	historyReadsPerMin    = 120
	tripGap               = 30 * time.Minute // a longer pause between fixes starts a new segment
	simplifyToleranceM    = 10.0
	historyDefaultMaxRows = 200000
)

// historyMaxRows caps the rows one request reads into memory; a larger match is thinned in
// SQL to an even sample. A variable so tests can lower it.
var historyMaxRows = historyDefaultMaxRows

// historyNow is the clock of the default time range.
var historyNow = time.Now

// historyRoutes registers the location history reads. They are admin-only (the caller is in
// the admin group) and write no audit rows. Hidden devices are readable: administrators see
// hidden devices in the device list as well.
func (s *server) historyRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.limitReads)
		r.Get("/devices/{prefix}/{device}/history", s.deviceHistory)
		r.Get("/devices/{prefix}/{device}/routes", s.deviceRoutes)
	})
}

func (s *server) limitReads(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := auth.UserFrom(r.Context())
		if ok, retry := s.reads.allow(u.ID); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeCmdError(w, http.StatusTooManyRequests, codeRateLimited, "too many requests, try again in a minute")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type histPoint struct {
	T        int64    `json:"t"`
	Lat      float64  `json:"lat"`
	Lon      float64  `json:"lon"`
	Accuracy *float64 `json:"accuracy"`
}

// parseRange reads from/to (RFC3339; default the last 24 h) and answers 400 itself.
func parseRange(w http.ResponseWriter, r *http.Request) (from, to time.Time, ok bool) {
	q := r.URL.Query()
	bad := func(msg string) (time.Time, time.Time, bool) {
		writeCmdError(w, http.StatusBadRequest, codeInvalid, msg)
		return time.Time{}, time.Time{}, false
	}
	to = historyNow()
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return bad("to must be an RFC3339 time")
		}
		to = t
	}
	from = to.Add(-historyDefaultRange)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return bad("from must be an RFC3339 time")
		}
		from = t
	}
	if !from.Before(to) {
		return bad("from must be before to")
	}
	if to.Sub(from) > historyMaxRange {
		return bad("the range must not exceed 31 days")
	}
	return from, to, true
}

// readPoints returns the device's points in [from,to] ordered by time, at most
// historyMaxRows of them: a larger match is thinned to an even sample (first and last kept).
func (s *server) readPoints(ctx context.Context, key string, from, to time.Time) (pts []histPoint, thinned bool, err error) {
	out := []histPoint{}
	if s.History == nil {
		return out, false, nil
	}
	f, t := from.Unix(), to.Unix()
	var n int
	if err := s.History.QueryRowContext(ctx,
		`SELECT count(*) FROM location_points WHERE device_key = ? AND ts >= ? AND ts <= ?`, key, f, t).Scan(&n); err != nil {
		return nil, false, err
	}
	stride := 1
	if n > historyMaxRows {
		stride = (n + historyMaxRows - 1) / historyMaxRows
	}
	q := `SELECT ts, lat, lon, accuracy FROM location_points WHERE device_key = ? AND ts >= ? AND ts <= ? ORDER BY ts LIMIT ?`
	args := []any{key, f, t, historyMaxRows + 1}
	if stride > 1 {
		q = `SELECT ts, lat, lon, accuracy FROM (
  SELECT ts, lat, lon, accuracy, row_number() OVER (ORDER BY ts) AS rn
  FROM location_points WHERE device_key = ? AND ts >= ? AND ts <= ?)
 WHERE (rn - 1) % ? = 0 OR rn = ? ORDER BY ts LIMIT ?`
		args = []any{key, f, t, stride, n, historyMaxRows + 1}
	}
	rows, err := s.History.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var p histPoint
		var acc *float64
		if err := rows.Scan(&p.T, &p.Lat, &p.Lon, &acc); err != nil {
			return nil, false, err
		}
		p.Accuracy = acc
		out = append(out, p)
	}
	return out, stride > 1, rows.Err()
}

// downsample picks limit evenly spaced points, always including the first and last.
func downsample(pts []histPoint, limit int) []histPoint {
	n := len(pts)
	if n <= limit || limit < 2 {
		return pts
	}
	out := make([]histPoint, limit)
	for i := range out {
		out[i] = pts[int(int64(i)*int64(n-1)/int64(limit-1))]
	}
	return out
}

func (s *server) deviceHistory(w http.ResponseWriter, r *http.Request) {
	key, ok := deviceKey(w, r)
	if !ok {
		return
	}
	from, to, ok := parseRange(w, r)
	if !ok {
		return
	}
	limit := historyDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 2 {
			writeCmdError(w, http.StatusBadRequest, codeInvalid, "limit must be an integer of at least 2")
			return
		}
		limit = min(n, historyMaxLimit)
	}
	if _, ok := s.loadDevice(w, r, key); !ok {
		return
	}
	pts, thinned, err := s.readPoints(r.Context(), key, from, to)
	if err != nil {
		s.internalError(w, "reading location history", err)
		return
	}
	truncated := thinned || len(pts) > limit
	writeJSON(w, http.StatusOK, struct {
		Points    []histPoint `json:"points"`
		Truncated bool        `json:"truncated"`
	}{downsample(pts, limit), truncated})
}

type routeDay struct {
	Date       string         `json:"date"`
	DistanceM  float64        `json:"distance_m"`
	PointCount int            `json:"point_count"`
	First      int64          `json:"first"`
	Last       int64          `json:"last"`
	Segments   [][][3]float64 `json:"segments"`
}

func (s *server) deviceRoutes(w http.ResponseWriter, r *http.Request) {
	key, ok := deviceKey(w, r)
	if !ok {
		return
	}
	from, to, ok := parseRange(w, r)
	if !ok {
		return
	}
	loc := time.UTC
	if v := r.URL.Query().Get("tz"); v != "" {
		l, err := time.LoadLocation(v)
		if err != nil || v == "Local" {
			writeCmdError(w, http.StatusBadRequest, codeInvalid, "tz must be an IANA time zone name")
			return
		}
		loc = l
	}
	if _, ok := s.loadDevice(w, r, key); !ok {
		return
	}
	pts, _, err := s.readPoints(r.Context(), key, from, to)
	if err != nil {
		s.internalError(w, "reading location history", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Days []routeDay `json:"days"`
	}{buildDays(pts, loc)})
}

// buildDays groups time-ordered points by local calendar day, splits each day into segments
// at gaps over tripGap and simplifies the segment geometry.
func buildDays(pts []histPoint, loc *time.Location) []routeDay {
	days := []routeDay{}
	var seg []histPoint // current segment, unsimplified
	var cur *routeDay
	flush := func() {
		if cur == nil || len(seg) == 0 {
			seg = seg[:0]
			return
		}
		for i := 1; i < len(seg); i++ {
			cur.DistanceM += geo.Haversine(seg[i-1].Lat, seg[i-1].Lon, seg[i].Lat, seg[i].Lon)
		}
		if len(seg) > 1 {
			cur.Segments = append(cur.Segments, simplify(seg, simplifyToleranceM))
		}
		seg = seg[:0]
	}
	for _, p := range pts {
		date := time.Unix(p.T, 0).In(loc).Format("2006-01-02")
		if cur == nil || cur.Date != date {
			flush()
			days = append(days, routeDay{Date: date, First: p.T, Segments: [][][3]float64{}})
			cur = &days[len(days)-1]
		} else if len(seg) > 0 && time.Duration(p.T-seg[len(seg)-1].T)*time.Second > tripGap {
			flush()
		}
		seg = append(seg, p)
		cur.PointCount++
		cur.Last = p.T
	}
	flush()
	for i := range days {
		days[i].DistanceM = math.Round(days[i].DistanceM*10) / 10
	}
	return days
}

// simplify applies Douglas-Peucker with tolerance tol (metres), keeping both endpoints, and
// returns [lat, lon, t] triples.
func simplify(seg []histPoint, tol float64) [][3]float64 {
	n := len(seg)
	// Equirectangular projection around the first point: accurate to well under tol for a trip.
	lat0 := seg[0].Lat
	kx := math.Cos(lat0*math.Pi/180) * 111320
	const ky = 110574.0
	xs, ys := make([]float64, n), make([]float64, n)
	for i, p := range seg {
		xs[i], ys[i] = (p.Lon-seg[0].Lon)*kx, (p.Lat-lat0)*ky
	}
	keep := make([]bool, n)
	keep[0], keep[n-1] = true, true
	stack := [][2]int{{0, n - 1}}
	for len(stack) > 0 {
		a, b := stack[len(stack)-1][0], stack[len(stack)-1][1]
		stack = stack[:len(stack)-1]
		idx, maxD := -1, tol
		for i := a + 1; i < b; i++ {
			if d := segDist(xs[i], ys[i], xs[a], ys[a], xs[b], ys[b]); d > maxD {
				idx, maxD = i, d
			}
		}
		if idx >= 0 {
			keep[idx] = true
			stack = append(stack, [2]int{a, idx}, [2]int{idx, b})
		}
	}
	out := make([][3]float64, 0, n)
	for i, k := range keep {
		if k {
			out = append(out, [3]float64{seg[i].Lat, seg[i].Lon, float64(seg[i].T)})
		}
	}
	return out
}

// segDist is the distance from (px,py) to the segment (ax,ay)-(bx,by).
func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/l2))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}
