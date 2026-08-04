// Package main is the EV Charge Points HTTP server.
//
//	@title			EV Charge Points API
//	@version		1.0.0
//	@description	Manage and query electric vehicle charge points.
//	@host			localhost:8080
//	@BasePath		/
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

	"github.com/ankittk/chargepoints-api/api"
	"github.com/ankittk/chargepoints-api/internal/config"
	"github.com/ankittk/chargepoints-api/internal/server"
	"github.com/ankittk/chargepoints-api/internal/store"
	"github.com/ankittk/chargepoints-api/internal/telemetry"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	otelShutdown, err := telemetry.Setup(context.Background(), telemetry.Config{
		ServiceName:  cfg.OTELServiceName,
		Exporter:     cfg.OTELTracesExporter,
		OTLPEndpoint: cfg.OTELExporterOTLPEndpoint,
	})
	if err != nil {
		return fmt.Errorf("otel: %w", err)
	}
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			slog.Error("otel shutdown", "err", err)
		}
	}()

	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.SeedIfEmpty(context.Background()); err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	apiSrv := server.New(db, server.Config{
		OpenAPI:      api.OpenAPI,
		RateLimitRPS: cfg.RateLimitRPS,
		RateBurst:    cfg.RateBurst,
		TrustProxy:   cfg.TrustProxy,
		CORSOrigin:   cfg.CORSOrigin,
		Logger:       slog.Default(),
	})
	defer apiSrv.Close()

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Cancel ctx on SIGINT/SIGTERM so we can shut down cleanly instead of dying mid-request.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Buffered so ListenAndServe can exit even if we already took the shutdown path.
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "db", cfg.DatabasePath)
		errCh <- httpSrv.ListenAndServe()
	}()

	// Wait for either a listen failure or a shutdown signal — whichever comes first.
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		slog.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("listen after shutdown: %w", err)
		}
		return nil
	}
}
