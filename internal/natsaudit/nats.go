package natsaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Jstarzz/geotagger/internal/audit"

	"github.com/nats-io/nats.go"
)

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
	if _, err := js.StreamInfo(stream); err != nil {
		if err != nats.ErrStreamNotFound {
			nc.Close()
			return nil, fmt.Errorf("stream info: %w", err)
		}
		if _, err := js.AddStream(&nats.StreamConfig{
			Name:      stream,
			Subjects:  []string{subject},
			Storage:   nats.FileStorage,
			Retention: nats.WorkQueuePolicy,
			MaxAge:    7 * 24 * time.Hour,
		}); err != nil {
			// Multiple API pods can race to provision the stream on first boot.
			if _, checkErr := js.StreamInfo(stream); checkErr != nil {
				nc.Close()
				return nil, fmt.Errorf("create stream: %w", err)
			}
		}
	}
	return &NATSPublisher{nc: nc, js: js, subject: subject}, nil
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
