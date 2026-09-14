package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/Jstarzz/geotagger/internal/audit"
	"github.com/Jstarzz/geotagger/internal/auth"
	"github.com/Jstarzz/geotagger/internal/geo"
)

type requestIDContextKey struct{}

type Server struct {
	lookup          geo.Lookup
	auth            *auth.Verifier
	audit           audit.Publisher
	auditTimeout    time.Duration
	auditIPMode     string
	auditHMACKey    []byte
	maxBodyBytes    int64
	allowPrivateIPs bool
	metrics         Metrics
}

type Config struct {
	Lookup          geo.Lookup
	Auth            *auth.Verifier
	Audit           audit.Publisher
	AuditTimeout    time.Duration
	AuditIPMode     string
	AuditHMACKey    []byte
	MaxBodyBytes    int64
	AllowPrivateIPs bool
}

type lookupResponse struct {
	IP        string `json:"ip"`
	Network   string `json:"network,omitempty"`
	Continent struct {
		Code string `json:"code,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"continent"`
	Country struct {
		Code string `json:"code,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"country"`
	Region struct {
		Code string `json:"code,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"region"`
	City       string `json:"city,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Location   struct {
		Latitude         *float64 `json:"latitude,omitempty"`
		Longitude        *float64 `json:"longitude,omitempty"`
		AccuracyRadiusKM uint16   `json:"accuracy_radius_km,omitempty"`
		TimeZone         string   `json:"timezone,omitempty"`
	} `json:"location"`
	ASN struct {
		Number       uint32 `json:"number,omitempty"`
		Organization string `json:"organization,omitempty"`
	} `json:"asn"`
	Classification struct {
		Public    bool   `json:"public"`
		Private   bool   `json:"private"`
		Loopback  bool   `json:"loopback"`
		LinkLocal bool   `json:"link_local"`
		IPVersion string `json:"ip_version"`
	} `json:"classification"`
	Source struct {
		CityDatabase string `json:"city_database"`
		ASNDatabase  string `json:"asn_database"`
	} `json:"source"`
	LookupLatencyUS uint64 `json:"lookup_latency_us"`
	RequestID       string `json:"request_id"`
}

func New(c Config) *Server {
	return &Server{lookup: c.Lookup, auth: c.Auth, audit: c.Audit, auditTimeout: c.AuditTimeout,
		auditIPMode: c.AuditIPMode, auditHMACKey: c.AuditHMACKey, maxBodyBytes: c.MaxBodyBytes,
		allowPrivateIPs: c.AllowPrivateIPs}
}

func (s *Server) APIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/country", s.country)
	mux.HandleFunc("POST /v1/lookup", s.lookupPOST)
	mux.HandleFunc("GET /v1/lookup", s.lookupGET)
	mux.HandleFunc("GET /v1/me", s.me)
	return s.requestMiddleware(mux)
}

func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", s.metrics.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.audit.Healthy() {
			http.Error(w, "audit transport unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (s *Server) requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		s.metrics.requests.Add(1)
		s.metrics.inflight.Add(1)
		defer func() {
			s.metrics.inflight.Add(-1)
			s.metrics.ObserveRequest(uint64(time.Since(started).Microseconds()))
		}()

		id := requestID()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) country(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	ip, ok := s.ipFromJSON(w, r, caller)
	if !ok {
		return
	}
	result, latencyUS, ok := s.performLookup(w, r, caller, ip, "country")
	if !ok {
		return
	}
	if result.Country == "" {
		if !s.publishAudit(r.Context(), caller, ip, result, "not_found", http.StatusNotFound, latencyUS) {
			writeError(w, http.StatusServiceUnavailable, "audit transport unavailable")
			return
		}
		writeError(w, http.StatusNotFound, "country not found")
		return
	}
	if !s.publishAudit(r.Context(), caller, ip, result, "ok", http.StatusOK, latencyUS) {
		writeError(w, http.StatusServiceUnavailable, "audit transport unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"country": result.Country})
}

func (s *Server) lookupPOST(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	ip, ok := s.ipFromJSON(w, r, caller)
	if !ok {
		return
	}
	s.fullLookup(w, r, caller, ip)
}

func (s *Server) lookupGET(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("ip"))
	if raw == "" {
		s.publishFailureAudit(r.Context(), caller, netip.Addr{}, "invalid_request", http.StatusBadRequest, 0)
		writeError(w, http.StatusBadRequest, "ip query parameter is required")
		return
	}
	ip, ok := s.parseIP(w, r, caller, raw)
	if !ok {
		return
	}
	s.fullLookup(w, r, caller, ip)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	ip, err := clientIP(r)
	if err != nil {
		s.publishFailureAudit(r.Context(), caller, netip.Addr{}, "client_ip_unavailable", http.StatusBadRequest, 0)
		writeError(w, http.StatusBadRequest, "unable to determine caller IP address")
		return
	}
	if !s.allowPrivateIPs && !publicTarget(ip) {
		s.publishFailureAudit(r.Context(), caller, ip, "disallowed_ip", http.StatusUnprocessableEntity, 0)
		writeError(w, http.StatusUnprocessableEntity, "caller IP address is not a permitted public address")
		return
	}
	s.fullLookup(w, r, caller, ip)
}

func (s *Server) fullLookup(w http.ResponseWriter, r *http.Request, caller string, ip netip.Addr) {
	result, latencyUS, ok := s.performLookup(w, r, caller, ip, "lookup")
	if !ok {
		return
	}
	if !s.publishAudit(r.Context(), caller, ip, result, "ok", http.StatusOK, latencyUS) {
		writeError(w, http.StatusServiceUnavailable, "audit transport unavailable")
		return
	}
	writeJSON(w, http.StatusOK, buildLookupResponse(result, ip, latencyUS, requestIDFromContext(r.Context())))
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	caller, err := s.auth.VerifyAuthorization(r.Header.Get("Authorization"))
	if err == nil {
		return caller, true
	}
	s.metrics.authFailures.Add(1)
	// Do not parse or retain a target IP before the caller authenticates.
	s.publishFailureAudit(r.Context(), "unauthenticated", netip.Addr{}, "unauthorized", http.StatusUnauthorized, 0)
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthorized")
	return "", false
}

func (s *Server) ipFromJSON(w http.ResponseWriter, r *http.Request, caller string) (netip.Addr, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBodyBytes+1))
	if err != nil {
		s.publishFailureAudit(r.Context(), caller, netip.Addr{}, "invalid_request", http.StatusBadRequest, 0)
		writeError(w, http.StatusBadRequest, "invalid request body")
		return netip.Addr{}, false
	}
	if int64(len(body)) > s.maxBodyBytes {
		s.publishFailureAudit(r.Context(), caller, netip.Addr{}, "request_too_large", http.StatusRequestEntityTooLarge, 0)
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return netip.Addr{}, false
	}
	var req struct {
		IP string `json:"ip"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.IP == "" {
		s.publishFailureAudit(r.Context(), caller, netip.Addr{}, "invalid_request", http.StatusBadRequest, 0)
		writeError(w, http.StatusBadRequest, "body must be JSON with an ip field")
		return netip.Addr{}, false
	}
	if err := ensureEOF(dec); err != nil {
		s.publishFailureAudit(r.Context(), caller, netip.Addr{}, "invalid_request", http.StatusBadRequest, 0)
		writeError(w, http.StatusBadRequest, "body must contain one JSON object")
		return netip.Addr{}, false
	}
	return s.parseIP(w, r, caller, req.IP)
}

func (s *Server) parseIP(w http.ResponseWriter, r *http.Request, caller, raw string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		s.publishFailureAudit(r.Context(), caller, netip.Addr{}, "invalid_ip", http.StatusUnprocessableEntity, 0)
		writeError(w, http.StatusUnprocessableEntity, "invalid IP address")
		return netip.Addr{}, false
	}
	ip = ip.Unmap()
	if !s.allowPrivateIPs && !publicTarget(ip) {
		s.publishFailureAudit(r.Context(), caller, ip, "disallowed_ip", http.StatusUnprocessableEntity, 0)
		writeError(w, http.StatusUnprocessableEntity, "IP address is not a permitted public address")
		return netip.Addr{}, false
	}
	return ip, true
}

func (s *Server) performLookup(w http.ResponseWriter, r *http.Request, caller string, ip netip.Addr, kind string) (geo.Result, uint64, bool) {
	started := time.Now()
	result, found, err := s.lookup.Lookup(ip)
	latencyUS := uint64(time.Since(started).Microseconds())
	s.metrics.ObserveLookup(latencyUS)
	if err != nil {
		s.metrics.lookupFailures.Add(1)
		if !s.publishAudit(r.Context(), caller, ip, geo.Result{}, "lookup_error", http.StatusInternalServerError, latencyUS) {
			writeError(w, http.StatusServiceUnavailable, "audit transport unavailable")
			return geo.Result{}, latencyUS, false
		}
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return geo.Result{}, latencyUS, false
	}
	if !found {
		if !s.publishAudit(r.Context(), caller, ip, result, "not_found", http.StatusNotFound, latencyUS) {
			writeError(w, http.StatusServiceUnavailable, "audit transport unavailable")
			return geo.Result{}, latencyUS, false
		}
		if kind == "country" {
			writeError(w, http.StatusNotFound, "country not found")
		} else {
			writeError(w, http.StatusNotFound, "IP intelligence not found")
		}
		return geo.Result{}, latencyUS, false
	}
	return result, latencyUS, true
}

func buildLookupResponse(result geo.Result, ip netip.Addr, latencyUS uint64, requestID string) lookupResponse {
	var out lookupResponse
	out.IP = ip.String()
	out.Network = result.Network
	out.Continent.Code = result.ContinentCode
	out.Continent.Name = result.Continent
	out.Country.Code = result.CountryCode
	out.Country.Name = result.Country
	out.Region.Code = result.RegionCode
	out.Region.Name = result.Region
	out.City = result.City
	out.PostalCode = result.PostalCode
	out.Location.Latitude = result.Latitude
	out.Location.Longitude = result.Longitude
	out.Location.AccuracyRadiusKM = result.AccuracyRadiusKM
	out.Location.TimeZone = result.TimeZone
	out.ASN.Number = result.ASN
	out.ASN.Organization = result.ASNOrganization
	out.Classification.Public = publicTarget(ip)
	out.Classification.Private = ip.IsPrivate()
	out.Classification.Loopback = ip.IsLoopback()
	out.Classification.LinkLocal = ip.IsLinkLocalUnicast()
	if ip.Is4() {
		out.Classification.IPVersion = "IPv4"
	} else {
		out.Classification.IPVersion = "IPv6"
	}
	out.Source.CityDatabase = result.CityDatabaseVersion
	out.Source.ASNDatabase = result.ASNDatabaseVersion
	out.LookupLatencyUS = latencyUS
	out.RequestID = requestID
	return out
}

func clientIP(r *http.Request) (netip.Addr, error) {
	// Cloudflare Tunnel is the intended public ingress. Prefer its canonical
	// client-IP header, then the first X-Forwarded-For hop, then RemoteAddr for
	// trusted direct/internal testing.
	for _, raw := range []string{r.Header.Get("CF-Connecting-IP"), firstForwardedFor(r.Header.Get("X-Forwarded-For"))} {
		if raw == "" {
			continue
		}
		if ip, err := netip.ParseAddr(strings.TrimSpace(raw)); err == nil {
			return ip.Unmap(), nil
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return netip.Addr{}, err
	}
	return ip.Unmap(), nil
}

func firstForwardedFor(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return strings.TrimSpace(v)
}

func (s *Server) publishFailureAudit(parent context.Context, caller string, ip netip.Addr, outcome string, status int, latencyUS uint64) {
	_ = s.publishAudit(parent, caller, ip, geo.Result{}, outcome, status, latencyUS)
}

func (s *Server) publishAudit(parent context.Context, caller string, ip netip.Addr, result geo.Result, outcome string, status int, latencyUS uint64) bool {
	ctx, cancel := context.WithTimeout(parent, s.auditTimeout)
	defer cancel()
	event := audit.Event{
		Timestamp: time.Now().UTC(), RequestID: requestIDFromContext(parent), CallerID: caller,
		IPMode: s.auditIPMode, CountryCode: result.CountryCode, Country: result.Country,
		Outcome: outcome, StatusCode: uint16(status), LookupLatencyUS: latencyUS, MMDBVersion: s.auditMMDBVersion(result),
	}
	if ip.IsValid() {
		event.IPValue = audit.IPValue(s.auditIPMode, s.auditHMACKey, ip)
	}
	started := time.Now()
	err := s.audit.Publish(ctx, event)
	s.metrics.ObserveAuditPublish(uint64(time.Since(started).Microseconds()))
	if err != nil {
		s.metrics.auditFailures.Add(1)
		return false
	}
	return true
}

func (s *Server) auditMMDBVersion(result geo.Result) string {
	city := strings.TrimSpace(result.CityDatabaseVersion)
	asn := strings.TrimSpace(result.ASNDatabaseVersion)
	if city == "" && asn == "" {
		return s.lookup.Version()
	}
	return "city=" + city + ";asn=" + asn
}

func publicTarget(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

func requestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDContextKey{}).(string); ok && id != "" {
		return id
	}
	return requestID()
}

func requestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("extra JSON value")
	}
	return err
}
