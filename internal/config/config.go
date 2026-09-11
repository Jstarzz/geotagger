package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type API struct {
	HTTPAddr        string
	AdminAddr       string
	MMDBPath        string
	MMDBReload      time.Duration
	APIKeys         string
	NATSURL         string
	AuditStream     string
	AuditSubject    string
	AuditTimeout    time.Duration
	AuditIPMode     string
	AuditHMACKey    string
	MaxBodyBytes    int64
	ShutdownTimeout time.Duration
	AllowPrivateIPs bool
}

type Worker struct {
	NATSURL            string
	AuditStream        string
	AuditSubject       string
	DurableName        string
	ClickHouseURL      string
	ClickHouseUser     string
	ClickHousePassword string
	BatchSize          int
	FlushInterval      time.Duration
	ShutdownTimeout    time.Duration
}

func LoadAPI() (API, error) {
	c := API{
		HTTPAddr:        getenv("HTTP_ADDR", ":8080"),
		AdminAddr:       getenv("ADMIN_ADDR", ":9090"),
		MMDBPath:        getenv("MMDB_PATH", "/data/GeoLite2-Country.mmdb"),
		APIKeys:         os.Getenv("API_KEYS"),
		NATSURL:         getenv("NATS_URL", "nats://nats:4222"),
		AuditStream:     getenv("AUDIT_STREAM", "GEOTAGGER_AUDIT"),
		AuditSubject:    getenv("AUDIT_SUBJECT", "geotagger.audit.lookup"),
		AuditIPMode:     getenv("AUDIT_IP_MODE", "hmac"),
		AuditHMACKey:    os.Getenv("AUDIT_HMAC_KEY"),
		MaxBodyBytes:    1024,
		AllowPrivateIPs: false,
	}
	var err error
	if c.MMDBReload, err = duration("MMDB_RELOAD_INTERVAL", 5*time.Minute); err != nil {
		return API{}, err
	}
	if c.AuditTimeout, err = duration("AUDIT_TIMEOUT", 50*time.Millisecond); err != nil {
		return API{}, err
	}
	if c.ShutdownTimeout, err = duration("SHUTDOWN_TIMEOUT", 10*time.Second); err != nil {
		return API{}, err
	}
	if c.MaxBodyBytes, err = int64Value("MAX_BODY_BYTES", 1024); err != nil {
		return API{}, err
	}
	if c.AllowPrivateIPs, err = boolValue("ALLOW_PRIVATE_IPS", false); err != nil {
		return API{}, err
	}

	if c.APIKeys == "" {
		return API{}, errors.New("API_KEYS is required")
	}
	if c.AuditIPMode != "hmac" && c.AuditIPMode != "raw" && c.AuditIPMode != "omit" {
		return API{}, fmt.Errorf("AUDIT_IP_MODE must be hmac, raw, or omit")
	}
	if c.AuditIPMode == "hmac" && c.AuditHMACKey == "" {
		return API{}, errors.New("AUDIT_HMAC_KEY is required when AUDIT_IP_MODE=hmac")
	}
	return c, nil
}

func LoadWorker() (Worker, error) {
	c := Worker{
		NATSURL:            getenv("NATS_URL", "nats://nats:4222"),
		AuditStream:        getenv("AUDIT_STREAM", "GEOTAGGER_AUDIT"),
		AuditSubject:       getenv("AUDIT_SUBJECT", "geotagger.audit.lookup"),
		DurableName:        getenv("AUDIT_DURABLE", "geotagger-clickhouse"),
		ClickHouseURL:      getenv("CLICKHOUSE_URL", "http://clickhouse:8123"),
		ClickHouseUser:     getenv("CLICKHOUSE_USER", "default"),
		ClickHousePassword: os.Getenv("CLICKHOUSE_PASSWORD"),
		BatchSize:          1000,
	}
	var err error
	if c.BatchSize, err = intValue("BATCH_SIZE", 1000); err != nil {
		return Worker{}, err
	}
	if c.FlushInterval, err = duration("FLUSH_INTERVAL", 250*time.Millisecond); err != nil {
		return Worker{}, err
	}
	if c.ShutdownTimeout, err = duration("SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return Worker{}, err
	}
	if c.BatchSize < 1 {
		return Worker{}, errors.New("BATCH_SIZE must be positive")
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func duration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func intValue(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func int64Value(key string, fallback int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func boolValue(key string, fallback bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}
