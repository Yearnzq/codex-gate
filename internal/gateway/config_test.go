package gateway

import (
	"errors"
	"testing"
)

func TestDefaultConfigIsLoopbackSafe(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("unexpected host %q", cfg.Host)
	}
	if cfg.Port != 8080 {
		t.Fatalf("unexpected port %d", cfg.Port)
	}
	if !cfg.RedactLogs {
		t.Fatal("redaction should be enabled by default")
	}
	if cfg.UpstreamModel != "" {
		t.Fatalf("default upstream model override should be empty, got %q", cfg.UpstreamModel)
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
}

func TestRejectsNonLoopbackWithoutExplicitApproval(t *testing.T) {
	candidates := []string{"0.0.0.0", "192.168.1.2", "example.com"}
	for _, host := range candidates {
		cfg := DefaultConfig()
		cfg.Host = host
		cfg.AllowWideBind = false
		err := ValidateConfig(cfg)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("expected invalid config for host %q, got %v", host, err)
		}
	}
}

func TestAllowsNonLoopbackWithExplicitApproval(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Host = "0.0.0.0"
	cfg.AllowWideBind = true
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestLoopbackVariantsAllowed(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
		cfg := DefaultConfig()
		cfg.Host = host
		cfg.AllowWideBind = false
		if err := ValidateConfig(cfg); err != nil {
			t.Fatalf("expected loopback host %q to pass: %v", host, err)
		}
	}
}

func TestParseConfigText(t *testing.T) {
	cfg, err := ParseConfigText(`
[gateway]
host = "127.0.0.1"
port = 18080
log_level = "debug"
redact_logs = true
allow_wide_bind = false
upstream_model = "gpt-5.3-codex"
`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if cfg.Port != 18080 {
		t.Fatalf("unexpected port %d", cfg.Port)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("unexpected log level %q", cfg.LogLevel)
	}
	if cfg.UpstreamModel != "gpt-5.3-codex" {
		t.Fatalf("unexpected upstream model %q", cfg.UpstreamModel)
	}
}
