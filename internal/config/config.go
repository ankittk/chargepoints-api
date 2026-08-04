package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is process configuration loaded from the environment.
type Config struct {
	Addr            string
	DatabasePath    string
	ShutdownTimeout time.Duration
	RateLimitRPS    float64
	RateBurst       int
	// TrustProxy, when true, uses the first X-Forwarded-For hop as client IP.
	// Only enable behind a trusted reverse proxy.
	TrustProxy bool
	// CORSOrigin is the Access-Control-Allow-Origin value (default "*").
	CORSOrigin string

	// OTELServiceName becomes resource attribute service.name.
	OTELServiceName string
	// OTELTracesExporter is none|stdout|otlp (empty = otlp if endpoint set, else none).
	OTELTracesExporter string
	// OTELExporterOTLPEndpoint is the OTLP HTTP collector host:port (e.g. localhost:4318).
	OTELExporterOTLPEndpoint string
}

// FromEnv loads Config from process environment variables.
// Returns an error if a set variable has an invalid value (no silent fallback).
func FromEnv() (Config, error) {
	cfg := Config{
		Addr:                     get("ADDR", ":8080"),
		DatabasePath:             get("DATABASE_PATH", "./data/chargepoints.db"),
		CORSOrigin:               get("CORS_ORIGIN", "*"),
		TrustProxy:               envBool("TRUST_PROXY", false),
		OTELServiceName:          get("OTEL_SERVICE_NAME", "chargepoints-api"),
		OTELTracesExporter:       get("OTEL_TRACES_EXPORTER", ""),
		OTELExporterOTLPEndpoint: get("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
	}

	var err error
	if cfg.ShutdownTimeout, err = envDuration("SHUTDOWN_TIMEOUT", 10*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitRPS, err = envFloat("RATE_LIMIT_RPS", 20); err != nil {
		return Config{}, err
	}
	if cfg.RateBurst, err = envInt("RATE_LIMIT_BURST", 40); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitRPS <= 0 {
		return Config{}, fmt.Errorf("RATE_LIMIT_RPS must be > 0")
	}
	if cfg.RateBurst <= 0 {
		return Config{}, fmt.Errorf("RATE_LIMIT_BURST must be > 0")
	}
	return cfg, nil
}

func get(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid int %q", key, v)
	}
	return n, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid float %q", key, v)
	}
	return n, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q", key, v)
	}
	return d, nil
}
