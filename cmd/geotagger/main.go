package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Jstarzz/geotagger/internal/accessauth"
	"github.com/Jstarzz/geotagger/internal/adminhttp"
	"github.com/Jstarzz/geotagger/internal/auth"
	"github.com/Jstarzz/geotagger/internal/config"
	"github.com/Jstarzz/geotagger/internal/httpapi"
	"github.com/Jstarzz/geotagger/internal/keystore"
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
	lookup, err := maxmindgeo.OpenMaxMind(cfg.CityMMDBPath, cfg.ASNMMDBPath)
	if err != nil {
		logger.Error("open MaxMind databases", "error", err)
		os.Exit(1)
	}
	defer lookup.Close()

	publisher, err := natsaudit.NewNATSPublisher(cfg.NATSURL, cfg.AuditStream, cfg.AuditSubject)
	if err != nil {
		logger.Error("initialize audit publisher", "error", err)
		os.Exit(1)
	}
	defer publisher.Close()

	keyStore, err := keystore.Open(cfg.NATSURL, cfg.ManagedKeyBucket)
	if err != nil {
		logger.Error("initialize managed API key store", "error", err)
		os.Exit(1)
	}
	defer keyStore.Close()

	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	defer cancelRuntime()
	keyReady, err := keyStore.StartVerifierSync(runtimeCtx, verifier, func(err error) {
		logger.Error("managed API key synchronization failed", "error", err)
	})
	if err != nil {
		logger.Error("start managed API key synchronization", "error", err)
		os.Exit(1)
	}
	select {
	case err := <-keyReady:
		if err != nil {
			logger.Error("initial managed API key snapshot failed", "error", err)
			os.Exit(1)
		}
	case <-time.After(5 * time.Second):
		logger.Error("initial managed API key snapshot timed out")
		os.Exit(1)
	}

	var accessVerifier adminhttp.AccessVerifier
	if cfg.CFAccessTeamDomain != "" {
		access, err := accessauth.New(cfg.CFAccessTeamDomain, cfg.CFAccessAudience)
		if err != nil {
			logger.Error("invalid Cloudflare Access configuration", "error", err)
			os.Exit(1)
		}
		accessVerifier = access
		logger.Info("admin control plane enabled", "team_domain", cfg.CFAccessTeamDomain)
	} else {
		logger.Warn("admin control plane disabled until CF_ACCESS_TEAM_DOMAIN and CF_ACCESS_AUD are configured")
	}

	stopReload := make(chan struct{})
	go lookup.Watch(cfg.MMDBReload, stopReload,
		func(err error) { logger.Error("MMDB reload failed", "error", err) },
		func(version string) { logger.Info("MMDBs reloaded", "version", version) },
	)
	defer close(stopReload)

	app := httpapi.New(httpapi.Config{Lookup: lookup, Auth: verifier, Audit: publisher, AuditTimeout: cfg.AuditTimeout,
		AuditIPMode: cfg.AuditIPMode, AuditHMACKey: []byte(cfg.AuditHMACKey), MaxBodyBytes: cfg.MaxBodyBytes,
		AllowPrivateIPs: cfg.AllowPrivateIPs})
	adminControl := adminhttp.New(adminhttp.Config{
		Lookup: lookup, Auth: verifier, Audit: publisher, Keys: keyStore,
		Access: accessVerifier, Logger: logger, MaxBodySize: 4096,
	})

	adminMux := http.NewServeMux()
	adminMux.Handle("/admin/", adminControl.Handler())
	probeHandler := app.AdminHandler()
	adminMux.Handle("GET /metrics", probeHandler)
	adminMux.Handle("GET /healthz", probeHandler)
	adminMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !publisher.Healthy() || !keyStore.Healthy() {
			http.Error(w, "required runtime dependency unavailable", http.StatusServiceUnavailable)
			return
		}
		probeHandler.ServeHTTP(w, r)
	})

	apiServer := &http.Server{Addr: cfg.HTTPAddr, Handler: app.APIHandler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second,
		WriteTimeout: 3 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8 << 10}
	adminServer := &http.Server{Addr: cfg.AdminAddr, Handler: adminMux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("API listening", "addr", cfg.HTTPAddr, "mmdb", lookup.Version())
		errCh <- apiServer.ListenAndServe()
	}()
	go func() { logger.Info("admin listener ready", "addr", cfg.AdminAddr); errCh <- adminServer.ListenAndServe() }()

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
	cancelRuntime()
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	_ = apiServer.Shutdown(ctx)
	_ = adminServer.Shutdown(ctx)
}
