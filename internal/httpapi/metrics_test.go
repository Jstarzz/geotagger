package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLatencyHistograms(t *testing.T) {
	var m Metrics
	m.ObserveLookup(50)
	m.ObserveLookup(2000)
	m.ObserveLookup(60000)
	m.ObserveAuditPublish(75)
	m.ObserveAuditPublish(3000)
	m.ObserveRequest(900)
	m.ObserveRequest(120000)

	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	body := rr.Body.String()

	for _, want := range []string{
		`geotagger_lookup_latency_microseconds_bucket{le="100"} 1`,
		`geotagger_lookup_latency_microseconds_bucket{le="2500"} 2`,
		`geotagger_lookup_latency_microseconds_bucket{le="+Inf"} 3`,
		`geotagger_lookup_latency_microseconds_sum 62050`,
		`geotagger_lookup_latency_microseconds_count 3`,
		`geotagger_audit_publish_latency_microseconds_bucket{le="100"} 1`,
		`geotagger_audit_publish_latency_microseconds_bucket{le="5000"} 2`,
		`geotagger_audit_publish_latency_microseconds_sum 3075`,
		`geotagger_audit_publish_latency_microseconds_count 2`,
		`geotagger_request_latency_microseconds_bucket{le="1000"} 1`,
		`geotagger_request_latency_microseconds_bucket{le="250000"} 2`,
		`geotagger_request_latency_microseconds_sum 120900`,
		`geotagger_request_latency_microseconds_count 2`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}
