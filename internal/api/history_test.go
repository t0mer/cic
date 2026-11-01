package api

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/geo"
)

func histEnv(t *testing.T) *cmdEnv {
	t.Helper()
	return newCmdEnvWith(t, func(d *Deps, _ *cmdEnv) { d.History = testDB })
}

func insertPts(t *testing.T, key string, pts ...[4]float64) { // ts, lat, lon, accuracy(<0 = NULL)
	t.Helper()
	for _, p := range pts {
		var acc any
		if p[3] >= 0 {
			acc = p[3]
		}
		if _, err := testDB.Exec(`INSERT INTO location_points(device_key, ts, lat, lon, accuracy) VALUES (?,?,?,?,?)`,
			key, int64(p[0]), p[1], p[2], acc); err != nil {
			t.Fatal(err)
		}
	}
}

func rfc(ts int64) string { return time.Unix(ts, 0).UTC().Format(time.RFC3339) }

func getHist(e *cmdEnv, who, path string) *httptest.ResponseRecorder {
	return e.send("GET", path, "", who, false)
}

type histResp struct {
	Points []struct {
		T        int64    `json:"t"`
		Lat      float64  `json:"lat"`
		Lon      float64  `json:"lon"`
		Accuracy *float64 `json:"accuracy"`
	} `json:"points"`
	Truncated bool `json:"truncated"`
}

func decodeHist(t *testing.T, rec *httptest.ResponseRecorder) histResp {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var h histResp
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	return h
}

type routesResp struct {
	Days []struct {
		Date       string         `json:"date"`
		DistanceM  float64        `json:"distance_m"`
		PointCount int            `json:"point_count"`
		First      int64          `json:"first"`
		Last       int64          `json:"last"`
		Segments   [][][3]float64 `json:"segments"`
	} `json:"days"`
}

func decodeRoutes(t *testing.T, rec *httptest.ResponseRecorder) routesResp {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var r routesResp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHistoryAccessControl(t *testing.T) {
	e := histEnv(t)
	for _, p := range []string{"/api/devices/dradis/a/history", "/api/devices/dradis/a/routes"} {
		if rec := getHist(e, "", p); rec.Code != 401 {
			t.Errorf("%s anonymous: %d", p, rec.Code)
		}
		if rec := getHist(e, "viewer", p); rec.Code != 403 {
			t.Errorf("%s viewer: %d", p, rec.Code)
		}
		if rec := getHist(e, "admin", p); rec.Code != 200 {
			t.Errorf("%s admin: %d %s", p, rec.Code, rec.Body)
		}
		if rec := getHist(e, "admin", p[:len("/api/devices/dradis/")]+"nope"+p[len("/api/devices/dradis/a"):]); rec.Code != 404 {
			t.Errorf("%s unknown device: %d", p, rec.Code)
		}
		if rec := getHist(e, "admin", p[:len("/api/devices/")]+"bad%20prefix"+p[len("/api/devices/dradis"):]); rec.Code != 400 && rec.Code != 404 {
			t.Errorf("%s bad prefix: %d", p, rec.Code)
		}
	}
	// A hidden device stays readable for administrators.
	insertPts(t, "dradis/h", [4]float64{1700000000, 32, 34, -1})
	h := decodeHist(t, getHist(e, "admin", "/api/devices/dradis/h/history?from="+rfc(1699999000)+"&to="+rfc(1700001000)))
	if len(h.Points) != 1 {
		t.Errorf("hidden device points: %+v", h)
	}
	// Reads leave no audit rows.
	var n int
	if err := testDB.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&n); err != nil || n != 0 {
		t.Errorf("audit rows %d err %v", n, err)
	}
}

func TestHistoryValidation(t *testing.T) {
	e := histEnv(t)
	base := "/api/devices/dradis/a/"
	from, to := rfc(1700000000), rfc(1700003600)
	bad := []string{
		"history?from=yesterday",
		"history?to=x",
		"history?from=" + to + "&to=" + from,
		"history?from=" + from + "&to=" + from,
		"history?from=" + rfc(1700000000-32*86400) + "&to=" + from,
		"history?from=" + from + "&to=" + to + "&limit=abc",
		"history?from=" + from + "&to=" + to + "&limit=1",
		"history?from=" + from + "&to=" + to + "&limit=0",
		"routes?from=" + to + "&to=" + from,
		"routes?from=" + from + "&to=" + to + "&tz=Not/AZone",
		"routes?from=" + from + "&to=" + to + "&tz=Local",
	}
	for _, p := range bad {
		rec := getHist(e, "admin", base+p)
		var body struct{ Error, Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != 400 || body.Code != "invalid" || body.Error == "" {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
	// Exactly 31 days is allowed; limit above the max is clamped.
	ok := base + "history?from=" + rfc(1700000000-31*86400) + "&to=" + from + "&limit=99999"
	if rec := getHist(e, "admin", ok); rec.Code != 200 {
		t.Errorf("31 days: %d %s", rec.Code, rec.Body)
	}
	if rec := getHist(e, "admin", base+"routes?from="+from+"&to="+to+"&tz=Asia/Jerusalem"); rec.Code != 200 {
		t.Errorf("tz: %d", rec.Code)
	}
}

func TestHistoryDefaultsLast24h(t *testing.T) {
	now := time.Unix(1700100000, 0)
	historyNow = func() time.Time { return now }
	t.Cleanup(func() { historyNow = time.Now })
	e := histEnv(t)
	insertPts(t, "dradis/a",
		[4]float64{float64(now.Unix() - 25*3600), 1, 1, -1}, // too old
		[4]float64{float64(now.Unix() - 23*3600), 2, 2, 5},
		[4]float64{float64(now.Unix() - 10), 3, 3, -1})
	h := decodeHist(t, getHist(e, "admin", "/api/devices/dradis/a/history"))
	if len(h.Points) != 2 || h.Truncated || h.Points[0].Lat != 2 || h.Points[1].Lat != 3 {
		t.Fatalf("%+v", h)
	}
	if h.Points[0].Accuracy == nil || *h.Points[0].Accuracy != 5 || h.Points[1].Accuracy != nil {
		t.Errorf("accuracy: %+v", h.Points)
	}
	// An empty result is [] rather than null.
	rec := getHist(e, "admin", "/api/devices/dradis/b/history")
	if got := rec.Body.String(); got != `{"points":[],"truncated":false}`+"\n" && got != `{"points":[],"truncated":false}` {
		t.Errorf("empty body %q", got)
	}
}

func TestHistoryOtherDevicesExcluded(t *testing.T) {
	e := histEnv(t)
	insertPts(t, "dradis/a", [4]float64{1700000100, 1, 1, -1})
	insertPts(t, "dradis/b", [4]float64{1700000100, 9, 9, -1})
	h := decodeHist(t, getHist(e, "admin", "/api/devices/dradis/a/history?from="+rfc(1700000000)+"&to="+rfc(1700001000)))
	if len(h.Points) != 1 || h.Points[0].Lat != 1 {
		t.Fatalf("%+v", h)
	}
}

func TestHistoryDownsampleKeepsEnds(t *testing.T) {
	e := histEnv(t)
	for i := 0; i < 1000; i++ {
		insertPts(t, "dradis/a", [4]float64{float64(1700000000 + i*10), 32 + float64(i)*1e-5, 34, -1})
	}
	q := "/api/devices/dradis/a/history?from=" + rfc(1699999000) + "&to=" + rfc(1700020000)
	h := decodeHist(t, getHist(e, "admin", q+"&limit=100"))
	if len(h.Points) != 100 || !h.Truncated {
		t.Fatalf("n=%d truncated=%v", len(h.Points), h.Truncated)
	}
	if h.Points[0].T != 1700000000 || h.Points[99].T != 1700000000+9990 {
		t.Errorf("ends %d %d", h.Points[0].T, h.Points[99].T)
	}
	for i := 1; i < len(h.Points); i++ {
		if h.Points[i].T <= h.Points[i-1].T {
			t.Fatalf("not ascending at %d", i)
		}
	}
	h = decodeHist(t, getHist(e, "admin", q+"&limit=1000"))
	if len(h.Points) != 1000 || h.Truncated {
		t.Errorf("exact limit: n=%d truncated=%v", len(h.Points), h.Truncated)
	}
	// Default limit is 5000, so all 1000 return.
	if h = decodeHist(t, getHist(e, "admin", q)); len(h.Points) != 1000 {
		t.Errorf("default limit: %d", len(h.Points))
	}
}

func TestHistoryRowCap(t *testing.T) {
	historyMaxRows = 100
	t.Cleanup(func() { historyMaxRows = historyDefaultMaxRows })
	e := histEnv(t)
	for i := 0; i < 450; i++ {
		insertPts(t, "dradis/a", [4]float64{float64(1700000000 + i*10), 32, 34 + float64(i)*1e-5, -1})
	}
	h := decodeHist(t, getHist(e, "admin", "/api/devices/dradis/a/history?from="+rfc(1699999000)+"&to="+rfc(1700020000)+"&limit=20000"))
	if len(h.Points) > 101 || len(h.Points) < 90 || !h.Truncated {
		t.Fatalf("n=%d truncated=%v", len(h.Points), h.Truncated)
	}
	if h.Points[0].T != 1700000000 || h.Points[len(h.Points)-1].T != 1700000000+4490 {
		t.Errorf("ends %d %d", h.Points[0].T, h.Points[len(h.Points)-1].T)
	}
}

func TestHistoryUsesIndex(t *testing.T) {
	_ = histEnv(t)
	rows, err := testDB.Query(`EXPLAIN QUERY PLAN SELECT ts, lat, lon, accuracy FROM location_points WHERE device_key = ? AND ts >= ? AND ts <= ? ORDER BY ts LIMIT ?`, "k", 0, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !contains(plan, "idx_location_points_device_ts") || contains(plan, "TEMP B-TREE") || contains(plan, "SCAN location_points") {
		t.Errorf("plan:\n%s", plan)
	}
}

func contains(s, sub string) bool { return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestRoutesDaySplitJerusalem(t *testing.T) {
	e := histEnv(t)
	jer, _ := time.LoadLocation("Asia/Jerusalem")
	at := func(y int, m time.Month, d, h, mi int) float64 {
		return float64(time.Date(y, m, d, h, mi, 0, 0, jer).Unix())
	}
	// 2026-10-25 is the DST end (clocks go back at 02:00); 23:50 local and 00:10 local straddle midnight.
	insertPts(t, "dradis/a",
		[4]float64{at(2026, 10, 24, 23, 50), 32.0, 34.0, -1},
		[4]float64{at(2026, 10, 24, 23, 55), 32.001, 34.0, -1},
		[4]float64{at(2026, 10, 25, 0, 10), 32.002, 34.0, -1},
		[4]float64{at(2026, 10, 25, 0, 15), 32.003, 34.0, -1},
		[4]float64{at(2026, 10, 26, 0, 5), 32.004, 34.0, -1},
	)
	q := func(tz string) string {
		return "/api/devices/dradis/a/routes?from=" + url.QueryEscape(rfc(int64(at(2026, 10, 24, 0, 0)))) + "&to=" +
			url.QueryEscape(rfc(int64(at(2026, 10, 27, 0, 0)))) + tz
	}
	r := decodeRoutes(t, getHist(e, "admin", q("&tz=Asia/Jerusalem")))
	if len(r.Days) != 3 || r.Days[0].Date != "2026-10-24" || r.Days[1].Date != "2026-10-25" || r.Days[2].Date != "2026-10-26" {
		t.Fatalf("%+v", r.Days)
	}
	if r.Days[0].PointCount != 2 || r.Days[1].PointCount != 2 || r.Days[2].PointCount != 1 {
		t.Errorf("counts %+v", r.Days)
	}
	// Single-point day: counted, no geometry, no distance.
	if len(r.Days[2].Segments) != 0 || r.Days[2].DistanceM != 0 || r.Days[2].First != r.Days[2].Last {
		t.Errorf("single point day %+v", r.Days[2])
	}
	if r.Days[0].First != int64(at(2026, 10, 24, 23, 50)) || r.Days[0].Last != int64(at(2026, 10, 24, 23, 55)) {
		t.Errorf("first/last %+v", r.Days[0])
	}
	// In UTC (default) all of them land on other dates: 23:50+03 = 20:50Z on the 24th.
	u := decodeRoutes(t, getHist(e, "admin", q("")))
	if u.Days[0].Date != "2026-10-24" || len(u.Days) < 2 {
		t.Errorf("utc %+v", u.Days)
	}
	// Across the DST end the 25th is a 25 h day: a point at 23:30 local still belongs to it.
	insertPts(t, "dradis/b", [4]float64{at(2026, 10, 25, 0, 30), 1, 1, -1}, [4]float64{at(2026, 10, 25, 23, 30), 1.0001, 1, -1}, [4]float64{at(2026, 10, 26, 0, 30), 1.0002, 1, -1})
	r = decodeRoutes(t, getHist(e, "admin", "/api/devices/dradis/b/routes?from="+url.QueryEscape(rfc(int64(at(2026, 10, 24, 0, 0))))+"&to="+url.QueryEscape(rfc(int64(at(2026, 10, 27, 0, 0))))+"&tz=Asia/Jerusalem"))
	if len(r.Days) != 2 || r.Days[0].PointCount != 2 || r.Days[1].PointCount != 1 {
		t.Errorf("dst day %+v", r.Days)
	}
}

func TestRoutesTripSplitDistanceAndSimplify(t *testing.T) {
	e := histEnv(t)
	t0 := int64(1700006400 + 3600) // 2023-11-15 01:00Z, so no trip crosses a UTC midnight
	var pts [][4]float64
	// Trip 1: 21 collinear points heading north, 1 min apart (0.0005 deg lat ~ 55.6 m each).
	for i := 0; i < 21; i++ {
		pts = append(pts, [4]float64{float64(t0 + int64(i)*60), 32 + float64(i)*0.0005, 34, -1})
	}
	// 45 min gap, then trip 2: three points with a clear corner.
	t2 := t0 + 20*60 + 45*60
	pts = append(pts,
		[4]float64{float64(t2), 33, 34, -1},
		[4]float64{float64(t2 + 60), 33.01, 34, -1},
		[4]float64{float64(t2 + 120), 33.01, 34.01, -1})
	// Another gap, then a lone point (counted, no geometry).
	pts = append(pts, [4]float64{float64(t2 + 120 + 3600), 34, 34, -1})
	insertPts(t, "dradis/a", pts...)
	r := decodeRoutes(t, getHist(e, "admin", "/api/devices/dradis/a/routes?tz=UTC&from="+rfc(t0-100)+"&to="+rfc(t0+86000)))
	if len(r.Days) != 1 {
		t.Fatalf("days %+v", r.Days)
	}
	d := r.Days[0]
	if d.PointCount != 25 || len(d.Segments) != 2 {
		t.Fatalf("count=%d segs=%d", d.PointCount, len(d.Segments))
	}
	s1 := d.Segments[0]
	if len(s1) != 2 || s1[0][2] != float64(t0) || s1[1][2] != float64(t0+20*60) || s1[0][0] != 32 || s1[1][0] != 32.01 {
		t.Errorf("collinear segment not reduced to endpoints: %v", s1)
	}
	if len(d.Segments[1]) != 3 {
		t.Errorf("corner lost: %v", d.Segments[1])
	}
	want := geo.Haversine(32, 34, 32.01, 34) + geo.Haversine(33, 34, 33.01, 34) + geo.Haversine(33.01, 34, 33.01, 34.01)
	if math.Abs(d.DistanceM-want) > 0.2 || math.Abs(want-3156) > 30 {
		t.Errorf("distance %v want %v", d.DistanceM, want)
	}
}

func TestSimplifyUnit(t *testing.T) {
	var seg []histPoint
	for i := 0; i < 100; i++ {
		seg = append(seg, histPoint{T: int64(i), Lat: 32 + float64(i)*1e-4, Lon: 34})
	}
	out := simplify(seg, 10)
	if len(out) != 2 || out[0][2] != 0 || out[1][2] != 99 {
		t.Errorf("collinear: %v", out)
	}
	// Jitter under the tolerance (~5 m) is dropped, a 50 m spike is kept.
	seg[50].Lon += 5 / (111320 * math.Cos(32*math.Pi/180))
	if out = simplify(seg, 10); len(out) != 2 {
		t.Errorf("jitter kept: %v", out)
	}
	seg[50].Lon += 50 / (111320 * math.Cos(32*math.Pi/180))
	if out = simplify(seg, 10); len(out) < 3 || !hasT(out, 50) {
		t.Errorf("spike: %v", out)
	}
	if out = simplify(seg[:2], 10); len(out) != 2 {
		t.Errorf("two points: %v", out)
	}
}

func TestHistoryServedWhenRecordingDisabled(t *testing.T) {
	// The routes depend only on stored rows: with locations.enabled=false they still serve what
	// was recorded earlier, while ui-config reports history_enabled=false.
	e := newCmdEnvWith(t, func(d *Deps, _ *cmdEnv) { d.History = testDB; d.UI = UIConfig{HistoryEnabled: false} })
	insertPts(t, "dradis/a", [4]float64{1700000100, 1, 1, -1})
	h := decodeHist(t, getHist(e, "admin", "/api/devices/dradis/a/history?from="+rfc(1700000000)+"&to="+rfc(1700001000)))
	if len(h.Points) != 1 {
		t.Errorf("%+v", h)
	}
	var ui map[string]any
	if err := json.Unmarshal(getHist(e, "admin", "/api/ui-config").Body.Bytes(), &ui); err != nil || ui["history_enabled"] != false {
		t.Errorf("ui-config %v %v", ui, err)
	}
	e2 := newCmdEnvWith(t, func(d *Deps, _ *cmdEnv) { d.UI = UIConfig{HistoryEnabled: true} })
	if err := json.Unmarshal(getHist(e2, "viewer", "/api/ui-config").Body.Bytes(), &ui); err != nil || ui["history_enabled"] != true {
		t.Errorf("ui-config %v %v", ui, err)
	}
	// Without a database the routes answer empty results.
	if h = decodeHist(t, getHist(e2, "admin", "/api/devices/dradis/a/history")); len(h.Points) != 0 {
		t.Errorf("%+v", h)
	}
}

func TestHistoryRateLimited(t *testing.T) {
	e := histEnv(t)
	var last int
	for i := 0; i < historyReadsPerMin+1; i++ {
		last = getHist(e, "admin", "/api/devices/dradis/a/history").Code
	}
	if last != 429 {
		t.Errorf("last status %d", last)
	}
}

func hasT(p [][3]float64, t float64) bool {
	for _, v := range p {
		if v[2] == t {
			return true
		}
	}
	return false
}

// genPts inserts n zig-zag fixes 10 s apart starting at t0 straight in SQL.
func genPts(t *testing.T, key string, t0 int64, n int) {
	t.Helper()
	if _, err := testDB.Exec(`WITH RECURSIVE g(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM g WHERE i < ?)
		INSERT INTO location_points(device_key, ts, lat, lon) SELECT ?, ? + i*10, 32 + (i % 2) * 0.001, 34 + i * 1e-6 FROM g`,
		n-1, key, t0); err != nil {
		t.Fatal(err)
	}
}

func TestRoutesExactBeyondRowCap(t *testing.T) {
	historyMaxRows = 100
	t.Cleanup(func() { historyMaxRows = historyDefaultMaxRows })
	e := histEnv(t)
	const n = 450
	t0 := int64(1700006400 + 3600)
	genPts(t, "dradis/a", t0, n)
	var want float64
	for i := 1; i < n; i++ {
		want += geo.Haversine(32+float64((i-1)%2)*0.001, 34+float64(i-1)*1e-6, 32+float64(i%2)*0.001, 34+float64(i)*1e-6)
	}
	r := decodeRoutes(t, getHist(e, "admin", "/api/devices/dradis/a/routes?tz=UTC&from="+rfc(t0-100)+"&to="+rfc(t0+86000)))
	if len(r.Days) != 1 || r.Days[0].PointCount != n || len(r.Days[0].Segments) != 1 {
		t.Fatalf("%+v", r.Days)
	}
	if math.Abs(r.Days[0].DistanceM-want) > 0.1 {
		t.Errorf("distance %v want %v", r.Days[0].DistanceM, want)
	}
	if r.Days[0].Last != t0+int64(n-1)*10 {
		t.Errorf("last %d", r.Days[0].Last)
	}
}

func TestRoutesGeometryBounded(t *testing.T) {
	routeSegmentBuffer, routeDayVertices = 64, 100
	t.Cleanup(func() { routeSegmentBuffer, routeDayVertices = 50000, 20000 })
	e := histEnv(t)
	const n = 2000
	t0 := int64(1700006400 + 3600)
	genPts(t, "dradis/a", t0, n)
	r := decodeRoutes(t, getHist(e, "admin", "/api/devices/dradis/a/routes?tz=UTC&from="+rfc(t0-100)+"&to="+rfc(t0+86000)))
	d := r.Days[0]
	if d.PointCount != n || d.DistanceM < float64(n-1)*100 { // each hop is ~111 m
		t.Fatalf("count %d distance %v", d.PointCount, d.DistanceM)
	}
	v := 0
	for _, s := range d.Segments {
		v += len(s)
		if s[0][2] != float64(t0) || s[len(s)-1][2] != float64(t0+int64(n-1)*10) {
			t.Errorf("segment ends %v %v", s[0], s[len(s)-1])
		}
	}
	if v > 120 || v < 2 {
		t.Errorf("vertices %d", v)
	}
}
