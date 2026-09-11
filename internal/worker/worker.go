package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

type Config struct {
	NATSURL       string
	Stream        string
	Subject       string
	Durable       string
	BatchSize     int
	FlushInterval time.Duration
	Sink          *ClickHouse
	Logger        *slog.Logger
}

type Worker struct {
	cfg Config
	nc  *nats.Conn
	js  nats.JetStreamContext
	sub *nats.Subscription
	ch  chan *nats.Msg
}

func New(cfg Config) (*Worker, error) {
	nc, err := nats.Connect(cfg.NATSURL, nats.Name("geotagger-audit-worker"), nats.MaxReconnects(-1), nats.ReconnectWait(500*time.Millisecond))
	if err != nil {
		return nil, err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, err
	}
	w := &Worker{cfg: cfg, nc: nc, js: js, ch: make(chan *nats.Msg, cfg.BatchSize*4)}
	sub, err := js.QueueSubscribe(cfg.Subject, "geotagger-audit-writers", func(msg *nats.Msg) {
		select {
		case w.ch <- msg:
		default:
			_ = msg.Nak()
		}
	}, nats.Durable(cfg.Durable), nats.ManualAck(), nats.AckExplicit(), nats.BindStream(cfg.Stream), nats.MaxAckPending(cfg.BatchSize*8))
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("subscribe: %w", err)
	}
	w.sub = sub
	return w, nil
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()
	batch := make([]*nats.Msg, 0, w.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		rows := make([][]byte, len(batch))
		for i, msg := range batch {
			rows[i] = msg.Data
		}
		insertCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := w.cfg.Sink.Insert(insertCtx, rows)
		cancel()
		if err != nil {
			w.cfg.Logger.Error("audit batch insert failed", "count", len(batch), "error", err)
			for _, msg := range batch {
				_ = msg.Nak()
			}
		} else {
			for _, msg := range batch {
				_ = msg.Ack()
			}
		}
		batch = batch[:0]
	}
	for {
		select {
		case msg := <-w.ch:
			batch = append(batch, msg)
			if len(batch) >= w.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			flush()
			return nil
		}
	}
}

func (w *Worker) Close() error {
	if w.sub != nil {
		_ = w.sub.Unsubscribe()
	}
	if w.nc != nil {
		return w.nc.Drain()
	}
	return nil
}
