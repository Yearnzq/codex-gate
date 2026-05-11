package codexweb

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"codex-gate/internal/codexclient"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

type scriptedReadCloser struct {
	chunks [][]byte
	err    error
	index  int
}

func (r *scriptedReadCloser) Read(p []byte) (int, error) {
	if r.index < len(r.chunks) {
		n := copy(p, r.chunks[r.index])
		if n == len(r.chunks[r.index]) {
			r.index++
		} else {
			r.chunks[r.index] = r.chunks[r.index][n:]
		}
		return n, nil
	}
	if r.err != nil {
		err := r.err
		r.err = nil
		return 0, err
	}
	return 0, io.EOF
}

func (r *scriptedReadCloser) Close() error {
	return nil
}

func streamResponse(requestID string, body io.ReadCloser) *http.Response {
	header := http.Header{}
	header.Set("Content-Type", "text/event-stream")
	header.Set("x-request-id", requestID)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       body,
	}
}

func TestNewUsesLongDefaultTimeout(t *testing.T) {
	client, err := New(Config{
		BaseURL:     "http://codex-web.test",
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}
	httpClient, ok := client.httpClient.(*http.Client)
	if !ok {
		t.Fatalf("expected default http client, got %T", client.httpClient)
	}
	if httpClient.Timeout != 6*time.Hour {
		t.Fatalf("expected 6h default timeout, got %v", httpClient.Timeout)
	}
}

func TestNewCanDisableTimeout(t *testing.T) {
	client, err := New(Config{
		BaseURL:         "http://codex-web.test",
		AccessToken:     "test-access-token",
		Logger:          log.New(io.Discard, "", 0),
		TimeoutDisabled: true,
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}
	httpClient, ok := client.httpClient.(*http.Client)
	if !ok {
		t.Fatalf("expected default http client, got %T", client.httpClient)
	}
	if httpClient.Timeout != 0 {
		t.Fatalf("expected disabled timeout, got %v", httpClient.Timeout)
	}
}

func TestStreamEventsMapsCodexWebSSE(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_web")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_web\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"Read\",\"arguments\":\"{\\\"file_path\\\":\\\"README.md\\\"}\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_web\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	seen := make([]string, 0)
	result, err := client.StreamEvents(
		context.Background(),
		map[string]any{
			"model": "gpt-5.5",
			"input": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			},
		},
		codexclient.CallOptions{},
		func(event map[string]any) error {
			name, _ := event["event"].(string)
			seen = append(seen, name)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}
	if result.FinalStatus != "completed" {
		t.Fatalf("unexpected final status %q", result.FinalStatus)
	}
	if !strings.Contains(strings.Join(seen, ","), "response.tool_call") {
		t.Fatalf("expected tool call event, got %v", seen)
	}
	if authHeader != "Bearer test-access-token" {
		t.Fatalf("authorization header not set")
	}
}

func TestStreamEventsRetriesWhenStreamFailsBeforeFirstEvent(t *testing.T) {
	attempts := 0
	client, err := New(Config{
		BaseURL:     "http://codex-web.test",
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return streamResponse("req_retry_before_event", &scriptedReadCloser{
					err: io.ErrUnexpectedEOF,
				}), nil
			}
			return streamResponse("req_retry_success", io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_retry_success\"}}\n\n"+
					"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"+
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_retry_success\"}}\n\n",
			))), nil
		}),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	result, err := client.StreamEvents(
		context.Background(),
		map[string]any{"model": "gpt-5.5"},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("expected retry to recover stream: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected exactly one retry, got attempts=%d", attempts)
	}
	if result.RequestID != "req_retry_success" || result.FinalStatus != "completed" {
		t.Fatalf("unexpected retry result: %#v", result)
	}
}

func TestStreamEventsDoesNotRetryAfterFirstEvent(t *testing.T) {
	attempts := 0
	client, err := New(Config{
		BaseURL:     "http://codex-web.test",
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			return streamResponse("req_no_retry_after_event", &scriptedReadCloser{
				chunks: [][]byte{[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_no_retry_after_event\"}}\n\n")},
				err:    io.ErrUnexpectedEOF,
			}), nil
		}),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	result, err := client.StreamEvents(
		context.Background(),
		map[string]any{"model": "gpt-5.5"},
		codexclient.CallOptions{},
		nil,
	)
	if err == nil {
		t.Fatal("expected partial stream read error")
	}
	if attempts != 1 {
		t.Fatalf("partial stream must not be retried, attempts=%d", attempts)
	}
	if result == nil || len(result.Events) != 1 {
		t.Fatalf("expected partial event result, got %#v", result)
	}
}

func TestStreamEventsResumesPartialStreamWhenEnabled(t *testing.T) {
	attempts := 0
	var postBody map[string]any
	var resumeQuery url.Values
	resumeRetries := 1
	client, err := New(Config{
		BaseURL:             "http://codex-web.test",
		AccessToken:         "test-access-token",
		Logger:              log.New(io.Discard, "", 0),
		StreamResume:        true,
		StreamResumeRetries: &resumeRetries,
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			switch attempts {
			case 1:
				if req.Method != http.MethodPost {
					t.Fatalf("expected initial POST, got %s", req.Method)
				}
				if err := json.NewDecoder(req.Body).Decode(&postBody); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				return streamResponse("req_resume_initial", &scriptedReadCloser{
					chunks: [][]byte{[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_resume\"},\"sequence_number\":0}\n\n")},
					err:    io.ErrUnexpectedEOF,
				}), nil
			case 2:
				if req.Method != http.MethodGet {
					t.Fatalf("expected resume GET, got %s", req.Method)
				}
				if req.URL.Path != "/responses/resp_resume" {
					t.Fatalf("unexpected resume path: %s", req.URL.Path)
				}
				resumeQuery = req.URL.Query()
				return streamResponse("req_resume_followup", io.NopCloser(strings.NewReader(
					"data: {\"type\":\"response.output_text.delta\",\"delta\":\"resumed\",\"sequence_number\":1}\n\n"+
						"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_resume\"},\"sequence_number\":2}\n\n",
				))), nil
			default:
				t.Fatalf("unexpected attempt %d", attempts)
				return nil, nil
			}
		}),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	result, err := client.StreamEvents(
		context.Background(),
		map[string]any{"model": "gpt-5.5"},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("expected resume to recover stream: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected one initial request and one resume request, got %d", attempts)
	}
	if _, ok := postBody["background"]; ok {
		t.Fatalf("resume must not implicitly enable background mode: %#v", postBody)
	}
	if postBody["store"] != false {
		t.Fatalf("codex web requests must keep store=false, got %#v", postBody)
	}
	if resumeQuery.Get("stream") != "true" || resumeQuery.Get("starting_after") != "0" {
		t.Fatalf("unexpected resume query: %s", resumeQuery.Encode())
	}
	if result.FinalStatus != "completed" || len(result.Events) != 3 {
		t.Fatalf("unexpected resumed result: %#v", result)
	}
}

func TestStreamEventsSetsStoreFalse(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_background")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_background\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_background\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.StreamEvents(
		context.Background(),
		map[string]any{"model": "gpt-5.5"},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}
	if _, ok := captured["background"]; ok {
		t.Fatalf("background must not be sent to chatgpt codex backend, got %#v", captured)
	}
	if captured["store"] != false {
		t.Fatalf("codex web requests must keep store=false, got %#v", captured)
	}
}

func TestClassifyStreamReadError(t *testing.T) {
	category, message := classifyStreamReadError(io.ErrUnexpectedEOF)
	if category != "unexpected_eof" || !strings.Contains(message, "unexpectedly") {
		t.Fatalf("unexpected EOF classification: %s %s", category, message)
	}

	category, _ = classifyStreamReadError(context.Canceled)
	if category != "context_canceled" {
		t.Fatalf("unexpected context classification: %s", category)
	}

	category, _ = classifyStreamReadError(io.ErrClosedPipe)
	if category != "read_error" {
		t.Fatalf("unexpected generic classification: %s", category)
	}
}

func TestCreateReducesStreamToCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_create")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_create\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"client-ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_create\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	result, err := client.Create(context.Background(), map[string]any{"model": "gpt-5.5"}, codexclient.CallOptions{})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	output, ok := result.Response["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("unexpected output %#v", result.Response["output"])
	}
}

func TestStreamEventsAddsDefaultInstructionsWhenMissing(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_instructions")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_instructions\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_instructions\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.StreamEvents(
		context.Background(),
		map[string]any{
			"model": "gpt-5.5",
			"input": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			},
		},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}
	instructions, _ := captured["instructions"].(string)
	if strings.TrimSpace(instructions) == "" {
		t.Fatalf("expected default instructions, got %#v", captured["instructions"])
	}
}

func TestStreamEventsDropsUnsupportedMaxOutputTokens(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_no_max_output_tokens")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_no_max_output_tokens\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_no_max_output_tokens\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.StreamEvents(
		context.Background(),
		map[string]any{
			"model":             "gpt-5.5",
			"max_output_tokens": float64(32),
			"input": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			},
		},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}
	if _, ok := captured["max_output_tokens"]; ok {
		t.Fatalf("max_output_tokens should not be sent to codex web backend: %#v", captured)
	}
}

func TestStreamEventsAddsMessageTypeToInputMessages(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_message_type")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_message_type\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_message_type\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.StreamEvents(
		context.Background(),
		map[string]any{
			"model": "gpt-5.5",
			"input": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			},
		},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}
	input, ok := captured["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("expected one input message, got %#v", captured["input"])
	}
	message, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("expected input message object, got %#v", input[0])
	}
	if message["type"] != "message" {
		t.Fatalf("expected type=message, got %#v", message)
	}
}

func TestStreamEventsConvertsAssistantInputTextToOutputText(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_assistant_history")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_assistant_history\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_assistant_history\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.StreamEvents(
		context.Background(),
		map[string]any{
			"model": "gpt-5.5",
			"input": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
				map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "input_text", "text": "Hi!"}}},
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "introduce yourself"}}},
			},
		},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}
	input, ok := captured["input"].([]any)
	if !ok || len(input) != 3 {
		t.Fatalf("expected three input messages, got %#v", captured["input"])
	}
	assistantMessage, ok := input[1].(map[string]any)
	if !ok {
		t.Fatalf("expected assistant message object, got %#v", input[1])
	}
	content, ok := assistantMessage["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected assistant content block, got %#v", assistantMessage["content"])
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("expected assistant content object, got %#v", content[0])
	}
	if block["type"] != "output_text" {
		t.Fatalf("expected assistant text to become output_text, got %#v", block)
	}

	userMessage, ok := input[2].(map[string]any)
	if !ok {
		t.Fatalf("expected user message object, got %#v", input[2])
	}
	userContent := userMessage["content"].([]any)
	userBlock := userContent[0].(map[string]any)
	if userBlock["type"] != "input_text" {
		t.Fatalf("expected user text to remain input_text, got %#v", userBlock)
	}
}

func TestStreamEventsLiftsToolContentBlocksToResponseInputItems(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_tool_history")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_tool_history\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_tool_history\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.StreamEvents(
		context.Background(),
		map[string]any{
			"model": "gpt-5.5",
			"input": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "input_text", "text": "I will inspect it."},
					map[string]any{"type": "tool_call", "id": "toolu_01", "name": "inspect", "arguments": map[string]any{"path": "README.md"}},
				}},
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "tool_result", "tool_call_id": "toolu_01", "content": "ok"},
				}},
			},
		},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}

	input, ok := captured["input"].([]any)
	if !ok || len(input) != 4 {
		t.Fatalf("expected four normalized input items, got %#v", captured["input"])
	}
	assistantMessage := input[1].(map[string]any)
	assistantContent := assistantMessage["content"].([]any)
	if len(assistantContent) != 1 {
		t.Fatalf("expected assistant message to retain text only, got %#v", assistantContent)
	}
	if block := assistantContent[0].(map[string]any); block["type"] != "output_text" {
		t.Fatalf("expected assistant text output_text, got %#v", block)
	}
	functionCall := input[2].(map[string]any)
	if functionCall["type"] != "function_call" || functionCall["call_id"] != "toolu_01" || functionCall["name"] != "inspect" {
		t.Fatalf("expected function_call item, got %#v", functionCall)
	}
	arguments, _ := functionCall["arguments"].(string)
	if !strings.Contains(arguments, "README.md") {
		t.Fatalf("expected JSON arguments string, got %#v", functionCall["arguments"])
	}
	functionOutput := input[3].(map[string]any)
	if functionOutput["type"] != "function_call_output" || functionOutput["call_id"] != "toolu_01" || functionOutput["output"] != "ok" {
		t.Fatalf("expected function_call_output item, got %#v", functionOutput)
	}
}

func TestStreamEventsDropsEmptyTextBlocksFromHistory(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_empty_text_history")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_empty_text_history\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_empty_text_history\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.StreamEvents(
		context.Background(),
		map[string]any{
			"model": "gpt-5.5",
			"input": []any{
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "input_text", "text": ""},
					map[string]any{"type": "input_text", "text": "hi"},
				}},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "input_text", "text": " "},
					map[string]any{"type": "tool_call", "id": "toolu_01", "name": "inspect", "arguments": map[string]any{"path": "README.md"}},
				}},
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "input_text", "text": ""},
				}},
			},
		},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}

	input, ok := captured["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected empty-only message to be dropped, got %#v", captured["input"])
	}
	userMessage := input[0].(map[string]any)
	userContent := userMessage["content"].([]any)
	if len(userContent) != 1 {
		t.Fatalf("expected user message to retain one text block, got %#v", userContent)
	}
	if block := userContent[0].(map[string]any); block["text"] != "hi" {
		t.Fatalf("expected non-empty text block to remain, got %#v", block)
	}
	if functionCall := input[1].(map[string]any); functionCall["type"] != "function_call" {
		t.Fatalf("expected tool call to remain as function_call, got %#v", functionCall)
	}
}

func TestStreamEventsPreservesExplicitInstructions(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_instructions_explicit")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_instructions_explicit\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_instructions_explicit\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.StreamEvents(
		context.Background(),
		map[string]any{
			"model":        "gpt-5.5",
			"instructions": "System instruction.",
			"input": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			},
		},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}
	if captured["instructions"] != "System instruction." {
		t.Fatalf("expected explicit instructions to be preserved, got %#v", captured["instructions"])
	}
}

func TestStreamEventsForwardsConfiguredReasoningAndServiceTier(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_tuning")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_tuning\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_tuning\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:         server.URL,
		AccessToken:     "test-access-token",
		Logger:          log.New(io.Discard, "", 0),
		ReasoningEffort: "xhigh",
		ServiceTier:     "fast",
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.StreamEvents(
		context.Background(),
		map[string]any{
			"model": "gpt-5.5",
			"input": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			},
		},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}
	reasoning, ok := captured["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "xhigh" {
		t.Fatalf("expected reasoning effort to be forwarded, got %#v", captured["reasoning"])
	}
	if captured["service_tier"] != "fast" {
		t.Fatalf("expected service_tier=fast, got %#v", captured["service_tier"])
	}
}

func TestStreamEventsNormalizesAnthropicToolChoiceForResponses(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req_tool_choice")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_tool_choice\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_tool_choice\"}}\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-access-token",
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.StreamEvents(
		context.Background(),
		map[string]any{
			"model":       "gpt-5.5",
			"tool_choice": map[string]any{"type": "tool", "name": "Bash"},
			"input": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			},
		},
		codexclient.CallOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("stream events failed: %v", err)
	}
	choice := captured["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["name"] != "Bash" {
		t.Fatalf("expected Responses function tool_choice, got %#v", choice)
	}
}
