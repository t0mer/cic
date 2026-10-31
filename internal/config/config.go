// Package config loads and validates the cic configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config is the complete application configuration.
type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Storage  StorageConfig  `mapstructure:"storage"`
	MQTT     MQTTConfig     `mapstructure:"mqtt"`
	Devices  DevicesConfig  `mapstructure:"devices"`
	Commands CommandsConfig `mapstructure:"commands"`
	Geocode  GeocodeConfig  `mapstructure:"geocode"`
	Map      MapConfig      `mapstructure:"map"`
	Auth     AuthConfig     `mapstructure:"auth"`
	Audit    AuditConfig    `mapstructure:"audit"`
	Log      LogConfig      `mapstructure:"log"`
}

// ServerConfig configures the HTTP server.
type ServerConfig struct {
	Listen          string `mapstructure:"listen"`
	BaseURL         string `mapstructure:"base_url"`
	InsecureCookies bool   `mapstructure:"insecure_cookies"`
	// TrustedProxies lists CIDRs/IPs of reverse proxies whose X-Forwarded-For and
	// X-Forwarded-Proto are honoured. Leave it empty only without a reverse proxy: otherwise all
	// clients share the proxy address (one login rate-limit bucket) and https is not detected.
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

// StorageConfig configures persistence.
type StorageConfig struct {
	Path string `mapstructure:"path"`
}

// MQTTConfig configures MQTT topics and the single supported broker.
type MQTTConfig struct {
	Prefixes []string     `mapstructure:"prefixes"`
	Broker   BrokerConfig `mapstructure:"broker"`
}

// BrokerConfig describes the MQTT broker connection.
type BrokerConfig struct {
	Host               string `mapstructure:"host"`
	Port               int    `mapstructure:"port"`
	TLS                bool   `mapstructure:"tls"`
	CAFile             string `mapstructure:"ca_file"`
	InsecureSkipVerify bool   `mapstructure:"insecure_skip_verify"`
	Username           string `mapstructure:"username"`
	Password           string `mapstructure:"password"`
}

// DevicesConfig configures device liveness.
type DevicesConfig struct {
	OfflineAfter time.Duration `mapstructure:"offline_after"`
	MaxDevices   int           `mapstructure:"max_devices"`
}

// CommandsConfig configures remote command limits.
type CommandsConfig struct {
	PingSeconds int `mapstructure:"ping_seconds"`
	SMSDailyCap int `mapstructure:"sms_daily_cap"`
}

// GeocodeConfig configures reverse geocoding.
type GeocodeConfig struct {
	Enabled       bool          `mapstructure:"enabled"`
	APIKey        string        `mapstructure:"api_key"`
	MinMoveMeters int           `mapstructure:"min_move_meters"`
	MaxAge        time.Duration `mapstructure:"max_age"`
	DailyCap      int           `mapstructure:"daily_cap"`
	Reserve       int           `mapstructure:"reserve"`
}

// MapConfig selects the map tile provider.
type MapConfig struct {
	Provider      string `mapstructure:"provider"`
	LatlngMapsKey string `mapstructure:"latlng_maps_key"`
}

// AuthConfig configures authentication.
type AuthConfig struct {
	OIDC OIDCConfig `mapstructure:"oidc"`
}

// OIDCConfig configures OpenID Connect login.
type OIDCConfig struct {
	Enabled       bool     `mapstructure:"enabled"`
	Issuer        string   `mapstructure:"issuer"`
	ClientID      string   `mapstructure:"client_id"`
	ClientSecret  string   `mapstructure:"client_secret"`
	AllowedEmails []string `mapstructure:"allowed_emails"`
}

// AuditConfig configures audit log retention.
type AuditConfig struct {
	Retention time.Duration `mapstructure:"retention"`
}

// LogConfig configures logging.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// flagKeys maps command-line flag names to configuration keys.
var flagKeys = map[string]string{
	"listen":     "server.listen",
	"db-path":    "storage.path",
	"log-level":  "log.level",
	"log-format": "log.format",

	"insecure-cookies": "server.insecure_cookies",
}

var prefixRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func newViper() *viper.Viper {
	v := viper.New()
	v.SetEnvPrefix("CIC")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	for k, val := range map[string]any{
		"server.listen":                    ":8080",
		"server.base_url":                  "",
		"server.insecure_cookies":          false,
		"server.trusted_proxies":           []string{},
		"storage.path":                     "/data/cic.db",
		"mqtt.prefixes":                    []string{"dradis"},
		"mqtt.broker.host":                 "",
		"mqtt.broker.port":                 1883,
		"mqtt.broker.tls":                  false,
		"mqtt.broker.ca_file":              "",
		"mqtt.broker.insecure_skip_verify": false,
		"mqtt.broker.username":             "",
		"mqtt.broker.password":             "",
		"devices.offline_after":            5 * time.Minute,
		"devices.max_devices":              500,
		"commands.ping_seconds":            3,
		"commands.sms_daily_cap":           50,
		"geocode.enabled":                  true,
		"geocode.api_key":                  "",
		"geocode.min_move_meters":          150,
		"geocode.max_age":                  24 * time.Hour,
		"geocode.daily_cap":                250,
		"geocode.reserve":                  20,
		"map.provider":                     "openfreemap",
		"map.latlng_maps_key":              "",
		"auth.oidc.enabled":                false,
		"auth.oidc.issuer":                 "https://accounts.google.com",
		"auth.oidc.client_id":              "",
		"auth.oidc.client_secret":          "",
		"auth.oidc.allowed_emails":         []string{},
		"audit.retention":                  2160 * time.Hour,
		"log.level":                        "info",
		"log.format":                       "text",
	} {
		v.SetDefault(k, val)
	}
	return v
}

// Load reads configuration with precedence flags > env > YAML file > defaults.
// path may be empty; flags may be nil. The result is not validated.
func Load(path string, flags *pflag.FlagSet) (*Config, error) {
	v := newViper()
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config file %q: %w", path, err)
		}
	}
	if flags != nil {
		for name, key := range flagKeys {
			f := flags.Lookup(name)
			if f == nil {
				continue
			}
			if err := v.BindPFlag(key, f); err != nil {
				return nil, fmt.Errorf("binding flag %q: %w", name, err)
			}
		}
	}
	var c Config
	// ErrorUnused rejects keys (e.g. a misspelled YAML key) that match no field.
	strict := func(dc *mapstructure.DecoderConfig) { dc.ErrorUnused = true }
	if err := v.Unmarshal(&c, strict); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}
	return &c, nil
}

// Validate checks the configuration for invalid values.
func (c *Config) Validate() error {
	var errs []error
	if c.Server.Listen == "" {
		errs = append(errs, errors.New("server.listen must not be empty"))
	}
	for _, p := range c.Server.TrustedProxies {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(p); err != nil && net.ParseIP(p) == nil {
			errs = append(errs, fmt.Errorf("server.trusted_proxies entry %q must be an IP or CIDR", p))
		}
	}
	switch c.Map.Provider {
	case "openfreemap", "latlng":
	default:
		errs = append(errs, fmt.Errorf("map.provider %q must be openfreemap or latlng", c.Map.Provider))
	}
	if len(c.MQTT.Prefixes) == 0 {
		errs = append(errs, errors.New("mqtt.prefixes must contain at least one prefix"))
	}
	for _, p := range c.MQTT.Prefixes {
		if !prefixRe.MatchString(p) {
			errs = append(errs, fmt.Errorf("mqtt.prefixes entry %q must match %s", p, prefixRe))
		}
	}
	if c.Commands.PingSeconds < 1 || c.Commands.PingSeconds > 600 {
		errs = append(errs, fmt.Errorf("commands.ping_seconds %d must be between 1 and 600", c.Commands.PingSeconds))
	}
	if _, ok := parseLevel(c.Log.Level); !ok {
		errs = append(errs, fmt.Errorf("log.level %q must be debug, info, warn, warning or error", c.Log.Level))
	}
	if b := c.MQTT.Broker; b.Host != "" {
		if b.Port < 1 || b.Port > 65535 {
			errs = append(errs, fmt.Errorf("mqtt.broker.port %d must be between 1 and 65535", b.Port))
		}
		if b.TLS && b.CAFile != "" {
			if _, err := os.Stat(b.CAFile); err != nil {
				errs = append(errs, fmt.Errorf("mqtt.broker.ca_file: %w", err))
			}
		}
	}
	if c.Audit.Retention <= 0 {
		c.Audit.Retention = 2160 * time.Hour // never "keep forever": the audit table must not grow unbounded
	}
	if c.Devices.OfflineAfter <= 0 {
		errs = append(errs, fmt.Errorf("devices.offline_after %s must be greater than 0", c.Devices.OfflineAfter))
	}
	if c.Devices.MaxDevices < 0 {
		errs = append(errs, fmt.Errorf("devices.max_devices %d must not be negative", c.Devices.MaxDevices))
	}
	if c.Commands.SMSDailyCap < 0 {
		errs = append(errs, fmt.Errorf("commands.sms_daily_cap %d must not be negative", c.Commands.SMSDailyCap))
	}
	if g := c.Geocode; g.Enabled {
		// An empty api_key is allowed: geocoding simply stays off.
		if g.DailyCap < 0 {
			errs = append(errs, fmt.Errorf("geocode.daily_cap %d must not be negative", g.DailyCap))
		}
		if g.Reserve < 0 {
			errs = append(errs, fmt.Errorf("geocode.reserve %d must not be negative", g.Reserve))
		}
		if g.Reserve > g.DailyCap {
			errs = append(errs, fmt.Errorf("geocode.reserve %d must not exceed geocode.daily_cap %d", g.Reserve, g.DailyCap))
		}
	}
	if c.Map.Provider == "latlng" && c.Map.LatlngMapsKey == "" {
		errs = append(errs, errors.New("map.latlng_maps_key is required when map.provider is latlng"))
	}
	if o := c.Auth.OIDC; o.Enabled {
		for name, val := range map[string]string{"issuer": o.Issuer, "client_id": o.ClientID, "client_secret": o.ClientSecret} {
			if val == "" {
				errs = append(errs, fmt.Errorf("auth.oidc.%s is required when auth.oidc.enabled is true", name))
			}
		}
		if len(o.AllowedEmails) == 0 {
			errs = append(errs, errors.New("auth.oidc.allowed_emails must not be empty when auth.oidc.enabled is true"))
		}
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		errs = append(errs, fmt.Errorf("log.format %q must be text or json", c.Log.Format))
	}
	return errors.Join(errs...)
}

// BrokerConfigured reports whether an MQTT broker host has been set.
func (c *Config) BrokerConfigured() bool {
	return c.MQTT.Broker.Host != ""
}

// SlogLevel returns the configured log level.
func (c *Config) SlogLevel() slog.Level {
	lvl, _ := parseLevel(c.Log.Level)
	return lvl
}

// parseLevel accepts debug, info, warn, warning and error (case-insensitive).
func parseLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return slog.LevelInfo, false
}

const redacted = "[redacted]"

func mask(s string) string {
	if s == "" {
		return ""
	}
	return redacted
}

// Redacted returns a deep-enough copy with secrets masked.
func (c *Config) Redacted() Config {
	r := *c
	r.MQTT.Broker.Password = mask(r.MQTT.Broker.Password)
	r.Geocode.APIKey = mask(r.Geocode.APIKey)
	r.Map.LatlngMapsKey = mask(r.Map.LatlngMapsKey)
	r.Auth.OIDC.ClientSecret = mask(r.Auth.OIDC.ClientSecret)
	return r
}

// String formats the configuration with secrets redacted.
func (c *Config) String() string {
	r := c.Redacted()
	return fmt.Sprintf("%+v", r)
}

// LogValue implements slog.LogValuer with secrets redacted.
func (c *Config) LogValue() slog.Value {
	return slog.StringValue(c.String())
}
