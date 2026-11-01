package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/t0mer/cic/internal/api"
	"github.com/t0mer/cic/internal/audit"
	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/commands"
	"github.com/t0mer/cic/internal/config"
	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/geocode"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/places"
	"github.com/t0mer/cic/internal/secret"
	"github.com/t0mer/cic/internal/store"
	"github.com/t0mer/cic/internal/version"
	"github.com/t0mer/cic/internal/webui"
)

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Real environment variables win; a missing .env is fine.
			_ = godotenv.Load()
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			return runServe(cmd.Context(), cfg)
		},
	}
	cmd.Flags().String("listen", ":8080", "HTTP listen address (env CIC_SERVER_LISTEN)")
	cmd.Flags().String("db-path", "/data/cic.db", "SQLite database path (env CIC_STORAGE_PATH)")
	cmd.Flags().Bool("insecure-cookies", false, "do not set the Secure attribute on the session cookie; plain-HTTP development only (env CIC_SERVER_INSECURE_COOKIES)")
	return cmd
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.SlogLevel()}
	if cfg.Log.Format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func newRouter(d api.Deps, baseURL string, trusted []*net.IPNet) http.Handler {
	r := chi.NewRouter()
	r.Use(api.SecurityHeaders(baseURL, trusted, d.UI.MapOrigins))
	r.Get("/healthz", api.Healthz)
	r.Get("/readyz", api.Readyz(d))
	r.Mount("/api", api.New(d))
	r.NotFound(webui.Handler().ServeHTTP)
	return r
}

// uiConfig derives the browser-facing configuration; it never carries the geocode key.
func uiConfig(cfg *config.Config) api.UIConfig {
	provider := cfg.Map.Provider
	light, dark := cfg.EffectiveStyles()
	return api.UIConfig{Provider: provider, StyleLight: light, StyleDark: dark, Attribution: cfg.Attribution(),
		GeocodeEnabled: cfg.GeocodeUsable(), HistoryEnabled: cfg.Locations.Enabled, MapOrigins: cfg.MapOrigins()}
}

// warnProxyConfig flags the classic reverse-proxy misconfiguration: an https public URL
// means TLS ends at a proxy, but without trusted_proxies every client appears to come from
// the proxy's address, so they all share one login rate-limit bucket.
func warnProxyConfig(log *slog.Logger, baseURL string, proxies []*net.IPNet) {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(baseURL)), "https") && len(proxies) == 0 {
		log.Warn("server.base_url is https but server.trusted_proxies is empty: behind a reverse proxy every client "+
			"shares the proxy's address for login rate limiting and X-Forwarded-Proto is ignored; list the proxy in server.trusted_proxies",
			"base_url", baseURL)
	}
}

func runServe(parent context.Context, cfg *config.Config) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := newLogger(cfg)
	slog.SetDefault(log)
	log.Info("starting cic", "version", version.Version, "listen", cfg.Server.Listen, "broker_configured", cfg.BrokerConfigured())

	db, err := store.Open(ctx, cfg.Storage.Path)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer db.Close()

	registry := devices.NewRegistry(db, cfg.Devices.OfflineAfter, log)
	registry.SetMaxDevices(cfg.Devices.MaxDevices)
	stopHistory := startHistory(ctx, db, registry, cfg.Locations, log)
	defer func() { stop(); stopHistory() }() // cancel, then flush queued fixes before db.Close
	placeStore, err := places.NewStore(ctx, db)
	if err != nil {
		return fmt.Errorf("loading safe places: %w", err)
	}
	registry.SetAnnotator(placeStore.Annotate)
	placeStore.OnChange(func() { registry.Renotify() })
	hub := events.NewHub()
	defer hub.Close()
	proxies, err := auth.ParseProxies(cfg.Server.TrustedProxies)
	if err != nil {
		return fmt.Errorf("parsing server.trusted_proxies: %w", err)
	}
	if cfg.Server.InsecureCookies {
		log.Warn("session cookies are not marked Secure (--insecure-cookies); use only for plain-HTTP development")
	}
	warnProxyConfig(log, cfg.Server.BaseURL, proxies)
	secretKey, err := loadSecretKey(os.Getenv(secret.KeyEnv), filepath.Join(filepath.Dir(cfg.Storage.Path), "secret.key"), log)
	if err != nil {
		return err
	}
	authSvc := auth.NewService(auth.Config{DB: db, Log: log, InsecureCookies: cfg.Server.InsecureCookies,
		TrustedProxies: proxies, BaseURL: cfg.Server.BaseURL, SecretKey: secretKey})
	if secretKey == nil {
		// Without a key nobody can complete a second factor: with enrolled users that must
		// not look like a healthy start.
		if has, err := authSvc.Store().HasTOTPUsers(ctx); err != nil {
			return err
		} else if has {
			return errors.New("two-factor is enabled for some accounts but the secret key is unavailable; restore secret.key or set CIC_SECRET_KEY")
		}
	}
	if err := authSvc.Store().CheckTOTPKey(ctx); err != nil {
		log.Error("two-factor secrets cannot be decrypted; two-factor logins will fail until the original secret key is restored", "err", err)
	}
	if err := auth.Bootstrap(ctx, authSvc.Store(), log, os.Getenv); err != nil {
		return err
	}
	go authSvc.RunJanitor(ctx, 10*time.Minute)
	auditLog := audit.New(db, nil)
	if n, err := auditLog.RecoverPending(ctx); err != nil {
		return fmt.Errorf("recovering audit log: %w", err)
	} else if n > 0 {
		log.Warn("audit rows left pending by a previous run marked unknown", "rows", n)
	}
	authSvc.SetAuditor(func(ctx context.Context, actor, action, detail string) error {
		_, err := auditLog.Begin(ctx, audit.Entry{User: actor, Device: "-", Action: action, Status: "ok", Detail: detail})
		return err
	})
	go auditLog.RunJanitor(ctx, cfg.Audit.Retention, auditPurgeEvery, log)
	relay := newOutcomeRelay(hub, auditLog, log)
	defer relay.Close() // runs before db.Close
	correlator := newCorrelator(relay, nil)
	go runSweeper(ctx, correlator, sweepEvery, time.Now)
	deps := api.Deps{Devices: registry, DB: db, Hub: hub, Log: log, Auth: authSvc, Audit: auditLog, Places: placeStore, History: db, UI: uiConfig(cfg)}
	if cfg.GeocodeKeyIsPublic() {
		log.Error("geocode disabled: the configured key is a public maps key; set the LatLng server key (latlng_…) in CIC_GEOCODE_API_KEY and put the pk_ key in CIC_MAP_LATLNG_MAPS_KEY")
	}
	if cfg.GeocodeUsable() {
		gw := geocode.NewWorker(registry, geocode.NewClient("", cfg.Geocode.APIKey), geocode.Store{DB: db},
			geocode.Config{MinMoveMeters: cfg.Geocode.MinMoveMeters, MaxAge: cfg.Geocode.MaxAge,
				DailyCap: cfg.Geocode.DailyCap, Reserve: cfg.Geocode.Reserve}, log, nil)
		gw.SetPresence(hub)           // provider calls only while a UI stream is open
		registry.OnChange(gw.Enqueue) // before the MQTT client starts
		deps.Geocode = gw
		go gw.Run(ctx)
	}
	var publisher commands.Publisher = noBroker{}

	mqttDone := make(chan struct{})
	if cfg.BrokerConfigured() {
		client, err := mqtt.New(ctx, cfg.MQTT, db, fanoutSink{sinks: []mqtt.Sink{registry, observerSink{correlator}}, log: log}, log)
		if err != nil {
			return fmt.Errorf("creating mqtt client: %w", err)
		}
		deps.Broker = client
		publisher = client
		deps.BrokerAddr = net.JoinHostPort(cfg.MQTT.Broker.Host, strconv.Itoa(cfg.MQTT.Broker.Port))
		go func() {
			defer close(mqttDone)
			client.Run(ctx)
		}()
	} else {
		close(mqttDone)
		log.Info("mqtt broker not configured, telemetry ingest disabled")
	}

	deps.Commands = commands.NewService(publisher, registry, commands.Config{
		Prefixes:    cfg.MQTT.Prefixes,
		PingSeconds: cfg.Commands.PingSeconds,
		SMSDailyCap: cfg.Commands.SMSDailyCap,
		Log:         log,
		Usage:       commands.SQLUsage{DB: db},
		Correlator:  correlator,
	})
	api.StartFeeds(ctx, deps)
	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           newRouter(deps, cfg.Server.BaseURL, proxies),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		stop()
		<-mqttDone
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	hub.Close() // ends SSE streams so Shutdown does not wait on them
	<-mqttDone
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	return nil
}

// loadSecretKey loads (or creates) the two-factor encryption key. An invalid CIC_SECRET_KEY
// is an operator error and stops startup. Any other failure (for example an unwritable data
// directory) is logged and yields a nil key: installs that never use two-factor keep
// working, and the two-factor endpoints answer 503.
func loadSecretKey(envValue, keyPath string, log *slog.Logger) ([]byte, error) {
	key, created, err := secret.LoadOrCreateKey(envValue, keyPath, log)
	if err != nil {
		if strings.TrimSpace(envValue) != "" {
			return nil, fmt.Errorf("loading secret key: %w", err)
		}
		log.Error("secret key unavailable; two-factor authentication is disabled until it is fixed", "path", keyPath, "err", err)
		return nil, nil
	}
	if created {
		log.Info("generated a new secret key file for encrypting two-factor secrets; back it up with the database", "path", keyPath)
	}
	return key, nil
}
