package natsaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Jstarzz/geotagger/internal/audit"

	"github.com/nats-io/nats.go"
)

const auditStreamMaxBytes int64 = 8 << 30 // leave headroom on the 10 GiB JetStream PVC

type NATSPublisher struct {
	nc      *nats.Conn
	js      nats.JetStreamContext
	subject string
}

func NewNATSPublisher(url, stream, subject string) (*NATSPublisher, error) {
	nc, err := nats.Connect(url,
		nats.Name("geotagger-api"),
		nats.Timeout(2*time.Second),
		nats.ReconnectWait(500*time.Millisecond),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	if err := ensureAuditStream(js, stream, subject); err != nil {
		nc.Close()
		return nil, err
	}
	return &NATSPublisher{nc: nc, js: js, subject: subject}, nil
}

func ensureAuditStream(js nats.JetStreamContext, stream, subject string) error {
	info, err := js.StreamInfo(stream)
	if err != nil {
		if err != nats.ErrStreamNotFound {
			return fmt.Errorf("stream info: %w", err)
		}
		_, err = js.AddStream(&nats.StreamConfig{
			Name:              stream,
			Subjects:          []string{subject},
			Storage:           nats.FileStorage,
			Retention:         nats.WorkQueuePolicy,
			MaxConsumers:      -1,
			MaxMsgs:           -1,
			MaxMsgsPerSubject: -1,
			MaxBytes:          auditStreamMaxBytes,
			MaxAge:            0,
			Discard:           nats.DiscardNew,
			NoAck:             false,
		})
		if err == nil {
			return nil
		}
		// Multiple API pods can race to provision the stream on first boot.
		info, err = js.StreamInfo(stream)
		if err != nil {
			return fmt.Errorf("create stream: %w", err)
		}
	}

	// Memory-backed JetStream does not satisfy the durable-audit contract. Do
	// not silently continue or attempt an in-place storage migration: fail the
	// API startup/readiness path and require an explicit operator migration.
	if info.Config.Storage != nats.FileStorage {
		return fmt.Errorf("audit stream %q storage=%v; file storage is required", stream, info.Config.Storage)
	}

	// Reconcile the mutable safety contract on every API startup. A stale stream
	// from an older deployment must not silently weaken durability or retain ACKed
	// work forever. WorkQueue removes successfully ACKed messages; the only
	// configured capacity bound is MaxBytes, and DiscardNew makes that bound fail
	// closed instead of evicting older unpersisted audit events.
	cfg := info.Config
	changed := false
	if len(cfg.Subjects) != 1 || cfg.Subjects[0] != subject {
		cfg.Subjects = []string{subject}
		changed = true
	}
	if cfg.Retention != nats.WorkQueuePolicy {
		cfg.Retention = nats.WorkQueuePolicy
		changed = true
	}
	if cfg.MaxConsumers != -1 {
		cfg.MaxConsumers = -1
		changed = true
	}
	if cfg.MaxMsgs != -1 {
		cfg.MaxMsgs = -1
		changed = true
	}
	if cfg.MaxMsgsPerSubject != -1 {
		cfg.MaxMsgsPerSubject = -1
		changed = true
	}
	if cfg.MaxAge != 0 {
		cfg.MaxAge = 0
		changed = true
	}
	if cfg.MaxBytes != auditStreamMaxBytes {
		cfg.MaxBytes = auditStreamMaxBytes
		changed = true
	}
	if cfg.Discard != nats.DiscardNew {
		cfg.Discard = nats.DiscardNew
		changed = true
	}
	if cfg.NoAck {
		cfg.NoAck = false
		changed = true
	}
	if changed {
		if _, err := js.UpdateStream(&cfg); err != nil {
			return fmt.Errorf("reconcile audit stream durability contract: %w", err)
		}
	}
	return nil
}

func (p *NATSPublisher) Publish(ctx context.Context, event audit.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = p.js.Publish(p.subject, payload, nats.Context(ctx))
	return err
}

func (p *NATSPublisher) Healthy() bool { return p.nc != nil && p.nc.IsConnected() }
func (p *NATSPublisher) Close() error {
	if p.nc == nil {
		return nil
	}
	if err := p.nc.Drain(); err != nil {
		p.nc.Close()
		return err
	}
	return nil
}
