package codexclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fakeDoer func(req *http.Request) (*http.Response, error)

func (f fakeDoer) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

type timeoutError struct {
	message string
}

func (e timeoutError) Error() string   { return e.message }
func (e timeoutError) Timeout() bool   { return true }
func (e timeoutError) Temporary() bool { return true }

func TestCreateFromFixtureAndRequestIDCapture(t *testing.T) {
	normalBody := fixtureResponseBody(t, "codex_normal_completion.json")
	promptText := "super sensitive prompt content"
	apiKey := "sk-" + strings.Repeat("x", 24)
	var logBuffer bytes.Buffer
	callCount := 0

	service, err := New(Config{
		BaseURL: "https://codex.test",
		APIKey:  apiKey,
		Logger:  log.New(&logBuffer, "", 0),
		HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
			callCount++
			if req.URL.Path != "/v1/responses" {
				t.Fatalf("unexpected path %q", req.URL.Path)
			}
			if req.Header.Get("Authorization") != "Bearer "+apiKey {
				t.Fatalf("authorization header missing or incorrect")
			}
			return httpResponse(200, "req-normal-01", normalBody), nil
		}),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	result, err := service.Create(
		context.Background(),
		map[string]any{"prompt": promptText},
		CallOptions{SafeToRetry: false},
	)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected exactly one call, got %d", callCount)
	}
	if result.RequestID != "req-normal-01" {
		t.Fatalf("unexpected request id %q", result.RequestID)
	}
	if result.Response["status"] != "completed" {
		t.Fatalf("unexpected response payload %#v", result.Response)
	}

	logText := logBuffer.String()
	if strings.Contains(logText, promptText) {
		t.Fatalf("prompt leaked in logs: %s", logText)
	}
	if strings.Contains(logText, apiKey) {
		t.Fatalf("api key leaked in logs: %s", logText)
	}
}

func TestStreamFromFixture(t *testing.T) {
	service, err := New(Config{
		BaseURL: "https://codex.test",
		HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/v1/responses" {
				t.Fatalf("unexpected stream path %q", req.URL.Path)
			}
			body := strings.Join([]string{
				`data: {"type":"response.created","response":{"id":"resp_stream"}}`,
				`data: {"type":"response.output_text.delta","delta":"Hello"}`,
				`data: {"type":"response.output_text.delta","delta":" world"}`,
				`data: {"type":"response.completed","response":{"id":"resp_stream"}}`,
				``,
			}, "\n\n")
			return httpResponseRaw(200, "req-stream-01", []byte(body)), nil
		}),
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	result, err := service.Stream(context.Background(), map[string]any{"stream": true}, CallOptions{})
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if result.FinalStatus != "completed" {
		t.Fatalf("unexpected final status %q", result.FinalStatus)
	}
	if len(result.Events) != 4 {
		t.Fatalf("unexpected event count %d", len(result.Events))
	}
	if result.RequestID != "req-stream-01" {
		t.Fatalf("unexpected request id %q", result.RequestID)
	}
}

func TestStreamUsesResponsesSSEEndpoint(t *testing.T) {
	var captured map[string]any
	service, err := New(Config{
		BaseURL: "https://codex.test",
		HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/v1/responses" {
				t.Fatalf("stream should use standard Responses endpoint, got %q", req.URL.Path)
			}
			if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			body := strings.Join([]string{
				`data: {"type":"response.created","response":{"id":"resp_sse"}}`,
				`data: {"type":"response.output_text.delta","delta":"sse-ok"}`,
				`data: {"type":"response.completed","response":{"id":"resp_sse"}}`,
				``,
			}, "\n\n")
			return httpResponseRaw(200, "req-sse-01", []byte(body)), nil
		}),
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	result, err := service.Stream(context.Background(), map[string]any{"model": "gpt-5.5"}, CallOptions{})
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if captured["stream"] != true {
		t.Fatalf("stream request must set stream=true, got %#v", captured)
	}
	if result.RequestID != "req-sse-01" || result.FinalStatus != "completed" {
		t.Fatalf("unexpected stream result: %#v", result)
	}
	if len(result.Events) != 3 {
		t.Fatalf("expected 3 mapped events, got %#v", result.Events)
	}
	if result.Events[1]["event"] != "response.output_text.delta" || result.Events[1]["delta"] != "sse-ok" {
		t.Fatalf("unexpected delta event: %#v", result.Events[1])
	}
}

func TestStreamPartialFailureFromFixtureReturnsError(t *testing.T) {
	service, err := New(Config{
		BaseURL: "https://codex.test",
		HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
			body := strings.Join([]string{
				`data: {"type":"response.created","response":{"id":"resp_stream_fail"}}`,
				`data: {"type":"response.output_text.delta","delta":"partial"}`,
				`data: {"type":"response.failed","response":{"id":"resp_stream_fail"},"error":{"message":"upstream failed"}}`,
				``,
			}, "\n\n")
			return httpResponseRaw(200, "req-stream-fail-01", []byte(body)), nil
		}),
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	result, callErr := service.Stream(context.Background(), map[string]any{"stream": true}, CallOptions{})
	if result == nil {
		t.Fatalf("expected stream result with partial events")
	}
	if len(result.Events) == 0 {
		t.Fatalf("expected partial stream events")
	}
	if callErr == nil {
		t.Fatalf("expected stream failure error")
	}
	var clientErr *ClientError
	if !errors.As(callErr, &clientErr) {
		t.Fatalf("expected ClientError, got %T", callErr)
	}
	if clientErr.Kind != KindStreamFailed {
		t.Fatalf("expected stream failure kind, got %q", clientErr.Kind)
	}
	if clientErr.RequestID != "req-stream-fail-01" {
		t.Fatalf("unexpected request id %q", clientErr.RequestID)
	}
}

func TestErrorClassificationFromFixtures(t *testing.T) {
	testCases := []struct {
		name       string
		fixture    string
		httpStatus int
		kind       ErrorKind
		retryable  bool
	}{
		{name: "401 unauthorized", fixture: "codex_error_401.json", httpStatus: 401, kind: KindUnauthorized, retryable: false},
		{name: "403 forbidden", fixture: "codex_error_403.json", httpStatus: 403, kind: KindForbidden, retryable: false},
		{name: "408 timeout", fixture: "codex_timeout.json", httpStatus: 408, kind: KindTimeout, retryable: true},
		{name: "429 rate limit", fixture: "codex_error_429.json", httpStatus: 429, kind: KindRateLimited, retryable: true},
		{name: "504 timeout", fixture: "codex_timeout.json", httpStatus: 504, kind: KindTimeout, retryable: true},
		{name: "500 server error", fixture: "codex_timeout.json", httpStatus: 500, kind: KindServerError, retryable: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body := fixtureResponseBody(t, tc.fixture)
			service, err := New(Config{
				BaseURL: "https://codex.test",
				HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
					return httpResponse(tc.httpStatus, "req-error-01", body), nil
				}),
			})
			if err != nil {
				t.Fatalf("new client failed: %v", err)
			}

			_, callErr := service.Create(context.Background(), map[string]any{"input": "x"}, CallOptions{})
			if callErr == nil {
				t.Fatalf("expected error")
			}
			var clientErr *ClientError
			if !errors.As(callErr, &clientErr) {
				t.Fatalf("expected ClientError, got %T", callErr)
			}
			if clientErr.Kind != tc.kind {
				t.Fatalf("expected kind %q, got %q", tc.kind, clientErr.Kind)
			}
			if clientErr.Retryable != tc.retryable {
				t.Fatalf("expected retryable=%v, got %v", tc.retryable, clientErr.Retryable)
			}
			if clientErr.RequestID != "req-error-01" {
				t.Fatalf("unexpected request id %q", clientErr.RequestID)
			}
		})
	}
}

func TestNetworkTimeoutClassification(t *testing.T) {
	service, err := New(Config{
		BaseURL: "https://codex.test",
		HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
			return nil, timeoutError{message: "dial timed out"}
		}),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}
	_, callErr := service.Create(context.Background(), map[string]any{"input": "x"}, CallOptions{})
	var clientErr *ClientError
	if !errors.As(callErr, &clientErr) {
		t.Fatalf("expected ClientError, got %T", callErr)
	}
	if clientErr.Kind != KindNetworkTimeout {
		t.Fatalf("expected network timeout kind, got %q", clientErr.Kind)
	}
	if !clientErr.Retryable {
		t.Fatalf("network timeout should be retryable")
	}
}

func TestMalformedResponseClassification(t *testing.T) {
	service, err := New(Config{
		BaseURL: "https://codex.test",
		HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
			return httpResponseRaw(200, "req-malformed-01", []byte("not-json")), nil
		}),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}
	_, callErr := service.Create(context.Background(), map[string]any{"input": "x"}, CallOptions{})
	var clientErr *ClientError
	if !errors.As(callErr, &clientErr) {
		t.Fatalf("expected ClientError, got %T", callErr)
	}
	if clientErr.Kind != KindMalformedResponse {
		t.Fatalf("expected malformed response kind, got %q", clientErr.Kind)
	}
}

func TestRetryBoundedAndSafeCasesOnly(t *testing.T) {
	t.Run("safe retry succeeds after transient 429", func(t *testing.T) {
		first := fixtureResponseBody(t, "codex_error_429.json")
		second := fixtureResponseBody(t, "codex_normal_completion.json")
		callCount := 0
		service, err := New(Config{
			BaseURL:    "https://codex.test",
			MaxRetries: 2,
			RetryDelay: 0,
			HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
				callCount++
				if callCount == 1 {
					return httpResponse(429, "req-retry-01", first), nil
				}
				return httpResponse(200, "req-retry-02", second), nil
			}),
		})
		if err != nil {
			t.Fatalf("new client failed: %v", err)
		}
		_, callErr := service.Create(context.Background(), map[string]any{"input": "x"}, CallOptions{SafeToRetry: true})
		if callErr != nil {
			t.Fatalf("expected retry success, got %v", callErr)
		}
		if callCount != 2 {
			t.Fatalf("expected 2 attempts, got %d", callCount)
		}
	})

	t.Run("unsafe call does not retry", func(t *testing.T) {
		errorBody := fixtureResponseBody(t, "codex_error_429.json")
		callCount := 0
		service, err := New(Config{
			BaseURL:    "https://codex.test",
			MaxRetries: 2,
			RetryDelay: 0,
			HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
				callCount++
				return httpResponse(429, "req-no-retry-01", errorBody), nil
			}),
		})
		if err != nil {
			t.Fatalf("new client failed: %v", err)
		}
		_, callErr := service.Create(context.Background(), map[string]any{"input": "x"}, CallOptions{SafeToRetry: false})
		if callErr == nil {
			t.Fatalf("expected error")
		}
		if callCount != 1 {
			t.Fatalf("expected 1 attempt, got %d", callCount)
		}
	})

	t.Run("retry bounded at max retries", func(t *testing.T) {
		errorBody := fixtureResponseBody(t, "codex_error_429.json")
		callCount := 0
		service, err := New(Config{
			BaseURL:    "https://codex.test",
			MaxRetries: 1,
			RetryDelay: 0,
			HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
				callCount++
				return httpResponse(503, "req-bounded-01", errorBody), nil
			}),
		})
		if err != nil {
			t.Fatalf("new client failed: %v", err)
		}
		_, callErr := service.Create(context.Background(), map[string]any{"input": "x"}, CallOptions{SafeToRetry: true})
		if callErr == nil {
			t.Fatalf("expected error")
		}
		if callCount != 2 {
			t.Fatalf("expected 2 attempts, got %d", callCount)
		}
	})

	t.Run("safe retry can be explicitly disabled via zero override", func(t *testing.T) {
		errorBody := fixtureResponseBody(t, "codex_error_429.json")
		callCount := 0
		zero := 0
		service, err := New(Config{
			BaseURL:            "https://codex.test",
			MaxRetriesOverride: &zero,
			RetryDelay:         0,
			HTTPClient: fakeDoer(func(req *http.Request) (*http.Response, error) {
				callCount++
				return httpResponse(429, "req-zero-retry-01", errorBody), nil
			}),
		})
		if err != nil {
			t.Fatalf("new client failed: %v", err)
		}
		_, callErr := service.Create(context.Background(), map[string]any{"input": "x"}, CallOptions{SafeToRetry: true})
		if callErr == nil {
			t.Fatalf("expected error")
		}
		if callCount != 1 {
			t.Fatalf("expected single attempt with zero override, got %d", callCount)
		}
	})
}

func fixtureResponseBody(t *testing.T, fixtureFile string) []byte {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to resolve runtime caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	path := filepath.Join(root, "fixtures", "codex-responses", fixtureFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %q failed: %v", path, err)
	}
	var envelope struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode fixture %q failed: %v", path, err)
	}
	return envelope.Response
}

func httpResponse(status int, requestID string, responseObject []byte) *http.Response {
	return httpResponseRaw(status, requestID, responseObject)
}

func httpResponseRaw(status int, requestID string, body []byte) *http.Response {
	headers := make(http.Header)
	if requestID != "" {
		headers.Set("x-request-id", requestID)
	}
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}
