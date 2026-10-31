// Package config loads and validates the cic configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
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

// MapConfig selects the map tile provider and style.
type MapConfig struct {
	Provider      string `mapstructure:"provider"`
	LatlngMapsKey string `mapstructure:"latlng_maps_key"`
	// StyleLight and StyleDark optionally override the provider's MapLibre style URLs.
	StyleLight string `mapstructure:"style_light"`
	StyleDark  string `mapstructure:"style_dark"`
	// ExtraOrigins are additional https origins (scheme://host[:port]) allowed by the CSP for
	// map images, requests and fonts; needed when a custom style references other hosts.
	ExtraOrigins []string `mapstructure:"extra_origins"`
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
		"map.style_light":                  "",
		"map.style_dark":                   "",
		"map.extra_origins":                []string{},
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
	errs = append(errs, c.validateMap()...)
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

// Built-in map style URLs for the openfreemap provider (verified to exist).
const (
	openFreeMapLight = "https://tiles.openfreemap.org/styles/positron"
	openFreeMapDark  = "https://tiles.openfreemap.org/styles/dark"
)

// providerOrigins are the origins the provider's own assets are served from: the default
// OpenFreeMap styles reference only tiles.openfreemap.org (style, TileJSON, tiles, sprite,
// glyphs, raster); the LatLng tile server is tiles.latlng.work.
var providerOrigins = map[string][]string{
	"openfreemap": {"https://tiles.openfreemap.org"},
	"latlng":      {"https://tiles.latlng.work"},
}

var (
	hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	portRe = regexp.MustCompile(`^[0-9]{1,5}$`)
)

const maxStyleURL = 512

// originOf returns scheme://host[:port] of an https URL, or an error. Only ASCII hostnames
// (no IP literals, wildcards, IDN) and plain numeric ports are accepted so the result is
// safe to splice into a CSP header.
func originOf(u *url.URL) (string, error) {
	if u.Scheme != "https" {
		return "", errors.New("must use https")
	}
	if u.User != nil {
		return "", errors.New("must not contain credentials")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || !hostRe.MatchString(host) || len(host) > 253 {
		return "", errors.New("host must be an ASCII hostname")
	}
	origin := "https://" + host
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if !portRe.MatchString(p) || err != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid port")
		}
		origin += ":" + strconv.Itoa(n)
	} else if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("invalid port")
	}
	return origin, nil
}

func parseStyleURL(raw string) (*url.URL, error) {
	if len(raw) > maxStyleURL {
		return nil, fmt.Errorf("longer than %d characters", maxStyleURL)
	}
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, " \t\r\n;,'\"") {
		return nil, errors.New("contains whitespace or illegal characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("not a valid URL")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("must not contain a fragment")
	}
	if _, err := originOf(u); err != nil {
		return nil, err
	}
	return u, nil
}

func (c *Config) validateMap() []error {
	var errs []error
	m := c.Map
	for name, val := range map[string]string{"map.style_light": m.StyleLight, "map.style_dark": m.StyleDark} {
		if val == "" {
			continue
		}
		if _, err := parseStyleURL(val); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	for _, o := range m.ExtraOrigins {
		if _, err := parseExtraOrigin(o); err != nil {
			errs = append(errs, fmt.Errorf("map.extra_origins entry %q: %w", o, err))
		}
	}
	if m.LatlngMapsKey != "" && !strings.HasPrefix(m.LatlngMapsKey, "pk_") {
		errs = append(errs, errors.New("map.latlng_maps_key must be a public maps key starting with pk_"))
	}
	// LatLng publishes no ready-made MapLibre style URL (only TileJSON + vector tiles), so the
	// styles cannot be derived: both must be given, and carry the maps key themselves.
	if m.Provider == "latlng" && (m.StyleLight == "" || m.StyleDark == "") {
		errs = append(errs, errors.New("map.style_light and map.style_dark are both required when map.provider is latlng"))
	}
	return errs
}

func parseExtraOrigin(raw string) (string, error) {
	u, err := parseStyleURL(raw)
	if err != nil {
		return "", err
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("must be a bare origin (scheme://host[:port])")
	}
	return originOf(u)
}

// EffectiveStyles returns the light and dark MapLibre style URLs: the configured overrides,
// else the provider defaults (empty for latlng, which requires explicit URLs).
func (c *Config) EffectiveStyles() (light, dark string) {
	light, dark = c.Map.StyleLight, c.Map.StyleDark
	if c.Map.Provider == "openfreemap" || c.Map.Provider == "" {
		if light == "" {
			light = openFreeMapLight
		}
		if dark == "" {
			dark = openFreeMapDark
		}
	}
	return light, dark
}

// MapOrigins returns the deduplicated https origins the browser may load map assets from:
// the provider's built-in origins, the origins of the two effective style URLs and
// map.extra_origins. Invalid entries are skipped (Validate rejects them at startup).
func (c *Config) MapOrigins() []string {
	provider := c.Map.Provider
	if provider == "" {
		provider = "openfreemap"
	}
	light, dark := c.EffectiveStyles()
	var out []string
	seen := map[string]bool{}
	add := func(o string) {
		if o != "" && !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	for _, o := range providerOrigins[provider] {
		add(o)
	}
	for _, s := range []string{light, dark} {
		if u, err := parseStyleURL(s); err == nil {
			o, _ := originOf(u)
			add(o)
		}
	}
	for _, e := range c.Map.ExtraOrigins {
		if o, err := parseExtraOrigin(e); err == nil {
			add(o)
		}
	}
	return out
}

// Attribution is the map attribution text for the provider.
func (c *Config) Attribution() string {
	if c.Map.Provider == "latlng" {
		return "© OpenStreetMap contributors · LatLng"
	}
	return "© OpenStreetMap contributors · OpenFreeMap"
}

// GeocodeUsable reports whether reverse geocoding can run: it is enabled and a LatLng
// server key (latlng_…) is set. A public maps key (pk_…) is never usable for geocoding.
func (c *Config) GeocodeUsable() bool {
	k := c.Geocode.APIKey
	return c.Geocode.Enabled && k != "" && !strings.HasPrefix(k, "pk_") && strings.HasPrefix(k, "latlng_")
}

// GeocodeKeyIsPublic reports whether geocode.api_key holds a public maps key by mistake.
func (c *Config) GeocodeKeyIsPublic() bool {
	return strings.HasPrefix(c.Geocode.APIKey, "pk_")
}
