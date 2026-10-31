package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"

	"github.com/t0mer/cic/internal/config"
	"github.com/t0mer/cic/internal/store"
	"github.com/t0mer/cic/internal/version"
	"github.com/t0mer/cic/internal/webui"
)

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP server",
		RunE: func(cmd *cobra.Command, _ []string) error {
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

func newRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	// TODO(step 3): ready when DB ok and broker connected
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	})
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

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           newRouter(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	return nil
}
