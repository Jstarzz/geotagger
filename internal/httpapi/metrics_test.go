package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLookupLatencyHistogram(t *testing.T) {
	var m Metrics
	m.ObserveLookup(50)
	m.ObserveLookup(2000)
	m.ObserveLookup(60000)

	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	body := rr.Body.String()

	for _, want := range []string{
		`geotagger_lookup_latency_microseconds_bucket{le="100"} 1`,
		`geotagger_lookup_latency_microseconds_bucket{le="2500"} 2`,
		`geotagger_lookup_latency_microseconds_bucket{le="+Inf"} 3`,
		`geotagger_lookup_latency_microseconds_sum 62050`,
		`geotagger_lookup_latency_microseconds_count 3`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}
