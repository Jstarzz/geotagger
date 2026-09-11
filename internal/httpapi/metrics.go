package httpapi

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

var lookupLatencyBuckets = [...]uint64{100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000}

type Metrics struct {
	requests             atomic.Uint64
	authFailures         atomic.Uint64
	lookupFailures       atomic.Uint64
	auditFailures        atomic.Uint64
	lookupUS             atomic.Uint64
	lookupCount          atomic.Uint64
	lookupLatencyBuckets [len(lookupLatencyBuckets)]atomic.Uint64
	inflight             atomic.Int64
}

func (m *Metrics) ObserveLookup(us uint64) {
	m.lookupUS.Add(us)
	m.lookupCount.Add(1)
	for i, upper := range lookupLatencyBuckets {
		if us <= upper {
			m.lookupLatencyBuckets[i].Add(1)
		}
	}
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w,
			"# TYPE geotagger_requests_total counter\ngeotagger_requests_total %d\n"+
				"# TYPE geotagger_auth_failures_total counter\ngeotagger_auth_failures_total %d\n"+
				"# TYPE geotagger_lookup_failures_total counter\ngeotagger_lookup_failures_total %d\n"+
				"# TYPE geotagger_audit_failures_total counter\ngeotagger_audit_failures_total %d\n",
			m.requests.Load(), m.authFailures.Load(), m.lookupFailures.Load(), m.auditFailures.Load())

		_, _ = fmt.Fprintln(w, "# TYPE geotagger_lookup_latency_microseconds histogram")
		for i, upper := range lookupLatencyBuckets {
			_, _ = fmt.Fprintf(w, "geotagger_lookup_latency_microseconds_bucket{le=\"%d\"} %d\n",
				upper, m.lookupLatencyBuckets[i].Load())
		}
		count := m.lookupCount.Load()
		_, _ = fmt.Fprintf(w,
			"geotagger_lookup_latency_microseconds_bucket{le=\"+Inf\"} %d\n"+
				"geotagger_lookup_latency_microseconds_sum %d\n"+
				"geotagger_lookup_latency_microseconds_count %d\n"+
				"# TYPE geotagger_inflight_requests gauge\ngeotagger_inflight_requests %d\n",
			count, m.lookupUS.Load(), count, m.inflight.Load())
	})
}
