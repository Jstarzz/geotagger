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
	if ip.String() == "8.8.8.8" || ip.String() == "76.76.170.47" {
		lat, lon := 17.3026, -62.7177
		country, code := "United States", "US"
		city, region, regionCode, tz := "Mountain View", "California", "CA", "America/Los_Angeles"
		asn, org := uint32(15169), "Google LLC"
		if ip.String() == "76.76.170.47" {
			country, code = "Saint Kitts and Nevis", "KN"
			city, region, regionCode, tz = "Basseterre", "Saint George Basseterre", "03", "America/St_Kitts"
			asn, org = 11139, "Cable & Wireless"
		}
		return geo.Result{
			IP: ip.String(), Network: "76.76.168.0/22", ContinentCode: "NA", Continent: "North America",
			Country: country, CountryCode: code, Region: region, RegionCode: regionCode, City: city,
			Latitude: &lat, Longitude: &lon, AccuracyRadiusKM: 50, TimeZone: tz,
			ASN: asn, ASNOrganization: org, CityDatabaseVersion: "GeoLite2-City@test", ASNDatabaseVersion: "GeoLite2-ASN@test",
		}, true, nil
	}
	return geo.Result{}, false, nil
}
func (fakeLookup) Version() string { return "city=test;asn=test" }
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

func TestFullLookupPOST(t *testing.T) {
	s, a := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/lookup", strings.NewReader(`{"ip":"8.8.8.8"}`))
	req.Header.Set("Authorization", "Bearer svc.secret")
	rr := httptest.NewRecorder()
	s.APIHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got lookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Country.Code != "US" || got.ASN.Number != 15169 || got.City == "" {
		t.Fatalf("unexpected response: %#v", got)
	}
	if got.RequestID == "" || got.RequestID != rr.Header().Get("X-Request-ID") {
		t.Fatalf("request ID mismatch: body=%q header=%q", got.RequestID, rr.Header().Get("X-Request-ID"))
	}
	if got.Source.CityDatabase == "" || got.Source.ASNDatabase == "" {
		t.Fatalf("missing source metadata: %#v", got.Source)
	}
	if len(a.events) != 1 {
		t.Fatalf("events=%d", len(a.events))
	}
}

func TestFullLookupGET(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/lookup?ip=8.8.8.8", nil)
	req.Header.Set("Authorization", "Bearer svc.secret")
	rr := httptest.NewRecorder()
	s.APIHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMeUsesCloudflareClientIP(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer svc.secret")
	req.Header.Set("CF-Connecting-IP", "76.76.170.47")
	rr := httptest.NewRecorder()
	s.APIHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got lookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.IP != "76.76.170.47" || got.Country.Code != "KN" {
		t.Fatalf("unexpected /me response: %#v", got)
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
