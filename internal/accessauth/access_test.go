package accessauth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVerifyAccessJWT(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kid := "test-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kid": kid, "kty": "RSA", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
		}}})
	}))
	defer server.Close()

	v, err := New("unit-test.cloudflareaccess.com", "aud-123")
	if err != nil {
		t.Fatal(err)
	}
	v.certURL = server.URL

	now := time.Now().UTC()
	token := signedToken(t, privateKey, kid, map[string]any{
		"iss": "https://unit-test.cloudflareaccess.com",
		"aud": []string{"aud-123"},
		"email": "admin@example.com",
		"sub": "subject-1",
		"nbf": now.Add(-time.Minute).Unix(),
		"exp": now.Add(time.Hour).Unix(),
	})
	session, err := v.Verify(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if session.Email != "admin@example.com" || session.Subject != "subject-1" {
		t.Fatalf("unexpected session: %+v", session)
	}
}

func TestVerifyAccessJWTRejectsWrongAudienceAndExpiry(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kid := "test-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kid": kid, "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
		}}})
	}))
	defer server.Close()
	v, err := New("unit-test.cloudflareaccess.com", "expected")
	if err != nil {
		t.Fatal(err)
	}
	v.certURL = server.URL
	now := time.Now().UTC()

	wrongAudience := signedToken(t, privateKey, kid, map[string]any{
		"iss": "https://unit-test.cloudflareaccess.com", "aud": "wrong",
		"email": "admin@example.com", "exp": now.Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(t.Context(), wrongAudience); err == nil {
		t.Fatal("expected wrong audience rejection")
	}

	expired := signedToken(t, privateKey, kid, map[string]any{
		"iss": "https://unit-test.cloudflareaccess.com", "aud": "expected",
		"email": "admin@example.com", "exp": now.Add(-time.Hour).Unix(),
	})
	if _, err := v.Verify(t.Context(), expired); err == nil {
		t.Fatal("expected expired token rejection")
	}
}

func signedToken(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	payload, _ := json.Marshal(claims)
	h := base64.RawURLEncoding.EncodeToString(header)
	p := base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(h + "." + p))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sig)
}
