package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Jstarzz/geotagger/internal/config"
	"github.com/Jstarzz/geotagger/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadWorker()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	sink := worker.NewClickHouse(cfg.ClickHouseURL, cfg.ClickHouseUser, cfg.ClickHousePassword)
	w, err := worker.New(worker.Config{NATSURL: cfg.NATSURL, Stream: cfg.AuditStream, Subject: cfg.AuditSubject,
		Durable: cfg.DurableName, BatchSize: cfg.BatchSize, FlushInterval: cfg.FlushInterval, Sink: sink, Logger: logger})
	if err != nil {
		logger.Error("initialize worker", "error", err)
		os.Exit(1)
	}
	defer w.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := w.Run(ctx); err != nil {
		logger.Error("worker failed", "error", err)
		os.Exit(1)
	}
}
