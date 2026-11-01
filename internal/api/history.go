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
	days, err := s.streamDays(r.Context(), key, from, to, loc)
	if err != nil {
		s.internalError(w, "reading location history", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Days []routeDay `json:"days"`
	}{days})
}

// Caps on the geometry the routes endpoint keeps in memory and returns. Counts, distances, day
// grouping and trip breaks are computed from every matching row; only the geometry is bounded.
// Variables so tests can lower them.
var (
	routeSegmentBuffer = 50000 // points one open segment buffers before it is simplified in place
	routeDayVertices   = 20000 // geometry vertices kept per day
)

// dayBuilder groups time-ordered points by local calendar day, splits each day into segments
// at gaps over tripGap and simplifies the segment geometry, one point at a time.
type dayBuilder struct {
	loc  *time.Location
	days []routeDay
	seg  []histPoint // current segment: simplified in place whenever it outgrows routeSegmentBuffer
	cur  *routeDay
	prev histPoint // previous point of the current segment (exact distances)
}

func newDayBuilder(loc *time.Location) *dayBuilder {
	return &dayBuilder{loc: loc, days: []routeDay{}}
}

func (b *dayBuilder) flush() {
	if b.cur != nil && len(b.seg) > 1 {
		b.cur.Segments = append(b.cur.Segments, simplify(b.seg, simplifyToleranceM))
		if vertices(b.cur.Segments) > 2*routeDayVertices {
			thinDay(b.cur, routeDayVertices)
		}
	}
	b.seg = b.seg[:0]
}

func (b *dayBuilder) add(p histPoint) {
	date := time.Unix(p.T, 0).In(b.loc).Format("2006-01-02")
	if b.cur == nil || b.cur.Date != date {
		b.flush()
		if b.cur != nil {
			thinDay(b.cur, routeDayVertices)
		}
		b.days = append(b.days, routeDay{Date: date, First: p.T, Segments: [][][3]float64{}})
		b.cur = &b.days[len(b.days)-1]
	} else if len(b.seg) > 0 && time.Duration(p.T-b.prev.T)*time.Second > tripGap {
		b.flush()
	}
	if len(b.seg) > 0 {
		b.cur.DistanceM += geo.Haversine(b.prev.Lat, b.prev.Lon, p.Lat, p.Lon)
	}
	b.seg = append(b.seg, p)
	b.prev = p
	if len(b.seg) >= routeSegmentBuffer {
		b.compact()
	}
	b.cur.PointCount++
	b.cur.Last = p.T
}

// compact simplifies the open segment in place, keeping its endpoints, and stride-thins it if the
// simplification alone did not make room.
func (b *dayBuilder) compact() {
	kept := simplify(b.seg, simplifyToleranceM)
	if len(kept) > routeSegmentBuffer/2 {
		kept = strideThin(kept, routeSegmentBuffer/2)
	}
	b.seg = b.seg[:0]
	for _, k := range kept {
		b.seg = append(b.seg, histPoint{T: int64(k[2]), Lat: k[0], Lon: k[1]})
	}
}

func (b *dayBuilder) finish() []routeDay {
	b.flush()
	if b.cur != nil {
		thinDay(b.cur, routeDayVertices)
	}
	for i := range b.days {
		b.days[i].DistanceM = math.Round(b.days[i].DistanceM*10) / 10
	}
	return b.days
}

func vertices(segs [][][3]float64) (n int) {
	for _, s := range segs {
		n += len(s)
	}
	return n
}

// strideThin picks n evenly spaced vertices, always including the first and last.
func strideThin(v [][3]float64, n int) [][3]float64 {
	if len(v) <= n || n < 2 {
		return v
	}
	out := make([][3]float64, n)
	for i := range out {
		out[i] = v[int(int64(i)*int64(len(v)-1)/int64(n-1))]
	}
	return out
}

// thinDay stride-thins the day's segments, in proportion to their size, to at most budget
// vertices in all, keeping every segment's endpoints (so each segment keeps at least two).
func thinDay(d *routeDay, budget int) {
	total := vertices(d.Segments)
	if total <= budget {
		return
	}
	for i, s := range d.Segments {
		d.Segments[i] = strideThin(s, max(2, int(int64(len(s))*int64(budget)/int64(total))))
	}
}

// buildDays groups time-ordered points by local calendar day (see dayBuilder).
func buildDays(pts []histPoint, loc *time.Location) []routeDay {
	b := newDayBuilder(loc)
	for _, p := range pts {
		b.add(p)
	}
	return b.finish()
}

// streamDays is buildDays over every matching row, read through a cursor so memory stays bounded
// whatever the density of the range.
func (s *server) streamDays(ctx context.Context, key string, from, to time.Time, loc *time.Location) ([]routeDay, error) {
	b := newDayBuilder(loc)
	if s.History == nil {
		return b.finish(), nil
	}
	rows, err := s.History.QueryContext(ctx,
		`SELECT ts, lat, lon FROM location_points WHERE device_key = ? AND ts >= ? AND ts <= ? ORDER BY ts`,
		key, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p histPoint
		if err := rows.Scan(&p.T, &p.Lat, &p.Lon); err != nil {
			return nil, err
		}
		b.add(p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return b.finish(), nil
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
