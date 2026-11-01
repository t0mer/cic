package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func writeYAML(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func testFlags() *pflag.FlagSet {
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.String("listen", ":8080", "")
	fs.String("db-path", "/data/cic.db", "")
	fs.String("log-level", "info", "")
	fs.String("log-format", "text", "")
	return fs
}

func TestDefaults(t *testing.T) {
	c, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Server.Listen != ":8080" || c.Storage.Path != "/data/cic.db" || c.MQTT.Broker.Port != 1883 ||
		c.Devices.OfflineAfter != 5*time.Minute || c.Geocode.MaxAge != 24*time.Hour ||
		c.Audit.Retention != 2160*time.Hour || !c.Geocode.Enabled || c.Map.Provider != "openfreemap" ||
		len(c.MQTT.Prefixes) != 1 || c.MQTT.Prefixes[0] != "dradis" || c.Commands.SMSDailyCap != 50 || c.Devices.MaxDevices != 500 ||
		c.Auth.OIDC.Issuer != "https://accounts.google.com" || c.Log.Format != "text" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.BrokerConfigured() {
		t.Fatal("broker should not be configured by default")
	}
}

func TestPrecedence(t *testing.T) {
	yaml := writeYAML(t, "server:\n  listen: \":9000\"\ndevices:\n  offline_after: 10m\nmqtt:\n  prefixes: [a, b]\n  broker:\n    host: yaml-host\n")
	tests := []struct {
		name  string
		env   map[string]string
		args  []string
		check func(*Config) bool
	}{
		{"yaml overrides default", nil, nil, func(c *Config) bool {
			return c.Server.Listen == ":9000" && c.Devices.OfflineAfter == 10*time.Minute &&
				len(c.MQTT.Prefixes) == 2 && c.MQTT.Broker.Host == "yaml-host" && c.BrokerConfigured()
		}},
		{"env beats yaml", map[string]string{"CIC_SERVER_LISTEN": ":9001", "CIC_MQTT_BROKER_PASSWORD": "pw"}, nil, func(c *Config) bool {
			return c.Server.Listen == ":9001" && c.MQTT.Broker.Password == "pw"
		}},
		{"flag beats env", map[string]string{"CIC_SERVER_LISTEN": ":9001"}, []string{"--listen", ":9002"}, func(c *Config) bool {
			return c.Server.Listen == ":9002"
		}},
		{"unchanged flag does not override env", map[string]string{"CIC_LOG_LEVEL": "debug"}, nil, func(c *Config) bool {
			return c.Log.Level == "debug"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			fs := testFlags()
			if err := fs.Parse(tt.args); err != nil {
				t.Fatal(err)
			}
			c, err := Load(yaml, fs)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.check(c) {
				t.Fatalf("check failed: %+v", c)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateFailures(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*Config)
		want string
	}{
		{"empty listen", func(c *Config) { c.Server.Listen = "" }, "server.listen"},
		{"bad provider", func(c *Config) { c.Map.Provider = "x" }, "map.provider"},
		{"bad prefix", func(c *Config) { c.MQTT.Prefixes = []string{"a/b"} }, "mqtt.prefixes"},
		{"long prefix", func(c *Config) { c.MQTT.Prefixes = []string{strings.Repeat("a", 65)} }, "mqtt.prefixes"},
		{"ping low", func(c *Config) { c.Commands.PingSeconds = 0 }, "ping_seconds"},
		{"ping high", func(c *Config) { c.Commands.PingSeconds = 601 }, "ping_seconds"},
		{"bad level", func(c *Config) { c.Log.Level = "loud" }, "log.level"},
		{"bad format", func(c *Config) { c.Log.Format = "xml" }, "log.format"},
		{"level info+2", func(c *Config) { c.Log.Level = "info+2" }, "log.level"},
		{"broker port zero", func(c *Config) { c.MQTT.Broker.Host = "h"; c.MQTT.Broker.Port = 0 }, "mqtt.broker.port"},
		{"broker port high", func(c *Config) { c.MQTT.Broker.Host = "h"; c.MQTT.Broker.Port = 65536 }, "mqtt.broker.port"},
		{"no prefixes", func(c *Config) { c.MQTT.Prefixes = nil }, "at least one"},
		{"offline zero", func(c *Config) { c.Devices.OfflineAfter = 0 }, "devices.offline_after"},
		{"max devices negative", func(c *Config) { c.Devices.MaxDevices = -1 }, "devices.max_devices"},
		{"sms cap negative", func(c *Config) { c.Commands.SMSDailyCap = -1 }, "sms_daily_cap"},
		{"geocode cap negative", func(c *Config) { c.Geocode.DailyCap = -1; c.Geocode.Reserve = -2 }, "geocode.daily_cap"},
		{"geocode reserve negative", func(c *Config) { c.Geocode.Reserve = -1 }, "geocode.reserve"},
		{"reserve above cap", func(c *Config) { c.Geocode.Reserve = c.Geocode.DailyCap + 1 }, "must not exceed"},
		{"latlng without styles", func(c *Config) { c.Map.Provider = "latlng" }, "map.style_light and map.style_dark"},
		{"oidc missing client_id", func(c *Config) {
			c.Auth.OIDC = OIDCConfig{Enabled: true, Issuer: "i", ClientSecret: "s", AllowedEmails: []string{"a@b"}}
		}, "auth.oidc.client_id"},
		{"oidc missing secret", func(c *Config) {
			c.Auth.OIDC = OIDCConfig{Enabled: true, Issuer: "i", ClientID: "c", AllowedEmails: []string{"a@b"}}
		}, "auth.oidc.client_secret"},
		{"oidc missing issuer", func(c *Config) {
			c.Auth.OIDC = OIDCConfig{Enabled: true, ClientID: "c", ClientSecret: "s", AllowedEmails: []string{"a@b"}}
		}, "auth.oidc.issuer"},
		{"oidc no emails", func(c *Config) {
			c.Auth.OIDC = OIDCConfig{Enabled: true, Issuer: "i", ClientID: "c", ClientSecret: "s"}
		}, "allowed_emails"},
		{"missing ca file", func(c *Config) {
			c.MQTT.Broker.Host = "h"
			c.MQTT.Broker.TLS = true
			c.MQTT.Broker.CAFile = filepath.Join(os.TempDir(), "definitely-missing-ca.pem")
		}, "mqtt.broker.ca_file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load("", nil)
			if err != nil {
				t.Fatal(err)
			}
			tt.mod(c)
			err = c.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRedaction(t *testing.T) {
	c, _ := Load("", nil)
	c.MQTT.Broker.Password = "s1"
	c.Geocode.APIKey = "s2"
	c.Auth.OIDC.ClientSecret = "s3"
	c.Map.LatlngMapsKey = "s4"
	out := c.String() + c.LogValue().String()
	for _, s := range []string{"s1", "s2", "s3", "s4"} {
		if strings.Contains(out, s) {
			t.Fatalf("secret %s leaked: %s", s, out)
		}
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatal("expected redaction marker")
	}
	if c.MQTT.Broker.Password != "s1" {
		t.Fatal("original mutated")
	}
}

func TestValidateAccepts(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		mod  func(*Config)
	}{
		{"geocode enabled without api key", func(c *Config) { c.Geocode.Enabled = true; c.Geocode.APIKey = "" }},
		{"geocode disabled ignores caps", func(c *Config) { c.Geocode.Enabled = false; c.Geocode.Reserve = 999 }},
		{"level warning", func(c *Config) { c.Log.Level = "warning" }},
		{"level uppercase", func(c *Config) { c.Log.Level = "WARN" }},
		{"readable ca file", func(c *Config) { c.MQTT.Broker.Host = "h"; c.MQTT.Broker.TLS = true; c.MQTT.Broker.CAFile = ca }},
		{"port ignored without host", func(c *Config) { c.MQTT.Broker.Port = 0 }},
		{"oidc complete", func(c *Config) {
			c.Auth.OIDC = OIDCConfig{Enabled: true, Issuer: "i", ClientID: "c", ClientSecret: "s", AllowedEmails: []string{"a@b"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := Load("", nil)
			tt.mod(c)
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSlogLevel(t *testing.T) {
	tests := map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn,
		"warning": slog.LevelWarn, "Warning": slog.LevelWarn, "ERROR": slog.LevelError,
	}
	for in, want := range tests {
		c := &Config{Log: LogConfig{Level: in}}
		if got := c.SlogLevel(); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"top level typo", "serverr:\n  listen: ':1'\n", "serverr"},
		{"nested typo", "mqtt:\n  broker:\n    pasword: x\n", "pasword"},
		{"typo in section", "log:\n  levle: info\n", "levle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeYAML(t, tt.yaml), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error naming %q", err, tt.want)
			}
		})
	}
}

func TestEnvListAndDuration(t *testing.T) {
	t.Setenv("CIC_MQTT_PREFIXES", "a,b")
	t.Setenv("CIC_DEVICES_OFFLINE_AFTER", "1m")
	t.Setenv("CIC_DEVICES_MAX_DEVICES", "7")
	t.Setenv("CIC_AUTH_OIDC_ALLOWED_EMAILS", "x@y.z,q@y.z")
	c, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.MQTT.Prefixes, []string{"a", "b"}) || c.Devices.OfflineAfter != time.Minute || c.Devices.MaxDevices != 7 ||
		!reflect.DeepEqual(c.Auth.OIDC.AllowedEmails, []string{"x@y.z", "q@y.z"}) {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestFlagBeatsYAML(t *testing.T) {
	yaml := writeYAML(t, "server:\n  listen: ':9000'\nlog:\n  level: error\n")
	fs := testFlags()
	if err := fs.Parse([]string{"--listen", ":9100", "--log-level", "debug"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(yaml, fs)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Listen != ":9100" || c.Log.Level != "debug" {
		t.Fatalf("flag did not win: %+v", c)
	}
}

func TestInsecureCookiesFlagAndTrustedProxies(t *testing.T) {
	fs := testFlags()
	fs.Bool("insecure-cookies", false, "")
	c, err := Load("", fs)
	if err != nil || c.Server.InsecureCookies {
		t.Fatalf("default: %v %v", err, c)
	}
	if err := fs.Parse([]string{"--insecure-cookies"}); err != nil {
		t.Fatal(err)
	}
	if c, err = Load("", fs); err != nil || !c.Server.InsecureCookies {
		t.Fatalf("flag not applied: %v", err)
	}

	t.Setenv("CIC_SERVER_TRUSTED_PROXIES", "10.0.0.0/8,192.0.2.1")
	c, err = Load("", nil)
	if err != nil || !reflect.DeepEqual(c.Server.TrustedProxies, []string{"10.0.0.0/8", "192.0.2.1"}) {
		t.Fatalf("env proxies: %v %v", err, c.Server.TrustedProxies)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Server.TrustedProxies = []string{"not-a-cidr"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "trusted_proxies") {
		t.Fatalf("bad proxy accepted: %v", err)
	}
}

func TestNonPositiveAuditRetentionMeansDefault(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Hour} {
		c, err := Load("", nil)
		if err != nil {
			t.Fatal(err)
		}
		c.Audit.Retention = d
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Audit.Retention != 2160*time.Hour {
			t.Errorf("retention %v -> %v, want 2160h", d, c.Audit.Retention)
		}
	}
}

func TestLocationsDefaultsAndValidation(t *testing.T) {
	c, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	l := c.Locations
	if !l.Enabled || l.Retention != 2160*time.Hour || l.MinDistanceM != 25 || l.MinInterval != 5*time.Minute || l.MaxPointsPerDevice != 200000 {
		t.Fatalf("unexpected defaults: %+v", l)
	}
	for _, d := range []time.Duration{0, -time.Hour} {
		c.Locations.Retention = d
		if err := c.Validate(); err != nil || c.Locations.Retention != 2160*time.Hour {
			t.Errorf("retention %v -> %v, %v", d, c.Locations.Retention, err)
		}
	}
	for name, mut := range map[string]func(*Config){
		"min_distance_m":        func(c *Config) { c.Locations.MinDistanceM = -1 },
		"min_interval":          func(c *Config) { c.Locations.MinInterval = -time.Second },
		"max_points_per_device": func(c *Config) { c.Locations.MaxPointsPerDevice = -1 },
	} {
		c, _ := Load("", nil)
		mut(c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: negative accepted: %v", name, err)
		}
	}
}

func TestLocationsEnv(t *testing.T) {
	t.Setenv("CIC_LOCATIONS_ENABLED", "false")
	t.Setenv("CIC_LOCATIONS_MIN_DISTANCE_M", "40")
	t.Setenv("CIC_LOCATIONS_MIN_INTERVAL", "90s")
	c, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Locations.Enabled || c.Locations.MinDistanceM != 40 || c.Locations.MinInterval != 90*time.Second {
		t.Fatalf("env not applied: %+v", c.Locations)
	}
}

func TestDefaultStoragePath(t *testing.T) {
	for goos, want := range map[string]string{"linux": "/data/cic.db", "darwin": "/data/cic.db", "windows": "cic.db"} {
		if got := defaultStoragePath(goos); got != want {
			t.Errorf("defaultStoragePath(%q) = %q, want %q", goos, got, want)
		}
	}
	if got := DefaultStoragePath(); got != defaultStoragePath(runtime.GOOS) {
		t.Errorf("DefaultStoragePath() = %q", got)
	}
}

func TestStoragePathDefaultComesFromTheHelper(t *testing.T) {
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Path != DefaultStoragePath() {
		t.Fatalf("storage.path default = %q, want %q", cfg.Storage.Path, DefaultStoragePath())
	}
}
