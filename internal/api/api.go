// Package api serves the read-only HTTP API and the Server-Sent Events stream.
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

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

	Heartbeat time.Duration // SSE ping interval; default 25s
	PollEvery time.Duration // broker state poll interval; default 1s
}

func (d Deps) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// ExposureAllowed reports whether the API may be served on listen.
// TODO(step 7): remove when auth lands
func ExposureAllowed(listen string, override bool) bool {
	if override {
		return true
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// New returns the API router, to be mounted at /api.
func New(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(limitBody)
	s := &server{Deps: d}
	r.Get("/devices", s.listDevices)
	r.Get("/devices/{prefix}/{device}", s.getDevice)
	r.Get("/broker", s.broker)
	r.Get("/events", s.sse)
	r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openAPISpec)
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeError(w, http.StatusNotFound, "not found") })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	return r
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
