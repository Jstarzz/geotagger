package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"time"
)

type Event struct {
	Timestamp       time.Time `json:"timestamp"`
	RequestID       string    `json:"request_id"`
	CallerID        string    `json:"caller_id"`
	IPValue         string    `json:"ip_value"`
	IPMode          string    `json:"ip_mode"`
	CountryCode     string    `json:"country_code"`
	Country         string    `json:"country"`
	Outcome         string    `json:"outcome"`
	StatusCode      uint16    `json:"status_code"`
	LookupLatencyUS uint64    `json:"lookup_latency_us"`
	MMDBVersion     string    `json:"mmdb_version"`
}

func IPValue(mode string, key []byte, ip netip.Addr) string {
	switch mode {
	case "raw":
		return ip.String()
	case "hmac":
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte(ip.String()))
		return hex.EncodeToString(mac.Sum(nil))
	default:
		return ""
	}
}
