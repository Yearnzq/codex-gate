package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServerEndpoints(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Port = 8080

	var logBuffer strings.Builder
	logger := log.New(&logBuffer, "", 0)

	server, err := NewServer(cfg, logger)
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listener create failed: %v", err)
	}
	addr := listener.Addr().(*net.TCPAddr)

	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()

	baseURL := "http://127.0.0.1:" + strconv.Itoa(addr.Port)
	waitForServer(t, baseURL+"/healthz")

	status, payload := getJSON(t, baseURL+"/healthz")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if payload["status"] != "ok" {
		t.Fatalf("unexpected health payload %#v", payload)
	}

	status, payload = getJSON(t, baseURL+"/version")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if payload["name"] != ServiceName {
		t.Fatalf("unexpected service name %#v", payload["name"])
	}
	version, ok := payload["version"].(string)
	if !ok || strings.TrimSpace(version) == "" {
		t.Fatalf("version must be non-empty string, got %#v", payload["version"])
	}
	for _, key := range []string{"build_time", "git_version", "target_platform", "backend_mode", "protocol_compatibility"} {
		value, ok := payload[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			t.Fatalf("%s must be non-empty string, got %#v", key, payload[key])
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop in time")
	}

	logText := logBuffer.String()
	if strings.Contains(logText, "Bearer ") {
		t.Fatalf("logs should not contain raw bearer token text: %s", logText)
	}
	if !strings.Contains(logText, `"redaction_enabled":true`) {
		t.Fatalf("expected redaction flag in logs: %s", logText)
	}
	if !strings.Contains(logText, `"request_body":"[REDACTED_BODY]"`) {
		t.Fatalf("request body should be redacted in logs: %s", logText)
	}
	if !strings.Contains(logText, `"response_body":"[REDACTED_BODY]"`) {
		t.Fatalf("response body should be redacted in logs: %s", logText)
	}
	if strings.Contains(logText, `"response_body":"{\"status\":\"ok\"}"`) {
		t.Fatalf("raw response body leaked in logs: %s", logText)
	}
}

func waitForServer(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server did not become ready: %s", url)
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}

	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("invalid json response %q: %v", string(body), err)
	}
	return resp.StatusCode, payload
}
