package api

import (
	"net/http/httptest"
	"strings"
	"time"

	"context"
	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
	"testing"

	"github.com/t0mer/cic/internal/geocode"
)

type fakeGeocode struct{}

func (fakeGeocode) Usage(context.Context) (geocode.Usage, error) {
	rem := 80
	return geocode.Usage{Enabled: true, CallsToday: 7, CacheHitsToday: 3, DailyCap: 250, Remaining: 80, LastRemaining: &rem}, nil
}

func TestGeocodeUsageAuth(t *testing.T) {
	e := newSetEnv(t)
	e.d.Geocode = fakeGeocode{}
	e.h = router(e.d)
	if rec := e.send("nobody", "GET", "/api/geocode/usage", "", false); rec.Code != 401 {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	if rec := e.send("viewer", "GET", "/api/geocode/usage", "", false); rec.Code != 403 {
		t.Fatalf("viewer: %d", rec.Code)
	}
	m := e.ok(t, "admin", "GET", "/api/geocode/usage", "", 200)
	if m["enabled"] != true || m["calls_today"] != float64(7) || m["remaining"] != float64(80) || m["blocked_until"] != nil {
		t.Fatalf("body %v", m)
	}
}

func TestGeocodeUsageWhenOff(t *testing.T) {
	e := newSetEnv(t)
	m := e.ok(t, "admin", "GET", "/api/geocode/usage", "", 200)
	if m["enabled"] != false {
		t.Fatalf("body %v", m)
	}
}

func TestHiddenDeviceLocationNameNeverReachesViewer(t *testing.T) {
	e := newSetEnv(t)
	vis, sec := "Visible Place", "Secret Place 12 Hidden St"
	e.fs.devs = []devices.Device{
		{Key: "dradis/a", Prefix: "dradis", Device: "a", FullName: "A", LocationName: &vis},
		{Key: "dradis/h", Prefix: "dradis", Device: "h", FullName: "H", Hidden: true, LocationName: &sec},
	}
	for _, q := range []string{"", "?include_hidden=1"} {
		body := e.send("viewer", "GET", "/api/devices"+q, ``, false).Body.String()
		if strings.Contains(body, sec) || !strings.Contains(body, vis) {
			t.Errorf("viewer list%s: %s", q, body)
		}
	}
	if rec := e.send("viewer", "GET", "/api/devices/dradis/h", ``, false); rec.Code != 404 || strings.Contains(rec.Body.String(), sec) {
		t.Errorf("viewer get hidden: %d %s", rec.Code, rec.Body)
	}
	if body := e.send("admin", "GET", "/api/devices/dradis/h", ``, false).Body.String(); !strings.Contains(body, sec) {
		t.Errorf("admin get hidden lacks the name: %s", body)
	}

	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	view, adm := sseConnect(t, srv, e.who["viewer"].ck), sseConnect(t, srv, e.who["admin"].ck)
	view.until(t, "event: hello")
	adm.until(t, "event: hello")
	e.fs.fire(e.fs.devs[1])
	e.fs.fire(e.fs.devs[0])
	e.d.Hub.Publish(events.TypeBrokerState, brokerView{}) // canary
	var seen []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for view.sc.Scan() {
			seen = append(seen, view.sc.Text())
			if view.sc.Text() == "event: broker.state" {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("viewer never saw the canary")
	}
	all := strings.Join(seen, "\n")
	if strings.Contains(all, sec) {
		t.Errorf("viewer stream leaked a hidden location name:\n%s", all)
	}
	if !strings.Contains(all, vis) {
		t.Errorf("viewer stream lacks the visible name:\n%s", all)
	}
	if l := adm.until(t, "data: {"); !strings.Contains(l, sec) && !strings.Contains(adm.until(t, "data: {"), sec) {
		t.Error("admin stream lacks the hidden name")
	}
}
