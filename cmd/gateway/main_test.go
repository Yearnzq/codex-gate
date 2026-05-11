package main

import (
	"testing"
	"time"
)

func TestParseCodexWebTimeoutFromSeconds(t *testing.T) {
	t.Setenv("CODEX_WEB_TIMEOUT_SECONDS", "21600")

	timeout, disabled, err := parseCodexWebTimeout()
	if err != nil {
		t.Fatalf("parse timeout failed: %v", err)
	}
	if disabled {
		t.Fatal("timeout should be enabled")
	}
	if timeout != 6*time.Hour {
		t.Fatalf("expected 6h timeout, got %v", timeout)
	}
}

func TestParseCodexWebTimeoutCanDisable(t *testing.T) {
	t.Setenv("CODEX_WEB_TIMEOUT", "0")
	t.Setenv("CODEX_WEB_TIMEOUT_SECONDS", "21600")

	timeout, disabled, err := parseCodexWebTimeout()
	if err != nil {
		t.Fatalf("parse timeout failed: %v", err)
	}
	if timeout != 0 || !disabled {
		t.Fatalf("expected disabled timeout, got timeout=%v disabled=%v", timeout, disabled)
	}
}
