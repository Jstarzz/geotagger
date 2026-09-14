package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func TestVerifier(t *testing.T) {
	v, err := NewVerifier("azure-api:" + digest("secret"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := v.VerifyAuthorization("Bearer azure-api.secret")
	if err != nil {
		t.Fatal(err)
	}
	if id != "azure-api" {
		t.Fatalf("got %q", id)
	}
	if _, err := v.VerifyAuthorization("Bearer azure-api.wrong"); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestManagedKeyLifecycle(t *testing.T) {
	v, err := NewVerifier("break-glass:" + digest("static-secret"))
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(time.Hour)
	if err := v.ReplaceManaged([]ManagedKey{{ID: "ndhis-prod", DigestHex: digest("managed-secret"), ExpiresAt: &future}}); err != nil {
		t.Fatal(err)
	}
	if id, err := v.VerifyAuthorization("Bearer ndhis-prod.managed-secret"); err != nil || id != "ndhis-prod" {
		t.Fatalf("managed auth failed: id=%q err=%v", id, err)
	}
	if id, err := v.VerifyAuthorization("Bearer break-glass.static-secret"); err != nil || id != "break-glass" {
		t.Fatalf("static auth regressed: id=%q err=%v", id, err)
	}

	now := time.Now().UTC()
	if err := v.ReplaceManaged([]ManagedKey{{ID: "ndhis-prod", DigestHex: digest("managed-secret"), RevokedAt: &now}}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.VerifyAuthorization("Bearer ndhis-prod.managed-secret"); err == nil {
		t.Fatal("expected revoked managed key to be rejected")
	}
	counts := v.ManagedCounts(time.Now().UTC())
	if counts.Total != 1 || counts.Revoked != 1 || counts.Active != 0 {
		t.Fatalf("unexpected managed counts: %+v", counts)
	}
}

func TestManagedKeyExpiryAndCollision(t *testing.T) {
	v, err := NewVerifier("svc:" + digest("static-secret"))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := v.ReplaceManaged([]ManagedKey{{ID: "expired", DigestHex: digest("expired-secret"), ExpiresAt: &past}}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.VerifyAuthorization("Bearer expired.expired-secret"); err == nil {
		t.Fatal("expected expired key rejection")
	}
	if err := v.ReplaceManaged([]ManagedKey{{ID: "svc", DigestHex: digest("collision")}}); err == nil {
		t.Fatal("expected managed/static id collision to fail")
	}
}
