// Package api serves the read-only HTTP API and the Server-Sent Events stream.
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

//go:embed openapi.yaml
var openAPISpec []byte

const (
	maxBody          = 16 << 10
	defaultHeartbeat = 25 * time.Second
	defaultPoll      = time.Second
	defaultRecheck   = 5 * time.Second
	activeCacheTTL   = time.Second // a positive session check is trusted this long
)

// DeviceStore is the device registry as seen by the API.
type DeviceStore interface {
	List(ctx context.Context) ([]devices.Device, error)
	Get(ctx context.Context, key string) (devices.Device, error)
	OnChange(fn func(devices.Device))
}

// BrokerState reports the MQTT connection state.
type BrokerState interface {
	State() mqtt.State
}

// Pinger checks database liveness.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// Deps are the API dependencies. Broker may be nil when no broker is configured.
type Deps struct {
	Devices    DeviceStore
	Broker     BrokerState
	BrokerAddr string // host:port, shown by /api/broker
	DB         Pinger
	Hub        *events.Hub
	Log        *slog.Logger

	// Commands runs device commands (admin only). Nil means no broker is configured:
	// the command routes still exist and answer 503 not_connected.
	Commands Commander
	// Audit stores and lists command attempts (GET /api/activity, admin only).
	Audit ActivityLog

	// Auth authenticates every /api request except POST /api/auth/login. Required.
	Auth *auth.Service

	// UI is the browser-facing configuration served by GET /api/ui-config.
	UI UIConfig

	Heartbeat time.Duration // SSE ping interval; default 25s
	PollEvery time.Duration // broker state poll interval; default 1s

	// SessionRecheck is how often an idle SSE stream re-validates its session against the
	// database, to notice changes made by another process (CLI); default 5s.
	SessionRecheck time.Duration
}

func (d Deps) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// New returns the API router, to be mounted at /api.
func New(d Deps) http.Handler {
	if d.Auth == nil {
		panic("api: Deps.Auth is required; the API is never served unauthenticated")
	}
	r := chi.NewRouter()
	r.Use(limitBody)
	s := &server{Deps: d}
	r.Mount("/auth", d.Auth.Routes())
	// Everything else needs a session. Handlers that mutate state or touch settings
	// add auth.RequireRole(auth.RoleAdmin) in their own group.
	r.Group(func(r chi.Router) {
		r.Use(d.Auth.RequireAuth)
		r.Get("/devices", s.listDevices)
		r.Get("/devices/{prefix}/{device}", s.getDevice)
		r.Get("/broker", s.broker)
		r.Get("/ui-config", s.uiConfig)
		r.Get("/events", s.sse)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(auth.RoleAdmin))
			s.cmdRoutes(r)
		})
		r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/yaml")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(openAPISpec)
		})
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeError(w, http.StatusNotFound, "not found") })
	// 405: the Allow header lists the methods the route really has. Known paths outside
	// /auth ask for a session first, so an anonymous client gets 401, never a hint about
	// which methods exist; unknown paths stay 404.
	allow := newAllowTable(r)
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path // the full path, also when a sub-router dispatched here
		methods := allow.methods(path)
		respond := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if len(methods) > 0 {
				w.Header().Set("Allow", strings.Join(methods, ", "))
			}
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		})
		if strings.HasPrefix(path, "/auth/") || strings.HasPrefix(path, "/api/auth/") {
			respond(w, req)
			return
		}
		// The session is checked as for a GET: the method is wrong anyway, so a missing CSRF
		// token must not turn the answer into 403.
		probe := req.Clone(req.Context())
		probe.Method = http.MethodGet
		d.Auth.RequireAuth(respond).ServeHTTP(w, probe)
	})
	return r
}

// allowTable maps route patterns to their methods, built once from the finished router.
type allowTable struct {
	routes []allowRoute
}

type allowRoute struct {
	re      *regexp.Regexp
	methods map[string]bool
}

func newAllowTable(r chi.Router) *allowTable {
	byPattern := map[string]*allowRoute{}
	var order []string
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if strings.Contains(route, "*") {
			return nil
		}
		ar, ok := byPattern[route]
		if !ok {
			segs := strings.Split(route, "/")
			for i, sg := range segs {
				if strings.HasPrefix(sg, "{") && strings.HasSuffix(sg, "}") {
					segs[i] = "[^/]+"
				} else {
					segs[i] = regexp.QuoteMeta(sg)
				}
			}
			ar = &allowRoute{re: regexp.MustCompile("^(?:/api)?" + strings.Join(segs, "/") + "/?$"), methods: map[string]bool{}}
			byPattern[route] = ar
			order = append(order, route)
		}
		ar.methods[method] = true
		return nil
	})
	t := &allowTable{}
	for _, p := range order {
		t.routes = append(t.routes, *byPattern[p])
	}
	return t
}

// methods returns the sorted methods of every route matching path.
func (t *allowTable) methods(path string) []string {
	set := map[string]bool{}
	for _, ar := range t.routes {
		if ar.re.MatchString(path) {
			for m := range ar.methods {
				set[m] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

type server struct{ Deps }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *server) listDevices(w http.ResponseWriter, r *http.Request) {
	all, err := s.Devices.List(r.Context())
	if err != nil {
		s.log().Error("listing devices", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	incl := r.URL.Query().Get("include_hidden") == "1"
	out := make([]devices.Device, 0, len(all))
	for _, d := range all {
		if d.Hidden && !incl {
			continue
		}
		out = append(out, d)
	}
	slices.SortStableFunc(out, func(a, b devices.Device) int {
		if c := strings.Compare(a.FullName, b.FullName); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getDevice(w http.ResponseWriter, r *http.Request) {
	prefix, dev := chi.URLParam(r, "prefix"), chi.URLParam(r, "device")
	if !mqtt.ValidSegment(prefix) || !mqtt.ValidSegment(dev) {
		writeError(w, http.StatusBadRequest, "invalid prefix or device")
		return
	}
	d, err := s.Devices.Get(r.Context(), prefix+"/"+dev)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	if err != nil {
		s.log().Error("reading device", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// brokerView is the public broker state; it never carries credentials.
type brokerView struct {
	Connected bool       `json:"connected"`
	Host      string     `json:"host"`
	LastError string     `json:"last_error"`
	Since     *time.Time `json:"since"`
}

func (d Deps) brokerView() brokerView {
	v := brokerView{Host: d.BrokerAddr}
	if d.Broker == nil {
		return v
	}
	st := d.Broker.State()
	v.Connected, v.LastError = st.Connected, st.LastError
	if !st.Since.IsZero() {
		t := st.Since.UTC()
		v.Since = &t
	}
	return v
}

func (s *server) broker(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.brokerView())
}

// UIConfig is the configuration the browser needs. It holds no secrets by construction:
// the geocode server key never enters this struct (only whether geocoding is usable).
type UIConfig struct {
	Provider       string // "openfreemap" | "latlng"
	StyleLight     string
	StyleDark      string
	Attribution    string
	GeocodeEnabled bool
	// MapOrigins are the https origins added to the CSP (see SecurityHeaders); not served.
	MapOrigins []string
}

type uiConfigMap struct {
	Provider    string `json:"provider"`
	StyleLight  string `json:"style_light"`
	StyleDark   string `json:"style_dark"`
	Attribution string `json:"attribution"`
}

type uiConfigView struct {
	Map            uiConfigMap `json:"map"`
	GeocodeEnabled bool        `json:"geocode_enabled"`
}

func (s *server) uiConfig(w http.ResponseWriter, _ *http.Request) {
	u := s.UI
	writeJSON(w, http.StatusOK, uiConfigView{
		Map:            uiConfigMap{Provider: u.Provider, StyleLight: u.StyleLight, StyleDark: u.StyleDark, Attribution: u.Attribution},
		GeocodeEnabled: u.GeocodeEnabled,
	})
}

// StartFeeds wires registry changes and broker state changes into the hub.
// The poller stops when ctx is done.
func StartFeeds(ctx context.Context, d Deps) {
	d.Devices.OnChange(func(dev devices.Device) {
		d.Hub.Publish(events.TypeDeviceUpdated, dev)
	})
	if d.Broker == nil {
		return
	}
	every := d.PollEvery
	if every <= 0 {
		every = defaultPoll
	}
	go func() {
		last := d.Broker.State()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if cur := d.Broker.State(); cur != last {
					last = cur
					d.Hub.Publish(events.TypeBrokerState, d.brokerView())
				}
			}
		}
	}()
}

// Readyz reports ready only when the database answers and the broker is connected.
func Readyz(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.DB.PingContext(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		if d.Broker == nil {
			http.Error(w, "broker not configured", http.StatusServiceUnavailable)
			return
		}
		if !d.Broker.State().Connected {
			http.Error(w, "broker not connected", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, "ready")
	}
}

// Healthz always reports ok.
func Healthz(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }

// NotFoundJSON answers unknown /api paths.
func NotFoundJSON(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not found")
}
