package accessauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrUnauthorized = errors.New("cloudflare access authorization failed")

type Session struct {
	Email     string    `json:"email"`
	Subject   string    `json:"subject,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Verifier struct {
	teamDomain string
	audience   string
	issuer     string
	certURL    string
	client     *http.Client

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	keysUntil time.Time
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type jwtClaims struct {
	Audience json.RawMessage `json:"aud"`
	Email    string          `json:"email"`
	Subject  string          `json:"sub"`
	Issuer   string          `json:"iss"`
	Expires  int64           `json:"exp"`
	NotBefore int64          `json:"nbf"`
}

type jwksResponse struct {
	Keys []struct {
		KID string `json:"kid"`
		KTY string `json:"kty"`
		ALG string `json:"alg"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func New(teamDomain, audience string) (*Verifier, error) {
	teamDomain = strings.TrimSpace(teamDomain)
	audience = strings.TrimSpace(audience)
	if teamDomain == "" || audience == "" {
		return nil, errors.New("Cloudflare Access team domain and audience are required")
	}
	teamDomain = strings.TrimPrefix(teamDomain, "https://")
	teamDomain = strings.TrimSuffix(teamDomain, "/")
	if strings.Contains(teamDomain, "/") || strings.Contains(teamDomain, ":") || !strings.HasSuffix(strings.ToLower(teamDomain), ".cloudflareaccess.com") {
		return nil, errors.New("CF_ACCESS_TEAM_DOMAIN must be a <team>.cloudflareaccess.com hostname")
	}
	issuer := "https://" + teamDomain
	return &Verifier{
		teamDomain: teamDomain,
		audience:   audience,
		issuer:     issuer,
		certURL:    issuer + "/cdn-cgi/access/certs",
		client:     &http.Client{Timeout: 3 * time.Second},
		keys:       make(map[string]*rsa.PublicKey),
	}, nil
}

func (v *Verifier) Verify(ctx context.Context, token string) (Session, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Session{}, ErrUnauthorized
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Session{}, ErrUnauthorized
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Alg != "RS256" || header.Kid == "" {
		return Session{}, ErrUnauthorized
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Session{}, ErrUnauthorized
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return Session{}, ErrUnauthorized
	}
	if claims.Issuer != v.issuer || claims.Expires == 0 || claims.Email == "" || !audienceContains(claims.Audience, v.audience) {
		return Session{}, ErrUnauthorized
	}

	now := time.Now().UTC()
	const skew = 30 * time.Second
	if now.After(time.Unix(claims.Expires, 0).UTC().Add(skew)) {
		return Session{}, ErrUnauthorized
	}
	if claims.NotBefore != 0 && now.Add(skew).Before(time.Unix(claims.NotBefore, 0).UTC()) {
		return Session{}, ErrUnauthorized
	}

	key, err := v.key(ctx, header.Kid, false)
	if err != nil {
		key, err = v.key(ctx, header.Kid, true)
		if err != nil {
			return Session{}, ErrUnauthorized
		}
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Session{}, ErrUnauthorized
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		// Key rotation can race the local cache. Refresh once before rejecting.
		fresh, refreshErr := v.key(ctx, header.Kid, true)
		if refreshErr != nil || rsa.VerifyPKCS1v15(fresh, crypto.SHA256, sum[:], sig) != nil {
			return Session{}, ErrUnauthorized
		}
	}
	return Session{Email: claims.Email, Subject: claims.Subject, ExpiresAt: time.Unix(claims.Expires, 0).UTC()}, nil
}

func (v *Verifier) key(ctx context.Context, kid string, force bool) (*rsa.PublicKey, error) {
	if !force {
		v.mu.RLock()
		key := v.keys[kid]
		valid := time.Now().Before(v.keysUntil)
		v.mu.RUnlock()
		if key != nil && valid {
			return key, nil
		}
	}
	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	v.mu.RLock()
	key := v.keys[kid]
	v.mu.RUnlock()
	if key == nil {
		return nil, fmt.Errorf("Access signing key %q not found", kid)
	}
	return key, nil
}

func (v *Verifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.certURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch Cloudflare Access certs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("fetch Cloudflare Access certs: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var document jwksResponse
	if err := json.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("decode Cloudflare Access JWKS: %w", err)
	}
	next := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, jwk := range document.Keys {
		if jwk.KID == "" || jwk.KTY != "RSA" || (jwk.ALG != "" && jwk.ALG != "RS256") || jwk.N == "" || jwk.E == "" {
			continue
		}
		key, err := rsaKey(jwk.N, jwk.E)
		if err != nil {
			continue
		}
		next[jwk.KID] = key
	}
	if len(next) == 0 {
		return errors.New("Cloudflare Access JWKS contained no usable RSA keys")
	}
	ttl := cacheTTL(resp.Header.Get("Cache-Control"))
	v.mu.Lock()
	v.keys = next
	v.keysUntil = time.Now().Add(ttl)
	v.mu.Unlock()
	return nil
}

func rsaKey(nEncoded, eEncoded string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nEncoded)
	if err != nil || len(nBytes) == 0 {
		return nil, errors.New("invalid RSA modulus")
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eEncoded)
	if err != nil || len(eBytes) == 0 || len(eBytes) > 4 {
		return nil, errors.New("invalid RSA exponent")
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e < 3 {
		return nil, errors.New("invalid RSA exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

func audienceContains(raw json.RawMessage, want string) bool {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == want
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return false
	}
	for _, aud := range many {
		if aud == want {
			return true
		}
	}
	return false
}

func cacheTTL(value string) time.Duration {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "max-age=") {
			continue
		}
		seconds, err := strconv.Atoi(strings.TrimPrefix(part, "max-age="))
		if err == nil && seconds > 0 {
			if seconds > 6*60*60 {
				seconds = 6 * 60 * 60
			}
			return time.Duration(seconds) * time.Second
		}
	}
	return time.Hour
}

// CertURL is exposed for deterministic tests without weakening runtime URL
// validation. Tests may replace the verifier's HTTP transport instead.
func (v *Verifier) CertURL() *url.URL {
	u, _ := url.Parse(v.certURL)
	return u
}
