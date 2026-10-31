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
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/t0mer/cic/internal/api"
	"github.com/t0mer/cic/internal/config"
	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/mqtt"
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
	return cmd
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.SlogLevel()}
	if cfg.Log.Format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func newRouter(d api.Deps, apiEnabled bool) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", api.Healthz)
	r.Get("/readyz", api.Readyz(d))
	// TODO(step 7): remove when auth lands
	if apiEnabled {
		r.Mount("/api", api.New(d))
	} else {
		r.HandleFunc("/api/*", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"api disabled until authentication exists"}`, http.StatusServiceUnavailable)
		})
	}
	r.NotFound(webui.Handler().ServeHTTP)
	return r
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
	hub := events.NewHub()
	defer hub.Close()
	deps := api.Deps{Devices: registry, DB: db, Hub: hub, Log: log}

	mqttDone := make(chan struct{})
	if cfg.BrokerConfigured() {
		client, err := mqtt.New(ctx, cfg.MQTT, db, registry, log)
		if err != nil {
			return fmt.Errorf("creating mqtt client: %w", err)
		}
		deps.Broker = client
		deps.BrokerAddr = net.JoinHostPort(cfg.MQTT.Broker.Host, strconv.Itoa(cfg.MQTT.Broker.Port))
		go func() {
			defer close(mqttDone)
			client.Run(ctx)
		}()
	} else {
		close(mqttDone)
		log.Info("mqtt broker not configured, telemetry ingest disabled")
	}

	api.StartFeeds(ctx, deps)
	// TODO(step 7): remove when auth lands
	override := os.Getenv("CIC_UNSAFE_NO_AUTH") == "true"
	deps.AllowAnyHost = override
	apiEnabled := api.ExposureAllowed(cfg.Server.Listen, override)
	switch {
	case !apiEnabled:
		log.Error("API disabled: authentication is not implemented yet and the listen address is not loopback; set CIC_UNSAFE_NO_AUTH=true to expose device locations unauthenticated, or listen on 127.0.0.1", "listen", cfg.Server.Listen)
	case override && !api.ExposureAllowed(cfg.Server.Listen, false):
		log.Warn("API exposed without authentication on a non-loopback address (CIC_UNSAFE_NO_AUTH=true)", "listen", cfg.Server.Listen)
	}

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           newRouter(deps, apiEnabled),
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
