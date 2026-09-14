package adminhttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Jstarzz/geotagger/internal/accessauth"
	"github.com/Jstarzz/geotagger/internal/audit"
	"github.com/Jstarzz/geotagger/internal/auth"
	"github.com/Jstarzz/geotagger/internal/geo"
	"github.com/Jstarzz/geotagger/internal/keystore"
)

//go:embed ui/*
var uiFiles embed.FS

type AccessVerifier interface {
	Verify(context.Context, string) (accessauth.Session, error)
}

type Config struct {
	Lookup      geo.Lookup
	Auth        *auth.Verifier
	Audit       audit.Publisher
	Keys        *keystore.Store
	Access      AccessVerifier
	Logger      *slog.Logger
	MaxBodySize int64
}

type Server struct {
	lookup      geo.Lookup
	auth        *auth.Verifier
	audit       audit.Publisher
	keys        *keystore.Store
	access      AccessVerifier
	logger      *slog.Logger
	maxBodySize int64
}

type sessionContextKey struct{}

type keyView struct {
	ID               string     `json:"id"`
	DisplayName      string     `json:"display_name,omitempty"`
	Owner            string     `json:"owner,omitempty"`
	Environment      string     `json:"environment,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	RotatedAt        *time.Time `json:"rotated_at,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevocationReason string     `json:"revocation_reason,omitempty"`
	Status           string     `json:"status"`
}

type createKeyRequest struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	Owner       string     `json:"owner"`
	Environment string     `json:"environment"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

type revokeKeyRequest struct {
	Reason string `json:"reason"`
}

func New(c Config) *Server {
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	maxBody := c.MaxBodySize
	if maxBody <= 0 {
		maxBody = 4096
	}
	return &Server{
		lookup: c.Lookup, auth: c.Auth, audit: c.Audit, keys: c.Keys,
		access: c.Access, logger: logger, maxBodySize: maxBody,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/", s.index)
	mux.HandleFunc("GET /admin/assets/app.js", s.script)
	mux.HandleFunc("GET /admin/assets/style.css", s.style)
	mux.HandleFunc("GET /admin/api/session", s.session)
	mux.HandleFunc("GET /admin/api/status", s.status)
	mux.HandleFunc("GET /admin/api/keys", s.listKeys)
	mux.HandleFunc("POST /admin/api/keys", s.createKey)
	mux.HandleFunc("POST /admin/api/keys/{id}/rotate", s.rotateKey)
	mux.HandleFunc("POST /admin/api/keys/{id}/revoke", s.revokeKey)
	return s.secureHeaders(s.requireAccess(mux))
}

func (s *Server) requireAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.access == nil || s.keys == nil || s.auth == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin control plane is not configured"})
			return
		}
		token := strings.TrimSpace(r.Header.Get("Cf-Access-Jwt-Assertion"))
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Cloudflare Access token required"})
			return
		}
		session, err := s.access.Verify(r.Context(), token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Cloudflare Access authorization failed"})
			return
		}
		ctx := context.WithValue(r.Context(), sessionContextKey{}, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Request-ID", requestID())
		next.ServeHTTP(w, r)
	})
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	serveEmbedded(w, "ui/index.html", "text/html; charset=utf-8")
}

func (s *Server) script(w http.ResponseWriter, _ *http.Request) {
	serveEmbedded(w, "ui/app.js", "text/javascript; charset=utf-8")
}

func (s *Server) style(w http.ResponseWriter, _ *http.Request) {
	serveEmbedded(w, "ui/style.css", "text/css; charset=utf-8")
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	session, _ := r.Context().Value(sessionContextKey{}).(accessauth.Session)
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC()
	counts := s.auth.ManagedCounts(now)
	writeJSON(w, http.StatusOK, map[string]any{
		"ready":              s.audit != nil && s.audit.Healthy() && s.keys.Healthy(),
		"audit_transport":    s.audit != nil && s.audit.Healthy(),
		"managed_key_store":  s.keys.Healthy(),
		"managed_keys":       counts,
		"mmdb_version":       s.lookup.Version(),
		"server_time":        now,
	})
}

func (s *Server) listKeys(w http.ResponseWriter, _ *http.Request) {
	records, err := s.keys.List()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "managed key store unavailable"})
		return
	}
	views := make([]keyView, 0, len(records))
	now := time.Now().UTC()
	for _, record := range records {
		views = append(views, view(record, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": views})
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	var req createKeyRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Owner = strings.TrimSpace(req.Owner)
	req.Environment = strings.TrimSpace(req.Environment)
	if err := validateMetadata(req.ID, req.DisplayName, req.Owner, req.Environment, req.ExpiresAt); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	record, token, err := s.keys.Create(keystore.CreateInput{
		ID: req.ID, DisplayName: req.DisplayName, Owner: req.Owner,
		Environment: req.Environment, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err := s.refreshVerifier(); err != nil {
		s.logger.Error("managed key created but local verifier refresh failed", "key_id", req.ID, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "key stored but verifier refresh failed; retry status before distributing token"})
		return
	}
	s.logMutation(r, "create", req.ID, "ok")
	writeJSON(w, http.StatusCreated, map[string]any{"key": view(record, time.Now().UTC()), "token": token})
}

func (s *Server) rotateKey(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := keystore.ValidateID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	record, token, err := s.keys.Rotate(id)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err := s.refreshVerifier(); err != nil {
		s.logger.Error("managed key rotated but local verifier refresh failed", "key_id", id, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "key rotated but verifier refresh failed; retry status before distributing token"})
		return
	}
	s.logMutation(r, "rotate", id, "ok")
	writeJSON(w, http.StatusOK, map[string]any{"key": view(record, time.Now().UTC()), "token": token})
}

func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := keystore.ValidateID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var req revokeKeyRequest
	if r.ContentLength != 0 {
		if !s.decodeJSON(w, r, &req) {
			return
		}
	}
	if len(req.Reason) > 256 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "revocation reason must be 256 characters or fewer"})
		return
	}
	record, err := s.keys.Revoke(id, req.Reason)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err := s.refreshVerifier(); err != nil {
		s.logger.Error("managed key revoked but local verifier refresh failed", "key_id", id, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "key revoked but verifier refresh failed"})
		return
	}
	s.logMutation(r, "revoke", id, "ok")
	writeJSON(w, http.StatusOK, map[string]any{"key": view(record, time.Now().UTC())})
}

func (s *Server) refreshVerifier() error {
	records, err := s.keys.List()
	if err != nil {
		return err
	}
	managed := make([]auth.ManagedKey, 0, len(records))
	for _, record := range records {
		managed = append(managed, auth.ManagedKey{
			ID: record.ID, DigestHex: record.SecretSHA256,
			ExpiresAt: record.ExpiresAt, RevokedAt: record.RevokedAt,
		})
	}
	return s.auth.ReplaceManaged(managed)
}

func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBodySize+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return false
	}
	if int64(len(body)) > s.maxBodySize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must contain one JSON object"})
		return false
	}
	return true
}

func mutationAllowed(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-GeoTagger-Admin-CSRF") != "1" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin CSRF header required"})
		return false
	}
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site != "" && site != "same-origin" && site != "same-site" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site admin mutation rejected"})
		return false
	}
	return true
}

func validateMetadata(id, displayName, owner, environment string, expiresAt *time.Time) error {
	if err := keystore.ValidateID(id); err != nil {
		return err
	}
	if len(displayName) > 128 || len(owner) > 128 || len(environment) > 64 {
		return errors.New("display_name/owner/environment exceeds maximum length")
	}
	if expiresAt != nil && !expiresAt.After(time.Now().UTC()) {
		return errors.New("expires_at must be in the future")
	}
	return nil
}

func view(record keystore.Record, now time.Time) keyView {
	status := "active"
	if record.RevokedAt != nil {
		status = "revoked"
	} else if record.ExpiresAt != nil && !now.Before(*record.ExpiresAt) {
		status = "expired"
	}
	return keyView{
		ID: record.ID, DisplayName: record.DisplayName, Owner: record.Owner,
		Environment: record.Environment, CreatedAt: record.CreatedAt,
		RotatedAt: record.RotatedAt, ExpiresAt: record.ExpiresAt,
		RevokedAt: record.RevokedAt, RevocationReason: record.RevocationReason,
		Status: status,
	}
}

func (s *Server) logMutation(r *http.Request, operation, id, outcome string) {
	session, _ := r.Context().Value(sessionContextKey{}).(accessauth.Session)
	s.logger.Info("admin credential mutation", "admin_email", session.Email, "operation", operation, "key_id", id, "outcome", outcome)
}

func serveEmbedded(w http.ResponseWriter, path, contentType string) {
	body, err := uiFiles.ReadFile(path)
	if err != nil {
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
