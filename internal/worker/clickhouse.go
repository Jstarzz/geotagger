package worker

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ClickHouse struct {
	endpoint string
	user     string
	password string
	client   *http.Client
}

func NewClickHouse(endpoint, user, password string) *ClickHouse {
	return &ClickHouse{endpoint: strings.TrimRight(endpoint, "/"), user: user, password: password,
		client: &http.Client{Timeout: 10 * time.Second}}
}

func (c *ClickHouse) Insert(ctx context.Context, rows [][]byte) error {
	if len(rows) == 0 {
		return nil
	}

	// Batch sizes are bounded by worker configuration. Pre-sizing avoids the
	// repeated buffer growth/copy cycle on every ClickHouse flush.
	bodyBytes := len(rows) // one newline per row
	for _, row := range rows {
		bodyBytes += len(row)
	}
	var body bytes.Buffer
	body.Grow(bodyBytes)
	for _, row := range rows {
		body.Write(row)
		body.WriteByte('\n')
	}

	q := url.Values{}
	q.Set("query", "INSERT INTO geotagger.audit_events FORMAT JSONEachRow")
	q.Set("date_time_input_format", "best_effort")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/?"+q.Encode(), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("clickhouse returned %s", resp.Status)
	}
	return nil
}
