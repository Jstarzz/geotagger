package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Jstarzz/geotagger/internal/auth"
	"github.com/Jstarzz/geotagger/internal/config"
	"github.com/Jstarzz/geotagger/internal/httpapi"
	"github.com/Jstarzz/geotagger/internal/maxmindgeo"
	"github.com/Jstarzz/geotagger/internal/natsaudit"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadAPI()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	verifier, err := auth.NewVerifier(cfg.APIKeys)
	if err != nil {
		logger.Error("invalid API key configuration", "error", err)
		os.Exit(1)
	}
	lookup, err := maxmindgeo.OpenMaxMind(cfg.MMDBPath)
	if err != nil {
		logger.Error("open MaxMind database", "error", err)
		os.Exit(1)
	}
	defer lookup.Close()
	publisher, err := natsaudit.NewNATSPublisher(cfg.NATSURL, cfg.AuditStream, cfg.AuditSubject)
	if err != nil {
		logger.Error("initialize audit publisher", "error", err)
		os.Exit(1)
	}
	defer publisher.Close()

	stopReload := make(chan struct{})
	go lookup.Watch(cfg.MMDBReload, stopReload,
		func(err error) { logger.Error("MMDB reload failed", "error", err) },
		func(version string) { logger.Info("MMDB reloaded", "version", version) },
	)
	defer close(stopReload)

	app := httpapi.New(httpapi.Config{Lookup: lookup, Auth: verifier, Audit: publisher, AuditTimeout: cfg.AuditTimeout,
		AuditIPMode: cfg.AuditIPMode, AuditHMACKey: []byte(cfg.AuditHMACKey), MaxBodyBytes: cfg.MaxBodyBytes,
		AllowPrivateIPs: cfg.AllowPrivateIPs})
	apiServer := &http.Server{Addr: cfg.HTTPAddr, Handler: app.APIHandler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second,
		WriteTimeout: 3 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8 << 10}
	adminServer := &http.Server{Addr: cfg.AdminAddr, Handler: app.AdminHandler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second,
		WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("API listening", "addr", cfg.HTTPAddr, "mmdb", lookup.Version())
		errCh <- apiServer.ListenAndServe()
	}()
	go func() { logger.Info("admin listening", "addr", cfg.AdminAddr); errCh <- adminServer.ListenAndServe() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		logger.Info("shutdown requested", "signal", sig.String())
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	_ = apiServer.Shutdown(ctx)
	_ = adminServer.Shutdown(ctx)
}
