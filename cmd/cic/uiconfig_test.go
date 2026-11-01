package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/t0mer/cic/internal/api"
	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/config"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/store"
)

func TestUIConfigFromConfig(t *testing.T) {
	const serverKey, mapsKey = "latlng_SERVERSECRET", "pk_latlng_MAPSKEY"
	tests := []struct {
		name     string
		mod      func(*config.Config)
		usable   bool
		provider string
		origins  []string
	}{
		{"server key", func(c *config.Config) { c.Geocode.APIKey = serverKey }, true, "openfreemap", []string{"https://tiles.openfreemap.org"}},
		{"public key in geocode slot", func(c *config.Config) { c.Geocode.APIKey = mapsKey }, false, "openfreemap", []string{"https://tiles.openfreemap.org"}},
		{"custom styles and extras", func(c *config.Config) {
			c.Geocode.APIKey = serverKey
			c.Map.StyleLight, c.Map.StyleDark = "https://s.example.com/l", "https://s.example.com/d"
			c.Map.ExtraOrigins = []string{"https://t.example:8443"}
		}, true, "openfreemap", []string{"https://tiles.openfreemap.org", "https://s.example.com", "https://t.example:8443"}},
		{"latlng", func(c *config.Config) {
			c.Geocode.APIKey = serverKey
			c.Map.Provider, c.Map.LatlngMapsKey = "latlng", mapsKey
			c.Map.StyleLight, c.Map.StyleDark = "https://s.example.com/l?key="+mapsKey, "https://s.example.com/d?key="+mapsKey
		}, true, "latlng", []string{"https://tiles.latlng.work", "https://s.example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _ := config.Load("", nil)
			tt.mod(cfg)
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			ui := uiConfig(cfg)
			if ui.HistoryEnabled != cfg.Locations.Enabled || !ui.HistoryEnabled {
				t.Fatalf("history_enabled = %v", ui.HistoryEnabled)
			}
			if off := *cfg; func() bool { off.Locations.Enabled = false; return uiConfig(&off).HistoryEnabled }() {
				t.Fatal("history_enabled true with locations disabled")
			}
			if ui.GeocodeEnabled != tt.usable || ui.Provider != tt.provider || !reflect.DeepEqual(ui.MapOrigins, tt.origins) {
				t.Fatalf("ui = %+v", ui)
			}
			// No field of the browser-facing config may hold the server key.
			v := reflect.ValueOf(ui)
			for i := 0; i < v.NumField(); i++ {
				if strings.Contains(strings.Join(flatten(v.Field(i)), "|"), "SERVERSECRET") {
					t.Errorf("field %s leaks the server key", v.Type().Field(i).Name)
				}
			}

			db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ui.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			svc := auth.NewService(auth.Config{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), InsecureCookies: true})
			hub := events.NewHub()
			t.Cleanup(hub.Close)
			srv := httptest.NewServer(newRouter(api.Deps{Devices: fakeDevices{}, DB: db, Hub: hub, Auth: svc, UI: ui}, "", nil))
			t.Cleanup(srv.Close)
			resp, err := http.Get(srv.URL + "/healthz")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			csp := resp.Header.Get("Content-Security-Policy")
			if strings.Contains(csp, "SERVERSECRET") {
				t.Fatalf("CSP leaks key: %s", csp)
			}
			// Exactly the configured origins appear, each in img/connect/font only.
			if got, want := strings.Count(csp, "https://"), 3*len(tt.origins); got != want {
				t.Errorf("CSP has %d https origins, want %d: %s", got, want, csp)
			}
			for _, o := range tt.origins {
				if strings.Count(csp, o+" ")+strings.Count(csp, o+";") != 3 {
					t.Errorf("origin %s not exactly in img/connect/font: %s", o, csp)
				}
			}
			for _, d := range []string{"script-src 'self';", "worker-src 'self' blob:;", "style-src 'self' 'unsafe-inline';", "object-src 'none'"} {
				if !strings.Contains(csp, d) {
					t.Errorf("CSP lacks %q: %s", d, csp)
				}
			}
		})
	}
}

func flatten(v reflect.Value) []string {
	switch v.Kind() {
	case reflect.String:
		return []string{v.String()}
	case reflect.Slice:
		var out []string
		for i := 0; i < v.Len(); i++ {
			out = append(out, flatten(v.Index(i))...)
		}
		return out
	}
	return nil
}
