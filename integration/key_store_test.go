//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/Jstarzz/geotagger/internal/auth"
	"github.com/Jstarzz/geotagger/internal/keystore"
	"github.com/nats-io/nats.go"
)

func TestManagedKeyStoreLifecycleAndVerifierSync(t *testing.T) {
	natsURL := getenv("INTEGRATION_NATS_URL", "nats://127.0.0.1:4222")
	const bucket = "GEOTAGGER_API_KEYS_INTEGRATION"
	cleanupKVBucket(t, natsURL, bucket)
	defer cleanupKVBucket(t, natsURL, bucket)

	store, err := keystore.Open(natsURL, bucket)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck

	staticDigest := sha256.Sum256([]byte("break-glass-secret"))
	verifier, err := auth.NewVerifier("break-glass:" + hex.EncodeToString(staticDigest[:]))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready, err := store.StartVerifierSync(ctx, verifier, func(err error) { t.Errorf("sync error: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("managed-key initial snapshot timed out")
	}

	record, token, err := store.Create(keystore.CreateInput{
		ID: "ndhis-prod", DisplayName: "NDHIS production", Owner: "NDHIS", Environment: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "ndhis-prod.") {
		t.Fatalf("unexpected token format: %q", token)
	}
	secret := strings.TrimPrefix(token, "ndhis-prod.")
	if secret == "" || strings.Contains(record.SecretSHA256, secret) {
		t.Fatal("plaintext secret leaked into stored digest")
	}
	waitForAuthState(t, verifier, token, true)

	_, rotated, err := store.Rotate("ndhis-prod")
	if err != nil {
		t.Fatal(err)
	}
	if rotated == token {
		t.Fatal("rotation returned the previous token")
	}
	waitForAuthState(t, verifier, token, false)
	waitForAuthState(t, verifier, rotated, true)

	if _, err := store.Revoke("ndhis-prod", "integration test"); err != nil {
		t.Fatal(err)
	}
	waitForAuthState(t, verifier, rotated, false)

	if _, err := verifier.VerifyAuthorization("Bearer break-glass.break-glass-secret"); err != nil {
		t.Fatalf("static break-glass key stopped working: %v", err)
	}
	records, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].RevokedAt == nil || records[0].RevocationReason != "integration test" {
		t.Fatalf("unexpected stored record after revoke: %+v", records)
	}
}

func waitForAuthState(t *testing.T, verifier *auth.Verifier, token string, wantAllowed bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err := verifier.VerifyAuthorization("Bearer " + token)
		if (err == nil) == wantAllowed {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, err := verifier.VerifyAuthorization("Bearer " + token)
	t.Fatalf("auth state did not converge: allowed=%v want=%v err=%v", err == nil, wantAllowed, err)
}

func cleanupKVBucket(t *testing.T, natsURL, bucket string) {
	t.Helper()
	nc, err := nats.Connect(natsURL, nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if err := js.DeleteKeyValue(bucket); err != nil && !strings.Contains(err.Error(), "not found") {
		t.Fatalf("delete KV bucket %s: %v", bucket, err)
	}
}
