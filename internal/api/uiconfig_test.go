package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUIConfig(t *testing.T) {
	const serverKey = "latlng_TOPSECRETSERVERKEY"
	tests := []struct {
		name string
		ui   UIConfig
	}{
		{"openfreemap", UIConfig{Provider: "openfreemap", StyleLight: "https://tiles.openfreemap.org/styles/positron",
			StyleDark: "https://tiles.openfreemap.org/styles/dark", Attribution: "© OpenStreetMap contributors · OpenFreeMap", GeocodeEnabled: true}},
		{"latlng", UIConfig{Provider: "latlng", StyleLight: "https://s.example.com/l?key=pk_latlng_x",
			StyleDark: "https://s.example.com/d?key=pk_latlng_x", Attribution: "© OpenStreetMap contributors · LatLng", GeocodeEnabled: false,
			MapOrigins: []string{"https://tiles.latlng.work"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCmdEnvWith(t, func(d *Deps, _ *cmdEnv) { d.UI = tt.ui })
			if rec := e.send("GET", "/api/ui-config", "", "", false); rec.Code != 401 {
				t.Errorf("anonymous: %d", rec.Code)
			}
			for _, who := range []string{"viewer", "admin"} {
				rec := e.send("GET", "/api/ui-config", "", who, false)
				if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("%s: %d cc=%q", who, rec.Code, rec.Header().Get("Cache-Control"))
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
					t.Fatal(err)
				}
				if len(raw) != 3 {
					t.Fatalf("unexpected top-level keys: %s", rec.Body)
				}
				var body struct {
					Map            map[string]string `json:"map"`
					GeocodeEnabled bool              `json:"geocode_enabled"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				want := map[string]string{"provider": tt.ui.Provider, "style_light": tt.ui.StyleLight, "style_dark": tt.ui.StyleDark, "attribution": tt.ui.Attribution}
				if len(body.Map) != len(want) {
					t.Fatalf("map keys: %v", body.Map)
				}
				for k, v := range want {
					if body.Map[k] != v {
						t.Errorf("map.%s = %q, want %q", k, body.Map[k], v)
					}
				}
				if body.GeocodeEnabled != tt.ui.GeocodeEnabled {
					t.Errorf("geocode_enabled = %v", body.GeocodeEnabled)
				}
				if strings.Contains(rec.Body.String(), "tiles.latlng.work") || strings.Contains(rec.Body.String(), serverKey) {
					t.Errorf("leak: %s", rec.Body)
				}
			}
			// GET only: a POST is refused without revealing anything.
			if rec := e.send("POST", "/api/ui-config", "", "admin", true); rec.Code != 405 {
				t.Errorf("POST: %d", rec.Code)
			}
		})
	}
}
