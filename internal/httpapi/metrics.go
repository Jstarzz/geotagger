package httpapi

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

var lookupLatencyBuckets = [...]uint64{100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000}
var auditPublishLatencyBuckets = [...]uint64{100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000}
var requestLatencyBuckets = [...]uint64{500, 1000, 2500, 5000, 10000, 25000, 50000, 100000, 250000, 500000, 1000000, 2500000}

type Metrics struct {
	requests             atomic.Uint64
	authFailures         atomic.Uint64
	lookupFailures       atomic.Uint64
	auditFailures        atomic.Uint64
	lookupUS             atomic.Uint64
	lookupCount          atomic.Uint64
	lookupLatencyBuckets [len(lookupLatencyBuckets)]atomic.Uint64
	auditPublishUS       atomic.Uint64
	auditPublishCount    atomic.Uint64
	auditPublishBuckets  [len(auditPublishLatencyBuckets)]atomic.Uint64
	requestUS            atomic.Uint64
	requestCount         atomic.Uint64
	requestBuckets       [len(requestLatencyBuckets)]atomic.Uint64
	inflight             atomic.Int64
}

func observe(us uint64, upperBounds []uint64, buckets []atomic.Uint64) {
	for i, upper := range upperBounds {
		if us <= upper {
			buckets[i].Add(1)
		}
	}
}

func (m *Metrics) ObserveLookup(us uint64) {
	m.lookupUS.Add(us)
	m.lookupCount.Add(1)
	observe(us, lookupLatencyBuckets[:], m.lookupLatencyBuckets[:])
}

func (m *Metrics) ObserveAuditPublish(us uint64) {
	m.auditPublishUS.Add(us)
	m.auditPublishCount.Add(1)
	observe(us, auditPublishLatencyBuckets[:], m.auditPublishBuckets[:])
}

func (m *Metrics) ObserveRequest(us uint64) {
	m.requestUS.Add(us)
	m.requestCount.Add(1)
	observe(us, requestLatencyBuckets[:], m.requestBuckets[:])
}

func writeHistogram(w http.ResponseWriter, name string, upperBounds []uint64, buckets []atomic.Uint64, count, sum uint64) {
	_, _ = fmt.Fprintf(w, "# TYPE %s histogram\n", name)
	for i, upper := range upperBounds {
		_, _ = fmt.Fprintf(w, "%s_bucket{le=\"%d\"} %d\n", name, upper, buckets[i].Load())
	}
	_, _ = fmt.Fprintf(w,
		"%s_bucket{le=\"+Inf\"} %d\n%s_sum %d\n%s_count %d\n",
		name, count, name, sum, name, count)
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

		lookupCount := m.lookupCount.Load()
		writeHistogram(w, "geotagger_lookup_latency_microseconds", lookupLatencyBuckets[:], m.lookupLatencyBuckets[:], lookupCount, m.lookupUS.Load())

		auditCount := m.auditPublishCount.Load()
		writeHistogram(w, "geotagger_audit_publish_latency_microseconds", auditPublishLatencyBuckets[:], m.auditPublishBuckets[:], auditCount, m.auditPublishUS.Load())

		requestCount := m.requestCount.Load()
		writeHistogram(w, "geotagger_request_latency_microseconds", requestLatencyBuckets[:], m.requestBuckets[:], requestCount, m.requestUS.Load())

		_, _ = fmt.Fprintf(w, "# TYPE geotagger_inflight_requests gauge\ngeotagger_inflight_requests %d\n", m.inflight.Load())
	})
}
