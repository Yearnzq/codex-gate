package redaction

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRedactHeaders(t *testing.T) {
	headers := map[string]string{
		"Authorization": "Bearer demo_token_123",
		"Cookie":        "session=top-secret",
		"X-Request-Id":  "req-1",
	}
	redacted := RedactHeaders(headers)
	if redacted["Authorization"] != RedactedValue {
		t.Fatalf("authorization header should be redacted, got %q", redacted["Authorization"])
	}
	if redacted["Cookie"] != RedactedValue {
		t.Fatalf("cookie header should be redacted, got %q", redacted["Cookie"])
	}
	if redacted["X-Request-Id"] != "req-1" {
		t.Fatalf("non-sensitive header changed unexpectedly: %q", redacted["X-Request-Id"])
	}
}

func TestRedactText(t *testing.T) {
	raw := "Authorization=Bearer secret-token token=abc123"
	got := RedactText(raw)
	if strings.Contains(got, "secret-token") || strings.Contains(got, "abc123") {
		t.Fatalf("sensitive values leaked in redacted text: %q", got)
	}
	if !strings.Contains(got, RedactedValue) {
		t.Fatalf("expected redaction marker in %q", got)
	}
}

func TestRedactTextStandaloneSecretPatterns(t *testing.T) {
	openAIToken := "sk-" + strings.Repeat("a", 24)
	anthropicToken := "sk-ant-" + strings.Repeat("b", 24)
	githubToken := "ghp_" + strings.Repeat("c", 24)
	privateKeyMarker := "-----BEGIN " + "PRIVATE" + " KEY-----"
	raw := fmt.Sprintf(
		"tokens: %s %s %s marker:%s",
		openAIToken,
		anthropicToken,
		githubToken,
		privateKeyMarker,
	)
	got := RedactText(raw)
	for _, token := range []string{openAIToken, anthropicToken, githubToken, privateKeyMarker} {
		if strings.Contains(got, token) {
			t.Fatalf("standalone token leaked: %q in %q", token, got)
		}
	}
	if !strings.Contains(got, RedactedValue) {
		t.Fatalf("missing redaction marker in %q", got)
	}
}

func TestRedactBody(t *testing.T) {
	if got := RedactBody([]byte(`{"prompt":"sensitive"}`)); got != RedactedBody {
		t.Fatalf("expected %q, got %q", RedactedBody, got)
	}
}

func TestRedactError(t *testing.T) {
	err := errors.New("request failed: token=abc123")
	got := RedactError(err)
	if strings.Contains(got, "abc123") {
		t.Fatalf("sensitive token leaked in error: %q", got)
	}
	if !strings.Contains(got, RedactedValue) {
		t.Fatalf("missing redaction marker in error: %q", got)
	}
}

func TestRedactStructured(t *testing.T) {
	raw := map[string]any{
		"user":          "alice",
		"api_key":       "sk-demo-value",
		"nested":        map[string]any{"token": "abc123"},
		"plain_message": "ok",
	}
	got := RedactStructured(raw)
	out, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map output")
	}
	if out["api_key"] != RedactedValue {
		t.Fatalf("api_key should be redacted, got %#v", out["api_key"])
	}
	nested, ok := out["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested should remain a map")
	}
	if nested["token"] != RedactedValue {
		t.Fatalf("nested token should be redacted, got %#v", nested["token"])
	}
}

func TestToJSON(t *testing.T) {
	raw := map[string]any{"authorization": "Bearer super-secret"}
	out := ToJSON(raw)
	if strings.Contains(out, "super-secret") {
		t.Fatalf("token leaked in json output: %q", out)
	}
	if !strings.Contains(out, RedactedValue) {
		t.Fatalf("missing redaction marker in %q", out)
	}
}
