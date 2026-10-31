package config

import (
	"reflect"
	"strings"
	"testing"
)

const goodStyle = "https://styles.example.com/x/style.json?key=pk_abc"

func TestMapValidation(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", 500)
	tests := []struct {
		name string
		mod  func(*Config)
		want string // "" = valid
	}{
		{"defaults", func(*Config) {}, ""},
		{"custom light+dark", func(c *Config) { c.Map.StyleLight, c.Map.StyleDark = goodStyle, goodStyle }, ""},
		{"https with port", func(c *Config) { c.Map.StyleLight = "https://s.example.com:8443/a" }, ""},
		{"http rejected", func(c *Config) { c.Map.StyleLight = "http://s.example.com/a" }, "map.style_light"},
		{"no host", func(c *Config) { c.Map.StyleDark = "https:///a" }, "map.style_dark"},
		{"userinfo", func(c *Config) { c.Map.StyleLight = "https://u:p@s.example.com/a" }, "credentials"},
		{"fragment", func(c *Config) { c.Map.StyleLight = "https://s.example.com/a#x" }, "fragment"},
		{"empty fragment marker", func(c *Config) { c.Map.StyleLight = "https://s.example.com/a#" }, "fragment"},
		{"too long", func(c *Config) { c.Map.StyleLight = long }, "512"},
		{"IDN host", func(c *Config) { c.Map.StyleLight = "https://bücher.example/a" }, "ASCII"},
		{"IP literal v6", func(c *Config) { c.Map.StyleLight = "https://[::1]/a" }, "ASCII"},
		{"semicolon", func(c *Config) { c.Map.StyleLight = "https://s.example.com/a;b" }, "illegal"},
		{"space", func(c *Config) { c.Map.StyleLight = "https://s.example.com/a b" }, "illegal"},
		{"bad port", func(c *Config) { c.Map.StyleLight = "https://s.example.com:99999/a" }, "port"},
		{"maps key ok", func(c *Config) { c.Map.LatlngMapsKey = "pk_latlng_abc" }, ""},
		{"maps key short pk_ ok", func(c *Config) { c.Map.LatlngMapsKey = "pk_abc" }, ""},
		{"maps key server key", func(c *Config) { c.Map.LatlngMapsKey = "latlng_abc" }, "pk_"},
		{"latlng no styles", func(c *Config) { c.Map.Provider = "latlng"; c.Map.LatlngMapsKey = "pk_x" }, "both required"},
		{"latlng one style", func(c *Config) { c.Map.Provider = "latlng"; c.Map.StyleLight = goodStyle }, "both required"},
		{"latlng both styles", func(c *Config) { c.Map.Provider = "latlng"; c.Map.StyleLight, c.Map.StyleDark = goodStyle, goodStyle }, ""},
		{"extra origin ok", func(c *Config) { c.Map.ExtraOrigins = []string{"https://a.example", "https://b.example:8443"} }, ""},
		{"extra origin http", func(c *Config) { c.Map.ExtraOrigins = []string{"http://a.example"} }, "map.extra_origins"},
		{"extra origin path", func(c *Config) { c.Map.ExtraOrigins = []string{"https://a.example/x"} }, "bare origin"},
		{"extra origin query", func(c *Config) { c.Map.ExtraOrigins = []string{"https://a.example?x=1"} }, "map.extra_origins"},
		{"extra origin wildcard", func(c *Config) { c.Map.ExtraOrigins = []string{"https://*.example.com"} }, "map.extra_origins"},
		{"extra origin star", func(c *Config) { c.Map.ExtraOrigins = []string{"*"} }, "map.extra_origins"},
		{"extra origin IDN", func(c *Config) { c.Map.ExtraOrigins = []string{"https://bücher.example"} }, "map.extra_origins"},
		{"extra origin injection", func(c *Config) { c.Map.ExtraOrigins = []string{"https://a.example; script-src *"} }, "map.extra_origins"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := Load("", nil)
			tt.mod(c)
			err := c.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestMapEnvOverrides(t *testing.T) {
	t.Setenv("CIC_MAP_STYLE_LIGHT", goodStyle)
	t.Setenv("CIC_MAP_STYLE_DARK", "https://d.example.com/dark")
	t.Setenv("CIC_MAP_EXTRA_ORIGINS", "https://a.example,https://b.example:8443")
	c, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Map.StyleLight != goodStyle || c.Map.StyleDark != "https://d.example.com/dark" {
		t.Fatalf("styles: %+v", c.Map)
	}
	if !reflect.DeepEqual(c.Map.ExtraOrigins, []string{"https://a.example", "https://b.example:8443"}) {
		t.Fatalf("extra origins: %v", c.Map.ExtraOrigins)
	}
}

func TestEffectiveStylesAndOrigins(t *testing.T) {
	tests := []struct {
		name         string
		mod          func(*Config)
		light, dark  string
		origins      []string
		attribSuffix string
	}{
		{"openfreemap defaults", func(*Config) {},
			"https://tiles.openfreemap.org/styles/positron", "https://tiles.openfreemap.org/styles/dark",
			[]string{"https://tiles.openfreemap.org"}, "OpenFreeMap"},
		{"override one", func(c *Config) { c.Map.StyleDark = "https://d.example.com/s.json" },
			"https://tiles.openfreemap.org/styles/positron", "https://d.example.com/s.json",
			[]string{"https://tiles.openfreemap.org", "https://d.example.com"}, "OpenFreeMap"},
		{"duplicates and ports and extras", func(c *Config) {
			c.Map.StyleLight, c.Map.StyleDark = "https://s.example.com:8443/a", "https://s.example.com:8443/b"
			c.Map.ExtraOrigins = []string{"https://tiles.openfreemap.org", "https://x.example", "https://x.example"}
		}, "https://s.example.com:8443/a", "https://s.example.com:8443/b",
			[]string{"https://tiles.openfreemap.org", "https://s.example.com:8443", "https://x.example"}, "OpenFreeMap"},
		{"latlng", func(c *Config) {
			c.Map.Provider = "latlng"
			c.Map.StyleLight, c.Map.StyleDark = "https://s.example.com/l?key=pk_a", "https://s.example.com/d?key=pk_a"
		}, "https://s.example.com/l?key=pk_a", "https://s.example.com/d?key=pk_a",
			[]string{"https://tiles.latlng.work", "https://s.example.com"}, "LatLng"},
		{"mixed-case host normalised", func(c *Config) { c.Map.StyleLight = "https://S.Example.COM/a" },
			"https://S.Example.COM/a", "https://tiles.openfreemap.org/styles/dark",
			[]string{"https://tiles.openfreemap.org", "https://s.example.com"}, "OpenFreeMap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := Load("", nil)
			tt.mod(c)
			l, d := c.EffectiveStyles()
			if l != tt.light || d != tt.dark {
				t.Fatalf("styles %q %q", l, d)
			}
			if tt.origins != nil && !reflect.DeepEqual(c.MapOrigins(), tt.origins) {
				t.Fatalf("origins %v, want %v", c.MapOrigins(), tt.origins)
			}
			if a := c.Attribution(); !strings.HasPrefix(a, "© OpenStreetMap contributors · ") || !strings.HasSuffix(a, tt.attribSuffix) {
				t.Fatalf("attribution %q", a)
			}
		})
	}
	// Invalid entries never reach the origin list.
	c, _ := Load("", nil)
	c.Map.ExtraOrigins = []string{"https://ok.example", "http://bad.example", "https://a.example; x", "*"}
	if got := c.MapOrigins(); !reflect.DeepEqual(got, []string{"https://tiles.openfreemap.org", "https://ok.example"}) {
		t.Fatalf("origins %v", got)
	}
}

func TestGeocodeUsable(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		key     string
		want    bool
		public  bool
	}{
		{"server key", true, "latlng_abc", true, false},
		{"disabled", false, "latlng_abc", false, false},
		{"empty key", true, "", false, false},
		{"public key", true, "pk_latlng_abc", false, true},
		{"bare pk_", true, "pk_abc", false, true},
		{"unknown prefix", true, "abc123", false, false},
		{"upper case prefix", true, "LATLNG_abc", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := Load("", nil)
			c.Geocode.Enabled, c.Geocode.APIKey = tt.enabled, tt.key
			if got := c.GeocodeUsable(); got != tt.want {
				t.Fatalf("usable = %v", got)
			}
			if got := c.GeocodeKeyIsPublic(); got != tt.public {
				t.Fatalf("public = %v", got)
			}
			// A public/unknown key is never a config error: startup logs and carries on.
			if err := c.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

func TestRedactionCoversMapsKey(t *testing.T) {
	c, _ := Load("", nil)
	c.Map.LatlngMapsKey = "pk_latlng_SECRETVALUE"
	c.Geocode.APIKey = "latlng_SERVERSECRET"
	out := c.String() + c.LogValue().String()
	for _, s := range []string{"SECRETVALUE", "SERVERSECRET"} {
		if strings.Contains(out, s) {
			t.Fatalf("%s leaked: %s", s, out)
		}
	}
}
