package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/places"
	"io"
	"log/slog"
)

func newPlacesEnv(t *testing.T) *setEnv {
	t.Helper()
	return newSetEnvWith(t, func(d *Deps) {
		reg := devices.NewRegistry(testDB, 5*time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ps, err := places.NewStore(context.Background(), testDB)
		if err != nil {
			t.Fatal(err)
		}
		reg.SetAnnotator(ps.Annotate)
		ps.OnChange(func() { reg.Renotify() })
		d.Devices, d.Places = reg, ps
	})
}

func TestPlacesRBACAndCRUD(t *testing.T) {
	e := newPlacesEnv(t)
	body := `{"name":"Home","latitude":32,"longitude":34.8,"radius_m":200}`
	if rec := e.send("", "GET", "/api/places", "", false); rec.Code != 401 {
		t.Errorf("anon GET: %d", rec.Code)
	}
	for _, rt := range [][3]string{{"POST", "/api/places", body}, {"PATCH", "/api/places/1", `{"radius_m":300}`}, {"DELETE", "/api/places/1", ""}} {
		if rec := e.send("", rt[0], rt[1], rt[2], false); rec.Code != 401 {
			t.Errorf("anon %s: %d", rt[0], rec.Code)
		}
		if rec := e.send("viewer", rt[0], rt[1], rt[2], true); rec.Code != 403 {
			t.Errorf("viewer %s: %d", rt[0], rec.Code)
		}
		if rec := e.send("admin", rt[0], rt[1], rt[2], false); rec.Code != 403 {
			t.Errorf("admin no csrf %s: %d", rt[0], rec.Code)
		}
	}
	if rec := e.send("viewer", "GET", "/api/places", "", true); rec.Code != 403 {
		t.Errorf("viewer GET: %d %s", rec.Code, rec.Body)
	}
	m := e.ok(t, "admin", "POST", "/api/places", body, 201)
	if m["name"] != "Home" || m["radius_m"] != float64(200) || m["id"] == nil {
		t.Errorf("created %v", m)
	}
	e.ok(t, "admin", "POST", "/api/places", `{"name":"home","latitude":1,"longitude":1,"radius_m":50}`, 409)
	e.ok(t, "admin", "POST", "/api/places", `{"name":"X","latitude":91,"longitude":1,"radius_m":50}`, 400)
	e.ok(t, "admin", "POST", "/api/places", `{"name":"X"}`, 400)
	e.ok(t, "admin", "POST", "/api/places", `{"name":"X","latitude":1,"longitude":1,"radius_m":5}`, 400)
	m = e.ok(t, "admin", "PATCH", "/api/places/1", `{"radius_m":300,"name":"House"}`, 200)
	if m["radius_m"] != float64(300) || m["name"] != "House" || m["latitude"] != float64(32) {
		t.Errorf("patched %v", m)
	}
	if n := strings.Count(auditText(t), "place_update"); n != 1 {
		t.Fatalf("setup: %d place_update rows", n)
	}
	e.ok(t, "admin", "PATCH", "/api/places/1", `{}`, 400)
	if n := strings.Count(auditText(t), "place_update"); n != 1 {
		t.Errorf("empty patch was audited: %d place_update rows", n)
	}
	e.ok(t, "admin", "PATCH", "/api/places/99", `{"radius_m":300}`, 404)
	e.ok(t, "admin", "PATCH", "/api/places/1", `{"radius_m":1}`, 400)
	e.ok(t, "admin", "PATCH", "/api/places/abc", `{"radius_m":30}`, 400)
	e.ok(t, "admin", "DELETE", "/api/places/99", "", 404)
	e.ok(t, "admin", "DELETE", "/api/places/1", "", 204)
	text := auditText(t)
	for _, want := range []string{"admin|-|place_create|ok|place=Home\n", "admin|-|place_update|ok|place=Home fields=name,radius_m\n", "admin|-|place_delete|ok|place=House\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("audit lacks %q in:\n%s", want, text)
		}
	}
}

func TestDeviceCarriesSafePlace(t *testing.T) {
	e := newPlacesEnv(t)
	reg := e.d.Devices.(*devices.Registry)
	now := time.Now()
	loc := mqtt.Location{Latitude: 32, Longitude: 34.8}
	for _, dev := range []string{"in", "hid"} {
		ev := mqtt.Event{Prefix: "p", Device: dev, Leaf: mqtt.LeafLocation, ReceivedAt: now, Payload: loc}
		if err := reg.Apply(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Apply(context.Background(), mqtt.Event{Prefix: "p", Device: "noloc", Leaf: mqtt.LeafBattery, ReceivedAt: now, Payload: mqtt.Battery{Level: new(5)}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.UpdateProfile(context.Background(), "p/hid", devices.ProfilePatch{Hidden: new(true)}); err != nil {
		t.Fatal(err)
	}
	read := func(who string) map[string]map[string]any {
		rec := e.send(who, "GET", "/api/devices", "", true)
		var list []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatal(err, rec.Body)
		}
		out := map[string]map[string]any{}
		for _, d := range list {
			out[d["key"].(string)] = d
		}
		return out
	}
	if d := read("admin")["p/in"]; d["safe_place"] != nil {
		t.Errorf("no places yet: %v", d["safe_place"])
	}
	e.ok(t, "admin", "POST", "/api/places", `{"name":"Home","latitude":32,"longitude":34.8,"radius_m":100}`, 201)
	for _, who := range []string{"admin", "viewer"} {
		devs := read(who)
		sp, _ := devs["p/in"]["safe_place"].(map[string]any)
		if sp["name"] != "Home" || sp["id"] != float64(1) {
			t.Errorf("%s: safe_place = %v", who, devs["p/in"]["safe_place"])
		}
		if v, ok := devs["p/noloc"]; !ok || v["safe_place"] != nil {
			t.Errorf("%s: device without location: %v", who, v)
		}
	}
	if _, ok := read("viewer")["p/hid"]; ok {
		t.Error("hidden device visible to viewer")
	}
	if _, ok := read("admin")["p/hid"]; ok {
		// admin list excludes hidden unless include_hidden=1
		t.Error("hidden device listed without include_hidden")
	}
	rec := e.send("viewer", "GET", "/api/devices/p/hid", "", true)
	if rec.Code != 404 {
		t.Errorf("viewer hidden get: %d", rec.Code)
	}
	e.ok(t, "admin", "DELETE", "/api/places/1", "", 204)
	if d := read("viewer")["p/in"]; d["safe_place"] != nil {
		t.Errorf("after delete: %v", d["safe_place"])
	}
}

func TestPlaceWritePublishesOneRefresh(t *testing.T) {
	e := newPlacesEnv(t)
	reg := e.d.Devices.(*devices.Registry)
	now := time.Now()
	for i := 0; i < 150; i++ {
		ev := mqtt.Event{Prefix: "p", Device: fmt.Sprintf("d%03d", i), Leaf: mqtt.LeafLocation, ReceivedAt: now, Payload: mqtt.Location{Latitude: 32, Longitude: 34.8}}
		if err := reg.Apply(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	ch, cancel := e.d.Hub.Subscribe()
	defer cancel()
	e.ok(t, "admin", "POST", "/api/places", `{"name":"Home","latitude":32,"longitude":34.8,"radius_m":200}`, 201)
	var got []events.Event
	for len(ch) > 0 {
		got = append(got, <-ch)
	}
	if len(got) != 1 || got[0].Type != events.TypeDevicesRefresh {
		t.Fatalf("events = %+v, want exactly one devices.refresh", got)
	}
	if e.d.Hub.Dropped() != 0 {
		t.Errorf("dropped %d subscribers", e.d.Hub.Dropped())
	}
}
