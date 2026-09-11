package config

import "testing"

func TestLoadAPIRequiresKeys(t *testing.T) {
	t.Setenv("API_KEYS", "")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("expected missing API_KEYS to fail")
	}
}

func TestLoadAPIHMACRequiresKey(t *testing.T) {
	t.Setenv("API_KEYS", "svc:"+"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("AUDIT_IP_MODE", "hmac")
	t.Setenv("AUDIT_HMAC_KEY", "")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("expected missing AUDIT_HMAC_KEY to fail")
	}
}

func TestLoadAPIHMACRejectsShortKey(t *testing.T) {
	t.Setenv("API_KEYS", "svc:"+"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("AUDIT_IP_MODE", "hmac")
	t.Setenv("AUDIT_HMAC_KEY", "too-short")
	if _, err := LoadAPI(); err == nil {
		t.Fatal("expected short AUDIT_HMAC_KEY to fail")
	}
}

func TestLoadWorkerRequiresClickHousePassword(t *testing.T) {
	t.Setenv("CLICKHOUSE_PASSWORD", "")
	if _, err := LoadWorker(); err == nil {
		t.Fatal("expected missing CLICKHOUSE_PASSWORD to fail")
	}
}
