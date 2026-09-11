package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifier(t *testing.T) {
	sum := sha256.Sum256([]byte("secret"))
	v, err := NewVerifier("azure-api:" + hex.EncodeToString(sum[:]))
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
