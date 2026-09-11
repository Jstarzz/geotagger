//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Jstarzz/geotagger/internal/audit"
	"github.com/Jstarzz/geotagger/internal/natsaudit"
	"github.com/Jstarzz/geotagger/internal/worker"
	"github.com/nats-io/nats.go"
)

const (
	integrationStream  = "GEOTAGGER_AUDIT_INTEGRATION"
	integrationSubject = "geotagger.audit.integration"
	integrationDurable = "geotagger-integration-clickhouse"
)

func TestAuditPipeline(t *testing.T) {
	natsURL := getenv("INTEGRATION_NATS_URL", "nats://127.0.0.1:4222")
	clickhouseURL := getenv("INTEGRATION_CLICKHOUSE_URL", "http://127.0.0.1:8123")
	clickhouseUser := getenv("INTEGRATION_CLICKHOUSE_USER", "geotagger_ingest")
	clickhousePassword := os.Getenv("INTEGRATION_CLICKHOUSE_PASSWORD")
	if clickhousePassword == "" {
		t.Fatal("INTEGRATION_CLICKHOUSE_PASSWORD is required")
	}

	waitForClickHouse(t, clickhouseURL, clickhouseUser, clickhousePassword)
	execClickHouse(t, clickhouseURL, clickhouseUser, clickhousePassword, `
		CREATE DATABASE IF NOT EXISTS geotagger;
		CREATE TABLE IF NOT EXISTS geotagger.audit_events
		(
			timestamp DateTime64(6, 'UTC'), request_id String, caller_id LowCardinality(String),
			ip_value String, ip_mode LowCardinality(String), country_code LowCardinality(String),
			country String, outcome LowCardinality(String), status_code UInt16,
			lookup_latency_us UInt64, mmdb_version LowCardinality(String)
		) ENGINE = MergeTree
		ORDER BY (timestamp, caller_id, request_id)
	`)
	execClickHouse(t, clickhouseURL, clickhouseUser, clickhousePassword, "TRUNCATE TABLE geotagger.audit_events")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	publisher, err := natsaudit.NewNATSPublisher(natsURL, integrationStream, integrationSubject)
	if err != nil {
		t.Fatalf("publisher: %v", err)
	}
	defer publisher.Close() //nolint:errcheck

	assertStreamSafety(t, natsURL)

	sink := worker.NewClickHouse(clickhouseURL, clickhouseUser, clickhousePassword)
	w, err := worker.New(worker.Config{
		NATSURL: natsURL, Stream: integrationStream, Subject: integrationSubject,
		Durable: integrationDurable, BatchSize: 10, FlushInterval: 50 * time.Millisecond,
		Sink: sink, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	defer w.Close() //nolint:errcheck

	runCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- w.Run(runCtx) }()
	defer func() {
		stopWorker()
		select {
		case err := <-workerDone:
			if err != nil {
				t.Errorf("worker shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("worker did not stop")
		}
	}()

	requestID := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	event := audit.Event{
		Timestamp: time.Now().UTC(), RequestID: requestID, CallerID: "integration",
		IPValue: "hmac-value", IPMode: "hmac", CountryCode: "KN",
		Country: "Saint Kitts and Nevis", Outcome: "ok", StatusCode: 200,
		LookupLatencyUS: 42, MMDBVersion: "integration",
	}
	if err := publisher.Publish(ctx, event); err != nil {
		t.Fatalf("publish: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		count := queryClickHouse(t, clickhouseURL, clickhouseUser, clickhousePassword,
			fmt.Sprintf("SELECT count() FROM geotagger.audit_events WHERE request_id = '%s'", requestID))
		if strings.TrimSpace(count) == "1" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("audit event was not persisted in ClickHouse")
}

func assertStreamSafety(t *testing.T, natsURL string) {
	t.Helper()
	nc, err := nats.Connect(natsURL, nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	info, err := js.StreamInfo(integrationStream)
	if err != nil {
		t.Fatal(err)
	}
	if info.Config.MaxBytes != 8<<30 {
		t.Fatalf("MaxBytes=%d want=%d", info.Config.MaxBytes, int64(8<<30))
	}
	if info.Config.Discard != nats.DiscardNew {
		t.Fatalf("Discard=%v want DiscardNew", info.Config.Discard)
	}
	if info.Config.Retention != nats.WorkQueuePolicy {
		t.Fatalf("Retention=%v want WorkQueuePolicy", info.Config.Retention)
	}
}

func waitForClickHouse(t *testing.T, endpoint, user, password string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(endpoint, "/")+"/ping", nil)
		req.SetBasicAuth(user, password)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("ClickHouse did not become ready")
}

func execClickHouse(t *testing.T, endpoint, user, password, query string) {
	t.Helper()
	_ = clickhouseRequest(t, endpoint, user, password, query)
}

func queryClickHouse(t *testing.T, endpoint, user, password, query string) string {
	t.Helper()
	return clickhouseRequest(t, endpoint, user, password, query)
}

func clickhouseRequest(t *testing.T, endpoint, user, password, query string) string {
	t.Helper()
	values := url.Values{}
	values.Set("query", query)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(endpoint, "/")+"/?"+values.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("ClickHouse %s: %s", resp.Status, body)
	}
	return string(body)
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
