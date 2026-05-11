package codexclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"codex-gate/internal/redaction"
)

const (
	DefaultBaseURL    = "http://127.0.0.1:8080"
	DefaultMaxRetries = 2
	DefaultRetryDelay = 50 * time.Millisecond
)

type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Config struct {
	BaseURL            string
	APIKey             string
	HTTPClient         HTTPDoer
	Logger             *log.Logger
	MaxRetries         int
	MaxRetriesOverride *int
	RetryDelay         time.Duration
}

type CallOptions struct {
	SafeToRetry bool
}

type CompletionResult struct {
	RequestID  string
	HTTPStatus int
	Response   map[string]any
}

type StreamResult struct {
	RequestID   string
	HTTPStatus  int
	Events      []map[string]any
	FinalStatus string
}

type Client interface {
	Create(ctx context.Context, payload map[string]any, options CallOptions) (*CompletionResult, error)
	Stream(ctx context.Context, payload map[string]any, options CallOptions) (*StreamResult, error)
}

type StreamEventHandler func(event map[string]any) error

type EventStreamer interface {
	StreamEvents(
		ctx context.Context,
		payload map[string]any,
		options CallOptions,
		handler StreamEventHandler,
	) (*StreamResult, error)
}

type Service struct {
	baseURL    string
	apiKey     string
	httpClient HTTPDoer
	logger     *log.Logger
	maxRetries int
	retryDelay time.Duration
}

func New(cfg Config) (*Service, error) {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if cfg.MaxRetries < 0 {
		return nil, fmt.Errorf("max retries must be >= 0")
	}
	if cfg.MaxRetriesOverride != nil && *cfg.MaxRetriesOverride < 0 {
		return nil, fmt.Errorf("max retries override must be >= 0")
	}
	if cfg.RetryDelay < 0 {
		return nil, fmt.Errorf("retry delay must be >= 0")
	}
	maxRetries := DefaultMaxRetries
	if cfg.MaxRetries > 0 {
		maxRetries = cfg.MaxRetries
	}
	if cfg.MaxRetriesOverride != nil {
		maxRetries = *cfg.MaxRetriesOverride
	}
	retryDelay := cfg.RetryDelay
	if retryDelay == 0 {
		retryDelay = DefaultRetryDelay
	}

	return &Service{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     cfg.APIKey,
		httpClient: cfg.HTTPClient,
		logger:     cfg.Logger,
		maxRetries: maxRetries,
		retryDelay: retryDelay,
	}, nil
}

func (s *Service) Create(ctx context.Context, payload map[string]any, options CallOptions) (*CompletionResult, error) {
	responseBytes, statusCode, requestID, err := s.call(ctx, "/v1/responses", payload, options)
	if err != nil {
		return nil, err
	}

	var parsed map[string]any
	if err := json.Unmarshal(responseBytes, &parsed); err != nil {
		return nil, &ClientError{
			Kind:       KindMalformedResponse,
			StatusCode: statusCode,
			RequestID:  requestID,
			Retryable:  false,
			Message:    "unable to parse completion response JSON",
			Cause:      err,
		}
	}

	return &CompletionResult{
		RequestID:  requestID,
		HTTPStatus: statusCode,
		Response:   parsed,
	}, nil
}

func (s *Service) Stream(ctx context.Context, payload map[string]any, options CallOptions) (*StreamResult, error) {
	return s.StreamEvents(ctx, payload, options, nil)
}

func (s *Service) StreamEvents(
	ctx context.Context,
	payload map[string]any,
	options CallOptions,
	handler StreamEventHandler,
) (*StreamResult, error) {
	streamPayload := clonePayload(payload)
	streamPayload["stream"] = true
	bodyBytes, err := json.Marshal(streamPayload)
	if err != nil {
		return nil, &ClientError{
			Kind:      KindMalformedResponse,
			Retryable: false,
			Message:   "unable to encode stream request payload",
			Cause:     err,
		}
	}

	maxAttempts := 1
	if options.SafeToRetry {
		maxAttempts += s.maxRetries
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := s.doStreamOnce(ctx, bodyBytes, handler)
		if err == nil {
			s.logEvent(map[string]any{
				"event":      "codex_client_response",
				"path":       "/v1/responses",
				"status":     result.HTTPStatus,
				"request_id": result.RequestID,
				"attempt":    attempt,
			})
			return result, nil
		}

		lastErr = err
		clientErr := toClientError(err)
		s.logEvent(map[string]any{
			"event":      "codex_client_error",
			"path":       "/v1/responses",
			"attempt":    attempt,
			"kind":       clientErr.Kind,
			"status":     clientErr.StatusCode,
			"request_id": clientErr.RequestID,
			"retryable":  clientErr.Retryable,
			"error":      clientErr.Error(),
		})

		if result != nil && len(result.Events) > 0 {
			return result, clientErr
		}
		if !options.SafeToRetry || !clientErr.Retryable || attempt == maxAttempts {
			return result, clientErr
		}
		select {
		case <-ctx.Done():
			return result, &ClientError{
				Kind:       KindNetworkTimeout,
				StatusCode: clientErr.StatusCode,
				RequestID:  clientErr.RequestID,
				Retryable:  false,
				Message:    "context canceled while waiting to retry stream",
				Cause:      ctx.Err(),
			}
		case <-time.After(s.retryDelay):
		}
	}

	clientErr := toClientError(lastErr)
	return nil, clientErr
}

func (s *Service) call(
	ctx context.Context,
	path string,
	payload map[string]any,
	options CallOptions,
) ([]byte, int, string, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, "", &ClientError{
			Kind:      KindMalformedResponse,
			Retryable: false,
			Message:   "unable to encode request payload",
			Cause:     err,
		}
	}

	maxAttempts := 1
	if options.SafeToRetry {
		maxAttempts += s.maxRetries
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		responseBytes, statusCode, requestID, err := s.doOnce(ctx, path, bodyBytes)
		if err == nil {
			s.logEvent(map[string]any{
				"event":      "codex_client_response",
				"path":       path,
				"status":     statusCode,
				"request_id": requestID,
				"attempt":    attempt,
			})
			return responseBytes, statusCode, requestID, nil
		}

		lastErr = err
		clientErr := toClientError(err)
		s.logEvent(map[string]any{
			"event":      "codex_client_error",
			"path":       path,
			"attempt":    attempt,
			"kind":       clientErr.Kind,
			"status":     clientErr.StatusCode,
			"request_id": clientErr.RequestID,
			"retryable":  clientErr.Retryable,
			"error":      clientErr.Error(),
		})

		if !options.SafeToRetry || !clientErr.Retryable || attempt == maxAttempts {
			return nil, clientErr.StatusCode, clientErr.RequestID, clientErr
		}
		select {
		case <-ctx.Done():
			return nil, clientErr.StatusCode, clientErr.RequestID, &ClientError{
				Kind:       KindNetworkTimeout,
				StatusCode: clientErr.StatusCode,
				RequestID:  clientErr.RequestID,
				Retryable:  false,
				Message:    "context canceled while waiting to retry",
				Cause:      ctx.Err(),
			}
		case <-time.After(s.retryDelay):
		}
	}

	clientErr := toClientError(lastErr)
	return nil, clientErr.StatusCode, clientErr.RequestID, clientErr
}

func (s *Service) doOnce(
	ctx context.Context,
	path string,
	bodyBytes []byte,
) ([]byte, int, string, error) {
	url := s.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, "", &ClientError{
			Kind:      KindRequestBuildFailed,
			Retryable: false,
			Message:   "unable to construct HTTP request",
			Cause:     err,
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(s.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		if isNetworkTimeout(err) {
			return nil, 0, "", &ClientError{
				Kind:      KindNetworkTimeout,
				Retryable: true,
				Message:   "network timeout contacting codex endpoint",
				Cause:     err,
			}
		}
		return nil, 0, "", &ClientError{
			Kind:      KindTransportFailed,
			Retryable: false,
			Message:   "transport error contacting codex endpoint",
			Cause:     err,
		}
	}
	defer resp.Body.Close()

	requestID := resp.Header.Get("x-request-id")
	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, resp.StatusCode, requestID, &ClientError{
			Kind:       KindMalformedResponse,
			StatusCode: resp.StatusCode,
			RequestID:  requestID,
			Retryable:  false,
			Message:    "unable to read response body",
			Cause:      readErr,
		}
	}

	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, requestID, classifyHTTPError(resp.StatusCode, requestID, responseBody)
	}
	if bodyHasErrorObject(responseBody) {
		statusFromBody := extractHTTPStatus(responseBody)
		if statusFromBody == 0 {
			statusFromBody = resp.StatusCode
		}
		if statusFromBody >= 400 {
			return nil, statusFromBody, requestID, classifyHTTPError(statusFromBody, requestID, responseBody)
		}
	}

	return responseBody, resp.StatusCode, requestID, nil
}

func (s *Service) doStreamOnce(
	ctx context.Context,
	bodyBytes []byte,
	handler StreamEventHandler,
) (*StreamResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v1/responses", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, &ClientError{
			Kind:      KindRequestBuildFailed,
			Retryable: false,
			Message:   "unable to construct stream HTTP request",
			Cause:     err,
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if strings.TrimSpace(s.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		if isNetworkTimeout(err) {
			return nil, &ClientError{
				Kind:      KindNetworkTimeout,
				Retryable: true,
				Message:   "network timeout contacting codex stream endpoint",
				Cause:     err,
			}
		}
		return nil, &ClientError{
			Kind:      KindTransportFailed,
			Retryable: false,
			Message:   "transport error contacting codex stream endpoint",
			Cause:     err,
		}
	}
	defer resp.Body.Close()

	requestID := resp.Header.Get("x-request-id")
	if resp.StatusCode >= 400 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, classifyHTTPError(resp.StatusCode, requestID, responseBody)
	}

	events := make([]map[string]any, 0, 8)
	finalStatus := "completed"
	emit := func(event map[string]any) error {
		events = append(events, event)
		if handler == nil {
			return nil
		}
		return handler(event)
	}

	parseErr := readResponsesEventStream(resp.Body, func(raw map[string]any) error {
		mapped, err := s.mapResponsesStreamEvent(raw, requestID)
		if err != nil {
			return err
		}
		for _, event := range mapped {
			if event["event"] == "response.error" {
				finalStatus = "failed"
			}
			if err := emit(event); err != nil {
				return err
			}
		}
		return nil
	})
	if parseErr != nil {
		if len(events) == 0 {
			return nil, parseErr
		}
		finalStatus = "failed"
		return &StreamResult{
			RequestID:   requestID,
			HTTPStatus:  resp.StatusCode,
			Events:      events,
			FinalStatus: finalStatus,
		}, parseErr
	}
	if len(events) == 0 {
		return nil, &ClientError{
			Kind:       KindMalformedResponse,
			StatusCode: resp.StatusCode,
			RequestID:  requestID,
			Retryable:  false,
			Message:    "responses stream returned no events",
		}
	}

	return &StreamResult{
		RequestID:   requestID,
		HTTPStatus:  resp.StatusCode,
		Events:      events,
		FinalStatus: finalStatus,
	}, streamFailureError(resp.StatusCode, requestID, events, finalStatus)
}

func (s *Service) logEvent(event map[string]any) {
	s.logger.Print(redaction.ToJSON(event))
}

func clonePayload(payload map[string]any) map[string]any {
	cloned := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func readResponsesEventStream(reader io.Reader, onEvent func(map[string]any) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	dataLines := make([]string, 0, 4)
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		trimmed := strings.TrimSpace(data)
		if trimmed == "" || trimmed == "[DONE]" {
			return nil
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
			return &ClientError{
				Kind:      KindMalformedResponse,
				Retryable: false,
				Message:   "unable to parse responses SSE event",
				Cause:     err,
			}
		}
		return onEvent(raw)
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(trimmed, ":") || strings.HasPrefix(trimmed, "event:") {
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			continue
		}
		if strings.HasPrefix(trimmed, "{") {
			dataLines = append(dataLines, trimmed)
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return &ClientError{
			Kind:      KindTransportFailed,
			Retryable: true,
			Message:   "unable to read responses stream",
			Cause:     err,
		}
	}
	return flush()
}

func (s *Service) mapResponsesStreamEvent(raw map[string]any, fallbackID string) ([]map[string]any, error) {
	eventType := responsesEventType(raw)
	responseID := responsesResponseID(raw, fallbackID)
	switch eventType {
	case "response.started":
		return []map[string]any{{"event": "response.started", "response_id": responseID}}, nil
	case "response.created", "response.in_progress":
		return []map[string]any{{"event": "response.started", "response_id": responseID}}, nil
	case "response.output_text.delta":
		delta, _ := raw["delta"].(string)
		if delta == "" {
			return nil, nil
		}
		return []map[string]any{{"event": "response.output_text.delta", "delta": delta}}, nil
	case "response.tool_call":
		return []map[string]any{raw}, nil
	case "response.tool_result":
		return []map[string]any{raw}, nil
	case "response.output_item.done":
		item, _ := raw["item"].(map[string]any)
		if strings.TrimSpace(typeString(item)) != "function_call" {
			return nil, nil
		}
		toolCall, err := responsesToolCallFromItem(item)
		if err != nil {
			return nil, err
		}
		return []map[string]any{toolCall}, nil
	case "response.completed":
		return []map[string]any{{
			"event":       "response.completed",
			"response_id": responseID,
			"status":      "completed",
		}}, nil
	case "response.error", "response.failed", "response.incomplete", "error":
		return []map[string]any{{
			"event":       "response.error",
			"response_id": responseID,
			"code":        firstNonEmpty(stringValue(raw["code"]), "upstream_error"),
			"message":     responsesErrorMessage(raw),
		}}, nil
	default:
		if eventType != "" && !isKnownIgnorableResponsesEvent(eventType) {
			s.logEvent(map[string]any{
				"event":          "codex_client_stream_event_ignored",
				"upstream_event": redaction.RedactText(eventType),
				"request_id":     redaction.RedactText(fallbackID),
			})
		}
		return nil, nil
	}
}

func responsesEventType(raw map[string]any) string {
	if eventType, _ := raw["type"].(string); strings.TrimSpace(eventType) != "" {
		return strings.TrimSpace(eventType)
	}
	if eventType, _ := raw["event"].(string); strings.TrimSpace(eventType) != "" {
		return strings.TrimSpace(eventType)
	}
	return ""
}

func responsesResponseID(raw map[string]any, fallback string) string {
	if response, _ := raw["response"].(map[string]any); response != nil {
		if id, _ := response["id"].(string); strings.TrimSpace(id) != "" {
			return id
		}
	}
	if id, _ := raw["response_id"].(string); strings.TrimSpace(id) != "" {
		return id
	}
	if id, _ := raw["id"].(string); strings.TrimSpace(id) != "" {
		return id
	}
	if strings.TrimSpace(fallback) == "" {
		return "resp_stream"
	}
	return "resp_" + fallback
}

func responsesToolCallFromItem(item map[string]any) (map[string]any, error) {
	id := firstNonEmpty(stringValue(item["call_id"]), stringValue(item["id"]))
	name := stringValue(item["name"])
	arguments := map[string]any{}
	switch rawArgs := item["arguments"].(type) {
	case string:
		if strings.TrimSpace(rawArgs) != "" {
			if err := json.Unmarshal([]byte(rawArgs), &arguments); err != nil {
				return nil, &ClientError{
					Kind:      KindMalformedResponse,
					Retryable: false,
					Message:   "responses function_call arguments must be JSON",
					Cause:     err,
				}
			}
		}
	case map[string]any:
		arguments = rawArgs
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
		return nil, &ClientError{
			Kind:      KindMalformedResponse,
			Retryable: false,
			Message:   "responses function_call requires call_id and name",
		}
	}
	return map[string]any{
		"event":     "response.tool_call",
		"id":        id,
		"name":      name,
		"arguments": arguments,
	}, nil
}

func responsesErrorMessage(raw map[string]any) string {
	if message := stringValue(raw["message"]); strings.TrimSpace(message) != "" {
		return message
	}
	if errObj, _ := raw["error"].(map[string]any); errObj != nil {
		if message := stringValue(errObj["message"]); strings.TrimSpace(message) != "" {
			return message
		}
		if errType := stringValue(errObj["type"]); strings.TrimSpace(errType) != "" {
			return errType
		}
	}
	return "responses stream failed"
}

func typeString(item map[string]any) string {
	if item == nil {
		return ""
	}
	return stringValue(item["type"])
}

func stringValue(raw any) string {
	value, _ := raw.(string)
	return value
}

func isKnownIgnorableResponsesEvent(eventType string) bool {
	switch eventType {
	case "response.output_item.added",
		"response.content_part.added",
		"response.content_part.done",
		"response.output_text.done",
		"response.output_text.annotation.added",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.reasoning_summary_part.done":
		return true
	default:
		return false
	}
}

func bodyHasErrorObject(body []byte) bool {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	_, ok := parsed["error"].(map[string]any)
	return ok
}

func extractHTTPStatus(body []byte) int {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0
	}
	errorObj, ok := parsed["error"].(map[string]any)
	if !ok {
		return 0
	}
	httpStatus, ok := errorObj["http_status"].(float64)
	if !ok {
		return 0
	}
	return int(httpStatus)
}

func extractErrorMessage(body []byte) string {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	errorObj, ok := parsed["error"].(map[string]any)
	if !ok {
		return ""
	}
	message, _ := errorObj["message"].(string)
	return message
}

func isNetworkTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func streamFailureError(
	statusCode int,
	requestID string,
	events []map[string]any,
	finalStatus string,
) error {
	if strings.EqualFold(strings.TrimSpace(finalStatus), "completed") {
		return nil
	}

	message := fmt.Sprintf("stream ended with final_status=%s", finalStatus)
	for _, event := range events {
		eventName, _ := event["event"].(string)
		if eventName != "response.error" {
			continue
		}
		code, _ := event["code"].(string)
		eventMessage, _ := event["message"].(string)
		if code != "" || eventMessage != "" {
			message = "stream error event"
			if code != "" {
				message += " code=" + code
			}
			if eventMessage != "" {
				message += ": " + eventMessage
			}
			break
		}
	}

	return &ClientError{
		Kind:       KindStreamFailed,
		StatusCode: statusCode,
		RequestID:  requestID,
		Retryable:  false,
		Message:    message,
	}
}
