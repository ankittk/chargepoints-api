package config_test

import (
	"testing"
	"time"

	"github.com/ankittk/chargepoints-api/internal/config"
)

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("ADDR", "")
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("RATE_LIMIT_RPS", "")
	t.Setenv("RATE_LIMIT_BURST", "")
	t.Setenv("TRUST_PROXY", "")
	t.Setenv("CORS_ORIGIN", "")

	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("Addr = %q", cfg.Addr)
	}
	if cfg.DatabasePath != "./data/chargepoints.db" {
		t.Fatalf("DatabasePath = %q", cfg.DatabasePath)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
	}
	if cfg.RateLimitRPS != 20 || cfg.RateBurst != 40 {
		t.Fatalf("rate = %v/%d", cfg.RateLimitRPS, cfg.RateBurst)
	}
	if cfg.TrustProxy {
		t.Fatal("TrustProxy should default false")
	}
	if cfg.CORSOrigin != "*" {
		t.Fatalf("CORSOrigin = %q", cfg.CORSOrigin)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	t.Setenv("ADDR", ":9090")
	t.Setenv("DATABASE_PATH", "/tmp/x.db")
	t.Setenv("SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("RATE_LIMIT_RPS", "10.5")
	t.Setenv("RATE_LIMIT_BURST", "3")
	t.Setenv("TRUST_PROXY", "true")
	t.Setenv("CORS_ORIGIN", "https://example.com")

	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9090" || cfg.DatabasePath != "/tmp/x.db" {
		t.Fatalf("unexpected %#v", cfg)
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
	}
	if cfg.RateLimitRPS != 10.5 || cfg.RateBurst != 3 {
		t.Fatalf("rate = %v/%d", cfg.RateLimitRPS, cfg.RateBurst)
	}
	if !cfg.TrustProxy || cfg.CORSOrigin != "https://example.com" {
		t.Fatalf("proxy/cors %#v", cfg)
	}
}

func TestFromEnvInvalid(t *testing.T) {
	t.Setenv("RATE_LIMIT_RPS", "nope")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("expected error")
	}
}
