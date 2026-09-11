package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Jstarzz/geotagger/internal/audit"
	"github.com/Jstarzz/geotagger/internal/auth"
	"github.com/Jstarzz/geotagger/internal/geo"
)

type fakeLookup struct{}

func (fakeLookup) Lookup(ip netip.Addr) (geo.Result, bool, error) {
	if ip.String() == "8.8.8.8" {
		return geo.Result{Country: "United States", CountryCode: "US"}, true, nil
	}
	return geo.Result{}, false, nil
}
func (fakeLookup) Version() string { return "test" }
func (fakeLookup) Close() error    { return nil }

type fakeAudit struct{ events []audit.Event }

func (f *fakeAudit) Publish(_ context.Context, e audit.Event) error {
	f.events = append(f.events, e)
	return nil
}
func (f *fakeAudit) Healthy() bool { return true }
func (f *fakeAudit) Close() error  { return nil }

func testServer(t *testing.T) (*Server, *fakeAudit) {
	t.Helper()
	sum := sha256.Sum256([]byte("secret"))
	v, err := auth.NewVerifier("svc:" + hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	a := &fakeAudit{}
	return New(Config{Lookup: fakeLookup{}, Auth: v, Audit: a, AuditTimeout: time.Second, AuditIPMode: "hmac", AuditHMACKey: []byte("hash-key"), MaxBodyBytes: 1024}), a
}

func TestCountry(t *testing.T) {
	s, a := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/country", strings.NewReader(`{"ip":"8.8.8.8"}`))
	req.Header.Set("Authorization", "Bearer svc.secret")
	rr := httptest.NewRecorder()
	s.APIHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["country"] != "United States" {
		t.Fatalf("country=%q", got["country"])
	}
	if len(a.events) != 1 || a.events[0].IPValue == "8.8.8.8" {
		t.Fatalf("audit IP was not HMAC protected: %#v", a.events)
	}
	requestID := rr.Header().Get("X-Request-ID")
	if requestID == "" {
		t.Fatal("missing X-Request-ID")
	}
	if a.events[0].RequestID != requestID {
		t.Fatalf("audit request_id=%q response request_id=%q", a.events[0].RequestID, requestID)
	}
}

func TestCountryRejectsUnauthorizedAndAudits(t *testing.T) {
	s, a := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/country", strings.NewReader(`{"ip":"8.8.8.8"}`))
	rr := httptest.NewRecorder()
	s.APIHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rr.Code)
	}
	if len(a.events) != 1 {
		t.Fatalf("events=%d", len(a.events))
	}
	if a.events[0].CallerID != "unauthenticated" || a.events[0].Outcome != "unauthorized" {
		t.Fatalf("unexpected audit event: %#v", a.events[0])
	}
	if a.events[0].IPValue != "" {
		t.Fatalf("unauthorized request should not parse/store target IP: %#v", a.events[0])
	}
	if a.events[0].RequestID != rr.Header().Get("X-Request-ID") {
		t.Fatalf("audit request ID mismatch: %#v", a.events[0])
	}
}

func TestCountryRejectsPrivateIP(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/country", strings.NewReader(`{"ip":"10.0.0.1"}`))
	req.Header.Set("Authorization", "Bearer svc.secret")
	rr := httptest.NewRecorder()
	s.APIHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d", rr.Code)
	}
}
