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
			Name:      stream,
			Subjects:  []string{subject},
			Storage:   nats.FileStorage,
			Retention: nats.WorkQueuePolicy,
			MaxBytes:  auditStreamMaxBytes,
			Discard:   nats.DiscardNew,
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

	// Reconcile the mutable safety settings on startup. Unacknowledged audit
	// events must never disappear solely because they are old: capacity pressure
	// is handled fail-closed with MaxBytes + DiscardNew instead.
	cfg := info.Config
	changed := false
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
	if changed {
		if _, err := js.UpdateStream(&cfg); err != nil {
			return fmt.Errorf("update audit stream safety limits: %w", err)
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
