package httpapi

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

type Metrics struct {
	requests       atomic.Uint64
	authFailures   atomic.Uint64
	lookupFailures atomic.Uint64
	auditFailures  atomic.Uint64
	lookupUS       atomic.Uint64
	lookupCount    atomic.Uint64
	inflight       atomic.Int64
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w,
			"# TYPE geotagger_requests_total counter\ngeotagger_requests_total %d\n"+
				"# TYPE geotagger_auth_failures_total counter\ngeotagger_auth_failures_total %d\n"+
				"# TYPE geotagger_lookup_failures_total counter\ngeotagger_lookup_failures_total %d\n"+
				"# TYPE geotagger_audit_failures_total counter\ngeotagger_audit_failures_total %d\n"+
				"# TYPE geotagger_lookup_latency_microseconds_sum counter\ngeotagger_lookup_latency_microseconds_sum %d\n"+
				"# TYPE geotagger_lookup_latency_microseconds_count counter\ngeotagger_lookup_latency_microseconds_count %d\n"+
				"# TYPE geotagger_inflight_requests gauge\ngeotagger_inflight_requests %d\n",
			m.requests.Load(), m.authFailures.Load(), m.lookupFailures.Load(), m.auditFailures.Load(),
			m.lookupUS.Load(), m.lookupCount.Load(), m.inflight.Load())
	})
}
