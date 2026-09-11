package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"time"

	"github.com/Jstarzz/geotagger/internal/audit"
	"github.com/Jstarzz/geotagger/internal/auth"
	"github.com/Jstarzz/geotagger/internal/geo"
)

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

func New(c Config) *Server {
	return &Server{lookup: c.Lookup, auth: c.Auth, audit: c.Audit, auditTimeout: c.AuditTimeout,
		auditIPMode: c.AuditIPMode, auditHMACKey: c.AuditHMACKey, maxBodyBytes: c.MaxBodyBytes,
		allowPrivateIPs: c.AllowPrivateIPs}
}

func (s *Server) APIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/country", s.country)
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
		s.metrics.requests.Add(1)
		s.metrics.inflight.Add(1)
		defer s.metrics.inflight.Add(-1)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) country(w http.ResponseWriter, r *http.Request) {
	caller, err := s.auth.VerifyAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		s.metrics.authFailures.Add(1)
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if int64(len(body)) > s.maxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	var req struct {
		IP string `json:"ip"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.IP == "" {
		s.publishFailureAudit(r.Context(), caller, netip.Addr{}, "invalid_request", http.StatusBadRequest, 0)
		writeError(w, http.StatusBadRequest, "body must be JSON with an ip field")
		return
	}
	if err := ensureEOF(dec); err != nil {
		s.publishFailureAudit(r.Context(), caller, netip.Addr{}, "invalid_request", http.StatusBadRequest, 0)
		writeError(w, http.StatusBadRequest, "body must contain one JSON object")
		return
	}

	ip, err := netip.ParseAddr(req.IP)
	if err != nil {
		s.publishFailureAudit(r.Context(), caller, netip.Addr{}, "invalid_ip", http.StatusUnprocessableEntity, 0)
		writeError(w, http.StatusUnprocessableEntity, "invalid IP address")
		return
	}
	ip = ip.Unmap()
	if !s.allowPrivateIPs && !publicTarget(ip) {
		s.publishFailureAudit(r.Context(), caller, ip, "disallowed_ip", http.StatusUnprocessableEntity, 0)
		writeError(w, http.StatusUnprocessableEntity, "IP address is not a permitted public address")
		return
	}

	started := time.Now()
	result, found, err := s.lookup.Lookup(ip)
	latencyUS := uint64(time.Since(started).Microseconds())
	s.metrics.lookupUS.Add(latencyUS)
	s.metrics.lookupCount.Add(1)
	if err != nil {
		s.metrics.lookupFailures.Add(1)
		if !s.publishAudit(r.Context(), caller, ip, geo.Result{}, "lookup_error", http.StatusInternalServerError, latencyUS) {
			writeError(w, http.StatusServiceUnavailable, "audit transport unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if !found {
		if !s.publishAudit(r.Context(), caller, ip, geo.Result{}, "not_found", http.StatusNotFound, latencyUS) {
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

func (s *Server) publishFailureAudit(parent context.Context, caller string, ip netip.Addr, outcome string, status int, latencyUS uint64) {
	_ = s.publishAudit(parent, caller, ip, geo.Result{}, outcome, status, latencyUS)
}

func (s *Server) publishAudit(parent context.Context, caller string, ip netip.Addr, result geo.Result, outcome string, status int, latencyUS uint64) bool {
	ctx, cancel := context.WithTimeout(parent, s.auditTimeout)
	defer cancel()
	event := audit.Event{
		Timestamp: time.Now().UTC(), RequestID: requestID(), CallerID: caller,
		IPMode: s.auditIPMode, CountryCode: result.CountryCode, Country: result.Country,
		Outcome: outcome, StatusCode: uint16(status), LookupLatencyUS: latencyUS, MMDBVersion: s.lookup.Version(),
	}
	if ip.IsValid() {
		event.IPValue = audit.IPValue(s.auditIPMode, s.auditHMACKey, ip)
	}
	if err := s.audit.Publish(ctx, event); err != nil {
		s.metrics.auditFailures.Add(1)
		return false
	}
	return true
}

func publicTarget(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
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
