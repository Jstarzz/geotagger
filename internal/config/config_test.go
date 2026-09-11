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
